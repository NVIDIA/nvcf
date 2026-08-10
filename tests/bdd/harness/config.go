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

// Package harness carries the per-suite collaborators that the strict
// DSL step handlers depend on: command execution, file restoration,
// command-success caching, and the top-level lifecycle. Each type
// here is the contract spelled out in tests/bdd/PLAN.md.
package harness

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config carries every path and env var the suite needs. ResolveConfig
// fills it once at suite start; the values do not change for the rest
// of the run.
type Config struct {
	RepoRoot       string
	CLIPath        string
	OutDir         string
	LedgerDir      string
	CommandLogDir  string
	DiagnosticsDir string
}

// ResolveConfig discovers the repo root via `git rev-parse
// --show-toplevel` and assembles every other path under
// tests/bdd/out/<run-id>/. The CLI path is set to where the suite
// will build nvcf-cli; the binary itself is produced by Suite.
func ResolveConfig() (Config, error) {
	repoRoot, err := repoRootFromGit()
	if err != nil {
		return Config{}, err
	}
	runID := time.Now().UTC().Format("20060102-150405")
	outDir := filepath.Join(repoRoot, "tests", "bdd", "out", runID)
	return Config{
		RepoRoot:       repoRoot,
		CLIPath:        filepath.Join(outDir, "bin", "nvcf-cli"),
		OutDir:         outDir,
		LedgerDir:      filepath.Join(outDir, "originals"),
		CommandLogDir:  filepath.Join(outDir, "logs"),
		DiagnosticsDir: filepath.Join(outDir, "diagnostics"),
	}, nil
}

// repoRootFromGit shells out to git so the resolution survives any
// future move of tests/bdd under the repo.
func repoRootFromGit() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
