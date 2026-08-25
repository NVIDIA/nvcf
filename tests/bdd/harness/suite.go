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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Suite is the top-level lifecycle owner for one live BDD run. It
// builds nvcf-cli, exports NVCF_CLI and REPO_ROOT into the process
// environment, and exposes the Ledger, EnvLedger, and CommandCache
// that step handlers share across scenarios.
type Suite struct {
	Config          Config
	Runner          CommandRunner
	Ledger          *Ledger
	EnvLedger       *EnvLedger
	Cache           *CommandCache
	lifecycleLedger *Ledger
}

// SuiteOptions makes lifecycle side effects explicit. CLIStatePath is the
// exact nvcf-cli state file the suite may mutate. CleanupMode controls only
// the ncp-local pre-suite cleanup hook and must remain unset for attached
// remote targets.
type SuiteOptions struct {
	CLIStatePath string
	CleanupMode  CleanupMode
}

// NewSuite resolves Config, creates the run-id directory tree, builds
// nvcf-cli into Config.CLIPath, and exports the env vars feature files
// interpolate. The returned Suite is ready to drive a Godog scenario
// initializer.
func NewSuite(t *testing.T) (*Suite, error) {
	t.Helper()
	mode, err := ResolveCleanupMode()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return NewSuiteWithOptions(t, SuiteOptions{
		CLIStatePath: filepath.Join(home, ".nvcf-cli.nvcf-cli-local.state"),
		CleanupMode:  mode,
	})
}

// NewSuiteWithOptions builds the shared harness with explicit lifecycle
// inputs. Portable live suites use it to protect the state file selected by
// their target and to avoid inheriting local cleanup by accident.
func NewSuiteWithOptions(t *testing.T, options SuiteOptions) (*Suite, error) {
	t.Helper()
	if options.CLIStatePath == "" {
		return nil, errors.New("CLI state path must not be empty")
	}
	if !filepath.IsAbs(options.CLIStatePath) {
		return nil, errors.New("CLI state path must be absolute")
	}
	cfg, err := ResolveConfig()
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{cfg.OutDir, cfg.LedgerDir, cfg.CommandLogDir, cfg.DiagnosticsDir, filepath.Dir(cfg.CLIPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	runner := NewCommandRunner(cfg.RepoRoot, cfg.CommandLogDir)
	if err := buildCLI(cfg); err != nil {
		return nil, err
	}
	// t.Setenv scopes the env vars to the test that called NewSuite so
	// the live entry points do not leak them into later tests in the
	// same go test invocation.
	t.Setenv("NVCF_CLI", cfg.CLIPath)
	t.Setenv("REPO_ROOT", cfg.RepoRoot)
	suite := &Suite{
		Config:          cfg,
		Runner:          runner,
		Ledger:          NewLedger(filepath.Join(cfg.LedgerDir, "features")),
		EnvLedger:       NewEnvLedger(),
		Cache:           NewCommandCache(),
		lifecycleLedger: NewLedger(filepath.Join(cfg.LedgerDir, "lifecycle")),
	}
	// Pre-suite destructive cleanup (BDD_CLEANUP_MODE) runs BEFORE
	// snapshotting the CLI state file. Rationale: any destructive
	// mode invalidates the operator's pre-suite admin JWT (the cluster
	// it points at is gone or the api-keys release was wiped), so
	// snapshotting the pre-cleanup state would preserve nothing useful.
	// Cleanup never touches ~/.nvcf-cli.<config>.state, so the snapshot
	// taken after cleanup is the right baseline for teardown to
	// restore. Callers must pass a mode resolved through ResolveCleanupMode so
	// an invalid operator value cannot silently become no cleanup.
	if err := suite.RunPreSuiteCleanup(context.Background(), options.CleanupMode); err != nil {
		return nil, err
	}
	// nvcf-cli init writes its state file to ~/.nvcf-cli.<config-name>.state
	// (state.NewStateManagerForConfig resolves the path under the user's
	// home). Snapshot that path through the Ledger so teardown restores
	// whatever the operator had before (or removes the file if it did
	// not exist). HOME is intentionally not isolated here because k3d,
	// kubectl, docker, and helm all resolve their config under $HOME
	// and pointing HOME at an empty directory breaks the bootstrap
	// Givens that bring up the cluster.
	if err := suite.snapshotCLIStateFile(options.CLIStatePath); err != nil {
		return nil, err
	}
	return suite, nil
}

// snapshotCLIStateFile records the exact state file selected by the suite
// caller. The harness does not duplicate nvcf-cli's config-name-to-state-path
// rules because those rules belong to the CLI and may change independently.
func (s *Suite) snapshotCLIStateFile(statePath string) error {
	return s.lifecycleLedger.Snapshot(statePath)
}

// RestoreFeatureState restores file and environment mutations made through
// steps, then starts clean ledgers for another independently selectable
// feature. It does not restore suite-owned CLI state.
func (s *Suite) RestoreFeatureState() error {
	if err := errors.Join(s.Ledger.RestoreAll(), s.EnvLedger.RestoreAll()); err != nil {
		return err
	}
	s.Ledger = NewLedger(filepath.Join(s.Config.LedgerDir, "features"))
	s.EnvLedger = NewEnvLedger()
	return nil
}

// Teardown restores feature mutations and suite-owned CLI state. Live entry
// points should defer it.
func (s *Suite) Teardown() error {
	var lifecycleErr error
	if s.lifecycleLedger != nil {
		lifecycleErr = s.lifecycleLedger.RestoreAll()
	}
	return errors.Join(s.Ledger.RestoreAll(), s.EnvLedger.RestoreAll(), lifecycleErr)
}

// buildCLI invokes `go build` directly via exec.Command rather than
// routing through the CommandRunner so paths with spaces in the repo
// root cannot be silently mis-tokenized. The build runs inside the
// nvcf-cli source directory because the CLI has its own go.mod; the
// repo root is not a Go module.
func buildCLI(cfg Config) error {
	cliSource := filepath.Join(cfg.RepoRoot, "src", "clis", "nvcf-cli")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", cfg.CLIPath, ".")
	cmd.Dir = cliSource
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build nvcf-cli: %w (output: %s)", err, out)
	}
	return nil
}
