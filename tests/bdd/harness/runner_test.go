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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRunnerSuccess(t *testing.T) {
	runner := NewCommandRunner(t.TempDir(), "")
	result, err := runner.Run(context.Background(), "echo hello world")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello world") {
		t.Fatalf("stdout = %q, want hello world", result.Stdout)
	}
}

func TestCommandRunnerNonZeroExit(t *testing.T) {
	runner := NewCommandRunner(t.TempDir(), "")
	result, err := runner.Run(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if result.ExitCode != 1 {
		t.Fatalf("exit code = %d, want 1", result.ExitCode)
	}
}

func TestCommandRunnerEmptyCommand(t *testing.T) {
	runner := NewCommandRunner(t.TempDir(), "")
	if _, err := runner.Run(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestCommandRunnerCapturesStderr(t *testing.T) {
	runner := NewCommandRunner(t.TempDir(), "")
	// `ls` against a path that does not exist produces predictable stderr.
	result, _ := runner.Run(context.Background(), "ls /this/path/does/not/exist/bdd-test")
	if result.Stderr == "" {
		t.Fatal("expected stderr to be non-empty")
	}
}

func TestCommandRunnerHandlesQuotedArgs(t *testing.T) {
	runner := NewCommandRunner(t.TempDir(), "")
	// shlex tokenization keeps "hello world" as a single argv entry.
	result, err := runner.Run(context.Background(), `echo "hello world"`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result.Stdout, "hello world") {
		t.Fatalf("stdout = %q, want quoted arg preserved", result.Stdout)
	}
}

func TestCommandRunnerWritesLogFiles(t *testing.T) {
	logDir := t.TempDir()
	runner := NewCommandRunner(t.TempDir(), logDir)
	if _, err := runner.Run(context.Background(), "echo first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := runner.Run(context.Background(), "echo second"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, suffix := range []string{".cmd", ".stdout", ".stderr"} {
		if _, err := os.Stat(filepath.Join(logDir, "0001"+suffix)); err != nil {
			t.Fatalf("missing %s: %v", "0001"+suffix, err)
		}
		if _, err := os.Stat(filepath.Join(logDir, "0002"+suffix)); err != nil {
			t.Fatalf("missing %s: %v", "0002"+suffix, err)
		}
	}
}
