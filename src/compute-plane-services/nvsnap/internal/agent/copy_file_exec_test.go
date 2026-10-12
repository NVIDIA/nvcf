// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// A staged tool is executable even where a file of another mode was
// already at its path, as a rootfs diff replayed into a placeholder leaves.
func TestCopyFileExecOverExistingFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "criu")
	if err := os.WriteFile(src, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFileExec(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v (%v), want 0755", fi.Mode().Perm(), err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Errorf("content = %q", b)
	}
}
