// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// toolErr runs script as the tool would fail and wraps its error the way
// gpushareToolEnv does, with the tool's output in the message.
func toolErr(t *testing.T, script string) error {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "sh", "-c", script).CombinedOutput() //nolint:gosec // fixed scripts of this test
	if err == nil {
		t.Fatalf("%q succeeded", script)
	}
	return fmt.Errorf("%s suspend 7: %w (output: %s)", gpushareToolName, err, strings.TrimSpace(string(out)))
}

// A suspend that exited rolled back unless the tool says its rollback
// failed; one that did not exit, or whose rollback failed, leaves the
// source in a state to replace; a tool that never started changed nothing.
func TestSuspendFailure(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		stuck bool
	}{
		{"rolled back", toolErr(t, `echo "suspend failed; rolled back, the workload runs as before" >&2; exit 1`), false},
		{"rollback failed", toolErr(t, `echo "`+suspendRollbackFailed+`: the workload may be stuck" >&2; exit 1`), true},
		{"killed", toolErr(t, `kill -9 $$`), true},
		{"never started", fmt.Errorf("%s suspend 7: %w", gpushareToolName, exec.ErrNotFound), false},
	}
	for _, c := range cases {
		err := suspendFailure(c.err)
		if got := errors.Is(err, errSourceLeftSuspended); got != c.stuck {
			t.Errorf("%s: left suspended = %v, want %v (%v)", c.name, got, c.stuck, err)
		}
		if !errors.Is(err, c.err) && !strings.Contains(err.Error(), c.err.Error()) {
			t.Errorf("%s: the tool's error is lost: %v", c.name, err)
		}
	}
}

// stubTool replaces the tool with fn for the test.
func stubTool(t *testing.T, fn func(ctx context.Context, args []string) (string, error)) {
	t.Helper()
	prev := gpushareToolRun
	gpushareToolRun = func(ctx context.Context, _ int, _ []string, _ time.Duration, args ...string) (string, error) {
		return fn(ctx, args)
	}
	t.Cleanup(func() { gpushareToolRun = prev })
}

// The caller learns the source is suspended whenever the suspend
// succeeded, even when the stop after it failed; and a suspend that rolled
// back leaves nothing to resume.
func TestGPUShareSuspendReportsTheSource(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	stopFailed := errors.New("stop: no such process")
	stubTool(t, func(_ context.Context, args []string) (string, error) {
		switch args[0] {
		case "gpus":
			return "GPU-1\n", nil
		case "stop":
			return "", stopFailed
		}
		return "suspended\n", nil
	})
	if _, suspended, err := gpushareSuspend(t.Context(), 1, []int{7}, "", log); !suspended || !errors.Is(err, stopFailed) {
		t.Errorf("stop failed after the suspend: suspended=%v err=%v, want suspended and the stop's error", suspended, err)
	}

	rolledBack := toolErr(t, `echo "suspend failed; rolled back, the workload runs as before" >&2; exit 1`)
	stubTool(t, func(_ context.Context, args []string) (string, error) {
		if args[0] == "gpus" {
			return "GPU-1\n", nil
		}
		return "", rolledBack
	})
	if _, suspended, err := gpushareSuspend(t.Context(), 1, []int{7}, "", log); suspended || err == nil || errors.Is(err, errSourceLeftSuspended) {
		t.Errorf("rolled-back suspend: suspended=%v err=%v, want neither", suspended, err)
	}
}

// A cancelled capture does not cancel a suspend under way: killed half-way
// it would leave the source in a state nothing records.
func TestGPUShareSuspendOutlivesTheCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var suspendCtxErr error
	stubTool(t, func(c context.Context, args []string) (string, error) {
		switch args[0] {
		case "gpus":
			return "GPU-1\n", nil
		case "stop":
			return "", c.Err()
		}
		suspendCtxErr = c.Err()
		return "suspended\n", nil
	})
	_, suspended, _ := gpushareSuspend(ctx, 1, []int{7}, "", logrus.NewEntry(logrus.New()))
	if suspendCtxErr != nil {
		t.Errorf("the suspend ran under a cancelled context: %v", suspendCtxErr)
	}
	if !suspended {
		t.Error("a suspend that succeeded under a cancelled capture was not reported: nothing would resume the source")
	}
}

// A dump runs to its end when its caller goes away, and one its own
// deadline kills is reported as leaving the workload to replace.
func TestRunDump(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := runDump(ctx, 10*time.Second, nil, "sh", "-c", "sleep 0.2; echo dumped")
	if err != nil || strings.TrimSpace(string(out)) != "dumped" {
		t.Errorf("a dump under a cancelled caller: %q, %v", out, err)
	}
	_, err = runDump(t.Context(), 100*time.Millisecond, nil, "sleep", "5")
	if !errors.Is(err, errSourceLeftLocked) || !sourceMustBeReplaced(err) {
		t.Errorf("a dump past its deadline: %v, want errSourceLeftLocked", err)
	}
	_, err = runDump(t.Context(), 10*time.Second, nil, "sh", "-c", "exit 1")
	if err == nil || sourceMustBeReplaced(err) {
		t.Errorf("a dump that failed by itself (CRIU cleaned up): %v", err)
	}
}

// Either sentinel, on its own or as text from another node's agent, asks
// for the workload to be replaced.
func TestSourceMustBeReplaced(t *testing.T) {
	for _, e := range []error{errSourceLeftSuspended, errSourceLeftLocked} {
		wrapped := fmt.Errorf("member 1: %w", e)
		if !sourceMustBeReplaced(wrapped) || !sourceMustBeReplacedText("agent said: "+wrapped.Error()) {
			t.Errorf("%v not recognised", e)
		}
	}
	if sourceMustBeReplaced(errors.New("criu dump failed")) || sourceMustBeReplacedText("criu dump failed") {
		t.Error("an ordinary failure asks for the workload to be replaced")
	}
}

// A tool run that passes its deadline stops what it forked too, not only
// the nsenter in front of it: nsenter -p forks the tool.
func TestKillGroupOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	out, err := os.CreateTemp(t.TempDir(), "pid")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & echo $! > "+out.Name()+"; wait")
	killGroupOnCancel(cmd)
	_ = cmd.Run()
	b, _ := os.ReadFile(out.Name())
	child, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("child pid %q: %v", b, err)
	}
	// Gone, or a zombie waiting for its reaper.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		st, err := os.ReadFile("/proc/" + strconv.Itoa(child) + "/stat")
		if err != nil || strings.Contains(string(st), ") Z ") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the forked child %d outlived the deadline", child)
		}
	}
}
