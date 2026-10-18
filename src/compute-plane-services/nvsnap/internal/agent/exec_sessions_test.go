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
}

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
