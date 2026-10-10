// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBindUnixListenersRejectsBadSpec(t *testing.T) {
	for _, spec := range []string{"x:/tmp/a", "1:", "nocolon"} {
		if _, err := bindUnixListeners(spec); err == nil {
			t.Errorf("accepted %q", spec)
		}
	}
	if n, err := bindUnixListeners(""); err != nil || n != 0 {
		t.Errorf("empty spec: %v", err)
	}
}

// Placing an fd that already sits on its target (the lowest free fd) must
// not fail, and the result must survive exec.
func TestPlaceFD(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "x"))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.SetNonblock(fd, false)
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	if err := placeFD(fd, fd); err != nil {
		t.Fatalf("same fd: %v", err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC != 0 {
		t.Errorf("fd still close-on-exec (flags %d, %v)", flags, err)
	}
	_ = unix.Close(fd)
}
