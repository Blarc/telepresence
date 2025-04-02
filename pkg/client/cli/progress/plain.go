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
)

type plainWriter struct {
	out io.Writer
}

func (p plainWriter) IsNoOp() bool {
	return false
}

func (p plainWriter) Start(context.Context, string) {
}

func (p plainWriter) Stop() {
}

func (p plainWriter) Write(events ...Event) {
	for _, e := range events {
		_, _ = fmt.Fprintln(p.out, e.ID, e.Text, e.StatusText)
	}
}

func (p plainWriter) TailMsgf(msg string, args ...any) {
	_, _ = fmt.Fprintln(p.out, fmt.Sprintf(msg, args...))
}
