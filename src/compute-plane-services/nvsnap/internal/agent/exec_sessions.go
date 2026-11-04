// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Exec sessions. The container runtime starts a process inside a running
// container for an exec probe or a kubectl exec: the process enters the
// container's namespaces, but the runtime's shim, outside them, is its
// parent and holds its stdio (the node's /dev/null, pipes to the shim).
// A whole pid-namespace dump takes it with the container's own processes,
// and CRIU cannot dump it: its stdio is a file of no mount of the
// container ("Can't lookup mount=28 for fd=0 path=/dev/null", seen
// 2026-10-18 on a rank whose readiness probe runs every 5 s). Nor could a
// restore give it back: the shim that waits on it is gone. A session can
// also leave a process behind with the same stdio: the runtime kills a
// probe that times out, and the probe's child (a curl to an engine the
// capture suspended) is then the orphan of the container's pid 1 (seen
// 2026-10-18, rank 0's pid 761008). So the dump
// first stops the container (frozen, it admits no new exec: the runtime
// refuses to exec into a paused container), then ends the exec sessions
// in it, and CRIU seizes the container from its frozen cgroup. A probe
// that runs then fails, as every probe of a held pod does; the capture's
// liveness budget covers it (criu_group_guard.go).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// execSessionExitWait bounds the wait for killed exec sessions to exit.
const execSessionExitWait = 10 * time.Second

// execSessions returns the host pids of the exec sessions in the pid
// namespace of hostPID: each process of the namespace whose parent is
// outside it (the runtime's shim), with every process descended from one,
// and each whose stdio is a file of no mount of the container (one a
// session left behind). The namespace's init is never one. Zombies are
// left out: they hold no files, and their parent reaps them.
func execSessions(procBase string, hostPID int) ([]int, error) {
	mounts, err := mountIDs(procBase, hostPID)
	if err != nil {
		return nil, err
	}
	nsOf := func(pid int) string {
		s, _ := os.Readlink(filepath.Join(procBase, strconv.Itoa(pid), "ns", "pid"))
		return s
	}
	ns := nsOf(hostPID)
	if ns == "" {
		return nil, fmt.Errorf("pid namespace of %d: not readable", hostPID)
	}
	// In a pod that shares one pid namespace, the namespace also holds the
	// pause process and the other containers, each the child of its own
	// shim: only the processes of this container's cgroup are its exec
	// sessions to end (pidns_subtree.go).
	inContainer := func(int) bool { return true }
	if nsPID, nerr := nsPidOf(procBase, hostPID); nerr == nil && nsPID != 1 {
		cg, cerr := processCgroup(procBase, hostPID)
		if cerr != nil {
			return nil, cerr
		}
		inContainer = func(pid int) bool {
			c, perr := processCgroup(procBase, pid)
			return perr == nil && c == cg
		}
	}
	ents, err := os.ReadDir(procBase)
	if err != nil {
		return nil, err
	}
	parent := map[int]int{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == hostPID || nsOf(pid) != ns || !inContainer(pid) {
			continue
		}
		ppid, zombie, err := procParent(procBase, pid)
		if err != nil || zombie {
			continue // gone, or past holding anything
		}
		parent[pid] = ppid
	}
	var out []int
	for pid := range parent {
		if foreignStdio(procBase, pid, mounts) {
			out = append(out, pid)
			continue
		}
		for p := pid; ; {
			pp, inNS := parent[p]
			if !inNS {
				break
			}
			if pp != hostPID && nsOf(pp) != ns {
				out = append(out, pid)
				break
			}
			p = pp
		}
	}
	return out, nil
}

// mountIDs returns the ids of the mounts of hostPID's mount namespace.
func mountIDs(procBase string, hostPID int) (map[string]bool, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(hostPID), "mountinfo"))
	if err != nil {
		return nil, fmt.Errorf("mounts of %d: %w", hostPID, err)
	}
	ids := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			ids[f[0]] = true
		}
	}
	return ids, nil
}

// foreignStdio reports whether pid's stdin, stdout or stderr is a file on
// a mount not in mounts. Pipes, sockets and other anonymous files have no
// path and are never foreign.
func foreignStdio(procBase string, pid int, mounts map[string]bool) bool {
	dir := filepath.Join(procBase, strconv.Itoa(pid))
	for fd := 0; fd <= 2; fd++ {
		target, err := os.Readlink(filepath.Join(dir, "fd", strconv.Itoa(fd)))
		if err != nil || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "/memfd:") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "fdinfo", strconv.Itoa(fd)))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "mnt_id:"); ok && !mounts[strings.TrimSpace(v)] {
				return true
			}
		}
	}
	return false
}

// procParent reads the parent and whether pid is a zombie.
func procParent(procBase string, pid int) (int, bool, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false, err
	}
	ppid, zombie, seen := 0, false, false
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			if ppid, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return 0, false, err
			}
			seen = true
		}
		if v, ok := strings.CutPrefix(line, "State:"); ok {
			zombie = strings.HasPrefix(strings.TrimSpace(v), "Z")
		}
	}
	if !seen {
		return 0, false, fmt.Errorf("no PPid in %d/status", pid)
	}
	return ppid, zombie, nil
}

// endExecSessions kills the exec sessions in the pid namespace of hostPID
// and waits, bounded, for them to exit. The container is frozen: no new
// one starts, and a killed one still exits (a frozen cgroup lets a fatal
// signal through).
func endExecSessions(procBase string, hostPID int, kill func(int) error) ([]int, error) {
	pids, err := execSessions(procBase, hostPID)
	if err != nil || len(pids) == 0 {
		return nil, err
	}
	var errs []error
	for _, pid := range pids {
		if err := kill(pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, fmt.Errorf("kill exec session %d: %w", pid, err))
		}
	}
	if len(errs) > 0 {
		return pids, errors.Join(errs...)
	}
	for deadline := time.Now().Add(execSessionExitWait); ; {
		left, err := execSessions(procBase, hostPID)
		if err != nil {
			return pids, err
		}
		if len(left) == 0 {
			return pids, nil
		}
		if time.Now().After(deadline) {
			return pids, fmt.Errorf("exec sessions %v still in the container %s after they were killed", left, execSessionExitWait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func killPID(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
