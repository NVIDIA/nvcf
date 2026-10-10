// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/agent"
)

// pidNSRestoreExec runs inside a restore placeholder's mount and pid
// namespaces and execs CRIU there, for a checkpoint that carries its pid
// namespace (internal/agent/pidns_capture.go). CRIU needs a procfs of the
// namespace it runs in (its own /proc/<pid> lookups) and a writable
// /proc/sys, which a container's /proc is not. So: a private copy of the
// mount namespace, a fresh procfs in it, the pod's network namespace on fd
// 3 (the --inherit-fd the restore argv names), then exec. The agent binary
// is static, so it runs in any workload image.
//
//	pidns-restore-exec <criu> <args...>
func pidNSRestoreExec(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: pidns-restore-exec <criu> <args...>")
	}
	// Namespace changes apply to the calling thread; exec from the same one.
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("unshare mount namespace: %w", err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount procfs: %w", err)
	}
	// pid 1 of this pid namespace is the placeholder's own process; its
	// network namespace is the pod's.
	fd, err := unix.Open("/proc/1/ns/net", unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open the pod's network namespace: %w", err)
	}
	if fd != 3 {
		if err := unix.Dup3(fd, 3, 0); err != nil {
			return fmt.Errorf("network namespace to fd 3: %w", err)
		}
		_ = unix.Close(fd)
	}
	if err := bindUnixListeners(os.Getenv(agent.PIDNSUnixListenersEnv)); err != nil {
		return err
	}
	return unix.Exec(args[0], args, os.Environ())
}

// bindUnixListeners binds a fresh listening socket at each path in spec
// ("<type>:<path>" per line) and places them on fds 4, 5, ... in order:
// CRIU takes them in place of the workload's own (--inherit-fd), which it
// cannot re-create in a restored pid namespace. A stale socket file from
// the source is removed first.
func bindUnixListeners(spec string) error {
	type listener struct {
		typ  int
		path string
	}
	var ls []listener
	for _, line := range strings.Split(strings.TrimSpace(spec), "\n") {
		if line == "" {
			continue
		}
		typ, path, ok := strings.Cut(line, ":")
		t, err := strconv.Atoi(typ)
		if !ok || err != nil || path == "" {
			return fmt.Errorf("bad listener %q", line)
		}
		ls = append(ls, listener{t, path})
	}
	// Create every socket on a high fd first, so placing one never
	// clobbers another that happened to land on a target fd.
	const stage = 1000
	for i, l := range ls {
		_ = unix.Unlink(l.path)
		fd, err := unix.Socket(unix.AF_UNIX, l.typ, 0)
		if err != nil {
			return fmt.Errorf("socket for %s: %w", l.path, err)
		}
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: l.path}); err != nil {
			return fmt.Errorf("bind %s: %w", l.path, err)
		}
		if err := unix.Listen(fd, 4096); err != nil {
			return fmt.Errorf("listen %s: %w", l.path, err)
		}
		if err := unix.Dup3(fd, stage+i, 0); err != nil {
			return err
		}
		_ = unix.Close(fd)
	}
	for i := range ls {
		if err := unix.Dup3(stage+i, 4+i, 0); err != nil {
			return err
		}
		_ = unix.Close(stage + i)
	}
	return nil
}
