/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunUntilSuccessRetriesNonzeroExits(t *testing.T) {
	runner := &sequenceRunner{results: []Result{{ExitCode: 1}, {ExitCode: 1}, {ExitCode: 0, Stdout: "ready"}}}
	result, err := RunUntilSuccess(context.Background(), runner, "check readiness", "1s", "1ms")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Stdout != "ready" || len(runner.runs) != 3 {
		t.Fatalf("result=%+v runs=%v", result, runner.runs)
	}
}

func TestRunUntilSuccessStopsOnExecutionFailure(t *testing.T) {
	runner := &errorRunner{err: errors.New("parse failed")}
	_, err := RunUntilSuccess(context.Background(), runner, "bad command", "1s", "1ms")
	if err == nil || !strings.Contains(err.Error(), "did not execute") {
		t.Fatalf("error=%v", err)
	}
	if runner.runs != 1 {
		t.Fatalf("runs=%d want=1", runner.runs)
	}
}

func TestRunUntilSuccessTimesOut(t *testing.T) {
	runner := &recordingRunner{nextResult: Result{ExitCode: 1}}
	_, err := RunUntilSuccess(context.Background(), runner, "not ready", "5ms", "1ms")
	if err == nil || !strings.Contains(err.Error(), "did not succeed within") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunUntilSuccessRejectsInvalidDurations(t *testing.T) {
	runner := &recordingRunner{}
	for _, tc := range []struct {
		timeout  string
		interval string
	}{
		{timeout: "invalid", interval: "1s"},
		{timeout: "1s", interval: "0s"},
	} {
		if _, err := RunUntilSuccess(context.Background(), runner, "check", tc.timeout, tc.interval); err == nil {
			t.Fatalf("timeout=%q interval=%q: expected error", tc.timeout, tc.interval)
		}
	}
}

type errorRunner struct {
	err  error
	runs int
}

func (r *errorRunner) Run(_ context.Context, _ string) (Result, error) {
	r.runs++
	return Result{ExitCode: -1}, r.err
}

func (r *errorRunner) RunWithSensitiveStdin(ctx context.Context, command, _ string) (Result, error) {
	return r.Run(ctx, command)
}

func (r *errorRunner) RunWithTTY(ctx context.Context, command string) (Result, error) {
	return r.Run(ctx, command)
}
