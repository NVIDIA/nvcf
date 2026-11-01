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

package cmd

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted/progress"
)

// resetStatusFlags restores status command flag vars between tests.
func resetStatusFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		statusWatch = false
		statusWatchInterval = 5 * time.Second
		statusComponent = ""
		statusNoEvents = false
		statusClusterName = ""
		statusNCAID = ""
		selfHostedJSON = false
		selfHostedPlain = false
		selfHostedAccessible = false
	})
}

// fakeCollector is a test double for StatusCollector.
type fakeCollector struct {
	calls int64
}

func (f *fakeCollector) Collect(_ context.Context, sink progress.EventSink) error {
	atomic.AddInt64(&f.calls, 1)
	// Emit a minimal snapshot so the sink has something to process.
	_ = sink.Emit(context.Background(), progress.Snapshot{
		Cluster: "test-cluster",
		Verdict: "healthy",
	})
	return nil
}

func (f *fakeCollector) collectCount() int {
	return int(atomic.LoadInt64(&f.calls))
}

// injectFakeCollector replaces newStatusCollector for the duration of the test.
func injectFakeCollector(t *testing.T, fc *fakeCollector) {
	t.Helper()
	prev := newStatusCollector
	t.Cleanup(func() { newStatusCollector = prev })
	newStatusCollector = func(_, _, _ string) (StatusCollector, error) {
		return fc, nil
	}
}

// captureSink records emitted events and satisfies progress.EventSink.
type statusCaptureSink struct {
	events []progress.Event
}

func (s *statusCaptureSink) Emit(_ context.Context, e progress.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (s *statusCaptureSink) Close() error { return nil }

func TestStatusCmd_OneShot(t *testing.T) {
	resetStatusFlags(t)
	fc := &fakeCollector{}
	sink := &statusCaptureSink{}

	err := runStatusLoop(context.Background(), fc, sink, false /*watch*/, 5*time.Second)
	require.NoError(t, err)

	// Collect must have been called exactly once.
	assert.Equal(t, 1, fc.collectCount(), "Collect should be called once in one-shot mode")

	// Final{Success:true} must be the last event.
	require.NotEmpty(t, sink.events)
	final, ok := sink.events[len(sink.events)-1].(progress.Final)
	require.True(t, ok, "last event must be Final")
	assert.True(t, final.Success)
}

func TestStatusCmd_Watch(t *testing.T) {
	resetStatusFlags(t)
	fc := &fakeCollector{}
	sink := &statusCaptureSink{}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- runStatusLoop(ctx, fc, sink, true /*watch*/, 10*time.Millisecond)
	}()

	// Wait until at least 2 Collect calls have happened, then cancel.
	require.Eventually(t, func() bool {
		return fc.collectCount() >= 2
	}, 2*time.Second, 5*time.Millisecond, "expected ≥2 Collect calls before cancel")

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("watch loop did not exit within 2s after context cancel")
	}

	assert.GreaterOrEqual(t, fc.collectCount(), 2,
		"expected ≥2 Collect calls")

	// Final{Cancelled:true} must be in the events.
	var gotFinal bool
	for _, e := range sink.events {
		if f, ok := e.(progress.Final); ok && f.Cancelled {
			gotFinal = true
			break
		}
	}
	assert.True(t, gotFinal, "Final{Cancelled:true} event should be emitted on context cancellation")
}

func TestStatusCmd_JSONRendererIsComposed(t *testing.T) {
	resetStatusFlags(t)
	fc := &fakeCollector{}
	injectFakeCollector(t, fc)

	// Capture the output written by the JSONL renderer.
	var buf bytes.Buffer
	sink := progress.NewJSONLRendererForStatus(&buf)

	err := runStatusLoop(context.Background(), fc, sink, false /*watch*/, 5*time.Second)
	require.NoError(t, err)

	output := buf.String()
	// Output must contain the JSONL schema version header.
	assert.Contains(t, output, `"schemaVersion"`, "JSONL header should be present")
	// The composed snapshot event should appear.
	assert.Contains(t, output, `"snapshot"`, "snapshot event should be in JSONL output")
	// Final event must also be present.
	assert.Contains(t, output, `"final"`, "final event should terminate the stream")
}

func TestStatusCmd_RendererSelection(t *testing.T) {
	// Verify selectStatusRenderer returns JSONLRendererForStatus when --json is set.
	selfHostedJSON = true
	defer func() { selfHostedJSON = false }()

	var buf bytes.Buffer
	// watch=false matches the historical default this test was written
	// against — bypasses the new TTY one-shot branch via the !TTY check
	// against bytes.Buffer (it has no Fd()).
	sink := selectStatusRenderer(&buf, false)
	// Emit a Snapshot followed by a ComponentHealth and a Final.
	// In compose mode the ComponentHealth is buffered until Final triggers flush.
	ctx := context.Background()
	_ = sink.Emit(ctx, progress.Snapshot{Cluster: "x", Verdict: "healthy"})
	_ = sink.Emit(ctx, progress.ComponentHealth{Name: "SIS", Ready: 1, Total: 1, Healthy: true})
	_ = sink.Emit(ctx, progress.Final{Success: true})

	output := buf.String()
	// Fat composed snapshot: components array should appear inside the snapshot object.
	assert.Contains(t, output, `"components"`, "composed snapshot should include components array")
}
