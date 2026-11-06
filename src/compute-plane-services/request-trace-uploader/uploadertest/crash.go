// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package uploadertest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// EnvCrashAt names the crash point a crash child stops at.
const EnvCrashAt = "RTU_TEST_CRASH_AT"

const (
	crashMarker       = "uploadertest: crash point "
	crashChildTimeout = 2 * time.Minute
)

// childEnvDrop lists variables the child must not inherit. Bazel's Go test
// runner reads these; a child that kept them would write over the parent's
// result files, wrap itself in another process, or filter out the test it
// was started to run.
var childEnvDrop = []string{
	EnvCrashAt,
	"COVERAGE_OUTPUT_FILE",
	"GO_TEST_WRAP",
	"TESTBRIDGE_TEST_ONLY",
	"TEST_SHARD_INDEX",
	"TEST_SHARD_STATUS_FILE",
	"TEST_TOTAL_SHARDS",
	"XML_OUTPUT_FILE",
}

// CrashPoint kills the current process with SIGKILL when point is the crash
// point named in EnvCrashAt, and returns otherwise.
//
// Production code does not import this package. A package that needs crash
// points exposes a hook it calls at each named point, and only its _test.go
// files set that hook to CrashPoint. CrashPoint also does nothing outside a
// test binary, so crash points are inactive in any other binary even if a
// hook is wired by mistake.
func CrashPoint(point string) {
	crashAt(point, testing.Testing())
}

func crashAt(point string, testBinary bool) {
	if !testBinary || point == "" || os.Getenv(EnvCrashAt) != point {
		return
	}
	fmt.Fprintf(os.Stderr, "%s%s\n", crashMarker, point)
	process, err := os.FindProcess(os.Getpid())
	if err == nil {
		err = process.Kill()
	}
	if err != nil {
		panic(fmt.Sprintf("uploadertest: kill at crash point %q: %v", point, err))
	}
	// Code after the point must never run, even if the signal lands late.
	for {
		time.Sleep(time.Hour)
	}
}

// CrashChild reports whether this process is a crash child started by
// RunCrash. A child test skips itself when this is false.
func CrashChild() bool {
	return os.Getenv(EnvCrashAt) != ""
}

// CrashResult reports how a crash child ended.
type CrashResult struct {
	// Killed reports that the child died of SIGKILL.
	Killed bool
	// Point is the crash point the child reported before it was killed.
	Point string
	// Output is the child's combined output, for failure messages.
	Output string
}

// RunCrash runs the top-level test named test in a child copy of the current
// test binary, with EnvCrashAt set to point and env (KEY=value) added, and
// waits for it. The child is expected either to be killed at a crash point or
// to pass; a child that fails, times out, or runs no test named test fails t.
func RunCrash(t testing.TB, test, point string, env ...string) CrashResult {
	t.Helper()
	if CrashChild() {
		t.Fatalf("uploadertest: %s is already set in the parent test process", EnvCrashAt)
	}

	ctx, cancel := context.WithTimeout(context.Background(), crashChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^"+regexp.QuoteMeta(test)+"$", "-test.count=1", "-test.v")
	cmd.Env = append(childEnv(), EnvCrashAt+"="+point, "GO_TEST_WRAP=0")
	cmd.Env = append(cmd.Env, env...)
	raw, err := cmd.CombinedOutput()
	output := string(raw)

	if ctx.Err() != nil {
		t.Fatalf("uploadertest: crash child %s timed out after %s\n%s", test, crashChildTimeout, output)
	}
	if !strings.Contains(output, "=== RUN   "+test+"\n") {
		t.Fatalf("uploadertest: crash child ran no test named %s\n%s", test, output)
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
		return CrashResult{Killed: true, Point: reportedPoint(output), Output: output}
	}
	if err != nil {
		t.Fatalf("uploadertest: crash child %s failed: %v\n%s", test, err, output)
	}
	return CrashResult{Output: output}
}

func childEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(childEnvDrop, name)
	})
}

func reportedPoint(output string) string {
	for line := range strings.Lines(output) {
		if point, ok := strings.CutPrefix(strings.TrimSpace(line), crashMarker); ok {
			return point
		}
	}
	return ""
}
