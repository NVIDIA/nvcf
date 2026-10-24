// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/agent"
)

// pidNSRestoreExec runs the agent's own CRIU for a checkpoint that carries
// its pid namespace. It runs in the agent's filesystem, entered only into
// the placeholder's pid and network namespaces (nsenter -p -n), so nothing
// from the placeholder's filesystem is loaded or run
// (docs/design/TOOLS-OUTSIDE-THE-WORKLOAD.md). In a private copy of the
// agent's mount namespace it mounts a procfs of the placeholder's pid
// namespace and the placeholder's root (a descriptor the agent passes) at
// agent.PIDNSRestoreRoot, for CRIU's --root, and places the descriptors the
// agent passes (agent.PIDNSFDsEnv) where CRIU's --inherit-fd names them.
func pidNSRestoreExec(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: pidns-restore-exec <criu> <args...>")
	}
	fds, err := agent.ParsePIDNSFDs(os.Getenv(agent.PIDNSFDsEnv))
	if err != nil {
		return err
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
	base := filepath.Dir(agent.PIDNSRestoreRoot)
	if err := unix.Mount("nvsnap-restore", base, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700"); err != nil {
		return fmt.Errorf("mount %s: %w", base, err)
	}
	if err := os.Mkdir(agent.PIDNSRestoreRoot, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", agent.PIDNSRestoreRoot, err)
	}
	if err := unix.Mount("/proc/self/fd/"+strconv.Itoa(fds.Root), agent.PIDNSRestoreRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("mount the placeholder's root: %w", err)
	}
	_ = unix.Close(fds.Root)
	if err := placeFD(fds.Net, agent.PIDNSNetFD); err != nil {
		return fmt.Errorf("network namespace to fd %d: %w", agent.PIDNSNetFD, err)
	}
	next := agent.PIDNSUnixListenerFD
	for _, fd := range append(append([]int{}, fds.Listeners...), fds.Stdio...) {
		if err := placeFD(fd, next); err != nil {
			return fmt.Errorf("descriptor %d to fd %d: %w", fd, next, err)
		}
		next++
	}
	if err := unix.Exec(args[0], args, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w (%s)", args[0], err, describeExecPath(args[0]))
	}
	return nil
}

// bindUnixListenersExec binds the listening unix sockets of a restore in
// the placeholder's mount namespace (it runs there, from the node bundle:
// a static binary) and sends them to the agent over fd 3.
func bindUnixListenersExec() error {
	n, err := bindUnixListeners(os.Getenv(agent.PIDNSUnixListenersEnv))
	if err != nil {
		return err
	}
	fds := make([]int, n)
	for i := range fds {
		fds[i] = agent.PIDNSUnixListenerFD + i
	}
	if n == 0 {
		return nil
	}
	return unix.Sendmsg(3, []byte{0}, unix.UnixRights(fds...), nil, 0)
}

func describeExecPath(path string) string {
	var parts []string
	for p := path; ; p = filepath.Dir(p) {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err != nil {
			parts = append(parts, fmt.Sprintf("%s: %v", p, err))
		} else {
			d := fmt.Sprintf("%s %o %d:%d", p, st.Mode&0o7777, st.Uid, st.Gid)
			var fs unix.Statfs_t
			if unix.Statfs(p, &fs) == nil && fs.Flags&unix.ST_NOEXEC != 0 {
				d += " noexec"
			}
			parts = append(parts, d)
		}
		if p == "/" || p == "." {
			break
		}
	}
	return strings.Join(parts, "; ")
}

// bindUnixListeners binds a fresh listening socket at each path in spec
// ("<type>:<path>" per line) and places them on agent.PIDNSUnixListenerFD
// and the fds after it, in order:
// CRIU takes them in place of the workload's own (--inherit-fd), which it
// cannot re-create in a restored pid namespace. A stale socket file from
// the source is removed first.
func bindUnixListeners(spec string) (int, error) {
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
			return 0, fmt.Errorf("bad listener %q", line)
		}
		ls = append(ls, listener{t, path})
	}
	// Create every socket on a staging fd above the targets first, so
	// placing one never meets another that happened to land on a target.
	stage := agent.PIDNSUnixListenerFD + 1000
	for i, l := range ls {
		_ = unix.Unlink(l.path)
		fd, err := unix.Socket(unix.AF_UNIX, l.typ, 0)
		if err != nil {
			return 0, fmt.Errorf("socket for %s: %w", l.path, err)
		}
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: l.path}); err != nil {
			return 0, fmt.Errorf("bind %s: %w", l.path, err)
		}
		if err := unix.Listen(fd, 4096); err != nil {
			return 0, fmt.Errorf("listen %s: %w", l.path, err)
		}
		if err := placeFD(fd, stage+i); err != nil {
			return 0, fmt.Errorf("stage %s: %w", l.path, err)
		}
	}
	for i, l := range ls {
		if err := placeFD(stage+i, agent.PIDNSUnixListenerFD+i); err != nil {
			return 0, fmt.Errorf("place %s: %w", l.path, err)
		}
	}
	return len(ls), nil
}

// placeFD moves fd to target, inheritable. It refuses a target that is
// already open: dup3 would silently close whatever is there, which may be
// one of the Go runtime's own fds. dup3 also refuses fd == target (EINVAL),
// which happens when target was the lowest free fd.
func placeFD(fd, target int) error {
	if fd == target {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0)
		return err
	}
	if _, err := unix.FcntlInt(uintptr(target), unix.F_GETFD, 0); err == nil {
		return fmt.Errorf("fd %d is already open", target)
	}
	if err := unix.Dup3(fd, target, 0); err != nil {
		return err
	}
	return unix.Close(fd)
}
