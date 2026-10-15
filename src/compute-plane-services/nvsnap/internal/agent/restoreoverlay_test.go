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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

// recordingMounter records every mount/unmount call so tests can assert
// what the manager *would* have done without needing CAP_SYS_ADMIN.
type recordingMounter struct {
	mu       sync.Mutex
	mounts   []recordedMount
	unmounts []string
	mountErr error
}

type recordedMount struct {
	target, lower, upper, work string
}

func (r *recordingMounter) Mount(target, lower, upper, work string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mountErr != nil {
		return r.mountErr
	}
	r.mounts = append(r.mounts, recordedMount{target, lower, upper, work})
	return nil
}

func (r *recordingMounter) Unmount(target string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unmounts = append(r.unmounts, target)
	return nil
}

func newTestManager(t *testing.T) (*OverlayManager, *recordingMounter, string) {
	t.Helper()
	root := t.TempDir()
	rec := &recordingMounter{}
	log := logrus.New()
	log.SetOutput(os.Stderr)
	log.SetLevel(logrus.WarnLevel)
	return NewOverlayManager(root, log, rec), rec, root
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPrepare_MountsAndCreatesScratchTree(t *testing.T) {
	m, rec, root := newTestManager(t)
	lower := filepath.Join(t.TempDir(), "lower")
	mustMkdir(t, lower)

	mp, err := m.Prepare("pod-uid-1", lower, "/root/.cache/vllm")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	expectMp := filepath.Join(root, "pod-uid-1", "merged", "root", ".cache", "vllm")
	if mp != expectMp {
		t.Errorf("mountpoint = %q, want %q", mp, expectMp)
	}
	for _, sub := range []string{"merged", "upper", "work"} {
		p := filepath.Join(root, "pod-uid-1", sub, "root", ".cache", "vllm")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %q to exist: %v", p, err)
		}
	}
	if len(rec.mounts) != 1 {
		t.Fatalf("expected 1 mount call, got %d", len(rec.mounts))
	}
	if rec.mounts[0].lower != lower {
		t.Errorf("lower = %q, want %q", rec.mounts[0].lower, lower)
	}
}

func TestPrepare_Idempotent(t *testing.T) {
	m, rec, _ := newTestManager(t)
	lower := filepath.Join(t.TempDir(), "lower")
	mustMkdir(t, lower)

	mp1, err := m.Prepare("pod-x", lower, "/root/.cache/vllm")
	if err != nil {
		t.Fatal(err)
	}
	mp2, err := m.Prepare("pod-x", lower, "/root/.cache/vllm")
	if err != nil {
		t.Fatal(err)
	}
	if mp1 != mp2 {
		t.Errorf("idempotent prepare returned different paths: %q vs %q", mp1, mp2)
	}
	if len(rec.mounts) != 1 {
		t.Errorf("idempotent prepare should mount only once, got %d", len(rec.mounts))
	}
}

func TestPrepare_MultipleExtractPathsPerPod(t *testing.T) {
	m, rec, _ := newTestManager(t)
	lower := filepath.Join(t.TempDir(), "lower")
	mustMkdir(t, lower)

	for _, ep := range []string{"/root/.cache/vllm", "/root/.cache/huggingface", "/root/.triton"} {
		if _, err := m.Prepare("pod-multi", lower, ep); err != nil {
			t.Fatalf("Prepare(%q): %v", ep, err)
		}
	}
	if len(rec.mounts) != 3 {
		t.Errorf("expected 3 mounts, got %d", len(rec.mounts))
	}
}

func TestPrepare_Rejects(t *testing.T) {
	m, _, _ := newTestManager(t)
	good := t.TempDir()

	cases := []struct {
		name        string
		pod         string
		lower       string
		extractPath string
	}{
		{"empty pod-uid", "", good, "/x"},
		{"pod-uid with slash", "a/b", good, "/x"},
		{"pod-uid with dotdot", "..", good, "/x"},
		{"relative extract", "p", good, "x"},
		{"extract with dotdot", "p", good, "/x/../etc"},
		{"relative lower", "p", "rel", "/x"},
		{"nonexistent lower", "p", "/no/such/thing", "/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Prepare(tc.pod, tc.lower, tc.extractPath); err == nil {
				t.Errorf("expected error for %q", tc.name)
			}
		})
	}
}

func TestPrepare_MountError(t *testing.T) {
	m, rec, root := newTestManager(t)
	lower := t.TempDir()
	rec.mountErr = fmt.Errorf("simulated mount failure")

	if _, err := m.Prepare("pod-err", lower, "/x"); err == nil {
		t.Fatal("expected mount error to propagate")
	}
	// Bookkeeping must be empty so a retry doesn't think mount succeeded.
	if got := filepath.Join(root, "pod-err"); pathHasMountedExtracts(t, m, "pod-err") {
		_ = got
		t.Error("mount failure must not leave mounts map populated for the pod")
	}
}

func TestCleanup_UnmountsAndRemovesScratch(t *testing.T) {
	m, rec, root := newTestManager(t)
	lower := t.TempDir()
	for _, ep := range []string{"/a", "/b"} {
		if _, err := m.Prepare("pod-c", lower, ep); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Cleanup("pod-c"); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(rec.unmounts) != 2 {
		t.Errorf("expected 2 unmount calls, got %d", len(rec.unmounts))
	}
	if _, err := os.Stat(filepath.Join(root, "pod-c")); !os.IsNotExist(err) {
		t.Errorf("scratch tree still exists: %v", err)
	}
	// Repeat call is a no-op.
	if err := m.Cleanup("pod-c"); err != nil {
		t.Errorf("repeat Cleanup: %v", err)
	}
}

// TestCleanup_AfterAgentRestart is the regression test for the bug we
// observed on 2026-06-08: nvsnap-system_bench-whisper-rootfs-restored
// overlay survived 4 agent rollouts because Cleanup only iterated the
// in-memory m.mounts map (empty post-restart). Simulate that by
// constructing a fresh OverlayManager whose m.mounts has never been
// populated, but whose Root contains a per-pod dir on disk. Cleanup
// must still remove the dir AND attempt unmount on any discovered
// mountinfo entries.
//
// We can't easily inject /proc/self/mountinfo from a unit test, so this
// asserts the recovery property that's testable: the dir gets removed
// even when m.mounts is empty. The mountinfo path is covered by the
// live cluster test in the MR description (whisper rootfs-restored
// post-rollout cleanup).
func TestCleanup_AfterAgentRestart(t *testing.T) {
	m, _, root := newTestManager(t)

	// Simulate the post-restart state: an overlay dir exists on disk
	// (from the previous agent's Prepare) but m.mounts is empty (this
	// new agent never saw the create event).
	key := "nvsnap-system_bench-whisper-rootfs-restored"
	for _, sub := range []string{"merged/opt/nim/.cache", "upper/opt/nim/.cache", "work/opt/nim/.cache"} {
		mustMkdir(t, filepath.Join(root, key, sub))
	}

	if err := m.Cleanup(key); err != nil {
		t.Fatalf("Cleanup post-restart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, key)); !os.IsNotExist(err) {
		t.Errorf("scratch tree survived Cleanup: %v", err)
	}
}

// TestSweep_AcceptsNsNameKey confirms Sweep keeps a dir alive when the
// active set contains the ns_name form (not the UID). The webhook keys
// by ns_name on CREATE admission (pod.UID is empty there), so the
// agent's live-pod set MUST include both forms — otherwise Sweep
// thinks ns_name-keyed dirs are orphans and tears them down out from
// under live pods.
func TestSweep_AcceptsNsNameKey(t *testing.T) {
	m, _, root := newTestManager(t)
	lower := t.TempDir()
	if _, err := m.Prepare("default_my-restore-pod", lower, "/cache"); err != nil {
		t.Fatal(err)
	}
	// Active set contains ONLY the ns_name (matches the webhook's key
	// when admission.UID was empty). Sweep must treat the dir as alive.
	active := map[string]struct{}{"default_my-restore-pod": {}}
	if err := m.Sweep(active); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "default_my-restore-pod")); err != nil {
		t.Errorf("ns_name-keyed live pod's overlay got swept: %v", err)
	}
}

func TestSweep_RemovesOrphans(t *testing.T) {
	m, _, root := newTestManager(t)
	lower := t.TempDir()
	for _, uid := range []string{"alive", "orphan1", "orphan2"} {
		if _, err := m.Prepare(uid, lower, "/x"); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Sweep(map[string]struct{}{"alive": {}}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "alive")); err != nil {
		t.Errorf("alive pod was cleaned up: %v", err)
	}
	for _, uid := range []string{"orphan1", "orphan2"} {
		if _, err := os.Stat(filepath.Join(root, uid)); !os.IsNotExist(err) {
			t.Errorf("orphan %q not cleaned up: %v", uid, err)
		}
	}
}

func TestSweep_EmptyRootIsNoOp(t *testing.T) {
	m, _, _ := newTestManager(t)
	if err := m.Sweep(map[string]struct{}{}); err != nil {
		t.Errorf("Sweep on empty root: %v", err)
	}
}

func TestMountpointFor(t *testing.T) {
	m, _, root := newTestManager(t)
	mp, err := m.MountpointFor("pod-abc", "/root/.cache/vllm")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "pod-abc", "merged", "root", ".cache", "vllm")
	if mp != want {
		t.Errorf("mountpoint = %q, want %q", mp, want)
	}
}

// helper: peek at internal map state.
func pathHasMountedExtracts(t *testing.T, m *OverlayManager, podUID string) bool {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.mounts[podUID]) > 0
}
