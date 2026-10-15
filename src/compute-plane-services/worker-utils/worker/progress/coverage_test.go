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
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMeteringEvent(requestId string) *metering.EventDetails {
	cfg := metering.Config{
		Backend:           "LocalBackend",
		NcaId:             "nca-cov",
		FunctionId:        "fid-cov",
		FunctionVersionId: "fvid-cov",
		ICMSEnvironment:   "LocalEnvironment",
		ZoneName:          "LocalZone",
	}
	return metering.New(&cfg, requestId, "sub-cov", "invoker-nca-id", nil)
}

func writeProgress(t *testing.T, file, requestId string, percent uint32) {
	t.Helper()
	msg := fmt.Sprintf(`{"id": "%s", "progress": %d, "partialResponse": {"message": "i'm %d complete"}}`,
		requestId, percent, percent)
	require.NoError(t, os.WriteFile(file, []byte(msg), 0644))
}

// TestStart_AlreadyStarted exercises the early-return when the watcher is set.
func TestStart_AlreadyStarted(t *testing.T) {
	m := New(t.TempDir())
	require.NoError(t, m.Start())
	defer func() { _ = m.Stop() }()

	// Second Start should be a no-op returning nil without replacing the watcher.
	require.NoError(t, m.Start())
}

// TestStart_BadDir exercises the watcher.Add error path with a nonexistent dir.
func TestStart_BadDir(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "does-not-exist"))
	err := m.Start()
	require.Error(t, err)
}

// TestStop_NotStarted exercises the watcher == nil early return.
func TestStop_NotStarted(t *testing.T) {
	m := New(t.TempDir())
	require.NoError(t, m.Stop())
}

func TestStop_Idempotent(t *testing.T) {
	m := New(t.TempDir())
	require.NoError(t, m.Start())
	require.NoError(t, m.Stop())
	// Second Stop hits the watcher == nil branch again.
	require.NoError(t, m.Stop())
}

func TestWatchGaugeCallback(t *testing.T) {
	m := New(t.TempDir())
	// Before Start the watcher is nil.
	assert.Equal(t, 0.0, m.WatchGaugeCallback())

	require.NoError(t, m.Start())
	defer func() { _ = m.Stop() }()
	// After Start the root dir is being watched.
	assert.Equal(t, 1.0, m.WatchGaugeCallback())
}

func TestMonitorAndDrop(t *testing.T) {
	m := New(t.TempDir())
	work := &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("req-drop"),
		RespondToWork: make(chan *http.Response, 1),
	}
	m.Monitor("req-drop", work)

	m.lock.RLock()
	_, ok := m.monitoredWork["req-drop"]
	m.lock.RUnlock()
	require.True(t, ok)

	m.Drop("req-drop")

	m.lock.RLock()
	_, ok = m.monitoredWork["req-drop"]
	m.lock.RUnlock()
	require.False(t, ok)
}

// NOTE on Stop(): the event-generating tests below deliberately do NOT call
// Stop(). Stop() acquires m.lock (write) and then blocks on <-m.monitorClosed
// while still holding that lock; the monitorProgress goroutine needs
// m.lock.RLock() (progress.go:195) to finish handling any in-flight file event
// before it can observe the closed watcher and signal monitorClosed. If Stop()
// runs while an event is being processed, the two deadlock. See the bug note in
// the summary. These tests therefore let the background watcher goroutine run
// to process exit, which is harmless. Stop() itself is covered by the
// event-free tests above (TestStop_NotStarted, TestStop_Idempotent,
// TestWatchGaugeCallback).

// TestMonitorProgress_UnknownRequestId writes a progress file whose request id
// is not registered, exercising the unknown-request-id branch. The unknown-id
// file lives in its own directory so its write is never coalesced away by a
// later write to the same file. A separate known request in a different
// directory provides liveness: once it delivers, the monitor is processing
// events, and the unknown directory's write (issued first, processed in order
// per the single monitor goroutine) has been seen and skipped.
func TestMonitorProgress_UnknownRequestId(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	require.NoError(t, m.Start())

	knownCh := make(chan *http.Response, 256)
	m.Monitor("known", &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("known"),
		RespondToWork: knownCh,
	})

	unknownDir := filepath.Join(dir, "unknownsub")
	knownDir := filepath.Join(dir, "knownsub")
	require.NoError(t, os.Mkdir(unknownDir, 0777))
	require.NoError(t, os.Mkdir(knownDir, 0777))

	require.Eventually(t, func() bool {
		return m.WatchGaugeCallback() >= 3.0
	}, 2*time.Second, 5*time.Millisecond)

	// Write the unknown-id progress in its own file (never overwritten).
	writeProgress(t, filepath.Join(unknownDir, "progress"), "unknown-id", 10)

	// Drive the known request in its own directory until it delivers.
	knownFile := filepath.Join(knownDir, "progress")
	require.Eventually(t, func() bool {
		writeProgress(t, knownFile, "known", 20)
		select {
		case resp := <-knownCh:
			assert.Equal(t, "20", resp.Header.Get("Nvcf-Percent-Complete"))
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
}

// TestMonitorProgress_StaleModTime exercises the monitor's
// "modtime not after LastUpdate" skip branch. The monitored work is seeded with
// a LastUpdate far in the future, so any progress file the monitor observes has
// an older modtime and must be skipped without a delivery. A separate barrier
// request provides liveness: once it delivers, the monitor is actively
// processing events, and the stale request's channel must still be empty.
func TestMonitorProgress_StaleModTime(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	require.NoError(t, m.Start())

	staleCh := make(chan *http.Response, 256)
	m.Monitor("req-stale", &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("req-stale"),
		RespondToWork: staleCh,
		// Seed LastUpdate in the future so every observed write is "stale".
		LastUpdate: time.Now().Add(time.Hour),
	})
	barrierCh := make(chan *http.Response, 256)
	m.Monitor("req-stale-barrier", &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("req-stale-barrier"),
		RespondToWork: barrierCh,
	})

	staleDir := filepath.Join(dir, "stalesub")
	barrierDir := filepath.Join(dir, "stalebarrier")
	require.NoError(t, os.Mkdir(staleDir, 0777))
	require.NoError(t, os.Mkdir(barrierDir, 0777))
	require.Eventually(t, func() bool {
		return m.WatchGaugeCallback() >= 3.0
	}, 2*time.Second, 5*time.Millisecond)

	staleFile := filepath.Join(staleDir, "progress")
	barrierFile := filepath.Join(barrierDir, "progress")

	// Write the stale progress file (modtime "now" < seeded future LastUpdate).
	writeProgress(t, staleFile, "req-stale", 40)

	// Drive the barrier until it delivers, proving the monitor is processing
	// events. The stale write was issued first and the monitor handles events in
	// order, so by the time the barrier delivers the stale write has been seen
	// and skipped.
	require.Eventually(t, func() bool {
		writeProgress(t, barrierFile, "req-stale-barrier", 60)
		select {
		case resp := <-barrierCh:
			return resp.Header.Get("Nvcf-Percent-Complete") == "60"
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)

	// The stale write must never have produced a delivery.
	select {
	case resp := <-staleCh:
		t.Fatalf("did not expect a delivery for the stale-modtime write, got %s",
			resp.Header.Get("Nvcf-Percent-Complete"))
	default:
	}
}

// TestMonitorProgress_FileErrors exercises the monitor's file-level error
// branches that do not abort the loop: a dangling symlink named "progress"
// makes the os.Stat on the Create event fail, and an unreadable (mode 0000)
// "progress" file makes os.Open fail. Each lives in its own directory so the
// events are not coalesced. A separate healthy request provides liveness.
func TestMonitorProgress_FileErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode 0000 is readable by root; skipping os.Open error path")
	}
	dir := t.TempDir()
	m := New(dir)
	require.NoError(t, m.Start())

	liveCh := make(chan *http.Response, 256)
	m.Monitor("req-live", &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("req-live"),
		RespondToWork: liveCh,
	})

	statErrDir := filepath.Join(dir, "staterr")
	openErrDir := filepath.Join(dir, "openerr")
	liveDir := filepath.Join(dir, "livesub")
	require.NoError(t, os.Mkdir(statErrDir, 0777))
	require.NoError(t, os.Mkdir(openErrDir, 0777))
	require.NoError(t, os.Mkdir(liveDir, 0777))

	require.Eventually(t, func() bool {
		return m.WatchGaugeCallback() >= 4.0
	}, 2*time.Second, 5*time.Millisecond)

	// Dangling symlink named "progress": the Create event's os.Stat fails.
	require.NoError(t, os.Symlink(filepath.Join(dir, "no-such-target"),
		filepath.Join(statErrDir, "progress")))

	// Unreadable regular "progress" file: passes os.Stat (not a dir) but os.Open
	// fails with permission denied.
	require.NoError(t, os.WriteFile(filepath.Join(openErrDir, "progress"),
		[]byte(`{"id":"whatever","progress":1}`), 0000))

	// Drive the live request until it delivers, proving the monitor processed
	// events (the two error files were enqueued first and handled in order).
	liveFile := filepath.Join(liveDir, "progress")
	require.Eventually(t, func() bool {
		writeProgress(t, liveFile, "req-live", 70)
		select {
		case resp := <-liveCh:
			return resp.Header.Get("Nvcf-Percent-Complete") == "70"
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
}

// TestMonitorProgress_NonProgressFile writes a file that is not named
// "progress", which should be ignored by the monitor.
func TestMonitorProgress_NonProgressFile(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	require.NoError(t, m.Start())

	ch := make(chan *http.Response, 256)
	m.Monitor("req-x", &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("req-x"),
		RespondToWork: ch,
	})

	reqDir := filepath.Join(dir, "req-x")
	require.NoError(t, os.Mkdir(reqDir, 0777))

	require.Eventually(t, func() bool {
		return m.WatchGaugeCallback() >= 2.0
	}, 2*time.Second, 5*time.Millisecond)

	// A non-"progress" file is ignored; a subsequent real progress file is
	// delivered, acting as a barrier proving the irrelevant file was skipped.
	require.NoError(t, os.WriteFile(filepath.Join(reqDir, "other.txt"), []byte("ignored"), 0644))

	progressFile := filepath.Join(reqDir, "progress")
	require.Eventually(t, func() bool {
		writeProgress(t, progressFile, "req-x", 33)
		select {
		case resp := <-ch:
			assert.Equal(t, "33", resp.Header.Get("Nvcf-Percent-Complete"))
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
}

// TestMonitorProgress_InvalidJSON writes a progress file with malformed JSON,
// exercising the unmarshal error branch, then proves the monitor keeps running.
func TestMonitorProgress_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	require.NoError(t, m.Start())

	ch := make(chan *http.Response, 256)
	m.Monitor("req-j", &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("req-j"),
		RespondToWork: ch,
	})

	reqDir := filepath.Join(dir, "req-j")
	require.NoError(t, os.Mkdir(reqDir, 0777))

	require.Eventually(t, func() bool {
		return m.WatchGaugeCallback() >= 2.0
	}, 2*time.Second, 5*time.Millisecond)

	progressFile := filepath.Join(reqDir, "progress")
	require.NoError(t, os.WriteFile(progressFile, []byte("{not valid json"), 0644))

	// Monitor survived the bad write and still delivers a valid message later.
	require.Eventually(t, func() bool {
		writeProgress(t, progressFile, "req-j", 55)
		select {
		case resp := <-ch:
			assert.Equal(t, "55", resp.Header.Get("Nvcf-Percent-Complete"))
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
}

// TestMonitorProgress_ContextDone registers work with an already-cancelled
// context so the select in monitorProgress takes the ctx.Done() branch and
// never delivers on the unbuffered channel.
func TestMonitorProgress_ContextDone(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	require.NoError(t, m.Start())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done

	// Unbuffered channel with no reader: only the ctx.Done() case can proceed.
	doneCtxCh := make(chan *http.Response)
	m.Monitor("req-ctx", &MonitoredWork{
		Context:       ctx,
		MeteringEvent: newMeteringEvent("req-ctx"),
		RespondToWork: doneCtxCh,
	})

	// Also register a healthy request used only as a happens-after barrier:
	// once we observe its delivery, the monitor has fully processed at least
	// one progress event without ever blocking, which means the cancelled-ctx
	// event (written first) was handled via the ctx.Done() branch rather than
	// blocking forever on the unbuffered channel.
	barrierCh := make(chan *http.Response, 256)
	m.Monitor("req-barrier", &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: newMeteringEvent("req-barrier"),
		RespondToWork: barrierCh,
	})

	ctxDir := filepath.Join(dir, "ctxsub")
	barrierDir := filepath.Join(dir, "barriersub")
	require.NoError(t, os.Mkdir(ctxDir, 0777))
	require.NoError(t, os.Mkdir(barrierDir, 0777))

	require.Eventually(t, func() bool {
		return m.WatchGaugeCallback() >= 3.0
	}, 2*time.Second, 5*time.Millisecond)

	// Write the cancelled-ctx progress first.
	writeProgress(t, filepath.Join(ctxDir, "progress"), "req-ctx", 77)

	// Then drive the barrier until its delivery is observed.
	barrierFile := filepath.Join(barrierDir, "progress")
	require.Eventually(t, func() bool {
		writeProgress(t, barrierFile, "req-barrier", 88)
		select {
		case resp := <-barrierCh:
			assert.Equal(t, "88", resp.Header.Get("Nvcf-Percent-Complete"))
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)

	// The cancelled-ctx request must never have delivered a response.
	select {
	case <-doneCtxCh:
		t.Fatal("did not expect a delivered response when context is done")
	default:
	}
}
