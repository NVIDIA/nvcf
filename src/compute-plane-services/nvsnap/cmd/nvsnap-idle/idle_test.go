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

// A failed restore in a pod nothing would recreate starts the workload in
// place: SIGUSR1 makes the placeholder exec its command, in the same
// process, and leaves the marker so a restarted container starts it at once.
func TestIdleColdStart(t *testing.T) {
	bin := buildIdle(t)
	dir := t.TempDir()
	marker, out := filepath.Join(dir, "cold-start"), filepath.Join(dir, "out")
	workload := []string{"/bin/sh", "-c", `echo "$0 $1" > ` + out + `; exec sleep 30`, "a b", "c"}

	cmd := exec.CommandContext(t.Context(), bin, append([]string{"--cold-start", marker, "--"}, workload...)...) //nolint:gosec // runs the program under test
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the marker exists before the signal")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the workload started before the signal")
	}

	_ = cmd.Process.Signal(syscall.SIGUSR1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) == "a b c" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "a b c" {
		t.Fatalf("the workload did not start with its arguments intact: %q", b)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("no marker after the cold start: %v", err)
	}
	// Exec, not a child: the workload is the same process.
	if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); strings.TrimSpace(string(b)) != "sleep" {
		t.Errorf("pid %d runs %q, want the workload in place", pid, b)
	}

	// A restarted container finds the marker and starts the workload at once.
	_ = os.Remove(out)
	again := exec.CommandContext(t.Context(), bin, append([]string{"--cold-start", marker, "--"}, workload...)...) //nolint:gosec // runs the program under test
	if err := again.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Process.Kill(); _ = again.Wait() }()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("a restart with the marker present did not start the workload")
}

// SIGTERM still stops a placeholder that may cold start, without starting it.
func TestIdleColdStartStopsOnTerm(t *testing.T) {
	bin := buildIdle(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "cold-start")
	cmd := exec.CommandContext(t.Context(), bin, "--cold-start", marker, "--", "/bin/sh", "-c", "touch "+filepath.Join(dir, "ran")) //nolint:gosec // runs the program under test
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
	if _, err := os.Stat(marker); err == nil {
		t.Error("SIGTERM left a marker")
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
