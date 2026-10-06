/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package tarstream

import (
	"archive/tar"
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

// tarOf builds a stream from raw entries, for archives Write never emits.
func tarOf(t *testing.T, entries ...*tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		body := []byte(nil)
		if h.Typeflag == tar.TypeReg {
			body = []byte("overwritten")
			h.Size = int64(len(body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != nil {
			tw.Write(body)
		}
	}
	tw.Close()
	return &buf
}

// Each link passes the lexical check on its own, but "d" points at the
// destination itself, so "d/f -> ../victim" resolves outside it, and a
// regular-file entry at "d/f" would then truncate a file next to the
// destination. Extraction refuses entries under a symlinked directory.
func TestExtract_RefusesWritesThroughSymlinkedParents(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "rank")
	victim := filepath.Join(root, "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	stream := tarOf(t,
		&tar.Header{Name: "d", Typeflag: tar.TypeSymlink, Linkname: "."},
		&tar.Header{Name: "d/f", Typeflag: tar.TypeSymlink, Linkname: "../victim"},
		&tar.Header{Name: "d/f", Typeflag: tar.TypeReg},
	)
	if _, _, err := Extract(stream, dest, false); err == nil {
		t.Error("an entry under a symlinked directory must be refused")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("extraction escaped the destination: victim now %q", b)
	}
}

// A file entry replaces a symlink left at its path (an earlier partial
// extraction) instead of writing through it.
func TestExtract_ReplacesSymlinkAtFilePath(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "rank")
	os.MkdirAll(dest, 0o755)
	victim := filepath.Join(root, "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	os.Symlink(victim, filepath.Join(dest, "f"))
	if _, _, err := Extract(tarOf(t, &tar.Header{Name: "f", Typeflag: tar.TypeReg}), dest, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("file entry wrote through a symlink: victim now %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(dest, "f")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("f must be a regular file now: %v %v", fi, err)
	}
}

// A retried transfer starts from an empty tree: whatever an interrupted
// attempt left is not merged into the result, and a failed attempt leaves
// the previous tree and no staging behind.
func TestExtractFresh_PublishesOnlyCompleteTrees(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "1")
	os.MkdirAll(dest, 0o755)
	os.Symlink("../victim", filepath.Join(dest, "left-by-partial"))

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "kernel.so"), []byte("binary"), 0o644)
	var buf bytes.Buffer
	if err := Write(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	full := buf.Bytes()

	// Interrupted stream: an error, and the previous tree is untouched.
	if _, _, err := ExtractFresh(bytes.NewReader(full[:len(full)/2]), dest, false); err == nil {
		t.Fatal("a truncated stream must fail")
	}
	if _, err := os.Lstat(filepath.Join(dest, "left-by-partial")); err != nil {
		t.Errorf("a failed attempt must not touch the published tree: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "*.partial*")); len(left) != 0 {
		t.Errorf("staging must be removed after a failure: %v", left)
	}

	// Complete stream: exactly the archive's tree.
	if _, _, err := ExtractFresh(bytes.NewReader(full), dest, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "left-by-partial")); !os.IsNotExist(err) {
		t.Errorf("leftovers of an earlier attempt must not survive: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "kernel.so")); string(b) != "binary" {
		t.Errorf("published tree: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "*.partial*")); len(left) != 0 {
		t.Errorf("no staging left after publishing: %v", left)
	}
}
