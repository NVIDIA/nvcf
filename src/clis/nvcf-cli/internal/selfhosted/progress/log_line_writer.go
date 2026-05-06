/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

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
	"bytes"
	"context"
	"io"
	"sync"
)

// LogLineWriter is an io.Writer that splits input on newlines and emits one
// LogLine event per complete line through the supplied EventSink. Used to
// route helmfile / kubectl / similar subprocess output into the bubbletea
// "Recent" panel without letting raw bytes corrupt the alt-screen redraw.
//
// Partial trailing bytes (no terminating newline yet) are buffered until the
// next Write completes the line. Close() flushes any held remainder as a
// final LogLine — exec.Cmd may exit before its stdout buffer is fully
// flushed, leaving a few bytes without a newline that would otherwise be
// lost.
//
// Concurrency: Write/Close acquire an internal mutex so multiple goroutines
// can share one writer (helmfile internally fans out goroutines per release;
// each may write to the same Stdout/Stderr concurrently). The downstream
// EventSink is responsible for its own concurrency (JSONLRenderer's mu;
// TTYRenderer's tea.Program.Send is goroutine-safe by design).
type LogLineWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	sink    EventSink
	stream  string // "stdout" | "stderr"
	source  string // short producer tag, e.g. "helmfile-cp"
	closed  bool
}

// NewLogLineWriter constructs a LogLineWriter feeding into sink. stream and
// source are baked into every LogLine event emitted; pick descriptive
// values so consumers can tell helmfile-control-plane chatter from
// helmfile-compute-plane or kubectl probes.
func NewLogLineWriter(sink EventSink, stream, source string) *LogLineWriter {
	return &LogLineWriter{
		sink:   sink,
		stream: stream,
		source: source,
	}
}

// Write splits p on '\n' and emits one LogLine per complete line via the
// sink. Partial last line (no terminating newline) is buffered for the next
// Write. Always returns len(p), nil — emit failures are swallowed because
// LogLine is best-effort decoration; we don't want a sink hiccup to break
// the subprocess pipe.
func (w *LogLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(p), nil
	}
	w.buf.Write(p)
	for {
		idx := bytes.IndexByte(w.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := w.buf.Next(idx + 1) // includes the '\n'
		// Drop the trailing newline (and any \r before it) before emitting.
		trimmed := line[:len(line)-1]
		if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\r' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		w.emit(string(trimmed))
	}
	return len(p), nil
}

// Close flushes any buffered remainder as a final LogLine and marks the
// writer closed. Idempotent.
func (w *LogLineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.buf.Len() > 0 {
		w.emit(w.buf.String())
		w.buf.Reset()
	}
	return nil
}

// emit fires one LogLine event. Best-effort — sink failures are swallowed
// because subprocess output noise shouldn't propagate up the call chain.
func (w *LogLineWriter) emit(line string) {
	if line == "" {
		return
	}
	_ = w.sink.Emit(context.Background(), LogLine{
		Stream: w.stream,
		Source: w.source,
		Line:   line,
	})
}

// Compile-time check: LogLineWriter implements io.WriteCloser.
var _ io.WriteCloser = (*LogLineWriter)(nil)
