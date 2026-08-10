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

package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// fakeBackend is a hand-rolled stub for checkpointstore.Backend. We
// don't pull in gomock for one method — the surface is small and the
// behaviour we want to assert (called/not-called, blocking, error,
// panic) is direct enough that ad-hoc atomic counters are clearer
// than a generated mock would be.
type fakeBackend struct {
	called       atomic.Int32
	delay        time.Duration
	returnErr    error
	panicWith    any
	gotHash      atomic.Value // string
	gotSourceDir atomic.Value // string
	done         chan struct{}
}

func newFakeBackend() *fakeBackend { return &fakeBackend{done: make(chan struct{})} }

func (f *fakeBackend) Put(ctx context.Context, hash string, sources []checkpointstore.CaptureSource, _ checkpointstore.Manifest) (checkpointstore.Manifest, error) {
	defer close(f.done)
	f.called.Add(1)
	f.gotHash.Store(hash)
	if len(sources) > 0 {
		f.gotSourceDir.Store(sources[0].SrcPath)
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return checkpointstore.Manifest{}, ctx.Err()
		}
	}
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return checkpointstore.Manifest{Hash: hash}, f.returnErr
}

// fakeBackend only exercises Store.Put (the surface runL2PromoteAsync
// actually uses). The Backend interface also requires Get/Stat/Delete
// (Store) and Mount (Mounter) for full restore-side coverage; the
// promote path never calls them, so we satisfy the type with panicking
// stubs that surface loudly if an accidental call is ever made.
func (f *fakeBackend) Get(_ context.Context, _, _ string) (checkpointstore.Manifest, error) {
	panic("fakeBackend.Get not used by promote path")
}
func (f *fakeBackend) Stat(_ context.Context, _ string) (checkpointstore.Manifest, error) {
	panic("fakeBackend.Stat not used by promote path")
}
func (f *fakeBackend) Delete(_ context.Context, _ string) error {
	panic("fakeBackend.Delete not used by promote path")
}
func (f *fakeBackend) Mount(_ context.Context, _ string, _ checkpointstore.VolumeMeta) (checkpointstore.PodMount, error) {
	panic("fakeBackend.Mount not used by promote path")
}

// silentLog returns a logrus entry whose output is discarded — tests
// don't want test stdout polluted by Warn lines, but we still exercise
// the same code paths that log in production.
func silentLog() *logrus.Entry {
	l := logrus.New()
	l.SetOutput(devNull{})
	l.SetLevel(logrus.PanicLevel + 1) // suppress all
	return l.WithField("test", "true")
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

// TestRunL2PromoteAsync_NilBackend verifies the "no L2 configured"
// short-circuit. Filestore-only clusters and dev environments set
// l2Backend=nil; the capture handler must call this with the nil and
// have it be a no-op (no goroutine, no panic, immediate return).
func TestRunL2PromoteAsync_NilBackend(t *testing.T) {
	t.Parallel()
	// No goroutine should be spawned; if it were, the test would
	// race with goroutine-leak detection in -race mode.
	runL2PromoteAsync(nil, silentLog(), l2PromoteInput{
		Hash:        "abcdef0123456789",
		HostDumpDir: "/host/var/lib/nvsnap/checkpoints/foo",
		CapturedAt:  time.Now(),
	})
	// If we reach here without blocking or panicking, the
	// nil-backend short-circuit works.
}

// TestRunL2PromoteAsync_EmptyHash skips when the hash is blank. This
// is a programmer-error guard — every CRIU capture has a hash by the
// time it reaches L2 promote — but the guard exists so an upstream
// regression doesn't try to provision rwx-/snap-/rox- with an empty
// suffix.
func TestRunL2PromoteAsync_EmptyHash(t *testing.T) {
	t.Parallel()
	fb := newFakeBackend()
	runL2PromoteAsync(fb, silentLog(), l2PromoteInput{
		Hash:        "",
		HostDumpDir: "/host/foo",
		CapturedAt:  time.Now(),
	})
	// Give the bad-input path a beat in case the guard is missing
	// and a goroutine actually starts. 50ms is far longer than the
	// guard check needs.
	time.Sleep(50 * time.Millisecond)
	if fb.called.Load() != 0 {
		t.Fatalf("Backend.Put called for empty hash; expected guard to short-circuit (calls=%d)", fb.called.Load())
	}
}

// TestRunL2PromoteAsync_EmptyHostDumpDir skips when the path
// translation failed. checkpointHostPath returns an error if
// CheckpointHostDir is unset; the capture handler logs and passes
// "" through. We don't want to provision PVCs for that case.
func TestRunL2PromoteAsync_EmptyHostDumpDir(t *testing.T) {
	t.Parallel()
	fb := newFakeBackend()
	runL2PromoteAsync(fb, silentLog(), l2PromoteInput{
		Hash:        "deadbeef",
		HostDumpDir: "",
		CapturedAt:  time.Now(),
	})
	time.Sleep(50 * time.Millisecond)
	if fb.called.Load() != 0 {
		t.Fatalf("Backend.Put called with empty host dump dir; expected guard to short-circuit (calls=%d)", fb.called.Load())
	}
}

// TestRunL2PromoteAsync_ReturnsImmediately is the central regression
// test for nvsnap#166: the caller must NOT block on Backend.Put. If
// the agent waits for promote, the nvsnap-server HTTP timeout race
// (server.go:81, 10 min) re-opens.
func TestRunL2PromoteAsync_ReturnsImmediately(t *testing.T) {
	t.Parallel()
	fb := newFakeBackend()
	fb.delay = 2 * time.Second // simulate a slow promote
	start := time.Now()
	runL2PromoteAsync(fb, silentLog(), l2PromoteInput{
		Hash:        "0e6d08905a606c1f",
		HostDumpDir: "/host/var/lib/nvsnap/checkpoints/foo",
		CapturedAt:  time.Now(),
	})
	elapsed := time.Since(start)

	// Return time must be << the simulated promote delay. Allow some
	// CI slack but reject anything close to delay/2.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("runL2PromoteAsync blocked for %v (Put delay=%v); should return immediately while goroutine runs in background", elapsed, fb.delay)
	}

	// And the goroutine must actually run — wait for it.
	select {
	case <-fb.done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatalf("Backend.Put goroutine never completed within 5s; goroutine wasn't spawned")
	}
	if fb.called.Load() != 1 {
		t.Fatalf("expected Backend.Put called exactly once, got %d", fb.called.Load())
	}
	if got := fb.gotHash.Load(); got != "0e6d08905a606c1f" {
		t.Fatalf("hash mismatch: got %v, want 0e6d08905a606c1f", got)
	}
	if got := fb.gotSourceDir.Load(); got != "/host/var/lib/nvsnap/checkpoints/foo" {
		t.Fatalf("source dir mismatch: got %v, want /host/var/lib/nvsnap/checkpoints/foo", got)
	}
}

// TestRunL2PromoteAsync_BackendError documents the failure path: a
// promote error MUST NOT propagate to the caller (the HTTP response
// has already been sent) and MUST NOT panic the agent.
func TestRunL2PromoteAsync_BackendError(t *testing.T) {
	t.Parallel()
	fb := newFakeBackend()
	fb.returnErr = errors.New("rwx PVC create timed out")
	runL2PromoteAsync(fb, silentLog(), l2PromoteInput{
		Hash:        "abcd1234",
		HostDumpDir: "/host/foo",
		CapturedAt:  time.Now(),
	})
	select {
	case <-fb.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Backend.Put never ran")
	}
	// The whole point: the test reaches here without crashing.
}

// TestRunL2PromoteAsync_BackendPanic verifies the recover() in the
// goroutine. A nil pointer in PerCapturePVCBackend (e.g. a dropped
// k8s client) used to bring down the entire agent, killing every
// other capture on the node. The recover keeps the DaemonSet up.
func TestRunL2PromoteAsync_BackendPanic(t *testing.T) {
	t.Parallel()
	fb := newFakeBackend()
	fb.panicWith = "simulated nil-pointer in Backend.Put"
	runL2PromoteAsync(fb, silentLog(), l2PromoteInput{
		Hash:        "abcd1234",
		HostDumpDir: "/host/foo",
		CapturedAt:  time.Now(),
	})
	select {
	case <-fb.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Backend.Put never ran")
	}
	// Give the goroutine an extra moment to actually execute the
	// recover() defer (close(done) fires BEFORE the panic unwinds).
	time.Sleep(50 * time.Millisecond)
	// If recover isn't in place, this test process would already
	// be terminated by the time we got here.
}
