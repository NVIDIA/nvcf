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
	"sort"
	"strconv"
	"syscall"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// fakeContainerRoot builds a <procRoot>/<fixturePID>/root tree and returns
// procRoot. The PID is fixed: nothing under test varies with it.
const fixturePID = 7

func fakeContainerRoot(t *testing.T, dirs ...string) string {
	t.Helper()
	procRoot := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(procRoot, strconv.Itoa(fixturePID), "root", d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return procRoot
}

func byPath(dirs []checkpointstore.EntryRuntimeDir) map[string]checkpointstore.EntryRuntimeDir {
	out := make(map[string]checkpointstore.EntryRuntimeDir, len(dirs))
	for _, d := range dirs {
		out[d.Path] = d
	}
	return out
}

// Recording the paths is not enough: restore recreates these directories with
// the recorded mode and ownership, so metadata that is silently zero would
// produce directories the workload cannot write to.
func TestReadEntryRuntimeDirs(t *testing.T) {
	procRoot := fakeContainerRoot(t, "run/vllm", "run/lock/sub", "var/run/other")

	// A non-default mode the umask would not produce by accident.
	src := filepath.Join(procRoot, strconv.Itoa(fixturePID), "root", "run", "vllm")
	if err := os.Chmod(src, 0o731); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(src, &st); err != nil {
		t.Fatalf("stat: %v", err)
	}

	got := byPath(readEntryRuntimeDirs(procRoot, fixturePID))

	for _, want := range []string{"/run/vllm", "/run/lock", "/run/lock/sub", "/var/run/other"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s; got %v", want, keys(got))
		}
	}
	d, ok := got["/run/vllm"]
	if !ok {
		t.Fatal("/run/vllm not recorded")
	}
	if d.Mode != 0o731 {
		t.Errorf("Mode = %o, want 731 (restore recreates with this)", d.Mode)
	}
	if d.UID != st.Uid || d.GID != st.Gid {
		t.Errorf("UID/GID = %d/%d, want %d/%d (source ownership)", d.UID, d.GID, st.Uid, st.Gid)
	}
}

// The depth bound must prune at the boundary, not before it: a walker that
// stopped one level early would satisfy an absence-only assertion.
func TestReadEntryRuntimeDirsRespectsDepth(t *testing.T) {
	procRoot := fakeContainerRoot(t, "run/a/b/c/d/e/f")

	got := byPath(readEntryRuntimeDirs(procRoot, fixturePID))

	if _, ok := got["/run/a/b/c/d"]; !ok {
		t.Errorf("/run/a/b/c/d is within the depth bound and must be recorded; got %v", keys(got))
	}
	for _, tooDeep := range []string{"/run/a/b/c/d/e", "/run/a/b/c/d/e/f"} {
		if _, ok := got[tooDeep]; ok {
			t.Errorf("recorded %s beyond the depth bound", tooDeep)
		}
	}
}

// A workload can put a large tree under /run. Assert the exact cap: accepting
// "at most maxRuntimeDirs" would also pass for a walk that recorded nothing.
func TestReadEntryRuntimeDirsStopsAtCap(t *testing.T) {
	dirs := make([]string, 0, maxRuntimeDirs*4)
	for i := 0; i < maxRuntimeDirs*4; i++ {
		dirs = append(dirs, fmt.Sprintf("run/d%03d", i))
	}
	procRoot := fakeContainerRoot(t, dirs...)

	if got := readEntryRuntimeDirs(procRoot, fixturePID); len(got) != maxRuntimeDirs {
		t.Fatalf("recorded %d dirs, want exactly %d", len(got), maxRuntimeDirs)
	}
}

// A missing /run (or an unreadable one) yields no entries rather than failing
// the capture: most workloads need none of this.
func TestReadEntryRuntimeDirsMissingRoots(t *testing.T) {
	procRoot := fakeContainerRoot(t, "opt/only")

	if got := readEntryRuntimeDirs(procRoot, fixturePID); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func keys(m map[string]checkpointstore.EntryRuntimeDir) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
