// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package idle tests cmd/nvsnap-idle, the C program that holds CRIU restore
// placeholders.
package idle

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The placeholder contract: one thread, always one live child, exit 0 on
// SIGTERM. A restore needs every task but init above the reserved pid floor
// soon after the agent raises it, which only holds if nothing else lives
// here for long.
func TestIdle(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	bin := filepath.Join(t.TempDir(), "nvsnap-idle")
	if out, err := exec.Command(cc, "-static", "-Os", "-Wall", "-Wextra", "-Werror", "-o", bin, "c/nvsnap-idle.c").CombinedOutput(); err != nil {
		if strings.Contains(string(out), "cannot find -lc") {
			t.Skip("no static libc")
		}
		t.Fatalf("compile: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	time.Sleep(200 * time.Millisecond)

	tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if len(tasks) != 1 {
		t.Errorf("want a single thread, got %d", len(tasks))
	}
	children := func() []string {
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
		return strings.Fields(string(b))
	}
	first := children()
	if len(first) != 1 {
		t.Fatalf("want one live child, got %v", first)
	}
	// The child is replaced as it exits, one at a time.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c := children()
		if len(c) > 1 {
			t.Fatalf("more than one child at once: %v", c)
		}
		if len(c) == 1 && c[0] != first[0] {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if c := children(); len(c) != 1 || c[0] == first[0] {
		t.Errorf("the child was not replaced after a tick: %v -> %v", first, c)
	}

	start := time.Now()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("want exit 0 on SIGTERM, got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s to stop", d)
	}
}

// buildIdle compiles the program the way the agent image does.
func buildIdle(t *testing.T) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	bin := filepath.Join(t.TempDir(), "nvsnap-idle")
	if out, err := exec.CommandContext(t.Context(), cc, "-static", "-Os", "-Wall", "-Wextra", "-Werror", "-o", bin, "c/nvsnap-idle.c").CombinedOutput(); err != nil { //nolint:gosec // runs the program under test
		if strings.Contains(string(out), "cannot find -lc") {
			t.Skip("no static libc")
		}
		t.Fatalf("compile: %v\n%s", err, out)
	}
	return bin
}

// A placeholder given a cold start idles until its marker exists; the agent
// creates the marker and kills it, and the restarted container execs the
// workload as its init, with its arguments intact, and again on every later
// restart.
func TestIdleColdStart(t *testing.T) {
	bin := buildIdle(t)
	dir := t.TempDir()
	marker, out := filepath.Join(dir, "cold-start"), filepath.Join(dir, "out")
	workload := []string{"/bin/sh", "-c", `echo "$0 $1" > ` + out + `; exec sleep 30`, "a b", "c"}
	args := append([]string{"--cold-start", marker, "--"}, workload...)

	idle := exec.CommandContext(t.Context(), bin, args...) //nolint:gosec // runs the program under test
	if err := idle.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the workload started without the marker")
	}
	if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", idle.Process.Pid)); strings.TrimSpace(string(b)) != "nvsnap-idle" {
		t.Errorf("without the marker it runs %q, want the idle program", b)
	}
	// What the agent does: the marker, then a kill of the container's init.
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = idle.Process.Kill()
	_ = idle.Wait()

	for restart := range 2 {
		_ = os.Remove(out)
		c := exec.CommandContext(t.Context(), bin, args...) //nolint:gosec // runs the program under test
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) == "a b c" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "a b c" {
			t.Fatalf("restart %d: the workload did not start with its arguments intact: %q", restart, b)
		}
		// Exec, not a child: the workload is the container's init.
		if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", c.Process.Pid)); strings.TrimSpace(string(b)) != "sleep" {
			t.Errorf("restart %d: pid %d runs %q, want the workload in place", restart, c.Process.Pid, b)
		}
		_ = c.Process.Kill()
		_ = c.Wait()
	}
}

// A workload that cannot be started is reported, not idled over.
func TestIdleColdStartExecFailure(t *testing.T) {
	bin := buildIdle(t)
	marker := filepath.Join(t.TempDir(), "cold-start")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := exec.CommandContext(t.Context(), bin, "--cold-start", marker, "--", "/nonexistent/workload").Run() //nolint:gosec // runs the program under test
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 127 {
		t.Errorf("want exit 127, got %v", err)
	}
}

// SIGTERM stops a placeholder that may cold start, without starting it.
func TestIdleColdStartStopsOnTerm(t *testing.T) {
	bin := buildIdle(t)
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), bin, "--cold-start", filepath.Join(dir, "cold-start"), "--", "/bin/sh", "-c", "touch "+filepath.Join(dir, "ran")) //nolint:gosec // runs the program under test
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("want exit 0 on SIGTERM, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Error("the workload ran on SIGTERM")
	}
}

func TestIdleUsage(t *testing.T) {
	bin := buildIdle(t)
	for _, args := range [][]string{
		{"--cold-start"},
		{"--cold-start", "/m", "--"},
		{"--cold-start", "", "--", "/bin/true"},
		{"--cold-start", "/m", "x", "/bin/true"},
		{"--other", "/m", "--", "/bin/true"},
	} {
		err := exec.CommandContext(t.Context(), bin, args...).Run() //nolint:gosec // runs the program under test
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Errorf("%q: want exit 2, got %v", args, err)
		}
	}
}
