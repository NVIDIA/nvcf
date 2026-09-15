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

package dsl

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHelmfileEnvironmentPathSupportsKnownStacks(t *testing.T) {
	repoRoot := t.TempDir()
	for _, stack := range []string{"self-managed", "observability", "nvcf-compute-plane"} {
		t.Run(stack, func(t *testing.T) {
			got, err := HelmfileEnvironmentPath(repoRoot, stack, "local-bdd")
			if err != nil {
				t.Fatalf("environment path: %v", err)
			}
			want := filepath.Join(repoRoot, "deploy", "stacks", stack, "environments", "local-bdd.yaml")
			if got != want {
				t.Fatalf("path = %q, want %q", got, want)
			}
		})
	}
}

func TestHelmfileEnvironmentPathRejectsInvalidInput(t *testing.T) {
	repoRoot := t.TempDir()
	tests := []struct {
		name        string
		root        string
		stack       string
		environment string
		want        string
	}{
		{name: "relative root", root: "repo", stack: "self-managed", environment: "local", want: "repository root must be absolute"},
		{name: "unknown stack", root: repoRoot, stack: "other", environment: "local", want: `unsupported Helmfile stack "other"`},
		{name: "empty environment", root: repoRoot, stack: "self-managed", environment: "", want: `invalid Helmfile environment name ""`},
		{name: "path traversal", root: repoRoot, stack: "self-managed", environment: "../local", want: `invalid Helmfile environment name "../local"`},
		{name: "path separator", root: repoRoot, stack: "self-managed", environment: "team/local", want: `invalid Helmfile environment name "team/local"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := HelmfileEnvironmentPath(tc.root, tc.stack, tc.environment)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
