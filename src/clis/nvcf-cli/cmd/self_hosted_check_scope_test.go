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
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted"
)

// runCheckRecording runs `self-hosted check` in ModeSingle with the given
// flags and returns the namespaces the stale-namespace probe was asked about
// and how many times SIS was contacted.
func runCheckRecording(t *testing.T, args ...string) (namespaces []string, sisHits int64) {
	t.Helper()
	resetCheckFlags(t)
	var hits atomic.Int64
	sis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sis.Close)
	t.Setenv("NVCF_ICMS_URL", sis.URL)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")

	var mu sync.Mutex
	prev := newStaleNamespaceProberForSelfHosted
	newStaleNamespaceProberForSelfHosted = func() selfhosted.StaleNamespaceProber {
		return func(_ context.Context, _ string, ns []string) ([]selfhosted.StaleNamespace, error) {
			mu.Lock()
			defer mu.Unlock()
			namespaces = append(namespaces, ns...)
			return nil, nil
		}
	}
	t.Cleanup(func() { newStaleNamespaceProberForSelfHosted = prev })

	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs(append([]string{"self-hosted", "check", "--skip-cluster-validation", "--json"}, args...))
	_ = rootCmd.Execute()
	return namespaces, hits.Load()
}

// Each role's namespaces are scanned only when that role runs. Merging both in
// for a single-role run reported a stuck kai-scheduler on --control-plane.
func TestCheck_StaleNamespacesFollowTheRequestedRoles(t *testing.T) {
	ns, _ := runCheckRecording(t, "--control-plane")
	assert.Contains(t, ns, "vault-system")
	assert.NotContains(t, ns, "kai-scheduler", "--control-plane must not scan compute namespaces")

	ns, _ = runCheckRecording(t, "--compute-plane")
	assert.Contains(t, ns, "kai-scheduler")
	assert.NotContains(t, ns, "vault-system", "--compute-plane must not scan control-plane namespaces")

	ns, _ = runCheckRecording(t, "--all")
	assert.Contains(t, ns, "vault-system")
	assert.Contains(t, ns, "kai-scheduler", "--all scans both")
}

// A bare --pre skips SIS, which is not up before install; an explicit --all or
// --compute-plane still asks for it; --control-plane never contacts it.
func TestCheck_SISReachabilityScope(t *testing.T) {
	_, hits := runCheckRecording(t, "--pre")
	assert.Zero(t, hits, "--pre alone is pre-install")
	_, hits = runCheckRecording(t, "--pre", "--all")
	assert.NotZero(t, hits, "--all asks for every category, --pre included")
	_, hits = runCheckRecording(t, "--pre", "--compute-plane")
	assert.NotZero(t, hits)
	_, hits = runCheckRecording(t, "--control-plane")
	assert.Zero(t, hits, "SIS is a compute-plane check")
}

// --local-only overrides the required scope flag; the run says so on stderr.
func TestCheck_LocalOnlyNotesSkippedClusterChecks(t *testing.T) {
	resetCheckFlags(t)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	rootCmd.SetArgs([]string{"self-hosted", "check", "--all", "--local-only", "--json"})
	_ = rootCmd.Execute()
	assert.Contains(t, stderr.String(), "--local-only runs the local host checks only")
}

// An interrupted run reports the interrupt, not a verdict: with the validator
// cut short the results would otherwise read as a failure, or as a pass.
func TestCheck_InterruptExits130(t *testing.T) {
	resetCheckFlags(t)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	// Cobra keeps a subcommand's context from an earlier Execute, so set it
	// on the command that runs.
	selfHostedCheckCmd.SetContext(ctx)
	t.Cleanup(func() { selfHostedCheckCmd.SetContext(context.Background()) })
	rootCmd.SetArgs([]string{"self-hosted", "check", "--all", "--local-only", "--json"})
	err := rootCmd.Execute()

	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	assert.Regexp(t, `"event":"final".*"cancelled":true`, stderr.String(),
		"the JSON stream still ends with a final event, marked cancelled")
}

// runCheckWithBudget runs check --control-plane --json with the outer budget
// shortened to budget and a validator stub that holds its role until the
// context ends, or returns result at once.
func runCheckWithBudget(t *testing.T, budget time.Duration, validator func(context.Context) selfhosted.ClusterValidatorResult,
	args ...string) (error, string) {
	t.Helper()
	resetCheckFlags(t)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	prevBudget, prevCV, prevTools := checkBudget, newClusterValidatorForSelfHosted, checkPreflightTools
	checkBudget = func(time.Duration) time.Duration { return budget }
	checkPreflightTools = passingPreflightTools
	t.Cleanup(func() { checkPreflightTools = prevTools })
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(ctx context.Context, _ selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			return validator(ctx)
		}
	}
	t.Cleanup(func() { checkBudget, newClusterValidatorForSelfHosted = prevBudget, prevCV })
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	rootCmd.SetArgs(append([]string{"self-hosted", "check", "--control-plane", "--json",
		"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"}, args...))
	return rootCmd.Execute(), stderr.String()
}

func holdUntilDone(ctx context.Context) selfhosted.ClusterValidatorResult {
	<-ctx.Done()
	return selfhosted.ClusterValidatorResult{Err: ctx.Err()}
}

// A run that outlives its budget is a timeout, never a pass: exit 5 with a
// final event that says it failed, not success on whatever had run.
func TestCheck_BudgetSpentIsATimeout(t *testing.T) {
	for _, args := range [][]string{nil, {"--wait", "1m"}} {
		err, stderr := runCheckWithBudget(t, 300*time.Millisecond, holdUntilDone, args...)
		var exitErr *ExitCodeError
		require.ErrorAs(t, err, &exitErr, "args %v", args)
		assert.Equal(t, 5, exitErr.Code, "args %v", args)
		assert.Regexp(t, `"event":"final".*"success":false`, stderr, "args %v", args)
	}
}

// --wait keeps polling while the validator reports a rollout in progress,
// instead of stopping on the first run that exits 0.
func TestCheck_WaitPollsOnARolloutInProgress(t *testing.T) {
	calls := 0
	err, _ := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		calls++
		logs := "Validator role: control-plane\nCluster is NVCF-Ready\n"
		if calls == 1 {
			logs = "Validator role: control-plane\nnvcf/api: rollout in progress (updated: 2/3)\n" +
				"Cluster is NVCF-Ready (with warnings)\n"
		}
		return selfhosted.ClusterValidatorResult{Passed: true, Logs: logs}
	}, "--wait", "30s")
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "the second run, after the rollout, ends the wait")
}

// Results from an iteration that ran on a dead context are not a verdict, even
// when every row that did run passed; and a budget that runs out while --wait
// sleeps between polls is a timeout too.
func TestCheck_BudgetSpentIsATimeoutEvenWhenRowsPassed(t *testing.T) {
	passOnDeadCtx := func(ctx context.Context) selfhosted.ClusterValidatorResult {
		<-ctx.Done()
		return selfhosted.ClusterValidatorResult{Passed: true, Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
	}
	failFast := func(context.Context) selfhosted.ClusterValidatorResult {
		return selfhosted.ClusterValidatorResult{ExitCode: 1, Logs: "Validator role: control-plane\n"}
	}
	for name, tc := range map[string]struct {
		budget time.Duration
		v      func(context.Context) selfhosted.ClusterValidatorResult
		args   []string
	}{
		"passed on a dead context":      {300 * time.Millisecond, passOnDeadCtx, []string{"--wait", "1m"}},
		"budget ends between polls":     {3 * time.Second, failFast, []string{"--wait", "1m"}},
		"single run, dead-context pass": {300 * time.Millisecond, passOnDeadCtx, nil},
	} {
		err, stderr := runCheckWithBudget(t, tc.budget, tc.v, tc.args...)
		var exitErr *ExitCodeError
		require.ErrorAs(t, err, &exitErr, name)
		assert.Equal(t, 5, exitErr.Code, name)
		assert.Contains(t, stderr, `"event":"final"`, name)
	}
}

// Each --wait poll is a new validator run, so --no-cleanup would keep a full
// set of cluster-wide RBAC, Secret, ConfigMap and Job per poll.
func TestCheck_WaitWithNoCleanupIsRejected(t *testing.T) {
	resetCheckFlags(t)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	rootCmd.SetArgs([]string{"self-hosted", "check", "--control-plane", "--wait", "1m", "--no-cleanup"})
	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined with --wait")
}

// passingPreflightTools is the real tool set with every tool found at its
// minimum version.
func passingPreflightTools() []selfhosted.BinarySpec {
	specs := selfHostedPreflightTools()
	for i := range specs {
		v := specs[i].MinVer
		if v == nil {
			v = semver.MustParse("1.0.0")
		}
		name := specs[i].Name
		specs[i].LookPath = func(string) (string, error) { return "/usr/local/bin/" + name, nil }
		specs[i].Version = func(context.Context, string) (*semver.Version, error) { return v, nil }
	}
	return specs
}
