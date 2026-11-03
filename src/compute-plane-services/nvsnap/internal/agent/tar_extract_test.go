// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func extractInto(t *testing.T, archive []byte, dst string, o tarExtractOpts) (int, error) {
	t.Helper()
	d, err := openConfinedDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	return extractTar(bytes.NewReader(archive), d, o)
}

// An archive GNU tar made the way a capture makes one extracts to the same
// tree: contents, modes with their special bits, links, holes, times.
func TestExtractTarMatchesGNUTar(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(src, "a", "b"), 0o750))
	must(os.WriteFile(filepath.Join(src, "a", "b", "f"), []byte("data"), 0o640))
	must(os.Chmod(filepath.Join(src, "a", "b", "f"), 0o2750))
	must(os.Symlink("b/f", filepath.Join(src, "a", "l")))
	must(os.Link(filepath.Join(src, "a", "b", "f"), filepath.Join(src, "a", "h")))
	must(syscall.Mkfifo(filepath.Join(src, "a", "p"), 0o600))
	sp, err := os.Create(filepath.Join(src, "sparse"))
	must(err)
	_, err = sp.WriteAt([]byte("end"), 8<<20)
	must(err)
	must(sp.Close())
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	must(os.Chtimes(filepath.Join(src, "a", "b", "f"), old, old))
	must(os.Chtimes(filepath.Join(src, "a", "b"), old, old))
	xattr := unix.Setxattr(filepath.Join(src, "a", "b", "f"), "user.nvsnap", []byte("v"), 0) == nil

	out, err := exec.CommandContext(t.Context(), "tar", "-C", src, "-cf", "-", "--xattrs", "--acls", "--numeric-owner", "--sparse", ".").Output() //nolint:gosec // the test's own temporary tree
	if err != nil {
		t.Skipf("GNU tar: %v", err)
	}
	if _, err := extractInto(t, out, dst, tarExtractOpts{}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"a", "a/b", "a/b/f", "a/l", "a/h", "a/p", "sparse"} {
		s, d := lstat(t, filepath.Join(src, p)), lstat(t, filepath.Join(dst, p))
		if s.Mode != d.Mode || s.Uid != d.Uid || s.Gid != d.Gid {
			t.Errorf("%s: mode %o uid %d gid %d, want %o %d %d", p, d.Mode, d.Uid, d.Gid, s.Mode, s.Uid, s.Gid)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a", "b", "f")); string(b) != "data" {
		t.Errorf("content %q", b)
	}
	if l, _ := os.Readlink(filepath.Join(dst, "a", "l")); l != "b/f" {
		t.Errorf("symlink to %q", l)
	}
	if lstat(t, filepath.Join(dst, "a", "h")).Ino != lstat(t, filepath.Join(dst, "a", "b", "f")).Ino {
		t.Error("hard link not restored as a link")
	}
	if fi, _ := os.Stat(filepath.Join(dst, "a", "b", "f")); !fi.ModTime().Equal(old) {
		t.Errorf("file mtime %v, want %v", fi.ModTime(), old)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "a", "b")); !fi.ModTime().Equal(old) {
		t.Errorf("directory mtime %v, want %v", fi.ModTime(), old)
	}
	s := lstat(t, filepath.Join(dst, "sparse"))
	if s.Size != 8<<20+3 || s.Blocks*512 >= 1<<20 {
		t.Errorf("sparse file: size %d, %d bytes allocated; want its holes kept", s.Size, s.Blocks*512)
	}
	if xattr {
		v := make([]byte, 8)
		n, err := unix.Getxattr(filepath.Join(dst, "a", "b", "f"), "user.nvsnap", v)
		if err != nil || string(v[:n]) != "v" {
			t.Errorf("xattr: %q, %v", v[:n], err)
		}
	}
}

func lstat(t *testing.T, p string) syscall.Stat_t {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func tarOf(t *testing.T, hs ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range hs {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := w.Write(make([]byte, h.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Nothing is created outside the extraction directory: a name that climbs
// out is refused, and a symlink in the archive leads nowhere outside.
func TestExtractTarStaysInside(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	for name, a := range map[string][]byte{
		"climbing name": tarOf(t, &tar.Header{Name: "../x", Typeflag: tar.TypeReg, Mode: 0o644, Uid: uid, Gid: gid}),
		"through a symlink": tarOf(t,
			&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/", Mode: 0o777, Uid: uid, Gid: gid},
			&tar.Header{Name: "l/x", Typeflag: tar.TypeReg, Mode: 0o644, Uid: uid, Gid: gid}),
		"hard link out": tarOf(t, &tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "../../etc/hostname", Uid: uid, Gid: gid}),
	} {
		parent := t.TempDir()
		dst := filepath.Join(parent, "dst")
		if err := os.Mkdir(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := extractInto(t, a, dst, tarExtractOpts{})
		if err == nil {
			t.Errorf("%s: extracted", name)
		}
		if _, serr := os.Lstat(filepath.Join(parent, "x")); serr == nil {
			t.Errorf("%s: created a file outside the directory", name)
		}
	}
}

// Skipped entries (the placeholder's own mounts) and skipped attributes are
// left alone; an existing file is replaced, an existing directory kept.
func TestExtractTarSkipsAndOverwrites(t *testing.T) {
	dst := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	if err := os.MkdirAll(filepath.Join(dst, "mnt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "mnt", "own"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "f"), []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := tarOf(t,
		&tar.Header{Name: "./mnt/own", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644, Uid: uid, Gid: gid},
		&tar.Header{Name: "./f", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644, Uid: uid, Gid: gid},
		&tar.Header{Name: "./mnt", Typeflag: tar.TypeDir, Mode: 0o755, Uid: uid, Gid: gid},
	)
	n, err := extractInto(t, a, dst, tarExtractOpts{skip: func(name string) bool { return name == "mnt/own" }})
	if err != nil || n != 2 {
		t.Fatalf("extracted %d, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "mnt", "own")); string(b) != "mine" {
		t.Errorf("a skipped entry was replaced: %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "f")); fi.Size() != 2 {
		t.Errorf("an existing file was not replaced: %d bytes", fi.Size())
	}
}

func TestEncodePosixACL(t *testing.T) {
	b, err := encodePosixACL("user::rw-,user:1000:r--,group::r--,mask::r--,other::---")
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(b) != 2 || len(b) != 4+5*8 {
		t.Fatalf("header or length: %x", b)
	}
	// user:1000:r--
	e := b[4+8:]
	if binary.LittleEndian.Uint16(e) != 0x02 || binary.LittleEndian.Uint16(e[2:]) != 4 || binary.LittleEndian.Uint32(e[4:]) != 1000 {
		t.Errorf("named user entry %x", e[:8])
	}
	for _, bad := range []string{"bogus::rw-", "user:x:r--", "user::rwz"} {
		if _, err := encodePosixACL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	_ = strings.TrimSpace
}
