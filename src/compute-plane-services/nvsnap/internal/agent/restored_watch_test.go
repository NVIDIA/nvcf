// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func fakeProcTree(t *testing.T, procs map[int][2]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, pc := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte("PPid:\t"+pc[0]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(pc[1]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestOnlyPlaceholderLeft(t *testing.T) {
	alive := fakeProcTree(t, map[int][2]string{100: {"50", "sh"}, 101: {"100", "sleep"}, 102: {"100", "python3"}, 103: {"102", "VLLM::Worker"}})
	if onlyPlaceholderLeft(alive, 100) {
		t.Error("a running restored engine read as gone")
	}
	dead := fakeProcTree(t, map[int][2]string{100: {"50", "sh"}, 101: {"100", "sleep"}, 300: {"50", "python3"}})
	if !onlyPlaceholderLeft(dead, 100) {
		t.Error("a placeholder with only its shell left read as running an engine (another container's process counted)")
	}
}
