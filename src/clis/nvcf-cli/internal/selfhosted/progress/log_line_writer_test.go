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
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSink is a minimal EventSink implementation that records every
// LogLine emitted (other event types are dropped). Goroutine-safe so
// concurrent-write tests work.
type captureSink struct {
	mu    sync.Mutex
	lines []LogLine
}

func (s *captureSink) Emit(_ context.Context, e Event) error {
	if ll, ok := e.(LogLine); ok {
		s.mu.Lock()
		s.lines = append(s.lines, ll)
		s.mu.Unlock()
	}
	return nil
}
func (s *captureSink) Close() error { return nil }

func (s *captureSink) snapshot() []LogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogLine, len(s.lines))
	copy(out, s.lines)
	return out
}

// TestLogLineWriter_SplitsOnNewline asserts the writer emits one LogLine per
// '\n'-terminated chunk and buffers partial trailing input for the next
// Write. Helmfile commonly flushes line-by-line; verify we don't drop or
// duplicate lines on chunk boundaries.
func TestLogLineWriter_SplitsOnNewline(t *testing.T) {
	sink := &captureSink{}
	w := NewLogLineWriter(sink, "stdout", "helmfile")
	defer func() { _ = w.Close() }()

	// First write: two complete lines + a partial.
	input := []byte("Pulling cassandra\nComparing release=cassandra\nUpgra")
	n, err := w.Write(input)
	require.NoError(t, err)
	require.Equal(t, len(input), n)

	// Only the two complete lines should have been emitted yet.
	got := sink.snapshot()
	require.Len(t, got, 2)
	assert.Equal(t, "Pulling cassandra", got[0].Line)
	assert.Equal(t, "Comparing release=cassandra", got[1].Line)
	assert.Equal(t, "stdout", got[0].Stream)
	assert.Equal(t, "helmfile", got[0].Source)

	// Second write completes the partial.
	_, err = w.Write([]byte("ding nats\n"))
	require.NoError(t, err)
	got = sink.snapshot()
	require.Len(t, got, 3)
	assert.Equal(t, "Upgrading nats", got[2].Line)
}

// TestLogLineWriter_StripsCarriageReturn asserts that CRLF-terminated input
// (Windows-style or `kubectl` carriage-return progress redraws) emits the
// line without the trailing \r so the bubbletea panel doesn't render
// stray ^M glyphs.
func TestLogLineWriter_StripsCarriageReturn(t *testing.T) {
	sink := &captureSink{}
	w := NewLogLineWriter(sink, "stdout", "test")
	defer func() { _ = w.Close() }()

	_, err := w.Write([]byte("line one\r\nline two\r\n"))
	require.NoError(t, err)

	got := sink.snapshot()
	require.Len(t, got, 2)
	assert.Equal(t, "line one", got[0].Line)
	assert.Equal(t, "line two", got[1].Line)
}

// TestLogLineWriter_FlushesOnClose asserts that Close emits any buffered
// trailing data even when the subprocess exited without a final newline.
func TestLogLineWriter_FlushesOnClose(t *testing.T) {
	sink := &captureSink{}
	w := NewLogLineWriter(sink, "stderr", "kubectl")

	_, err := w.Write([]byte("partial-line-no-newline"))
	require.NoError(t, err)
	require.Empty(t, sink.snapshot(), "partial line should not emit until Close")

	require.NoError(t, w.Close())
	got := sink.snapshot()
	require.Len(t, got, 1)
	assert.Equal(t, "partial-line-no-newline", got[0].Line)

	// Close is idempotent.
	require.NoError(t, w.Close())
	assert.Len(t, sink.snapshot(), 1, "double-close must not re-emit")
}

// TestLogLineWriter_SkipsBlankLines asserts that empty lines (bare '\n' or
// blank-after-trim) are dropped so subprocess flushes that emit a stray
// blank line don't push real activity out of the TTY ring buffer.
func TestLogLineWriter_SkipsBlankLines(t *testing.T) {
	sink := &captureSink{}
	w := NewLogLineWriter(sink, "stdout", "test")
	defer func() { _ = w.Close() }()

	_, err := w.Write([]byte("real line\n\n\nanother real line\n"))
	require.NoError(t, err)

	got := sink.snapshot()
	require.Len(t, got, 2, "blank lines should be skipped")
	assert.Equal(t, "real line", got[0].Line)
	assert.Equal(t, "another real line", got[1].Line)
}

// TestLogLineWriter_AfterCloseSwallowsWrites asserts that a Write after
// Close returns the byte count without touching the sink. exec.Cmd sometimes
// writes after the parent has decided cleanup is done; we don't want a
// late LogLine to surface noise after the dashboard has shown success.
func TestLogLineWriter_AfterCloseSwallowsWrites(t *testing.T) {
	sink := &captureSink{}
	w := NewLogLineWriter(sink, "stdout", "test")
	require.NoError(t, w.Close())

	n, err := w.Write([]byte("late line\n"))
	require.NoError(t, err)
	assert.Equal(t, 10, n)
	assert.Empty(t, sink.snapshot(), "writes after Close must not emit")
}

// TestLogLineWriter_ConcurrentWrites asserts the mutex serializes
// concurrent producers correctly — helmfile fans out goroutines per release
// and each may write to the same Stdout/Stderr pipe at the same time. The
// total emitted lines must equal the total written; ordering across
// goroutines is not guaranteed.
func TestLogLineWriter_ConcurrentWrites(t *testing.T) {
	sink := &captureSink{}
	w := NewLogLineWriter(sink, "stdout", "test")
	defer func() { _ = w.Close() }()

	const goroutines = 8
	const linesPerGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(_ int) {
			defer wg.Done()
			for i := 0; i < linesPerGoroutine; i++ {
				_, _ = w.Write([]byte("payload\n"))
			}
		}(g)
	}
	wg.Wait()

	got := sink.snapshot()
	assert.Len(t, got, goroutines*linesPerGoroutine,
		"every line must reach the sink exactly once")
}
