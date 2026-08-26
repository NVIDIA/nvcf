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
	"strings"
	"testing"
)

// Suite is the top-level lifecycle owner for one live BDD run. It
// builds nvcf-cli, exports NVCF_CLI and REPO_ROOT into the process
// environment, and exposes the Ledger, EnvLedger, and CommandCache
// that step handlers share across scenarios.
type Suite struct {
	Config             Config
	CLIConfigPath      string
	CLIStatePath       string
	Runner             CommandRunner
	Ledger             *Ledger
	EnvLedger          *EnvLedger
	Cache              *CommandCache
	lifecycleLedger    *Ledger
	featureStateLedger *Ledger
}

// SuiteOptions makes lifecycle side effects explicit. CLIConfigPath selects
// the CLI state namespace. IsolateCLIState copies that config and its current
// state into a unique session so concurrent smoke runs cannot share mutable
// CLI selection state. BeforeStateSnapshot lets a suite run its own preparation
// policy before the harness captures CLI state.
type SuiteOptions struct {
	CLIConfigPath       string
	IsolateCLIState     bool
	BeforeStateSnapshot func(context.Context, *Suite) error
}

// NewSuiteWithOptions builds the shared harness with explicit lifecycle
// inputs. Callers choose their CLI state and preparation policy.
func NewSuiteWithOptions(t *testing.T, options SuiteOptions) (*Suite, error) {
	t.Helper()
	if strings.TrimSpace(options.CLIConfigPath) == "" {
		return nil, errors.New("CLI config path must not be empty")
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
		CLIConfigPath:   options.CLIConfigPath,
		Runner:          runner,
		Ledger:          NewLedger(filepath.Join(cfg.LedgerDir, "features")),
		EnvLedger:       NewEnvLedger(),
		Cache:           NewCommandCache(),
		lifecycleLedger: NewLedger(filepath.Join(cfg.LedgerDir, "lifecycle")),
	}
	if options.BeforeStateSnapshot != nil {
		if err := options.BeforeStateSnapshot(context.Background(), suite); err != nil {
			return nil, err
		}
	}
	if options.IsolateCLIState {
		if err := suite.prepareIsolatedCLISession(options.CLIConfigPath); err != nil {
			return nil, err
		}
	} else {
		statePath, err := CLIStatePathForConfig(options.CLIConfigPath)
		if err != nil {
			return nil, err
		}
		suite.CLIStatePath = statePath
	}
	// HOME remains unchanged because k3d, kubectl, docker, and helm resolve
	// their own configuration there. Only the exact nvcf-cli state file is
	// ledger-backed.
	if err := suite.snapshotCLIStateFile(suite.CLIStatePath); err != nil {
		return nil, err
	}
	if err := suite.beginFeatureCLIState(); err != nil {
		return nil, err
	}
	return suite, nil
}

// CLIStatePathForConfig mirrors nvcf-cli's documented config-basename state
// namespace so callers cannot supply a config and restore a different file.
func CLIStatePathForConfig(configPath string) (string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return "", errors.New("CLI config path must not be empty")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	contextName := filepath.Base(configPath)
	if ext := filepath.Ext(contextName); ext != "" {
		contextName = strings.TrimSuffix(contextName, ext)
	}
	if contextName == "" || contextName == "default" || contextName == ".nvcf-cli" {
		return filepath.Join(home, ".nvcf-cli.state"), nil
	}
	return filepath.Join(home, fmt.Sprintf(".nvcf-cli.%s.state", contextName)), nil
}

func (s *Suite) prepareIsolatedCLISession(sourceConfigPath string) error {
	if !filepath.IsAbs(sourceConfigPath) {
		sourceConfigPath = filepath.Join(s.Config.RepoRoot, sourceConfigPath)
	}
	sourceConfig, err := os.ReadFile(sourceConfigPath)
	if err != nil {
		return fmt.Errorf("read CLI config for isolated session: %w", err)
	}
	runID := filepath.Base(s.Config.OutDir)
	isolationDir := filepath.Join(s.Config.OutDir, "cli-session")
	if err := os.MkdirAll(isolationDir, 0o700); err != nil {
		return fmt.Errorf("mkdir isolated CLI session: %w", err)
	}
	isolatedConfigPath := filepath.Join(isolationDir, "nvcf-cli-"+runID+".yaml")
	if err := os.WriteFile(isolatedConfigPath, sourceConfig, 0o600); err != nil {
		return fmt.Errorf("write isolated CLI config: %w", err)
	}
	sourceStatePath, err := CLIStatePathForConfig(sourceConfigPath)
	if err != nil {
		return err
	}
	isolatedStatePath, err := CLIStatePathForConfig(isolatedConfigPath)
	if err != nil {
		return err
	}
	s.CLIConfigPath = isolatedConfigPath
	s.CLIStatePath = isolatedStatePath
	if err := s.snapshotCLIStateFile(isolatedStatePath); err != nil {
		return err
	}
	state, err := os.ReadFile(sourceStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read CLI state for isolated session: %w", err)
	}
	if err := os.WriteFile(isolatedStatePath, state, 0o600); err != nil {
		return fmt.Errorf("write isolated CLI state: %w", err)
	}
	return nil
}

// snapshotCLIStateFile records the exact state file selected by the suite
// caller. The harness does not duplicate nvcf-cli's config-name-to-state-path
// rules because those rules belong to the CLI and may change independently.
func (s *Suite) snapshotCLIStateFile(statePath string) error {
	return s.lifecycleLedger.Snapshot(statePath)
}

func (s *Suite) beginFeatureCLIState() error {
	s.featureStateLedger = NewLedger(filepath.Join(s.Config.LedgerDir, "feature-cli-state"))
	return s.featureStateLedger.Snapshot(s.CLIStatePath)
}

// RestoreFeatureState restores step-owned files, environment, and the CLI
// state baseline, then resets the successful-command cache. One independently
// selectable feature cannot provide selection state or cached setup to another.
func (s *Suite) RestoreFeatureState() error {
	if err := errors.Join(s.Ledger.RestoreAll(), s.EnvLedger.RestoreAll(), s.featureStateLedger.RestoreAll()); err != nil {
		return err
	}
	s.Ledger = NewLedger(filepath.Join(s.Config.LedgerDir, "features"))
	s.EnvLedger = NewEnvLedger()
	s.Cache = NewCommandCache()
	return s.beginFeatureCLIState()
}

// Teardown restores feature mutations and suite-owned CLI state. Live entry
// points should defer it.
func (s *Suite) Teardown() error {
	var featureStateErr error
	if s.featureStateLedger != nil {
		featureStateErr = s.featureStateLedger.RestoreAll()
	}
	var lifecycleErr error
	if s.lifecycleLedger != nil {
		lifecycleErr = s.lifecycleLedger.RestoreAll()
	}
	return errors.Join(s.Ledger.RestoreAll(), s.EnvLedger.RestoreAll(), featureStateErr, lifecycleErr)
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
