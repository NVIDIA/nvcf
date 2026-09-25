/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func guardLog() *logrus.Entry {
	l := logrus.New()
	l.SetOutput(os.NewFile(0, os.DevNull))
	return logrus.NewEntry(l)
}

// writeCores creates core-<nspid>.img for each pid, as a completed dump would.
func writeCores(t *testing.T, dir string, nsPids ...int) {
	t.Helper()
	for _, p := range nsPids {
		f := filepath.Join(dir, fmt.Sprintf("core-%d.img", p))
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The case this guard exists for. Quiescing NCCL on a live multi-GPU engine can
// take the executor down with it; CRIU then dumps the surviving API server
// perfectly and exits 0. Without this check the agent publishes a checkpoint
// missing the processes that held the GPU state, and reports success.
func TestAssertGPUProcessesCapturedRejectsAPartialCapture(t *testing.T) {
	dir := t.TempDir()
	writeCores(t, dir, 301) // only the API server survived

	nsPids := map[int]int{
		3122085: 301, // API server  -- captured
		3122857: 730, // EngineCore  -- died during quiesce
		3123125: 894, // Worker_TP0  -- died during quiesce
		3123126: 895, // Worker_TP1  -- died during quiesce
	}

	err := assertGPUProcessesCaptured(dir, nsPids, guardLog())
	if err == nil {
		t.Fatal("a capture missing 3 of 4 GPU processes must be refused, not published")
	}
	for _, want := range []string{"3 of 4", "3122857", "3123125", "3123126"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name what is missing (%q): %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "3122085") {
		t.Errorf("the captured process must not be reported as missing: %v", err)
	}
}

func TestAssertGPUProcessesCapturedAcceptsACompleteCapture(t *testing.T) {
	dir := t.TempDir()
	writeCores(t, dir, 301, 730, 894, 895)

	nsPids := map[int]int{3122085: 301, 3122857: 730, 3123125: 894, 3123126: 895}
	if err := assertGPUProcessesCaptured(dir, nsPids, guardLog()); err != nil {
		t.Fatalf("a complete capture must pass: %v", err)
	}
}

// Losing even one rank makes the checkpoint unrestorable, so there is no
// "mostly captured" that should be allowed through.
func TestAssertGPUProcessesCapturedRejectsASingleMissingRank(t *testing.T) {
	dir := t.TempDir()
	writeCores(t, dir, 301, 730, 894)

	nsPids := map[int]int{3122085: 301, 3122857: 730, 3123125: 894, 3123126: 895}
	err := assertGPUProcessesCaptured(dir, nsPids, guardLog())
	if err == nil {
		t.Fatal("one missing rank must still fail the capture")
	}
	if !strings.Contains(err.Error(), "1 of 4") {
		t.Errorf("error should report 1 of 4 missing: %v", err)
	}
}

// A workload with no GPU processes is a legitimate capture, not an empty one.
// Failing here would break every CPU-only workload.
func TestAssertGPUProcessesCapturedIgnoresWorkloadsWithNoGPU(t *testing.T) {
	if err := assertGPUProcessesCaptured(t.TempDir(), nil, guardLog()); err != nil {
		t.Fatalf("no GPU processes means nothing to check: %v", err)
	}
	if err := assertGPUProcessesCaptured(t.TempDir(), map[int]int{}, guardLog()); err != nil {
		t.Fatalf("empty map means nothing to check: %v", err)
	}
}

// The mapping must be taken before the dump, because a non-leave-running dump
// kills the tree. This asserts the resolver's behaviour on pids that are
// already gone: skip them, do not fail the capture.
func TestGPUNSPidsSkipsUnresolvablePids(t *testing.T) {
	base := t.TempDir()
	// One live-looking pid with a NSpid line, one with no /proc entry at all.
	dir := filepath.Join(base, "4242")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"),
		[]byte("Name:\tvllm\nNSpid:\t4242\t301\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := gpuNSPids(base, []int{4242, 9999}, guardLog())
	if len(got) != 1 {
		t.Fatalf("expected only the resolvable pid, got %v", got)
	}
	if got[4242] != 301 {
		t.Errorf("expected host 4242 -> ns 301, got %v", got)
	}
	if _, ok := got[9999]; ok {
		t.Errorf("an unresolvable pid must be skipped, not invented: %v", got)
	}
}

// The innermost namespace pid is the one CRIU names its images after. Taking
// the first field instead would silently look for core-<hostpid>.img, which
// never exists, and fail every capture.
func TestGPUNSPidsUsesTheInnermostNamespacePid(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "5000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// host 5000 -> intermediate 200 -> container 7
	if err := os.WriteFile(filepath.Join(dir, "status"),
		[]byte("NSpid:\t5000\t200\t7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := gpuNSPids(base, []int{5000}, guardLog())
	if got[5000] != 7 {
		t.Errorf("expected innermost ns pid 7, got %v", got)
	}
}
