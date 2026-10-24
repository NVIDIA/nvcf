// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeSized(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// Before its first restore, a checkpoint's budget is its CRIU estimate, its
// GPU memory at the reload rate, and the reload's fixed part; after, its
// last whole restore's work with a tenth more; never past the bound.
func TestRestoreSpanBudget(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, filepath.Join(dir, "pages-1.img"), 3<<30)              // 1 s of CRIU at 3 GiB/s
	writeSized(t, filepath.Join(dir, "gpushare", "chunks", "a"), 32<<30) // 2 s of reload at 16 GiB/s
	if got, want := restoreSpanBudget(dir), time.Second+restoreClockSlack+2*time.Second+restoreGPUSlack; got != want {
		t.Errorf("estimate = %s, want %s", got, want)
	}
	if err := recordRestoreSpan(dir, 100*time.Second); err != nil {
		t.Fatal(err)
	}
	if got, want := restoreSpanBudget(dir), 110*time.Second+restoreClockSlack; got != want {
		t.Errorf("recorded = %s, want %s", got, want)
	}
	if err := recordRestoreSpan(dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := restoreSpanBudget(dir); got != restoreClockMax {
		t.Errorf("budget = %s, want the bound %s", got, restoreClockMax)
	}
}

// CRIU's arguments: none without the budget; CRIU's own restore, waited out
// by CRIU; or the whole restore, with every task left stopped.
func TestRestoreClockArgs(t *testing.T) {
	dir := t.TempDir()
	if err := recordRestoreSpan(dir, 80*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, restoreClockFile), []byte("4000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if args, _ := restoreClockArgs(dir, false, true); args != nil {
		t.Errorf("without --timens-budget: %v", args)
	}
	args, b := restoreClockArgs(dir, true, false)
	if b != 5*time.Second+restoreClockSlack || strings.Join(args, " ") != "--timens-budget "+strconv.FormatInt(b.Milliseconds(), 10) {
		t.Errorf("CRIU only: %v %s", args, b)
	}
	args, b = restoreClockArgs(dir, true, true)
	want := "--timens-budget " + strconv.FormatInt(b.Milliseconds(), 10) + " --leave-stopped --timens-no-wait"
	if b != 88*time.Second+restoreClockSlack || strings.Join(args, " ") != want {
		t.Errorf("through the GPU reload: %v %s, want %q", args, b, want)
	}
}

func TestParseTimensMonotonic(t *testing.T) {
	ns, err := parseTimensMonotonic([]byte(`{"magic":"TIMENS","entries":[{"monotonic":{"tv_sec":1234,"tv_nsec":5},"boottime":{"tv_sec":2000,"tv_nsec":0}}]}`))
	if err != nil || ns != 1234*int64(time.Second)+5 {
		t.Errorf("ns = %d, %v", ns, err)
	}
	for _, bad := range []string{`{"entries":[]}`, `{"entries":[{"boottime":{"tv_sec":1}}]}`,
		`{"entries":[{"monotonic":{"tv_sec":1,"tv_nsec":1000000000}}]}`, `not json`} {
		if _, err := parseTimensMonotonic([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestParseTimensResumePIDs(t *testing.T) {
	if got, err := parseTimensResumePIDs([]byte("1\n609\n7\n")); err != nil || len(got) != 3 || got[1] != 609 {
		t.Errorf("pids = %v, %v", got, err)
	}
	for _, bad := range []string{"1\nx\n", "0\n", "-3\n"} {
		if _, err := parseTimensResumePIDs([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// stoppedChild starts a process and stops it, as CRIU leaves a restored
// task.
func stoppedChild(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	waitState(t, cmd.Process.Pid, "T")
	return cmd.Process.Pid
}

func taskState(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	s := string(b)
	return strings.Fields(s[strings.LastIndexByte(s, ')')+2:])[0]
}

func waitState(t *testing.T, pid int, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for taskState(pid) != want {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d in state %q, want %q", pid, taskState(pid), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The listed tasks are resumed, the ones to skip (the GPU processes) are
// left to their resume, and a listed task that is not there fails it.
func TestContinueRestoredTasks(t *testing.T) {
	resume, gpu := stoppedChild(t), stoppedChild(t)
	n, err := continueRestoredTasks("/proc", os.Getpid(), []int{resume, gpu}, []int{gpu})
	if err != nil || n != 1 {
		t.Fatalf("resumed %d, %v", n, err)
	}
	waitState(t, resume, "S")
	if s := taskState(gpu); s != "T" {
		t.Errorf("the GPU process was resumed: state %q", s)
	}
	if _, err := continueRestoredTasks("/proc", os.Getpid(), []int{1 << 22}, nil); err == nil {
		t.Error("a missing task did not fail the resume")
	}
}

func TestClockThroughGPUSupported(t *testing.T) {
	criu := "--timens-budget MS ...\n  --timens-no-wait ..."
	tool := "--hold-gate: ...\n--clock-at NS: ..."
	if !clockThroughGPUSupported(criu, tool) {
		t.Error("both support it")
	}
	if clockThroughGPUSupported("--timens-budget MS", tool) {
		t.Error("CRIU without --timens-no-wait")
	}
	if clockThroughGPUSupported(criu, "--hold-gate: ...") {
		t.Error("a tool without --clock-at")
	}
}

// The wait a resume reports is its largest "behind"; a pid past the dump
// waited for nothing.
func TestClockWaited(t *testing.T) {
	out := "pid=693 clock 16.250s behind the dump\npid=694 clock 0.001s behind the dump\n" +
		"pid=695 clock 2.000s past the dump\nresumed thawed all pids\n"
	if got := clockWaited(out); got != 16250*time.Millisecond {
		t.Errorf("waited %s, want 16.25s", got)
	}
	if got := clockWaited("pid=7 clock 3.000s past the dump\n"); got != 0 {
		t.Errorf("an overrun waited %s", got)
	}
	if got := clockWaited("resumed\n"); got != 0 {
		t.Errorf("a resume without --clock-at waited %s", got)
	}
}

// Every pid's remap reply is kept whole, and nothing else.
func TestRemapReplies(t *testing.T) {
	out := "fabric map: 283 handle(s) in /f/out.694; waiting for /f/in\n" +
		"pid=694 remap /f/in: ok remapped 333 mapping(s), 77 host buffer(s) in 3.10s: host register 2.40s (614400 MiB; prefault 31.2s, waited 0.0s)\n" +
		"pid=695 remap /f/in: ok remapped 333 mapping(s)\n" +
		"pid=694 clock 1.000s behind the dump\nresumed\n"
	got := remapReplies(out)
	if len(got) != 2 || !strings.HasSuffix(got[0], "waited 0.0s)") || !strings.HasPrefix(got[1], "pid=695 remap") {
		t.Errorf("remap replies %q", got)
	}
	if remapReplies("resumed\n") != nil {
		t.Error("a resume without remap has replies")
	}
}

func TestGPUShareResumeArgsClockAt(t *testing.T) {
	got := strings.Join(gpushareResumeArgs("", "", true, 1234, []int{7}), " ")
	if got != "--hold-gate --clock-at 1234 resume 7" {
		t.Errorf("args %q", got)
	}
}
