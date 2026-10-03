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
	"time"
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

// Modification times survive the stream to the nanosecond: ninja-based
// JIT caches compare them with the build times in .ninja_log.
func TestWriteExtractPreservesModTimes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "op"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "op", "k.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 1, 5, 12, 33, 123456789, time.UTC)
	if err := os.Chtimes(filepath.Join(src, "op", "k.so"), want, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(src, "op"), want, want); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Extract(&buf, dst, true); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"op/k.so", "op"} {
		st, err := os.Stat(filepath.Join(dst, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().Equal(want) {
			t.Errorf("%s mtime = %s, want %s (nanoseconds included)", rel, st.ModTime().Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
		}
	}
}

// A skipped directory is left out with everything under it: the cache-set
// skip list names directories such as .ngc.
func TestWrite_SkipsDirectoryWithDescendants(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ngc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ngc", "config"), []byte("apikey = secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, dir, map[string]bool{".ngc": true}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	files, _, err := Extract(&buf, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("one file expected, got %d", files)
	}
	if _, err := os.Stat(filepath.Join(out, ".ngc", "config")); err == nil {
		t.Error(".ngc/config must not be in the stream")
	}
	if _, err := os.Stat(filepath.Join(out, "plan.json")); err != nil {
		t.Error("plan.json must be in the stream")
	}
}
