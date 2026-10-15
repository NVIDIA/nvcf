//go:build linux

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
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/sirupsen/logrus"
)

// TestPrepare_RealOverlay exercises the syscall mounter end-to-end:
// fake captured tree as lower, real overlay mount, write into upper,
// verify lower untouched, unmount via Cleanup.
//
// Requires CAP_SYS_ADMIN AND a non-stacked filesystem for the
// upper/work dirs. Skipped when:
//   - not running on linux
//   - not running as root (developer laptop)
//   - running inside bazel test sandbox: TempDir is on a stacked
//     tmpfs/bind-mount where kernel rejects overlay creation with
//     EINVAL (same fs-stacking issue we caught on GCP-H100-a — see
//     [[feedback_overlayfs_upper_layer_fs]]). The CI bazel sandbox
//     sets TEST_TMPDIR; bare `go test` does not.
func TestPrepare_RealOverlay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("overlay requires linux")
	}
	if syscall.Geteuid() != 0 {
		t.Skip("overlay mount requires root (CAP_SYS_ADMIN)")
	}
	if os.Getenv("TEST_TMPDIR") != "" {
		t.Skip("overlay mount inside bazel sandbox returns EINVAL (upper-on-stacked-fs)")
	}

	// Lower: pretend captured /root/.cache/vllm. Seed with a sentinel
	// file the workload would read.
	lower := t.TempDir()
	sentinel := filepath.Join(lower, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("captured"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)
	m := NewOverlayManager(root, log, nil) // nil → real syscallMounter

	mp, err := m.Prepare("pod-real", lower, "/root/.cache/vllm")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = m.Cleanup("pod-real") })

	// Read-through: sentinel is visible at the merged mountpoint.
	got, err := os.ReadFile(filepath.Join(mp, "sentinel.txt"))
	if err != nil {
		t.Fatalf("read sentinel through overlay: %v", err)
	}
	if string(got) != "captured" {
		t.Errorf("sentinel content = %q, want %q", got, "captured")
	}

	// Write-through: workload writes a new file. Goes to upper layer.
	written := filepath.Join(mp, "torch_compile_cache.bin")
	if err := os.WriteFile(written, []byte("runtime artifact"), 0o644); err != nil {
		t.Fatalf("write into overlay: %v", err)
	}

	// Lower must NOT see the runtime write — that's the whole point.
	lowerExtractDir := lower
	if _, err := os.Stat(filepath.Join(lowerExtractDir, "torch_compile_cache.bin")); !os.IsNotExist(err) {
		t.Errorf("lower captured tree got polluted with runtime write: %v", err)
	}

	// Cleanup unmounts + removes scratch.
	if err := m.Cleanup("pod-real"); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pod-real")); !os.IsNotExist(err) {
		t.Errorf("scratch tree survived Cleanup: %v", err)
	}
}
