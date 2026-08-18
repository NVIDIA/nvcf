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

package rootfsonly

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// fakeContainerRoot builds a <procRoot>/<pid>/root tree and returns procRoot.
func fakeContainerRoot(t *testing.T, pid string, dirs ...string) string {
	t.Helper()
	procRoot := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(procRoot, pid, "root", d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return procRoot
}

func recordedPaths(dirs []checkpointstore.EntryRuntimeDir) map[string]bool {
	out := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		out[d.Path] = true
	}
	return out
}

func TestReadEntryRuntimeDirs(t *testing.T) {
	procRoot := fakeContainerRoot(t, "7",
		"run/vllm", "run/lock/sub", "var/run/other")

	got := recordedPaths(readEntryRuntimeDirs(procRoot, 7))

	for _, want := range []string{"/run/vllm", "/run/lock", "/run/lock/sub", "/var/run/other"} {
		if !got[want] {
			t.Errorf("missing %s; got %v", want, got)
		}
	}
}

// The depth bound must prune rather than record arbitrarily deep trees.
func TestReadEntryRuntimeDirsRespectsDepth(t *testing.T) {
	procRoot := fakeContainerRoot(t, "7", "run/a/b/c/d/e/f")

	for p := range recordedPaths(readEntryRuntimeDirs(procRoot, 7)) {
		if p == "/run/a/b/c/d/e" || p == "/run/a/b/c/d/e/f" {
			t.Errorf("recorded %s beyond the depth bound", p)
		}
	}
}

// A workload can put a large tree under /run. The cap must bound what we
// record; the walk terminating (rather than continuing and discarding) is what
// keeps capture latency bounded, and the observable contract is that we stop at
// exactly maxRuntimeDirs.
func TestReadEntryRuntimeDirsStopsAtCap(t *testing.T) {
	dirs := make([]string, 0, maxRuntimeDirs*4)
	for i := 0; i < maxRuntimeDirs*4; i++ {
		dirs = append(dirs, fmt.Sprintf("run/d%03d", i))
	}
	procRoot := fakeContainerRoot(t, "7", dirs...)

	got := readEntryRuntimeDirs(procRoot, 7)
	if len(got) > maxRuntimeDirs {
		t.Fatalf("recorded %d dirs, want at most %d", len(got), maxRuntimeDirs)
	}
}

// A missing /run (or an unreadable one) yields no entries rather than failing
// the capture: most workloads need none of this.
func TestReadEntryRuntimeDirsMissingRoots(t *testing.T) {
	procRoot := fakeContainerRoot(t, "7", "opt/only")

	if got := readEntryRuntimeDirs(procRoot, 7); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}
