// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package idle tests cmd/nvsnap-idle, the C program that holds CRIU restore
// placeholders.
package idle

import (
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
