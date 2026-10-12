// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package treecopy

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Faithful mode copies a stored tree the way tar --numeric-owner would:
// every file type, hardlinks, modes with their special bits, times, and
// as root owners and device nodes (overlay whiteouts). Regular files are
// read with direct I/O, falling back where a short read leaves the offset
// unaligned.
func TestFaithfulCopy(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "out")
	big := make([]byte, directBufferLen+12345) // not a multiple of the block size
	_, _ = rand.Read(big)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(src, "d"), 0o755))
	must(syscall.Chmod(filepath.Join(src, "d"), 0o3775)) // setgid + sticky
	must(os.WriteFile(filepath.Join(src, "d", "chunk"), big, 0o640))
	must(os.Link(filepath.Join(src, "d", "chunk"), filepath.Join(src, "d", "chunk.link")))
	must(os.Symlink("chunk", filepath.Join(src, "d", "latest")))
	must(syscall.Mkfifo(filepath.Join(src, "d", "fifo"), 0o600))
	old := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	must(os.Chtimes(filepath.Join(src, "d", "chunk"), old, old))
	// Device nodes and foreign owners need privileges a test may not have
	// (a user namespace has neither); check them where they can be made.
	devices := syscall.Mknod(filepath.Join(src, "d", "whiteout"), syscall.S_IFCHR, 0) == nil
	owners := os.Lchown(filepath.Join(src, "d", "chunk"), 1000, 1000) == nil
	if !devices || !owners {
		t.Logf("checking device nodes: %v, owners: %v (not permitted here)", devices, owners)
	}

	c := NewCopier(nil, nil)
	c.SetWorkers(4)
	c.SetFaithful(true)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "d", "chunk"))
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("chunk content differs (err %v, %d of %d bytes)", err, len(got), len(big))
	}
	var a, b syscall.Stat_t
	must(syscall.Stat(filepath.Join(dst, "d", "chunk"), &a))
	must(syscall.Stat(filepath.Join(dst, "d", "chunk.link"), &b))
	if a.Ino != b.Ino {
		t.Error("hardlinks must stay one file")
	}
	if a.Mode&0o7777 != 0o640 {
		t.Errorf("chunk mode %o, want 640", a.Mode&0o7777)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "d", "chunk")); !fi.ModTime().Equal(old) {
		t.Errorf("mtime %v, want %v", fi.ModTime(), old)
	}
	var d syscall.Stat_t
	must(syscall.Stat(filepath.Join(dst, "d"), &d))
	if d.Mode&0o7777 != 0o3775 {
		t.Errorf("directory mode %o, want 3775 (setgid and sticky kept)", d.Mode&0o7777)
	}
	if l, err := os.Readlink(filepath.Join(dst, "d", "latest")); err != nil || l != "chunk" {
		t.Errorf("symlink = %q, %v", l, err)
	}
	if fi, err := os.Lstat(filepath.Join(dst, "d", "fifo")); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("fifo not recreated: %v %v", fi, err)
	}
	if devices {
		var w syscall.Stat_t
		must(syscall.Lstat(filepath.Join(dst, "d", "whiteout"), &w))
		if w.Mode&syscall.S_IFMT != syscall.S_IFCHR || w.Rdev != 0 {
			t.Errorf("whiteout not recreated: mode %o rdev %d", w.Mode, w.Rdev)
		}
	}
	if owners && (a.Uid != 1000 || a.Gid != 1000) {
		t.Errorf("owner %d:%d, want 1000:1000", a.Uid, a.Gid)
	}
}

// Without faithful mode a live root filesystem copy keeps its old
// behaviour: device nodes and FIFOs are not made.
func TestDefaultCopySkipsSpecialFiles(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "out")
	if err := syscall.Mkfifo(filepath.Join(src, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewCopier(nil, nil).Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "fifo")); !os.IsNotExist(err) {
		t.Errorf("default copy made a fifo: %v", err)
	}
}
