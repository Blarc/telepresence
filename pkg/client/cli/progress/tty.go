/*
   Copyright 2020 Docker Compose CLI authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package progress

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/docker/go-units"
	"github.com/moby/term"
	"github.com/morikuni/aec"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type ttyWriter struct {
	out             io.Writer
	ticker          *time.Timer
	events          map[string]Event
	eventIDs        []string
	repeated        bool
	numLines        int
	done            chan struct{}
	mtx             sync.Mutex
	tailEvents      []string
	skipChildEvents bool
	progressTitle   string
}

func newTTYWriter(out io.Writer) Writer {
	w := &ttyWriter{
		out:    out,
		events: make(map[string]Event),
		done:   make(chan struct{}),
	}
	w.ticker = time.AfterFunc(math.MaxInt64, w.print)
	return w
}

func (w *ttyWriter) Start(ctx context.Context, progressTitle string) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	w.events = make(map[string]Event)
	w.eventIDs = nil
	w.repeated = false
	w.numLines = 0
	w.done = make(chan struct{})
	w.tailEvents = nil
	w.skipChildEvents = false
	w.progressTitle = progressTitle
	go func() {
		defer w.ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				w.print()
				w.printTailEvents()
				return
			case <-w.done:
				return
			}
		}
	}()
}

func (w *ttyWriter) IsNoOp() bool {
	return false
}

func (w *ttyWriter) Stop() {
	select {
	case <-w.done:
		// Already closed
		return
	default:
		close(w.done)
		w.print()
		w.printTailEvents()
	}
}

func (w *ttyWriter) event(e Event) {
	if !slices.Contains(w.eventIDs, e.ID) {
		w.eventIDs = append(w.eventIDs, e.ID)
	}
	if _, ok := w.events[e.ID]; ok {
		last := w.events[e.ID]
		switch e.Status {
		case Done, Error, Warning:
			if last.Status != e.Status {
				last.stop()
			}
		case Working:
			last.hasMore()
		}
		last.Status = e.Status
		last.Text = e.Text
		last.StatusText = e.StatusText
		// progress can only go up
		if e.Total > last.Total {
			last.Total = e.Total
		}
		if e.Current > last.Current {
			last.Current = e.Current
		}
		if e.Percent > last.Percent {
			last.Percent = e.Percent
		}
		// allow set/unset of parent, but not swapping otherwise prompt is flickering
		if last.ParentID == "" || e.ParentID == "" {
			last.ParentID = e.ParentID
		}
		w.events[e.ID] = last
	} else {
		e.startTime = time.Now()
		e.spinner = newSpinner()
		if e.Status == Done || e.Status == Error {
			e.stop()
		}
		w.events[e.ID] = e
	}
}

func (w *ttyWriter) Write(events ...Event) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	for _, e := range events {
		w.event(e)
	}
	w.ticker.Reset(10 * time.Millisecond)
}

func (w *ttyWriter) TailMsgf(msg string, args ...any) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	w.tailEvents = append(w.tailEvents, fmt.Sprintf(msg, args...))
}

func (w *ttyWriter) printTailEvents() {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	for _, msg := range w.tailEvents {
		ioutil.Println(w.out, msg)
	}
}

func (w *ttyWriter) print() {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if len(w.eventIDs) == 0 {
		return
	}
	ws, err := term.GetWinsize(1)
	if err != nil {
		ws = &term.Winsize{
			Height: 25,
			Width:  80,
		}
	}
	b := aec.EmptyBuilder
	for i := 0; i <= w.numLines; i++ {
		b = b.Up(1)
	}
	single := len(w.events) == 1
	if single || !w.repeated {
		b = b.Down(1)
	}
	w.repeated = true
	ioutil.Print(w.out, b.Column(0).ANSI)

	// Hide the cursor while we are printing
	ioutil.Print(w.out, aec.Hide)
	defer func() {
		ioutil.Print(w.out, aec.Show)
	}()

	if !single {
		firstLine := fmt.Sprintf("[+] %s %d/%d", w.progressTitle, numDone(w.events), len(w.events))
		if numDone(w.events) == len(w.events) {
			firstLine = doneColor.Apply(firstLine)
		}
		ioutil.Println(w.out, firstLine)
	}

	var statusPadding int
	for _, v := range w.eventIDs {
		event := w.events[v]
		l := len(event.Text)
		if !single {
			l += len(event.ID) + 1
		}
		if statusPadding < l {
			statusPadding = l
		}
		if event.ParentID != "" {
			statusPadding -= 2
		}
	}

	if len(w.eventIDs) > int(ws.Height)-2 {
		w.skipChildEvents = true
	}
	numLines := 0
	for _, v := range w.eventIDs {
		event := w.events[v]
		if event.ParentID != "" {
			continue
		}
		line := w.lineText(event, single, "", int(ws.Width), statusPadding)
		ioutil.Print(w.out, line)
		numLines++
		for _, v := range w.eventIDs {
			ev := w.events[v]
			if ev.ParentID == event.ID {
				if w.skipChildEvents {
					continue
				}
				line := w.lineText(ev, single, "  ", int(ws.Width), statusPadding)
				ioutil.Print(w.out, line)
				numLines++
			}
		}
	}
	for i := numLines; i < w.numLines; i++ {
		if numLines < int(ws.Height)-2 {
			ioutil.Println(w.out, strings.Repeat(" ", int(ws.Width)))
			numLines++
		}
	}
	w.numLines = numLines
}

var percentChars = strings.Split("⠀⡀⣀⣄⣤⣦⣶⣷⣿", "") //nolint:gochecknoglobals // constant names

func (w *ttyWriter) lineText(event Event, single bool, pad string, terminalWidth, statusPadding int) string {
	endTime := time.Now()
	if event.Status != Working {
		endTime = event.startTime
		if (event.endTime != time.Time{}) {
			endTime = event.endTime
		}
	}
	elapsed := endTime.Sub(event.startTime).Seconds()

	var (
		hideDetails bool
		total       int64
		current     int64
		completion  []string
	)

	// only show the aggregated progress while the root operation is in-progress
	if parent := event; parent.Status == Working {
		for _, v := range w.eventIDs {
			child := w.events[v]
			if child.ParentID == parent.ID {
				if child.Status == Working && child.Total == 0 {
					// we don't have totals available for all the child events
					// so don't show the total progress yet
					hideDetails = true
				}
				total += child.Total
				current += child.Current
				completion = append(completion, percentChars[(len(percentChars)-1)*child.Percent/100])
			}
		}
	}

	// don't try to show detailed progress if we don't have any idea
	if total == 0 {
		hideDetails = true
	}

	var txt string
	if len(completion) > 0 {
		var details string
		if !hideDetails {
			details = fmt.Sprintf(" %7s / %-7s ", units.HumanSize(float64(current)), units.HumanSize(float64(total)))
		}
		txt = fmt.Sprintf("[%s]%s%s",
			successColor.Apply(strings.Join(completion, "")),
			details,
			event.Text,
		)
	} else {
		txt = event.Text
	}
	if !single {
		txt = fmt.Sprintf("%s %s", event.ID, txt)
	}
	textLen := len(txt)
	padding := statusPadding - textLen
	if padding < 0 {
		padding = 0
	}
	// calculate the max length for the status text, on errors it
	// is 2-3 lines long and breaks the line formatting
	maxStatusLen := terminalWidth - textLen - statusPadding - 15
	status := event.StatusText
	// in some cases (debugging under VS Code), terminalWidth is set to zero by goterm.Width() ; ensuring we don't tweak strings with negative char index
	if maxStatusLen > 0 && len(status) > maxStatusLen {
		status = status[:maxStatusLen] + "..."
	}
	if txt != "" && padding == 0 {
		padding++
	}
	text := fmt.Sprintf("%s %s %s%s%s",
		pad,
		event.Spinner(),
		txt,
		strings.Repeat(" ", padding),
		event.Status.color().Apply(status),
	)
	timer := fmt.Sprintf("%.1fs ", elapsed)
	o := align(text, timerColor.Apply(timer), terminalWidth)

	return o
}

func numDone(events map[string]Event) int {
	i := 0
	for _, e := range events {
		if e.Status != Working {
			i++
		}
	}
	return i
}

func align(l, r string, w int) string {
	ll := lenAnsi(l)
	lr := lenAnsi(r)
	pad := ""
	count := w - ll - lr
	if count > 0 {
		pad = strings.Repeat(" ", count)
	}
	return fmt.Sprintf("%s%s%s\n", l, pad, r)
}

// lenAnsi count of user-perceived characters in ANSI string.
func lenAnsi(s string) int {
	length := 0
	ansiCode := false
	for _, r := range s {
		if r == '\x1b' {
			ansiCode = true
			continue
		}
		if ansiCode && r == 'm' {
			ansiCode = false
			continue
		}
		if !ansiCode {
			length++
		}
	}
	return length
}
