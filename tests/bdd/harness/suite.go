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
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// Suite is the top-level lifecycle owner for one live BDD run. It
// builds nvcf-cli, exports NVCF_CLI and REPO_ROOT into the process
// environment, and exposes the Ledger, EnvLedger, and CommandCache
// that step handlers share across scenarios.
type Suite struct {
	Config    Config
	Runner    CommandRunner
	Ledger    *Ledger
	EnvLedger *EnvLedger
	Cache     *CommandCache

	signalMu      sync.Mutex
	signalContext context.Context
	signalCancel  context.CancelFunc
	stepMu        sync.RWMutex
	teardownMu    sync.Mutex
}

// NewSuite resolves Config, creates the run-id directory tree, builds
// nvcf-cli into Config.CLIPath, and exports the env vars feature files
// interpolate. The returned Suite is ready to drive a Godog scenario
// initializer.
func NewSuite(t *testing.T) (*Suite, error) {
	t.Helper()
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
		Config:    cfg,
		Runner:    runner,
		Ledger:    NewLedger(cfg.LedgerDir),
		EnvLedger: NewEnvLedger(),
		Cache:     NewCommandCache(),
	}
	if err := CheckExternalTools([]string{"grpcurl"}); err != nil {
		return nil, err
	}
	// Pre-suite destructive cleanup (BDD_CLEANUP_MODE) runs BEFORE
	// snapshotting the CLI state file. Rationale: any destructive
	// mode invalidates the operator's pre-suite admin JWT (the cluster
	// it points at is gone or the api-keys release was wiped), so
	// snapshotting the pre-cleanup state would preserve nothing useful.
	// Cleanup never touches ~/.nvcf-cli.<config>.state, so the snapshot
	// taken after cleanup is the right baseline for teardown to
	// restore. ResolveCleanupMode rejects unknown values; a typo
	// aborts the suite before any work runs.
	mode, err := ResolveCleanupMode()
	if err != nil {
		return nil, err
	}
	if err := suite.RunPreSuiteCleanup(context.Background(), mode); err != nil {
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
	if err := suite.snapshotCLIStateFile("nvcf-cli-local"); err != nil {
		return nil, err
	}
	return suite, nil
}

// snapshotCLIStateFile records the pre-suite contents of the CLI state
// file the BDD scenarios mutate through nvcf-cli init. The contextName
// must match the basename (sans extension) of the --config file the
// features pass to nvcf-cli; today every feature uses
// tests/bdd/fixtures/nvcf-cli-local.yaml, so this is hardcoded. If a
// future feature introduces a second config, this call must be extended.
func (s *Suite) snapshotCLIStateFile(contextName string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	statePath := filepath.Join(home, fmt.Sprintf(".nvcf-cli.%s.state", contextName))
	return s.Ledger.Snapshot(statePath)
}

// Teardown restores every file the Ledger tracked and every env var
// the EnvLedger tracked. Live entry points should defer it.
func (s *Suite) Teardown() error {
	s.teardownMu.Lock()
	defer s.teardownMu.Unlock()
	return errors.Join(s.Ledger.RestoreAll(), s.EnvLedger.RestoreAll())
}

type signalSafeStepKey struct{}

type signalSafeStep struct {
	parent  context.Context
	once    sync.Once
	cleanup func()
}

// BeginSignalSafeStep marks one live BDD step active and returns a context that
// is canceled when SIGINT or SIGTERM starts cleanup. EndSignalSafeStep must be
// called with the returned context. Signal cleanup waits for every active step
// to finish, so a file-writing step cannot recreate a ledger-backed credential
// after restoration.
func (s *Suite) BeginSignalSafeStep(ctx context.Context) (context.Context, error) {
	s.signalMu.Lock()
	signalContext := s.signalContext
	s.signalMu.Unlock()
	if signalContext == nil {
		return ctx, errors.New("signal cleanup is not installed")
	}

	s.stepMu.RLock()
	stepContext, cancelStep := context.WithCancel(ctx)
	stopSignalCancel := context.AfterFunc(signalContext, cancelStep)
	step := &signalSafeStep{parent: ctx, cleanup: func() {
		stopSignalCancel()
		cancelStep()
		s.stepMu.RUnlock()
	}}
	return context.WithValue(stepContext, signalSafeStepKey{}, step), nil
}

// EndSignalSafeStep releases the live-step guard installed by
// BeginSignalSafeStep and returns the uncanceled parent context for the next
// Godog step. Repeated calls are harmless.
func (s *Suite) EndSignalSafeStep(ctx context.Context) context.Context {
	step, ok := ctx.Value(signalSafeStepKey{}).(*signalSafeStep)
	if !ok {
		return ctx
	}
	step.once.Do(step.cleanup)
	return step.parent
}

// InstallSignalCleanup restores ledger-backed files and environment variables
// before an interrupted live run exits. It first cancels the active step, then
// waits for that step to quiesce before restoring. The returned stop function
// joins the signal goroutine and must run before normal Teardown.
func (s *Suite) InstallSignalCleanup() func() {
	signals := make(chan os.Signal, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	s.signalMu.Lock()
	s.signalContext, s.signalCancel = context.WithCancel(context.Background())
	cancelSteps := s.signalCancel
	s.signalMu.Unlock()
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	var stopOnce sync.Once
	go func() {
		defer close(done)
		select {
		case sig := <-signals:
			cancelSteps()
			s.stepMu.Lock()
			if err := s.Teardown(); err != nil {
				fmt.Fprintf(os.Stderr, "BDD interrupt cleanup failed: %v\n", err)
			}
			os.Exit(signalExitCode(sig))
		case <-stop:
		}
	}()
	return func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(stop)
			<-done
			cancelSteps()
		})
	}
}

func signalExitCode(sig os.Signal) int {
	if value, ok := sig.(syscall.Signal); ok {
		return 128 + int(value)
	}
	return 1
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
