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
	"context"
	"encoding/json"
	"strings"
	"testing"

	"nvcf-cli/internal/clusteragent"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// fakeValidator is a recording AgentValidator used to drive the cmd handlers
// without a real cluster.
type fakeValidator struct {
	result       *clusteragent.ValidationResult
	deployResult *clusteragent.DeploymentValidation
	err          error
	deployErr    error

	validateCalls int
	deployCalls   int
	lastOpts      clusteragent.ValidateOptions
	lastFuncID    string
	lastVerID     string
}

func (f *fakeValidator) Validate(_ context.Context, opts clusteragent.ValidateOptions) (*clusteragent.ValidationResult, error) {
	f.validateCalls++
	f.lastOpts = opts
	if f.result != nil {
		return f.result, f.err
	}
	return &clusteragent.ValidationResult{
		ClusterID:   "c1",
		ClusterName: "edge-1",
		Checks: []clusteragent.CheckResult{
			{Name: clusteragent.CheckNVCAReachable, Status: clusteragent.CheckPassed, Message: "ready"},
		},
	}, f.err
}

func (f *fakeValidator) ValidateDeployment(_ context.Context, functionID, versionID string, opts clusteragent.ValidateOptions) (*clusteragent.DeploymentValidation, error) {
	f.deployCalls++
	f.lastFuncID = functionID
	f.lastVerID = versionID
	f.lastOpts = opts
	if f.deployResult != nil {
		return f.deployResult, f.deployErr
	}
	return &clusteragent.DeploymentValidation{
		FunctionID:        functionID,
		FunctionVersionID: versionID,
		Checks: []clusteragent.CheckResult{
			{Name: "pod-readiness", Status: clusteragent.CheckPassed, Message: "healthy"},
		},
	}, f.deployErr
}

func withFakeValidator(t *testing.T, f *fakeValidator) {
	t.Helper()
	prev := newAgentValidator
	newAgentValidator = func(_ *cobra.Command) (clusteragent.AgentValidator, error) { return f, nil }
	t.Cleanup(func() { newAgentValidator = prev })
	resetValidateFlags(t)
}

// resetValidateFlags restores the validate command flags and the global json
// flag after a test. The bound --check slice is reset directly because pflag's
// StringSlice appends on repeated Set calls within one process.
func resetValidateFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, c := range []*cobra.Command{clusterAgentValidateCmd, clusterAgentValidateDeploymentCmd} {
			c.Flags().VisitAll(func(fl *pflag.Flag) {
				if fl.Value.Type() == "stringSlice" {
					fl.Changed = false
					return
				}
				_ = fl.Value.Set(fl.DefValue)
				fl.Changed = false
			})
		}
		clusterAgentValidateFlags.checks = nil
		if fl := rootCmd.PersistentFlags().Lookup("json"); fl != nil {
			_ = fl.Value.Set("false")
			fl.Changed = false
		}
		jsonOutput = false
	})
}

func executeValidate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), err
}

func TestValidateAllPassExitsZero(t *testing.T) {
	f := &fakeValidator{}
	withFakeValidator(t, f)

	out, err := executeValidate(t, "cluster", "agent", "validate", "--compute-plane-context", "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.validateCalls != 1 {
		t.Fatalf("validateCalls = %d, want 1", f.validateCalls)
	}
	if !strings.Contains(out, "Cluster Validation") || !strings.Contains(out, "PASS") {
		t.Errorf("expected a pass table, got:\n%s", out)
	}
	if !strings.Contains(out, "1 passed, 0 warning, 0 failed") {
		t.Errorf("expected a summary line, got:\n%s", out)
	}
}

func TestValidateFailureExitsTwo(t *testing.T) {
	f := &fakeValidator{result: &clusteragent.ValidationResult{
		ClusterID: "c1",
		Checks: []clusteragent.CheckResult{
			{Name: clusteragent.CheckTLSCert, Status: clusteragent.CheckFailed, Message: "expired"},
		},
	}}
	withFakeValidator(t, f)

	_, err := executeValidate(t, "cluster", "agent", "validate")
	if err == nil {
		t.Fatal("expected a non-nil error on check failure")
	}
	if code := ExitCodeFromError(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestValidateJSONOutput(t *testing.T) {
	f := &fakeValidator{result: &clusteragent.ValidationResult{
		ClusterID:   "c1",
		ClusterName: "edge-1",
		Checks: []clusteragent.CheckResult{
			{Name: clusteragent.CheckGPUCapacity, Status: clusteragent.CheckPassed, Message: "8/8"},
		},
	}}
	withFakeValidator(t, f)

	// OutputJSON writes to os.Stdout, so capture it rather than the cobra buffer.
	var execErr error
	out := captureMaintStdout(t, func() {
		_, execErr = executeValidate(t, "--json", "cluster", "agent", "validate")
	})
	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
	var got clusteragent.ValidationResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got.ClusterID != "c1" || len(got.Checks) != 1 || got.Checks[0].Name != clusteragent.CheckGPUCapacity {
		t.Errorf("unexpected JSON result: %+v", got)
	}
}

func TestValidateCheckFlagForwarded(t *testing.T) {
	f := &fakeValidator{}
	withFakeValidator(t, f)

	if _, err := executeValidate(t, "cluster", "agent", "validate", "--check", "gpu-capacity", "--check", "tls-cert"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{clusteragent.CheckGPUCapacity, clusteragent.CheckTLSCert}
	if len(f.lastOpts.CheckNames) != len(want) {
		t.Fatalf("CheckNames = %v, want %v", f.lastOpts.CheckNames, want)
	}
	for i, n := range want {
		if f.lastOpts.CheckNames[i] != n {
			t.Errorf("CheckNames[%d] = %q, want %q", i, f.lastOpts.CheckNames[i], n)
		}
	}
}

func TestValidateRejectsUnknownCheck(t *testing.T) {
	f := &fakeValidator{}
	withFakeValidator(t, f)

	_, err := executeValidate(t, "cluster", "agent", "validate", "--check", "bogus")
	if err == nil {
		t.Fatal("expected an error for an unknown --check value")
	}
	if f.validateCalls != 0 {
		t.Errorf("validateCalls = %d, want 0 (rejected before running)", f.validateCalls)
	}
}

func TestValidateDeploymentRequiresFunctionID(t *testing.T) {
	f := &fakeValidator{}
	withFakeValidator(t, f)

	if _, err := executeValidate(t, "cluster", "agent", "validate-deployment"); err == nil {
		t.Fatal("expected an arg error when function-id is missing")
	}
	if f.deployCalls != 0 {
		t.Errorf("deployCalls = %d, want 0", f.deployCalls)
	}
}

func TestValidateDeploymentForwardsArgsAndJSON(t *testing.T) {
	f := &fakeValidator{}
	withFakeValidator(t, f)

	var execErr error
	out := captureMaintStdout(t, func() {
		_, execErr = executeValidate(t, "--json", "cluster", "agent", "validate-deployment", "fn-1", "v2")
	})
	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
	if f.lastFuncID != "fn-1" || f.lastVerID != "v2" {
		t.Errorf("forwarded ids = %q/%q, want fn-1/v2", f.lastFuncID, f.lastVerID)
	}
	var got clusteragent.DeploymentValidation
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got.FunctionID != "fn-1" || got.FunctionVersionID != "v2" {
		t.Errorf("unexpected JSON result: %+v", got)
	}
}
