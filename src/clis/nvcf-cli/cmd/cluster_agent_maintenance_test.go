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

package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"nvcf-cli/internal/clusteragent"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var errFakeKill = errors.New("simulated kill failure")
var errFakeDrain = errors.New("simulated rollout failure")

// captureMaintStdout redirects os.Stdout while fn runs and returns what was
// written. It is local to this file so the test stays self-contained under
// Bazel, where each test target lists its sources explicitly.
func captureMaintStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// fakeMaintainer is a recording AgentMaintainer used to drive the cmd handlers
// without a real cluster.
type fakeMaintainer struct {
	target     *clusteragent.ClusterTarget
	resolveErr error

	drainResult   *clusteragent.DrainResult
	undrainResult *clusteragent.DrainResult
	drainErr      error
	killResult    *clusteragent.KillResult
	killErr       error

	drainCalls         int
	undrainCalls       int
	killFuncCalls      int
	killAllCalls       int
	killAllDryRunCalls int

	lastDrainOpts clusteragent.DrainOptions
	lastKillOpts  clusteragent.KillOptions
	lastFuncID    string
	lastVerID     string
}

func (f *fakeMaintainer) ResolveCluster(_ context.Context, _ string) (*clusteragent.ClusterTarget, error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	if f.target != nil {
		return f.target, nil
	}
	return &clusteragent.ClusterTarget{ClusterID: "c1", ClusterName: "edge-1", SystemNamespace: "nvca-system", RequestsNamespace: "nvcf-backend"}, nil
}

func (f *fakeMaintainer) Drain(_ context.Context, opts clusteragent.DrainOptions) (*clusteragent.DrainResult, error) {
	f.drainCalls++
	f.lastDrainOpts = opts
	if f.drainResult != nil {
		return f.drainResult, f.drainErr
	}
	return &clusteragent.DrainResult{ConfigChanged: true, RolloutTriggered: true, DryRun: opts.DryRun, Mode: "CordonAndDrain"}, f.drainErr
}

func (f *fakeMaintainer) Undrain(_ context.Context, opts clusteragent.DrainOptions) (*clusteragent.DrainResult, error) {
	f.undrainCalls++
	f.lastDrainOpts = opts
	if f.undrainResult != nil {
		return f.undrainResult, nil
	}
	return &clusteragent.DrainResult{ConfigChanged: true, RolloutTriggered: true, DryRun: opts.DryRun}, nil
}

func (f *fakeMaintainer) KillFunction(_ context.Context, functionID, versionID string, opts clusteragent.KillOptions) (*clusteragent.KillResult, error) {
	f.killFuncCalls++
	f.lastKillOpts = opts
	f.lastFuncID = functionID
	f.lastVerID = versionID
	return f.killResultFor(opts.DryRun), f.killErr
}

func (f *fakeMaintainer) KillAll(_ context.Context, opts clusteragent.KillOptions) (*clusteragent.KillResult, error) {
	if opts.DryRun {
		f.killAllDryRunCalls++
		return f.killResultFor(true), nil
	}
	f.killAllCalls++
	f.lastKillOpts = opts
	return f.killResultFor(false), f.killErr
}

func (f *fakeMaintainer) killResultFor(dryRun bool) *clusteragent.KillResult {
	if f.killResult == nil {
		return &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{}, DryRun: dryRun}
	}
	r := *f.killResult
	r.DryRun = dryRun
	return &r
}

func withFakeMaintainer(t *testing.T, f *fakeMaintainer) {
	t.Helper()
	prev := newAgentMaintainer
	newAgentMaintainer = func(_ *cobra.Command) (clusteragent.AgentMaintainer, error) { return f, nil }
	t.Cleanup(func() { newAgentMaintainer = prev })
	resetMaintenanceFlags(t)
}

// resetMaintenanceFlags restores the maintenance command flags and the global
// json flag after a test, since cobra retains parsed values on the command.
func resetMaintenanceFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		cmds := []*cobra.Command{
			clusterAgentCordonDrainCmd, clusterAgentUncordonCmd,
			clusterAgentKillFunctionCmd, clusterAgentKillAllCmd,
		}
		for _, c := range cmds {
			c.Flags().VisitAll(func(fl *pflag.Flag) {
				_ = fl.Value.Set(fl.DefValue)
				fl.Changed = false
			})
		}
		if fl := rootCmd.PersistentFlags().Lookup("json"); fl != nil {
			_ = fl.Value.Set("false")
			fl.Changed = false
		}
		jsonOutput = false
	})
}

func executeMaintenance(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), err
}

// --- cordon-and-drain / uncordon ---

func TestCordonDrainConfirmAccept(t *testing.T) {
	f := &fakeMaintainer{}
	withFakeMaintainer(t, f)

	out, err := executeMaintenance(t, "y\n", "cluster", "agent", "cordon-and-drain", "--compute-plane-context", "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.drainCalls != 1 {
		t.Fatalf("drainCalls = %d, want 1", f.drainCalls)
	}
	if !strings.Contains(out, "Proceed?") {
		t.Errorf("expected a confirmation prompt, got:\n%s", out)
	}
}

func TestCordonDrainConfirmReject(t *testing.T) {
	f := &fakeMaintainer{}
	withFakeMaintainer(t, f)

	_, err := executeMaintenance(t, "n\n", "cluster", "agent", "cordon-and-drain", "--compute-plane-context", "x")
	if err != nil {
		t.Fatalf("reject should not error: %v", err)
	}
	if f.drainCalls != 0 {
		t.Fatalf("drainCalls = %d, want 0 (rejected)", f.drainCalls)
	}
}

func TestCordonDrainYesBypass(t *testing.T) {
	f := &fakeMaintainer{}
	withFakeMaintainer(t, f)

	if _, err := executeMaintenance(t, "", "cluster", "agent", "cordon-and-drain", "--yes"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.drainCalls != 1 {
		t.Fatalf("drainCalls = %d, want 1", f.drainCalls)
	}
	if f.lastDrainOpts.DryRun {
		t.Error("DryRun should be false")
	}
}

func TestDrainAliasResolves(t *testing.T) {
	f := &fakeMaintainer{}
	withFakeMaintainer(t, f)

	if _, err := executeMaintenance(t, "", "cluster", "agent", "drain", "--yes"); err != nil {
		t.Fatalf("alias drain failed: %v", err)
	}
	if f.drainCalls != 1 {
		t.Fatalf("drainCalls = %d, want 1 via alias", f.drainCalls)
	}
}

func TestUncordonAndAlias(t *testing.T) {
	for _, name := range []string{"uncordon", "undrain"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeMaintainer{}
			withFakeMaintainer(t, f)
			if _, err := executeMaintenance(t, "", "cluster", "agent", name, "--yes"); err != nil {
				t.Fatalf("%s failed: %v", name, err)
			}
			if f.undrainCalls != 1 {
				t.Fatalf("undrainCalls = %d, want 1", f.undrainCalls)
			}
		})
	}
}

func TestExpectClusterIDGuard(t *testing.T) {
	t.Run("mismatch aborts before mutating", func(t *testing.T) {
		f := &fakeMaintainer{}
		withFakeMaintainer(t, f)
		_, err := executeMaintenance(t, "", "cluster", "agent", "cordon-and-drain", "--yes", "--expect-cluster-id", "wrong")
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("expected mismatch error, got %v", err)
		}
		if f.drainCalls != 0 {
			t.Errorf("drainCalls = %d, want 0 on mismatch", f.drainCalls)
		}
	})
	t.Run("match proceeds", func(t *testing.T) {
		f := &fakeMaintainer{}
		withFakeMaintainer(t, f)
		if _, err := executeMaintenance(t, "", "cluster", "agent", "cordon-and-drain", "--yes", "--expect-cluster-id", "c1"); err != nil {
			t.Fatalf("match should proceed: %v", err)
		}
		if f.drainCalls != 1 {
			t.Errorf("drainCalls = %d, want 1", f.drainCalls)
		}
	})
}

func TestDrainDryRunSkipsConfirm(t *testing.T) {
	f := &fakeMaintainer{}
	withFakeMaintainer(t, f)

	out, err := executeMaintenance(t, "", "cluster", "agent", "cordon-and-drain", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.drainCalls != 1 || !f.lastDrainOpts.DryRun {
		t.Fatalf("expected one dry-run drain call, got calls=%d dryRun=%v", f.drainCalls, f.lastDrainOpts.DryRun)
	}
	if strings.Contains(out, "Proceed?") {
		t.Errorf("dry-run must not prompt, got:\n%s", out)
	}
}

func TestDrainPartialResultEmittedOnError(t *testing.T) {
	f := &fakeMaintainer{
		drainResult: &clusteragent.DrainResult{
			ClusterID: "c1", ClusterName: "edge-1", SystemNamespace: "nvca-system",
			Mode: "CordonAndDrain", ConfigChanged: true, RolloutTriggered: false,
			Message: "agent-config updated but failed to restart NVCA",
		},
		drainErr: errFakeDrain,
	}
	withFakeMaintainer(t, f)

	out, err := executeMaintenance(t, "", "cluster", "agent", "cordon-and-drain", "--yes")
	if err == nil {
		t.Fatal("expected the drain error to propagate")
	}
	if !strings.Contains(out, "Drain cluster") || !strings.Contains(out, "agent-config updated") {
		t.Errorf("expected the partial drain result to be printed before the error, got:\n%s", out)
	}
}

// --- kill-function ---

func TestKillFunctionArgValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"no args", []string{"cluster", "agent", "kill-function", "--yes"}, true},
		{"too many args", []string{"cluster", "agent", "kill-function", "fn", "v1", "extra", "--yes"}, true},
		{"one arg ok", []string{"cluster", "agent", "kill-function", "fn", "--yes"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMaintainer{killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn"}}}}
			withFakeMaintainer(t, f)
			_, err := executeMaintenance(t, "", tc.args...)
			if tc.wantErr != (err != nil) {
				t.Fatalf("args %v: err = %v, wantErr = %v", tc.args, err, tc.wantErr)
			}
		})
	}
}

func TestKillFunctionPassesPositionals(t *testing.T) {
	f := &fakeMaintainer{killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn", FunctionVersionID: "v2"}}}}
	withFakeMaintainer(t, f)

	if _, err := executeMaintenance(t, "", "cluster", "agent", "kill-function", "fn", "v2", "--yes"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.killFuncCalls != 1 || f.lastFuncID != "fn" || f.lastVerID != "v2" {
		t.Fatalf("got calls=%d fn=%q ver=%q", f.killFuncCalls, f.lastFuncID, f.lastVerID)
	}
}

func TestKillFunctionReject(t *testing.T) {
	f := &fakeMaintainer{}
	withFakeMaintainer(t, f)

	if _, err := executeMaintenance(t, "n\n", "cluster", "agent", "kill-function", "fn"); err != nil {
		t.Fatalf("reject should not error: %v", err)
	}
	if f.killFuncCalls != 0 {
		t.Fatalf("killFuncCalls = %d, want 0 (rejected)", f.killFuncCalls)
	}
}

func TestKillFunctionDryRun(t *testing.T) {
	f := &fakeMaintainer{killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn"}}}}
	withFakeMaintainer(t, f)

	if _, err := executeMaintenance(t, "", "cluster", "agent", "kill-function", "fn", "--dry-run"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.killFuncCalls != 1 || !f.lastKillOpts.DryRun {
		t.Fatalf("expected one dry-run kill, got calls=%d dryRun=%v", f.killFuncCalls, f.lastKillOpts.DryRun)
	}
}

func TestKillFunctionPartialFailureReturnsError(t *testing.T) {
	f := &fakeMaintainer{
		killResult: &clusteragent.KillResult{
			RequestsNamespace: "nvcf-backend",
			Affected:          []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn", Error: "boom"}},
			FailedCount:       1,
		},
		killErr: errFakeKill,
	}
	withFakeMaintainer(t, f)

	out, err := executeMaintenance(t, "", "cluster", "agent", "kill-function", "fn", "--yes")
	if err == nil {
		t.Fatal("expected the aggregate error to propagate")
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("expected the failed request to be printed, got:\n%s", out)
	}
}

// --- kill-all ---

func TestKillAllTypeInInteractive(t *testing.T) {
	makeFake := func() *fakeMaintainer {
		return &fakeMaintainer{
			target:     &clusteragent.ClusterTarget{ClusterID: "c1", ClusterName: "edge-1", SystemNamespace: "nvca-system", RequestsNamespace: "nvcf-backend"},
			killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn"}}},
		}
	}

	t.Run("correct name proceeds", func(t *testing.T) {
		f := makeFake()
		withFakeMaintainer(t, f)
		out, err := executeMaintenance(t, "edge-1\n", "cluster", "agent", "kill-all")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.killAllCalls != 1 {
			t.Fatalf("killAllCalls = %d, want 1", f.killAllCalls)
		}
		if !strings.Contains(out, "Type the cluster name") {
			t.Errorf("expected type-in prompt, got:\n%s", out)
		}
	})

	t.Run("wrong name aborts", func(t *testing.T) {
		f := makeFake()
		withFakeMaintainer(t, f)
		if _, err := executeMaintenance(t, "nope\n", "cluster", "agent", "kill-all"); err != nil {
			t.Fatalf("abort should not error: %v", err)
		}
		if f.killAllCalls != 0 {
			t.Fatalf("killAllCalls = %d, want 0 (aborted)", f.killAllCalls)
		}
	})

	t.Run("falls back to id when cluster has no name", func(t *testing.T) {
		f := &fakeMaintainer{
			target:     &clusteragent.ClusterTarget{ClusterID: "c1", SystemNamespace: "nvca-system", RequestsNamespace: "nvcf-backend"},
			killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn"}}},
		}
		withFakeMaintainer(t, f)
		out, err := executeMaintenance(t, "c1\n", "cluster", "agent", "kill-all")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.killAllCalls != 1 {
			t.Fatalf("killAllCalls = %d, want 1", f.killAllCalls)
		}
		if !strings.Contains(out, "Type the cluster id") {
			t.Errorf("expected id fallback prompt, got:\n%s", out)
		}
	})
}

func TestKillAllAutomation(t *testing.T) {
	makeFake := func() *fakeMaintainer {
		return &fakeMaintainer{
			target:     &clusteragent.ClusterTarget{ClusterID: "c1", ClusterName: "edge-1", SystemNamespace: "nvca-system", RequestsNamespace: "nvcf-backend"},
			killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn"}}},
		}
	}

	t.Run("yes without confirm errors", func(t *testing.T) {
		f := makeFake()
		withFakeMaintainer(t, f)
		_, err := executeMaintenance(t, "", "cluster", "agent", "kill-all", "--yes")
		if err == nil || !strings.Contains(err.Error(), "--confirm") {
			t.Fatalf("expected --confirm requirement error, got %v", err)
		}
		if f.killAllCalls != 0 {
			t.Errorf("killAllCalls = %d, want 0", f.killAllCalls)
		}
	})

	t.Run("yes with wrong confirm errors", func(t *testing.T) {
		f := makeFake()
		withFakeMaintainer(t, f)
		_, err := executeMaintenance(t, "", "cluster", "agent", "kill-all", "--yes", "--confirm", "wrong")
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("expected confirm mismatch error, got %v", err)
		}
		if f.killAllCalls != 0 {
			t.Errorf("killAllCalls = %d, want 0", f.killAllCalls)
		}
	})

	t.Run("yes with matching confirm proceeds", func(t *testing.T) {
		f := makeFake()
		withFakeMaintainer(t, f)
		if _, err := executeMaintenance(t, "", "cluster", "agent", "kill-all", "--yes", "--confirm", "edge-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.killAllCalls != 1 {
			t.Fatalf("killAllCalls = %d, want 1", f.killAllCalls)
		}
	})
}

func TestKillAllEmptyClusterNoConfirm(t *testing.T) {
	f := &fakeMaintainer{
		target:     &clusteragent.ClusterTarget{ClusterID: "c1", RequestsNamespace: "nvcf-backend"},
		killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{}},
	}
	withFakeMaintainer(t, f)

	if _, err := executeMaintenance(t, "", "cluster", "agent", "kill-all"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.killAllCalls != 0 {
		t.Fatalf("killAllCalls = %d, want 0 (empty cluster)", f.killAllCalls)
	}
	if f.killAllDryRunCalls != 1 {
		t.Fatalf("expected one preview call, got %d", f.killAllDryRunCalls)
	}
}

func TestKillAllDryRun(t *testing.T) {
	f := &fakeMaintainer{
		target:     &clusteragent.ClusterTarget{ClusterID: "c1", RequestsNamespace: "nvcf-backend"},
		killResult: &clusteragent.KillResult{RequestsNamespace: "nvcf-backend", Affected: []clusteragent.KilledRequest{{Name: "r1", FunctionID: "fn"}}},
	}
	withFakeMaintainer(t, f)

	if _, err := executeMaintenance(t, "", "cluster", "agent", "kill-all", "--dry-run"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.killAllCalls != 0 {
		t.Fatalf("dry-run must not run a real kill-all, got %d", f.killAllCalls)
	}
	if f.killAllDryRunCalls != 1 {
		t.Fatalf("expected one dry-run kill-all, got %d", f.killAllDryRunCalls)
	}
}

// --- JSON output ---

func TestDrainJSONOutput(t *testing.T) {
	f := &fakeMaintainer{drainResult: &clusteragent.DrainResult{ClusterID: "c1", Mode: "CordonAndDrain", ConfigChanged: true, RolloutTriggered: true}}
	withFakeMaintainer(t, f)

	var err error
	out := captureMaintStdout(t, func() {
		_, err = executeMaintenance(t, "", "cluster", "agent", "cordon-and-drain", "--yes", "--json")
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"mode": "CordonAndDrain"`) || !strings.Contains(out, `"clusterId": "c1"`) {
		t.Errorf("unexpected JSON output:\n%s", out)
	}
}
