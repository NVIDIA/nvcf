/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package tarstream

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteExtractRoundTrip(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "torchinductor", "k1"), 0o755)
	os.WriteFile(filepath.Join(src, "torchinductor", "k1", "kernel.so"), []byte("binary"), 0o755)
	os.WriteFile(filepath.Join(src, "autotune.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(src, ".marker"), nil, 0o644)
	os.Symlink("autotune.json", filepath.Join(src, "latest.json"))
	os.Symlink("/etc/passwd", filepath.Join(src, "escape"))
	var buf bytes.Buffer
	if err := Write(&buf, src, map[string]bool{".marker": true}); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	files, n, err := Extract(&buf, dest, true)
	if err != nil || files != 2 || n != int64(len("binary")+2) {
		t.Fatalf("extract: files=%d bytes=%d err=%v", files, n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "torchinductor", "k1", "kernel.so")); string(b) != "binary" {
		t.Errorf("kernel.so content %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dest, "torchinductor", "k1", "kernel.so")); fi.Mode().Perm() != 0o777 {
		t.Errorf("executable is world-writable and executable: %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(dest, "autotune.json")); fi.Mode().Perm() != 0o666 {
		t.Errorf("file is world-writable: %v", fi.Mode())
	}
	if l, err := os.Readlink(filepath.Join(dest, "latest.json")); err != nil || l != "autotune.json" {
		t.Errorf("relative symlink kept: %v %q", err, l)
	}
	for _, absent := range []string{"escape", ".marker"} {
		if _, err := os.Lstat(filepath.Join(dest, absent)); err == nil {
			t.Errorf("%s must not be extracted", absent)
		}
	}
	// Preserved modes when not world-writable.
	buf.Reset()
	Write(&buf, src, nil)
	dest2 := t.TempDir()
	if _, _, err := Extract(&buf, dest2, false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dest2, "autotune.json")); fi.Mode().Perm() != 0o644 {
		t.Errorf("archive mode kept: %v", fi.Mode())
	}
}
