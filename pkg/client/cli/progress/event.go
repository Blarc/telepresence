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
	"time"

	"github.com/morikuni/aec"
)

// EventStatus indicates the status of an action.
type EventStatus int

func (s EventStatus) color() aec.ANSI {
	switch s {
	case Done:
		return successColor
	case Warning:
		return warningColor
	case Error:
		return errorColor
	default:
		return noColor{}
	}
}

const (
	// Working means that the current task is working.
	Working EventStatus = iota
	// Done means that the current task is done.
	Done
	// Warning means that the current task has warning.
	Warning
	// Error means that the current task has errored.
	Error
)

// Event represents a progress event.
type Event struct {
	ID         string
	ParentID   string
	Text       string
	Status     EventStatus
	StatusText string
	Current    int64
	Percent    int

	Total     int64
	startTime time.Time
	endTime   time.Time
	spinner   *spinner
}

// ErrorMessageEvent creates a new Error Event with a message.
func ErrorMessageEvent(id string, msg string) Event {
	return NewEvent(id, Error, msg)
}

// ErrorEvent creates a new Error Event.
func ErrorEvent(id string) Event {
	return NewEvent(id, Error, "Error")
}

// CreatingEvent creates a new Create in progress Event.
func CreatingEvent(id string) Event {
	return NewEvent(id, Working, "Creating")
}

// StartingEvent creates a new Starting in progress Event.
func StartingEvent(id string) Event {
	return NewEvent(id, Working, "Starting")
}

// StartedEvent creates a new Started in progress Event.
func StartedEvent(id string) Event {
	return NewEvent(id, Done, "Started")
}

// Waiting creates a new waiting event.
func Waiting(id string) Event {
	return NewEvent(id, Working, "Waiting")
}

// Healthy creates a new healthy event.
func Healthy(id string) Event {
	return NewEvent(id, Done, "Healthy")
}

// Exited creates a new exited event.
func Exited(id string) Event {
	return NewEvent(id, Done, "Exited")
}

// RestartingEvent creates a new Restarting in progress Event.
func RestartingEvent(id string) Event {
	return NewEvent(id, Working, "Restarting")
}

// RestartedEvent creates a new Restarted in progress Event.
func RestartedEvent(id string) Event {
	return NewEvent(id, Done, "Restarted")
}

// RunningEvent creates a new Running in progress Event.
func RunningEvent(id string) Event {
	return NewEvent(id, Done, "Running")
}

// CreatedEvent creates a new Created (done) Event.
func CreatedEvent(id string) Event {
	return NewEvent(id, Done, "Created")
}

// StoppingEvent creates a new Stopping in progress Event.
func StoppingEvent(id string) Event {
	return NewEvent(id, Working, "Stopping")
}

// StoppedEvent creates a new Stopping in progress Event.
func StoppedEvent(id string) Event {
	return NewEvent(id, Done, "Stopped")
}

// KillingEvent creates a new Killing in progress Event.
func KillingEvent(id string) Event {
	return NewEvent(id, Working, "Killing")
}

// KilledEvent creates a new Killed in progress Event.
func KilledEvent(id string) Event {
	return NewEvent(id, Done, "Killed")
}

// RemovingEvent creates a new Removing in progress Event.
func RemovingEvent(id string) Event {
	return NewEvent(id, Working, "Removing")
}

// RemovedEvent creates a new removed (done) Event.
func RemovedEvent(id string) Event {
	return NewEvent(id, Done, "Removed")
}

// BuildingEvent creates a new Building in progress Event.
func BuildingEvent(id string) Event {
	return NewEvent(id, Working, "Building")
}

// BuiltEvent creates a new built (done) Event.
func BuiltEvent(id string) Event {
	return NewEvent(id, Done, "Built")
}

// WorkingEvent creates a new <verb> in progress Event.
func WorkingEvent(id, verb string) Event {
	return NewEvent(id, Working, verb)
}

// DoneEvent creates a new <verb> done Event.
func DoneEvent(id, verb string) Event {
	return NewEvent(id, Done, verb)
}

// SkippedEvent creates a new Skipped Event.
func SkippedEvent(id string, reason string) Event {
	return Event{
		ID:         id,
		Status:     Warning,
		StatusText: "Skipped: " + reason,
	}
}

func NewEvent(id string, status EventStatus, statusText string) Event {
	return Event{
		ID:         id,
		Status:     status,
		StatusText: statusText,
	}
}

func (e *Event) WithText(msg string) Event {
	ec := *e
	ec.Text = msg
	return ec
}

func (e *Event) stop() {
	e.endTime = time.Now()
	e.spinner.Stop()
}

func (e *Event) hasMore() {
	e.spinner.Restart()
}

const (
	spinnerDone    = "✔"
	spinnerWarning = "!"
	spinnerError   = "✘"
)

func (e *Event) Spinner() any {
	switch e.Status {
	case Done:
		return successColor.Apply(spinnerDone)
	case Warning:
		return warningColor.Apply(spinnerWarning)
	case Error:
		return errorColor.Apply(spinnerError)
	default:
		return countColor.Apply(e.spinner.String())
	}
}
