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
	"testing"
)

// recordingRunner is a local test double that captures every command
// text the cleanup hook invokes and returns a configurable Result.
// Modeled on the fakeRunner pattern in godog_test.go but kept local
// to this file so the harness unit tests stay self-contained.
type recordingRunner struct {
	runs       []string
	nextResult Result
	nextErr    error
}

func (r *recordingRunner) Run(_ context.Context, command string) (Result, error) {
	r.runs = append(r.runs, command)
	return r.nextResult, r.nextErr
}

func TestResolveCleanupMode(t *testing.T) {
	cases := map[string]struct {
		envValue string
		want     CleanupMode
		wantErr  bool
	}{
		"unset":               {envValue: "", want: CleanupNone},
		"stack-single":        {envValue: "stack-single", want: CleanupStackSingle},
		"stack-multi":         {envValue: "stack-multi", want: CleanupStackMulti},
		"topology-single":     {envValue: "topology-single", want: CleanupTopologySingle},
		"topology-multi":      {envValue: "topology-multi", want: CleanupTopologyMulti},
		"typo rejected":       {envValue: "stack_single", wantErr: true},
		"legacy var rejected": {envValue: "fresh-topology", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BDD_CLEANUP_MODE", tc.envValue)
			got, err := ResolveCleanupMode()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestRunPreSuiteCleanup_invokesRightCommand(t *testing.T) {
	cases := []struct {
		mode    CleanupMode
		wantCmd string
	}{
		{CleanupTopologySingle, "make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local"},
		{CleanupTopologyMulti, "make -C tools/ncp-local-cluster destroy-all-ncp-local SHELL=/bin/bash"},
		{CleanupStackSingle, "tests/bdd/scripts/destroy-stack.sh single"},
		{CleanupStackMulti, "tests/bdd/scripts/destroy-stack.sh multi"},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			runner := &recordingRunner{}
			suite := &Suite{Runner: runner, Config: Config{CommandLogDir: t.TempDir()}}
			if err := suite.RunPreSuiteCleanup(context.Background(), tc.mode); err != nil {
				t.Fatalf("RunPreSuiteCleanup: %v", err)
			}
			if len(runner.runs) != 1 {
				t.Fatalf("runs=%d want=1", len(runner.runs))
			}
			if got := runner.runs[0]; got != tc.wantCmd {
				t.Fatalf("got %q want %q", got, tc.wantCmd)
			}
		})
	}
}

func TestRunPreSuiteCleanup_noopOnNone(t *testing.T) {
	runner := &recordingRunner{}
	suite := &Suite{Runner: runner, Config: Config{CommandLogDir: t.TempDir()}}
	if err := suite.RunPreSuiteCleanup(context.Background(), CleanupNone); err != nil {
		t.Fatalf("CleanupNone should be a no-op, got %v", err)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("CleanupNone should not invoke the runner, got %v", runner.runs)
	}
}

func TestRunPreSuiteCleanup_nonzeroExitFailsSuite(t *testing.T) {
	runner := &recordingRunner{nextResult: Result{ExitCode: 2}}
	suite := &Suite{Runner: runner, Config: Config{CommandLogDir: t.TempDir()}}
	if err := suite.RunPreSuiteCleanup(context.Background(), CleanupStackSingle); err == nil {
		t.Fatal("expected error on non-zero exit")
	}
}
