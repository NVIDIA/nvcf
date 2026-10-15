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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// fakePreparer is an in-memory stub. Each PrepareOverlay sleeps `delay`
// (so tests can observe concurrency), records the call, and either
// returns success or — for paths in failPaths — an error.
type fakePreparer struct {
	delay     time.Duration
	failPaths map[string]bool

	current atomic.Int32
	peak    atomic.Int32

	mu      sync.Mutex
	cleaned []string
}

func (f *fakePreparer) PrepareOverlay(podUID, captureHash string, vol checkpointstore.VolumeMeta, targetNode string) (string, error) {
	n := f.current.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer f.current.Add(-1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.failPaths[vol.MountPath] {
		return "", fmt.Errorf("simulated failure for %s", vol.MountPath)
	}
	return "/merged" + vol.MountPath, nil
}

func (f *fakePreparer) CleanupOverlayForPod(podUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleaned = append(f.cleaned, podUID)
	return nil
}

func makeMounts(n int) []checkpointstore.VolumeMeta {
	out := make([]checkpointstore.VolumeMeta, n)
	for i := range out {
		out[i] = checkpointstore.VolumeMeta{
			Name:      fmt.Sprintf("m%d", i),
			MountPath: fmt.Sprintf("/p/m%d", i),
			Type:      "rootfs-extract",
		}
	}
	return out
}

func waitFor(t *testing.T, m *PrepJobManager, podUID string, want PrepState, timeout time.Duration) PrepStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s, ok := m.Status(podUID)
		if ok && s.State == want {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	s, _ := m.Status(podUID)
	t.Fatalf("timed out waiting for state %q on podUID %q (last state=%q prepared=%d/%d failures=%d)",
		want, podUID, s.State, s.Prepared, s.Total, len(s.Failures))
	return s
}

// TestPrepJobManager_HappyPath: 32 mounts succeed, state goes
// preparing → ready, all PrepareOverlay calls were made.
func TestPrepJobManager_HappyPath(t *testing.T) {
	fp := &fakePreparer{delay: 5 * time.Millisecond}
	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)
	m := NewPrepJobManager(fp, log)

	mounts := makeMounts(32)
	req := PrepRequest{PodUID: "pod-1", CaptureHash: "abc123def4567890abc123def4567890abc123def4567890abc123def4567890", Mounts: mounts}
	first := m.Start(context.Background(), req)
	if first.State != PrepStatePreparing {
		t.Fatalf("first POST: want state=preparing, got %q", first.State)
	}
	if first.Total != 32 {
		t.Fatalf("first POST: want total=32, got %d", first.Total)
	}

	final := waitFor(t, m, "pod-1", PrepStateReady, 5*time.Second)
	if final.Prepared != 32 {
		t.Errorf("prepared=%d, want 32", final.Prepared)
	}
	if len(final.Failures) != 0 {
		t.Errorf("failures=%v, want none", final.Failures)
	}
	if final.CompletedAt == nil {
		t.Errorf("CompletedAt is nil but state is ready")
	}
}

// TestPrepJobManager_IsParallel: 32 mounts × 50ms with 16 workers
// must complete in <500ms (serial would be ≥1600ms). Confirms
// the bounded-fanout work pool runs concurrently.
func TestPrepJobManager_IsParallel(t *testing.T) {
	fp := &fakePreparer{delay: 50 * time.Millisecond}
	m := NewPrepJobManager(fp, nil)

	req := PrepRequest{PodUID: "pod-2", CaptureHash: "h", Mounts: makeMounts(32)}
	start := time.Now()
	m.Start(context.Background(), req)
	waitFor(t, m, "pod-2", PrepStateReady, 5*time.Second)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("32 × 50ms with 16 workers took %v (expected <500ms; serial would be ≥1600ms)", elapsed)
	}
	if peak := fp.peak.Load(); peak < 2 {
		t.Errorf("peak in-flight = %d, want ≥2 (serial behavior)", peak)
	}
}

// TestPrepJobManager_Idempotent: second POST for the same podUID
// returns the existing job's state, NOT a new one. Critical
// because the init container may retry POST on transient HTTP
// errors and must not race the agent into a parallel mount run.
func TestPrepJobManager_Idempotent(t *testing.T) {
	fp := &fakePreparer{delay: 100 * time.Millisecond}
	m := NewPrepJobManager(fp, nil)

	req := PrepRequest{PodUID: "pod-3", CaptureHash: "h", Mounts: makeMounts(8)}
	first := m.Start(context.Background(), req)

	// Re-POST immediately. Must return the same job (same StartedAt)
	// and not have spawned a second goroutine — we'll verify by
	// counting PrepareOverlay calls below.
	second := m.Start(context.Background(), req)
	if !second.StartedAt.Equal(first.StartedAt) {
		t.Errorf("second POST returned different StartedAt: %v vs %v", second.StartedAt, first.StartedAt)
	}

	waitFor(t, m, "pod-3", PrepStateReady, 2*time.Second)
	// Total PrepareOverlay calls must be exactly 8 (the manifest size),
	// not 16. We approximate by observing peak in-flight ≤ 8.
	if peak := fp.peak.Load(); peak > 8 {
		t.Errorf("peak in-flight = %d, want ≤8 (second POST should not double the work)", peak)
	}
}

// TestPrepJobManager_PartialFailure: 2 out of 8 mounts fail; state
// goes to failed; Failures lists the bad paths; init container
// can read the error reason and exit non-zero with details.
func TestPrepJobManager_PartialFailure(t *testing.T) {
	fp := &fakePreparer{
		delay: 5 * time.Millisecond,
		failPaths: map[string]bool{
			"/p/m1": true,
			"/p/m5": true,
		},
	}
	m := NewPrepJobManager(fp, nil)

	req := PrepRequest{PodUID: "pod-4", CaptureHash: "h", Mounts: makeMounts(8)}
	m.Start(context.Background(), req)

	final := waitFor(t, m, "pod-4", PrepStateFailed, 2*time.Second)
	if final.Prepared != 6 {
		t.Errorf("prepared=%d, want 6", final.Prepared)
	}
	if len(final.Failures) != 2 {
		t.Fatalf("failures=%d, want 2: %+v", len(final.Failures), final.Failures)
	}
	gotPaths := map[string]bool{}
	for _, f := range final.Failures {
		gotPaths[f.Path] = true
		if f.Error == "" {
			t.Errorf("failure for %q has empty error string", f.Path)
		}
	}
	for _, want := range []string{"/p/m1", "/p/m5"} {
		if !gotPaths[want] {
			t.Errorf("missing failure for %q in %v", want, gotPaths)
		}
	}
}

// TestPrepJobManager_EmptyMountsReady: a manifest with no rootfs-extract
// paths AND no user-data volumes is a valid case (e.g. a workload
// that only writes to non-captured paths). Job goes straight to
// Ready without spawning any goroutine, so the init container
// exits immediately and the pod starts fast.
func TestPrepJobManager_EmptyMountsReady(t *testing.T) {
	fp := &fakePreparer{}
	m := NewPrepJobManager(fp, nil)
	got := m.Start(context.Background(), PrepRequest{PodUID: "pod-empty", CaptureHash: "h", Mounts: nil})
	if got.State != PrepStateReady {
		t.Errorf("state=%q, want ready (empty mounts)", got.State)
	}
	if got.CompletedAt == nil {
		t.Errorf("CompletedAt nil for empty-mounts shortcut")
	}
}

// TestPrepJobManager_StatusMiss: GET status before any POST → not found.
// HTTP layer maps this to 404; init container treats it as "agent
// hasn't seen my POST yet, retry."
func TestPrepJobManager_StatusMiss(t *testing.T) {
	m := NewPrepJobManager(&fakePreparer{}, nil)
	if _, ok := m.Status("nope"); ok {
		t.Fatal("Status(nope) should miss before any Start")
	}
}

// TestPrepJobManager_Cleanup: removes the job + calls
// CleanupOverlayForPod on the preparer (so per-pod scratch trees
// are torn down).
func TestPrepJobManager_Cleanup(t *testing.T) {
	fp := &fakePreparer{}
	m := NewPrepJobManager(fp, nil)
	m.Start(context.Background(), PrepRequest{PodUID: "pod-clean", CaptureHash: "h", Mounts: makeMounts(2)})
	waitFor(t, m, "pod-clean", PrepStateReady, 2*time.Second)

	if err := m.Cleanup("pod-clean"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Status("pod-clean"); ok {
		t.Errorf("Status should miss after Cleanup")
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.cleaned) != 1 || fp.cleaned[0] != "pod-clean" {
		t.Errorf("CleanupOverlayForPod calls = %v, want [pod-clean]", fp.cleaned)
	}
}

// TestPrepJobManager_PendingJobs: the startup-sweep helper for
// surfacing in-flight jobs after agent restart.
func TestPrepJobManager_PendingJobs(t *testing.T) {
	m := NewPrepJobManager(&fakePreparer{delay: 100 * time.Millisecond}, nil)
	m.Start(context.Background(), PrepRequest{PodUID: "p1", CaptureHash: "h", Mounts: makeMounts(4)})
	m.Start(context.Background(), PrepRequest{PodUID: "p2", CaptureHash: "h", Mounts: makeMounts(4)})

	pending := m.PendingJobs()
	gotSet := map[string]bool{}
	for _, p := range pending {
		gotSet[p] = true
	}
	for _, want := range []string{"p1", "p2"} {
		if !gotSet[want] {
			t.Errorf("PendingJobs missing %q: got %v", want, pending)
		}
	}
}

// guard against accidental use of stdlib errors.Is on the sentinel.
var _ = errors.Is
