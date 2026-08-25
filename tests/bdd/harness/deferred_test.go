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

func TestDeferredCommandsRunInReverseOrder(t *testing.T) {
	runner := &recordingRunner{}
	deferred := NewDeferredCommands()
	for _, command := range []string{"delete function", "uncordon cluster"} {
		if err := deferred.Add(command, "1s"); err != nil {
			t.Fatalf("add %q: %v", command, err)
		}
	}
	if err := deferred.Run(context.Background(), runner, t.TempDir()); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{"uncordon cluster", "delete function"}
	if strings.Join(runner.runs, "|") != strings.Join(want, "|") {
		t.Fatalf("runs=%v want=%v", runner.runs, want)
	}
	if err := deferred.Run(context.Background(), runner, t.TempDir()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(runner.runs) != len(want) {
		t.Fatalf("second run repeated commands: %v", runner.runs)
	}
}

func TestDeferredCommandsContinueAfterFailure(t *testing.T) {
	runner := &sequenceRunner{results: []Result{{ExitCode: 2}, {ExitCode: 0}}}
	deferred := NewDeferredCommands()
	if err := deferred.Add("second", "1s"); err != nil {
		t.Fatal(err)
	}
	if err := deferred.Add("first", "1s"); err != nil {
		t.Fatal(err)
	}
	err := deferred.Run(context.Background(), runner, "/logs")
	if err == nil || !strings.Contains(err.Error(), "deferred command 1 of 2") {
		t.Fatalf("error=%v", err)
	}
	if len(runner.runs) != 2 {
		t.Fatalf("runs=%v want both commands", runner.runs)
	}
}

func TestDeferredCommandsReportExecutionFailureWithoutCommandText(t *testing.T) {
	runner := &errorRunner{err: errors.New("parse failed")}
	deferred := NewDeferredCommands()
	if err := deferred.Add("restore secret target", "1s"); err != nil {
		t.Fatal(err)
	}
	err := deferred.Run(context.Background(), runner, "/logs")
	if err == nil || !strings.Contains(err.Error(), "did not execute") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "secret target") {
		t.Fatalf("error includes command text: %v", err)
	}
}

func TestDeferredCommandsRejectInvalidInputs(t *testing.T) {
	for name, input := range map[string][2]string{
		"empty command":   {"  ", "1s"},
		"invalid timeout": {"restore", "never"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := NewDeferredCommands().Add(input[0], input[1]); err == nil {
				t.Fatal("expected deferred command error")
			}
		})
	}
}

func TestDeferredCommandsRunAfterScenarioContextCancellation(t *testing.T) {
	runner := &recordingRunner{}
	deferred := NewDeferredCommands()
	if err := deferred.Add("restore", "1s"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := deferred.Run(ctx, runner, t.TempDir()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(runner.runs) != 1 {
		t.Fatalf("runs=%v want compensation attempt", runner.runs)
	}
}

type sequenceRunner struct {
	results []Result
	runs    []string
}

func (r *sequenceRunner) Run(_ context.Context, command string) (Result, error) {
	r.runs = append(r.runs, command)
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func (r *sequenceRunner) RunWithSensitiveStdin(ctx context.Context, command, _ string) (Result, error) {
	return r.Run(ctx, command)
}

func (r *sequenceRunner) RunWithTTY(ctx context.Context, command string) (Result, error) {
	return r.Run(ctx, command)
}
