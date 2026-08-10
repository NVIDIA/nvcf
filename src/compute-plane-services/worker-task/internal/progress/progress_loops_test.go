/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/upload"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

// writeRaw atomically writes arbitrary bytes to the progress file (truncating).
func writeRaw(progressFile string, data []byte) error {
	return os.WriteFile(progressFile, data, 0o644)
}

// writeProgressMessageAt writes a progress update with an explicit
// lastUpdatedAt, used to exercise the staleness branch deterministically.
func writeProgressMessageAt(progressFile, taskId, resultName string, progress uint32, at time.Time) error {
	data, err := json.Marshal(Progress{
		TaskId:          taskId,
		Name:            resultName,
		PercentComplete: progress,
		Metadata:        map[string]any{"message": "stale"},
		LastUpdatedAt:   at.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(progressFile, data, 0o644)
}

// These tests exercise the long-running loop functions monitorProgress and
// pollingProgress plus the watcher/ticker bootstrap half of Start. They never
// bind a port: the progress package is filesystem/fsnotify based. All
// synchronization is done on channels or an assert-until-deadline poll loop,
// never fixed time.Sleep durations, so the tests are deterministic.

const (
	loopTaskId     = "loop-task"
	loopInstanceId = "loop-instance"
	loopInstance   = "loop-instance-type"
)

// loopMonitor builds a Monitor wired with a capturing SendResult plus a thread
// safe captured slice. TaskCompleted is unbuffered to match production wiring
// (the loop goroutine sends on it); every test reads it concurrently.
func loopMonitor(
	t *testing.T,
	progressFile string,
	pollProgress bool,
	pollInterval time.Duration,
	updateTimeout time.Duration,
	strategy types.ResultHandlingStrategy,
) (*Monitor, *[]*pb.ResultMetadataRequest, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	captured := make([]*pb.ResultMetadataRequest, 0)
	m := NewProgressMonitor(
		loopTaskId,
		loopInstanceId,
		t.TempDir(),
		loopInstance,
		progressFile,
		pollProgress,
		pollInterval,
		updateTimeout,
		strategy,
		func(req *pb.ResultMetadataRequest) error {
			mu.Lock()
			captured = append(captured, req)
			mu.Unlock()
			return nil
		},
	)
	return m, &captured, &mu
}

// awaitCompletion reads the (unbuffered) TaskCompleted channel concurrently and
// returns a function that blocks until the signal arrives or the deadline
// fires. The concurrent read guarantees the loop goroutine's send never blocks.
func awaitCompletion(t *testing.T, m *Monitor, timeout time.Duration) func() error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		select {
		case e := <-m.TaskCompleted:
			done <- e
		case <-time.After(timeout):
			done <- context.DeadlineExceeded
		}
	}()
	return func() error { return <-done }
}

// writeUntilCount writes via rewrite() then re-touches the file until the
// captured slice reaches at least want entries. Re-touching emits a fresh
// fsnotify event so a coalesced or dropped event cannot lose an update. No
// fixed inter-write sleep is used.
func writeUntilCount(
	t *testing.T,
	mu *sync.Mutex,
	captured *[]*pb.ResultMetadataRequest,
	want int,
	timeout time.Duration,
	rewrite func() error,
) {
	t.Helper()
	require.NoError(t, rewrite())
	deadline := time.Now().Add(timeout)
	lastRewrite := time.Now()
	for {
		mu.Lock()
		n := len(*captured)
		mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d captured messages, have %d", want, n)
		}
		if time.Since(lastRewrite) > 250*time.Millisecond {
			require.NoError(t, rewrite())
			lastRewrite = time.Now()
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// --------------------------------------------------------------------------
// monitorProgress (watcher path) via Start.

// Drives the watcher path through Start to completion: writes several progress
// updates and waits on TaskCompleted, not on sleeps. Exercises the
// watcher.Events branch, the event filter, ParseProgressFile, sendProgressUpdate
// and the 100% completion send.
func TestMonitorProgress_WatcherCompletes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"

	m, captured, mu := loopMonitor(t, progressFile, false, 0, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	for i, p := range []uint32{25, 50, 100} {
		want := i + 1
		// Write then re-touch until the monitor has captured this update.
		// Re-touching emits a fresh fsnotify event so a coalesced/dropped event
		// cannot lose an intermediate update; poll synchronization, no fixed
		// inter-write sleep.
		writeUntilCount(t, mu, captured, want, 10*time.Second, func() error {
			return writeProgressMessage(progressFile, loopTaskId, taskVersionId, p)
		})
	}

	require.NoError(t, wait(), "watcher monitor should report nil completion")
	require.NoError(t, m.Stop())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *captured, 3)
	assert.Equal(t, pb.ExecutionStatus_COMPLETED, (*captured)[2].GetStatus())
}

// The watcher path must flush a progress file that already exists at Start time
// (the pre-watch race-condition guard at the top of monitorProgress). Writing a
// completed file before Start and then starting must yield completion without
// any post-Start file write.
func TestMonitorProgress_WatcherFlushesPreexistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"
	require.NoError(t, writeProgressMessage(progressFile, loopTaskId, taskVersionId, 100))

	m, captured, mu := loopMonitor(t, progressFile, false, 0, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	require.NoError(t, wait(), "pre-existing completed file should flush to completion")
	require.NoError(t, m.Stop())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *captured, 1)
	assert.Equal(t, pb.ExecutionStatus_COMPLETED, (*captured)[0].GetStatus())
}

// Cancelling the context must terminate monitorProgress via its ctx.Done branch
// without sending on TaskCompleted. We assert no completion arrives and that a
// subsequent Stop is clean.
func TestMonitorProgress_ContextCancelStops(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"

	m, _, _ := loopMonitor(t, progressFile, false, 0, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, m.Start(ctx))
	cancel()

	// No completion signal should ever arrive after a bare context cancel.
	select {
	case e := <-m.TaskCompleted:
		t.Fatalf("unexpected completion signal after cancel: %v", e)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, m.Stop())
}

// A malformed progress file surfaced through the watcher path must propagate the
// parse error on TaskCompleted (the ParseProgressFile error branch in
// monitorProgress).
func TestMonitorProgress_WatcherParseErrorPropagates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"

	m, _, _ := loopMonitor(t, progressFile, false, 0, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	// Write invalid JSON to the watched file; ParseProgressFile must error and
	// monitorProgress must forward it.
	require.NoError(t, writeRaw(progressFile, []byte("{not-json")))

	err := wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrInvalidProgressFile)
	require.NoError(t, m.Stop())
}

// The watcher path's internal ticker (progressUpdateTimeout) must report
// ErrNoProgressFileUpdates when the progress file goes stale. A stale, valid,
// not-yet-complete file is written before Start so the pre-watch flush sends it
// once, then the short ticker fires checkProgressLastUpdateTime and fails.
func TestMonitorProgress_WatcherTickerStaleUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"
	require.NoError(t, writeProgressMessageAt(
		progressFile, loopTaskId, taskVersionId, 50, time.Now().Add(-time.Hour),
	))

	m, _, _ := loopMonitor(t, progressFile, false, 0, 20*time.Millisecond, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	err := wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrNoProgressFileUpdates)
	require.NoError(t, m.Stop())
}

// The watcher path's ticker must tolerate a still-missing progress file: it
// hits the ErrMissingProgressFile branch and continues (it is well within the
// 2h initialize window). We verify the loop keeps running (no completion
// signal) across several ticks, then cancel cleanly.
func TestMonitorProgress_WatcherTickerMissingFileContinues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress" // never created

	m, _, _ := loopMonitor(t, progressFile, false, 0, 20*time.Millisecond, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, m.Start(ctx))

	// Across many ticker fires the loop must keep continuing (missing file is
	// not an error until the 2h initialize window elapses), so no completion.
	select {
	case e := <-m.TaskCompleted:
		t.Fatalf("unexpected completion while file still missing: %v", e)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	require.NoError(t, m.Stop())
}

// --------------------------------------------------------------------------
// pollingProgress (ticker path) via Start.

// Drives the polling path to completion with a small interval, waiting on
// TaskCompleted. Exercises the pollProgressTicker.C branch, ParseProgressFile,
// checkProgressLastUpdateTime, sendProgressUpdate and completion.
func TestPollingProgress_Completes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"
	require.NoError(t, writeProgressMessage(progressFile, loopTaskId, taskVersionId, 100))

	m, captured, mu := loopMonitor(t, progressFile, true, 20*time.Millisecond, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	require.NoError(t, wait(), "polling monitor should report nil completion")
	require.NoError(t, m.Stop())

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(*captured), 1)
	assert.Equal(t, pb.ExecutionStatus_COMPLETED, (*captured)[len(*captured)-1].GetStatus())
}

// Polling with a missing progress file must propagate ErrMissingProgressFile on
// TaskCompleted (the ParseProgressFile error branch in pollingProgress).
func TestPollingProgress_MissingFilePropagates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress" // never created

	m, _, _ := loopMonitor(t, progressFile, true, 20*time.Millisecond, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	err := wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrMissingProgressFile)
	require.NoError(t, m.Stop())
}

// Polling over a malformed progress file must propagate ErrInvalidProgressFile
// on TaskCompleted (the ParseProgressFile error branch in pollingProgress).
// Deterministic: the file is invalid before Start, so the first ticker fire
// fails to parse.
func TestPollingProgress_InvalidJSONPropagates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"
	require.NoError(t, writeRaw(progressFile, []byte("{not-json")))

	m, _, _ := loopMonitor(t, progressFile, true, 20*time.Millisecond, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	err := wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrInvalidProgressFile)
	require.NoError(t, m.Stop())
}

// Polling with a stale (old) lastUpdatedAt and an aggressive update timeout must
// propagate ErrNoProgressFileUpdates from checkProgressLastUpdateTime.
func TestPollingProgress_StaleUpdateTimesOut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"
	// Write a stale, not-yet-complete update so checkProgressLastUpdateTime fails
	// once the ticker fires.
	require.NoError(t, writeProgressMessageAt(
		progressFile, loopTaskId, taskVersionId, 50,
		time.Now().Add(-time.Hour),
	))

	m, _, _ := loopMonitor(t, progressFile, true, 20*time.Millisecond, time.Second, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 10*time.Second)
	require.NoError(t, m.Start(ctx))

	err := wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrNoProgressFileUpdates)
	require.NoError(t, m.Stop())
}

// Cancelling the context must terminate pollingProgress via its ctx.Done branch
// without a completion signal.
func TestPollingProgress_ContextCancelStops(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress" // missing on purpose; cancel should win the race rarely, so use a long interval

	m, _, _ := loopMonitor(t, progressFile, true, time.Hour, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, m.Start(ctx))
	cancel()

	select {
	case e := <-m.TaskCompleted:
		t.Fatalf("unexpected completion signal after cancel: %v", e)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, m.Stop())
}

// --------------------------------------------------------------------------
// Start bootstrap branches.

// Start in UPLOAD_STRATEGY without an UploadClient returns ErrInternal before
// launching any goroutine, watcher, or ticker.
func TestStart_UploadStrategyMissingClient(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _ := loopMonitor(t, dir+"/progress", false, 0, 5*time.Minute, types.UPLOAD_STRATEGY)
	m.UploadClient = nil

	err := m.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrInternal)
}

// Start is idempotent: once a watcher exists a second Start is a no-op returning
// nil (the m.watcher != nil early-return branch).
func TestStart_SecondStartIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _ := loopMonitor(t, dir+"/progress", false, 0, 5*time.Minute, types.NO_STRATEGY)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))
	// Second call must short-circuit on the existing watcher.
	require.NoError(t, m.Start(ctx))
	require.NoError(t, m.Stop())
}

// Start in UPLOAD_STRATEGY with a mock UploadClient drives the watcher path and
// the UPLOAD branch of sendProgressUpdate to completion.
func TestStart_WatcherUploadStrategyCompletes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := dir + "/progress"

	m, captured, mu := loopMonitor(t, progressFile, false, 0, 5*time.Minute, types.UPLOAD_STRATEGY)
	m.UploadClient = upload.NewMockUploader(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := awaitCompletion(t, m, 15*time.Second)
	require.NoError(t, m.Start(ctx))

	writeUntilCount(t, mu, captured, 1, 10*time.Second, func() error {
		return writeProgressMessage(progressFile, loopTaskId, "checkpoint-a", 50)
	})
	writeUntilCount(t, mu, captured, 2, 10*time.Second, func() error {
		return writeProgressMessage(progressFile, loopTaskId, "checkpoint-b", 100)
	})

	require.NoError(t, wait())
	require.NoError(t, m.Stop())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *captured, 2)
	assert.Equal(t, pb.ExecutionStatus_COMPLETED, (*captured)[1].GetStatus())
}
