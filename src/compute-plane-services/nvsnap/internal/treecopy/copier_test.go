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

package treecopy

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// mustWrite writes a regular file or fails the test.
func mustWriteRegular(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// TestTreeCopier_PreservesMtime is the regression guard for the gemma
// SGLang full-kernel-recompile bug (2026-06-19): a plain copy stamps every
// file with copy-time, so ninja (sgl_kernel/tvm-ffi) sees its cached
// outputs as "modified after build" and recompiles the whole cache. The
// copier must preserve mtimes at nanosecond precision.
func TestTreeCopier_PreservesMtime(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	fp := filepath.Join(src, "k", "cuda_0.o")
	mustWriteRegular(t, fp, "obj\n", 0o644)

	// A real build time, well before the copy — sub-second precision so we
	// also catch a copier that truncates to whole seconds.
	want := unix.NsecToTimespec(int64(1_577_000_000)*1_000_000_000 + 123_456_789)
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, fp, []unix.Timespec{want, want}, 0); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NewCopier(nil, nil).Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(dst, "k", "cuda_0.o"))
	if err != nil {
		t.Fatal(err)
	}
	got := fi.Sys().(*syscall.Stat_t).Mtim
	if got.Sec != want.Sec || got.Nsec != want.Nsec {
		t.Errorf("dst mtime = %d.%09d, want %d.%09d (mtime not preserved → ninja recompiles)",
			got.Sec, got.Nsec, want.Sec, want.Nsec)
	}
}

func TestTreeCopier_RegularFile(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "a", "b", "f.txt"), "hello\n", 0o644)

	c := NewCopier(nil, nil)
	bytes, files, err := c.Copy(context.Background(), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, filepath.Join(dst, "a", "b", "f.txt")); got != "hello\n" {
		t.Fatalf("contents: %q", got)
	}
	if bytes != 6 {
		t.Errorf("bytes = %d, want 6", bytes)
	}
	// 1 regular + 2 dirs (a, a/b) = 3 entries
	if files != 3 {
		t.Errorf("files = %d, want 3 (1 regular + 2 dirs)", files)
	}
}

func TestTreeCopier_EmptyFile(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "empty"), "", 0o644)
	c := NewCopier(nil, nil)
	bytes, _, err := c.Copy(context.Background(), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 0 {
		t.Errorf("bytes = %d, want 0", bytes)
	}
	if _, err := os.Stat(filepath.Join(dst, "empty")); err != nil {
		t.Fatalf("empty file missing: %v", err)
	}
}

func TestTreeCopier_PreservesFileMode(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "exec"), "#!/bin/sh\n", 0o755)
	mustWriteRegular(t, filepath.Join(src, "ro"), "x", 0o400)
	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(dst, "exec"))
	if st.Mode().Perm() != 0o755 {
		t.Errorf("exec mode = %o, want 0755", st.Mode().Perm())
	}
	st, _ = os.Stat(filepath.Join(dst, "ro"))
	if st.Mode().Perm() != 0o400 {
		t.Errorf("ro mode = %o, want 0400", st.Mode().Perm())
	}
}

func TestTreeCopier_DirectoryPreservesMode(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteRegular(t, filepath.Join(src, "secret", "f"), "x", 0o600)
	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(dst, "secret"))
	if st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 0700", st.Mode().Perm())
	}
}

func TestTreeCopier_Symlink_AbsoluteTarget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "real"), "x", 0o644)
	if err := os.Symlink("/abs/target", filepath.Join(src, "abslink")); err != nil {
		t.Fatal(err)
	}
	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dst, "abslink"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "/abs/target" {
		t.Errorf("target = %q, want /abs/target", target)
	}
}

func TestTreeCopier_Symlink_RelativeTarget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "real"), "x", 0o644)
	if err := os.Symlink("real", filepath.Join(src, "rellink")); err != nil {
		t.Fatal(err)
	}
	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	target, _ := os.Readlink(filepath.Join(dst, "rellink"))
	if target != "real" {
		t.Errorf("target = %q, want real", target)
	}
}

func TestTreeCopier_Symlink_BrokenTarget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.Symlink("/does/not/exist", filepath.Join(src, "broken")); err != nil {
		t.Fatal(err)
	}
	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dst, "broken"))
	if err != nil {
		t.Fatalf("broken symlink not preserved: %v", err)
	}
	if target != "/does/not/exist" {
		t.Errorf("target = %q", target)
	}
}

func TestTreeCopier_HardlinkDeduplicatesByInode(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	a := filepath.Join(src, "a.txt")
	b := filepath.Join(src, "b.txt")
	mustWriteRegular(t, a, "shared-content", 0o644)
	if err := os.Link(a, b); err != nil {
		t.Skipf("hardlink not supported on this fs: %v", err)
	}
	c := NewCopier(nil, nil)
	bytes, _, err := c.Copy(context.Background(), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	// Bytes should be content size ONCE, not twice — second occurrence
	// is hardline.
	if bytes != int64(len("shared-content")) {
		t.Errorf("bytes = %d, want %d (hardlink dedup)", bytes, len("shared-content"))
	}
	// Verify both dst files share an inode.
	statA, _ := os.Stat(filepath.Join(dst, "a.txt"))
	statB, _ := os.Stat(filepath.Join(dst, "b.txt"))
	sysA := statA.Sys().(*syscall.Stat_t)
	sysB := statB.Sys().(*syscall.Stat_t)
	if sysA.Ino != sysB.Ino {
		t.Errorf("expected hardlink (same inode); a=%d b=%d", sysA.Ino, sysB.Ino)
	}
}

func TestTreeCopier_SocketSkipped(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "real"), "x", 0o644)
	l, err := net.Listen("unix", filepath.Join(src, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	c := NewCopier(nil, nil)
	_, _, err = c.Copy(context.Background(), src, dst)
	if err != nil {
		t.Fatalf("walk errored on socket: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sock")); !os.IsNotExist(err) {
		t.Errorf("socket should be skipped; got: %v", err)
	}
	// Real file alongside should still copy.
	if _, err := os.Stat(filepath.Join(dst, "real")); err != nil {
		t.Errorf("regular file in same dir as socket missing: %v", err)
	}
}

func TestTreeCopier_FIFOSkipped(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := unix.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}
	mustWriteRegular(t, filepath.Join(src, "real"), "x", 0o644)
	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "pipe")); !os.IsNotExist(err) {
		t.Errorf("FIFO should be skipped; got: %v", err)
	}
}

func TestTreeCopier_DeviceFileSkipped(t *testing.T) {
	// Creating a char device requires CAP_MKNOD (typically root). Skip
	// the test gracefully on non-root environments.
	src, dst := t.TempDir(), t.TempDir()
	devPath := filepath.Join(src, "whiteout")
	// dev=0 (overlayfs whiteout convention)
	if err := unix.Mknod(devPath, syscall.S_IFCHR|0o600, 0); err != nil {
		t.Skipf("mknod not supported (need CAP_MKNOD): %v", err)
	}
	mustWriteRegular(t, filepath.Join(src, "real"), "x", 0o644)
	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "whiteout")); !os.IsNotExist(err) {
		t.Errorf("device file (whiteout) should be skipped; got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "real")); err != nil {
		t.Errorf("regular file alongside whiteout missing: %v", err)
	}
}

func TestTreeCopier_ExcludeExactPath(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "etc", "hostname"), "container1", 0o644)
	mustWriteRegular(t, filepath.Join(src, "etc", "hosts"), "127.0.0.1", 0o644)
	c := NewCopier([]string{"/etc/hostname"}, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "etc", "hostname")); !os.IsNotExist(err) {
		t.Errorf("excluded file should be missing")
	}
	if _, err := os.Stat(filepath.Join(dst, "etc", "hosts")); err != nil {
		t.Errorf("non-excluded sibling should be present: %v", err)
	}
}

func TestTreeCopier_ExcludeSubtree(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "run", "vllm", "sock"), "x", 0o644)
	mustWriteRegular(t, filepath.Join(src, "run", "vllm", "log"), "x", 0o644)
	mustWriteRegular(t, filepath.Join(src, "tmp", "f"), "x", 0o644)
	c := NewCopier([]string{"/run"}, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "run")); !os.IsNotExist(err) {
		t.Errorf("excluded dir subtree should be missing")
	}
	if _, err := os.Stat(filepath.Join(dst, "tmp", "f")); err != nil {
		t.Errorf("unexcluded subtree should be present: %v", err)
	}
}

func TestTreeCopier_ENOENTMidWalkTolerated(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	// Build a tree where one dir has many files; we'll remove one of them
	// before the walker reaches it, simulating a live source pod.
	for i := range 50 {
		p := filepath.Join(src, "many", "f"+string(rune('a'+(i%26))))
		mustWriteRegular(t, p, "x", 0o644)
	}
	mustWriteRegular(t, filepath.Join(src, "real.txt"), "preserved", 0o644)
	// Remove "many" entirely between Lstat-ing the root and the walker
	// reaching it. We approximate by deleting it before Copy starts.
	if err := os.RemoveAll(filepath.Join(src, "many")); err != nil {
		t.Fatal(err)
	}
	c := NewCopier(nil, nil)
	_, _, err := c.Copy(context.Background(), src, dst)
	if err != nil {
		t.Fatalf("Copy errored on transient ENOENT: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(dst, "real.txt")); got != "preserved" {
		t.Errorf("real.txt: %q", got)
	}
}

func TestTreeCopier_TrustedOverlayXattrsDropped(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := filepath.Join(src, "f")
	mustWriteRegular(t, p, "x", 0o644)

	// Try to set trusted.overlay.* xattr (requires CAP_SYS_ADMIN, root).
	if err := unix.Setxattr(p, "trusted.overlay.opaque", []byte("y"), 0); err != nil {
		t.Skipf("trusted.* xattr not settable here (need CAP_SYS_ADMIN): %v", err)
	}
	// And set a user.* xattr that SHOULD be preserved.
	if err := unix.Setxattr(p, "user.preserved", []byte("yes"), 0); err != nil {
		t.Skipf("user.* xattr not supported on this fs: %v", err)
	}

	c := NewCopier(nil, nil)
	if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	dstP := filepath.Join(dst, "f")
	// trusted.overlay.* must NOT be on dst.
	if _, err := unix.Getxattr(dstP, "trusted.overlay.opaque", nil); err == nil {
		t.Errorf("trusted.overlay.* should be dropped on destination")
	}
	// user.* SHOULD be present.
	got, err := xattrGet(dstP, "user.preserved")
	if err != nil {
		t.Errorf("user.* xattr should be preserved: %v", err)
	}
	if string(got) != "yes" {
		t.Errorf("user.* value: got %q, want yes", string(got))
	}
}

func TestTreeCopier_StatsAccurate(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWriteRegular(t, filepath.Join(src, "a"), strings.Repeat("a", 1000), 0o644)
	mustWriteRegular(t, filepath.Join(src, "d", "b"), strings.Repeat("b", 500), 0o644)
	c := NewCopier(nil, nil)
	bytes, files, err := c.Copy(context.Background(), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 1500 {
		t.Errorf("bytes = %d, want 1500", bytes)
	}
	// 2 regular + 1 dir (d) = 3
	if files != 3 {
		t.Errorf("files = %d, want 3", files)
	}
}

func TestTreeCopier_SrcIsNotDirectoryErrors(t *testing.T) {
	src := t.TempDir()
	regular := filepath.Join(src, "f")
	mustWriteRegular(t, regular, "x", 0o644)
	c := NewCopier(nil, nil)
	_, _, err := c.Copy(context.Background(), regular, t.TempDir())
	if err == nil {
		t.Fatal("expected error when src is not a directory")
	}
}

func TestTreeCopier_CtxCancelStops(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	for i := range 200 {
		mustWriteRegular(t, filepath.Join(src, "many", "f"+string(rune('a'+(i%26)))), "x", 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	c := NewCopier(nil, nil)
	_, _, err := c.Copy(ctx, src, dst)
	if err == nil {
		t.Fatal("expected ctx error")
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
