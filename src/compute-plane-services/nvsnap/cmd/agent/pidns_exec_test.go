// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/agent"
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

// openFDs maps each open fd of this process to what it refers to.
func openFDs(t *testing.T) map[int]string {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	m := map[int]string{}
	for _, e := range es {
		fd, _ := strconv.Atoi(e.Name())
		if l, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil {
			m[fd] = l
		}
	}
	return m
}

// Placing the listeners must leave every fd that was already open alone:
// the Go runtime's netpoller fds among them. Replacing those made the
// helper abort at the runtime's next poll ("epollwait on fd 4 failed").
func TestBindUnixListenersKeepsOpenFDs(t *testing.T) {
	dir := t.TempDir()
	time.Sleep(time.Millisecond) // the runtime's netpoller is up
	before := openFDs(t)
	n, err := bindUnixListeners(fmt.Sprintf("%d:%s\n%d:%s\n",
		unix.SOCK_STREAM, filepath.Join(dir, "a"), unix.SOCK_SEQPACKET, filepath.Join(dir, "b")))
	if err != nil || n != 2 {
		t.Fatalf("bind: %d, %v", n, err)
	}
	defer func() {
		for i := 0; i < n; i++ {
			_ = unix.Close(agent.PIDNSUnixListenerFD + i)
		}
	}()
	after := openFDs(t)
	for fd, l := range before {
		if after[fd] != l {
			t.Errorf("fd %d was %s, now %q", fd, l, after[fd])
		}
	}
	for i := 0; i < n; i++ {
		if _, ok := after[agent.PIDNSUnixListenerFD+i]; ok {
			if _, was := before[agent.PIDNSUnixListenerFD+i]; was {
				t.Errorf("listener %d placed on an fd that was open", i)
			}
		} else {
			t.Errorf("listener %d not on fd %d", i, agent.PIDNSUnixListenerFD+i)
		}
	}
	// What aborted the helper: a GC and a netpoll after the placement.
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
}

func TestPlaceFDRefusesAnOpenTarget(t *testing.T) {
	a, err := unix.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(a)
	b, err := unix.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(b)
	if err := placeFD(a, b); err == nil {
		t.Fatal("placed over an open fd")
	}
}
