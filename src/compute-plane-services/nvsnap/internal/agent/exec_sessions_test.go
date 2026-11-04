// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
)

// sessionProc writes a /proc with one process per entry: its pid namespace,
// parent and state.
type sessionProcEntry struct {
	ns, ppid int
	state    string
	// stdin, when set, is fd 0: its path and mount id.
	stdin    string
	stdinMnt int
}

// The container's mounts (its root and /dev); the node's /dev is mount 28.
const sessionMountinfo = "3001 3000 0:300 / / rw - overlay overlay rw\n3002 3001 0:5 / /dev rw - tmpfs tmpfs rw\n"

func sessionProc(t *testing.T, procs map[int]sessionProcEntry) string {
	t.Helper()
	base := t.TempDir()
	for pid, p := range procs {
		writeSessionProc(t, base, pid, p)
	}
	return base
}

func writeSessionProc(t *testing.T, base string, pid int, p sessionProcEntry) {
	t.Helper()
	d := filepath.Join(base, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(d, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fmt.Sprintf("pid:[%d]", p.ns), filepath.Join(d, "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	state := p.state
	if state == "" {
		state = "S (sleeping)"
	}
	status := fmt.Sprintf("Name:\tx\nState:\t%s\nPPid:\t%d\n", state, p.ppid)
	if err := os.WriteFile(filepath.Join(d, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "mountinfo"), []byte(sessionMountinfo), 0o644); err != nil {
		t.Fatal(err)
	}
	if p.stdin != "" {
		for _, sub := range []string{"fd", "fdinfo"} {
			if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(p.stdin, filepath.Join(d, "fd", "0")); err != nil {
			t.Fatal(err)
		}
		info := fmt.Sprintf("pos:\t0\nflags:\t0100000\nmnt_id:\t%d\n", p.stdinMnt)
		if err := os.WriteFile(filepath.Join(d, "fdinfo", "0"), []byte(info), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A container: the shim (100, on the node), the engine (200, the
// container's pid 1) with a worker (201) and an orphan it adopted (202), a
// zombie (203), and an exec probe the shim started (300: sh, 301: curl).
func probeContainer() map[int]sessionProcEntry {
	const node, ctr = 1, 2
	return map[int]sessionProcEntry{
		1:   {ns: node, ppid: 0},
		100: {ns: node, ppid: 1},
		200: {ns: ctr, ppid: 100},
		201: {ns: ctr, ppid: 200},
		202: {ns: ctr, ppid: 201},
		203: {ns: ctr, ppid: 100, state: "Z (zombie)"},
		300: {ns: ctr, ppid: 100},
		301: {ns: ctr, ppid: 300},
	}
}

// A probe the runtime killed on its timeout left its child behind, adopted
// by the container's pid 1, its stdin still the node's /dev/null; the
// engine's own /dev/null is the container's.
func TestExecSessions_LeftBehind(t *testing.T) {
	procs := probeContainer()
	delete(procs, 300)
	delete(procs, 301)
	procs[201] = sessionProcEntry{ns: 2, ppid: 200, stdin: "/dev/null", stdinMnt: 3002}
	procs[400] = sessionProcEntry{ns: 2, ppid: 200, stdin: "/dev/null", stdinMnt: 28}
	procs[401] = sessionProcEntry{ns: 2, ppid: 200, stdin: "pipe:[77]", stdinMnt: 15}
	got, err := execSessions(sessionProc(t, procs), 200)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{400}) {
		t.Errorf("exec sessions = %v, want the one left behind [400]", got)
	}
}

func TestExecSessions(t *testing.T) {
	got, err := execSessions(sessionProc(t, probeContainer()), 200)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{300, 301}) {
		t.Errorf("exec sessions = %v, want the probe's [300 301]", got)
	}
}

func TestExecSessions_NoneInAContainerOfItsOwn(t *testing.T) {
	procs := probeContainer()
	delete(procs, 300)
	delete(procs, 301)
	got, err := execSessions(sessionProc(t, procs), 200)
	if err != nil || len(got) != 0 {
		t.Errorf("exec sessions = %v, %v; want none", got, err)
	}
}

// Killed, the sessions are waited out; the container's own processes are
// never killed.
func TestEndExecSessions(t *testing.T) {
	base := sessionProc(t, probeContainer())
	var killed []int
	kill := func(pid int) error {
		killed = append(killed, pid)
		return os.RemoveAll(filepath.Join(base, strconv.Itoa(pid)))
	}
	ended, err := endExecSessions(base, 200, kill)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(ended)
	slices.Sort(killed)
	if !slices.Equal(ended, []int{300, 301}) || !slices.Equal(killed, ended) {
		t.Errorf("ended %v, killed %v; want the probe's [300 301]", ended, killed)
	}
	if n, _ := endExecSessions(base, 200, kill); len(n) != 0 {
		t.Errorf("a second pass ended %v", n)
	}
}

// A session gone before its kill is not an error.
func TestEndExecSessions_GoneAlready(t *testing.T) {
	base := sessionProc(t, probeContainer())
	kill := func(pid int) error {
		_ = os.RemoveAll(filepath.Join(base, strconv.Itoa(pid)))
		return syscall.ESRCH
	}
	if _, err := endExecSessions(base, 200, kill); err != nil {
		t.Errorf("err = %v", err)
	}
}
