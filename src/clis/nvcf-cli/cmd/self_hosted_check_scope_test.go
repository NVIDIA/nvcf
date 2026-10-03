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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/progress"
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
	_, hits = runCheckRecording(t, "--pre", "--control-plane")
	assert.Zero(t, hits, "--pre visits the compute plane but --control-plane does not ask for SIS")
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
	checkBudget = func(d time.Duration) time.Duration { requestedBudget = d; return budget }
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

// requestedBudget is the outer budget the last runCheckWithBudget run asked for.
var requestedBudget time.Duration

func holdUntilDone(ctx context.Context) selfhosted.ClusterValidatorResult {
	<-ctx.Done()
	return selfhosted.ClusterValidatorResult{Err: ctx.Err()}
}

// checkEvents returns the JSON events of kind a --json run wrote to stderr,
// in order.
func checkEvents(t *testing.T, stderr, kind string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(stderr, "\n") {
		var m map[string]any
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &m) == nil && m["event"] == kind {
			out = append(out, m)
		}
	}
	return out
}

// finalEvent returns the run's final event.
func finalEvent(t *testing.T, stderr string) map[string]any {
	t.Helper()
	finals := checkEvents(t, stderr, "final")
	require.Len(t, finals, 1, "a run ends with exactly one final event")
	return finals[0]
}

// validatorRow returns the cluster-validator check_completed event.
func validatorRow(t *testing.T, stderr string) map[string]any {
	t.Helper()
	for _, e := range checkEvents(t, stderr, "check_completed") {
		if e["id"] == "cluster-validator" {
			return e
		}
	}
	t.Fatalf("no cluster-validator row in:\n%s", stderr)
	return nil
}

// A run that outlives its budget is a timeout, never a pass: exit 5 with a
// final event that says it failed, not success on whatever had run.
func TestCheck_BudgetSpentIsATimeout(t *testing.T) {
	for _, args := range [][]string{nil, {"--wait", "1m"}} {
		err, stderr := runCheckWithBudget(t, 300*time.Millisecond, holdUntilDone, args...)
		var exitErr *ExitCodeError
		require.ErrorAs(t, err, &exitErr, "args %v", args)
		assert.Equal(t, 5, exitErr.Code, "args %v", args)
		final := finalEvent(t, stderr)
		assert.Equal(t, false, final["success"], "args %v", args)
		assert.Equal(t, "timeout", final["verdict"], "args %v", args)
		assert.Contains(t, validatorRow(t, stderr)["message"], "cut short: ", "args %v", args)
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

// A budget that runs out after a check has its result changes nothing: the
// result is the verdict. A validator that passed passes, and its own failure
// is exit 2, not a timeout that tells an agent to retry with a longer budget.
func TestCheck_ResultsInHandWhenTheBudgetEndsAreGraded(t *testing.T) {
	ready := selfhosted.ClusterValidatorResult{Passed: true, Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
	for name, tc := range map[string]struct {
		result      selfhosted.ClusterValidatorResult
		args        []string
		wantCode    int
		wantVerdict string
	}{
		"passed":          {result: ready, wantVerdict: "ok"},
		"passed, --wait":  {result: ready, args: []string{"--wait", "1m"}, wantVerdict: "ok"},
		"its own timeout": {result: selfhosted.ClusterValidatorResult{Err: errors.New("the validator did not finish within 5m0s")}, wantCode: 2, wantVerdict: "failed"},
	} {
		err, stderr := runCheckWithBudget(t, 300*time.Millisecond, func(ctx context.Context) selfhosted.ClusterValidatorResult {
			<-ctx.Done()
			return tc.result
		}, tc.args...)
		if tc.wantCode == 0 {
			require.NoError(t, err, name)
		} else {
			var exitErr *ExitCodeError
			require.ErrorAs(t, err, &exitErr, name)
			assert.Equal(t, tc.wantCode, exitErr.Code, name)
		}
		final := finalEvent(t, stderr)
		assert.Equal(t, tc.wantCode == 0, final["success"], name)
		assert.Equal(t, tc.wantVerdict, final["verdict"], name)
	}
}

// A budget that runs out while --wait sleeps between polls is a timeout, and
// says the checks were still failing.
func TestCheck_BudgetEndingBetweenPollsIsATimeout(t *testing.T) {
	err, stderr := runCheckWithBudget(t, 2*time.Second, func(context.Context) selfhosted.ClusterValidatorResult {
		return selfhosted.ClusterValidatorResult{ExitCode: 1, Logs: "Validator role: control-plane\n"}
	}, "--wait", "1m")
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 5, exitErr.Code)
	assert.Contains(t, exitErr.Msg, "checks still failing")
	final := finalEvent(t, stderr)
	assert.Equal(t, false, final["success"])
	assert.Equal(t, "timeout", final["verdict"])
}

// A --wait that ends with a rollout still in progress has not passed: exit 5
// with success false, so a gate on the final event agrees with the exit code.
// The row that kept it polling is marked transient, and nothing failed.
func TestCheck_WaitTimeoutOnARolloutIsNotASuccess(t *testing.T) {
	err, stderr := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		return selfhosted.ClusterValidatorResult{Passed: true, Logs: "Validator role: control-plane\n" +
			"nvcf/api: rollout in progress (updated: 2/3)\nCluster is NVCF-Ready (with warnings)\n"}
	}, "--wait", "1s")
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 5, exitErr.Code)
	assert.Contains(t, exitErr.Msg, "a rollout was still in progress")
	assert.Equal(t, true, validatorRow(t, stderr)["transient"])
	final := finalEvent(t, stderr)
	assert.Equal(t, false, final["success"])
	assert.Equal(t, "timeout", final["verdict"])
	assert.EqualValues(t, 0, final["failedCount"])
}

// The dashboard's quit key interrupts the run as a signal does: exit 130. A
// validator that kept objects in the cluster still says how to remove them,
// on stderr too, as the dashboard may already be gone. Wiring the key to the
// budget's cancel instead read as a finished run.
func TestCheck_QuitKeyInterruptsAndPrintsTheCleanupCommand(t *testing.T) {
	prev := selectCheckRendererFn
	t.Cleanup(func() { selectCheckRendererFn = prev })
	quit := make(chan func(), 1)
	selectCheckRendererFn = func(w io.Writer, wait bool, onQuit func()) (progress.EventSink, error) {
		quit <- onQuit
		return prev(w, wait, onQuit)
	}
	err, stderr := runCheckWithBudget(t, time.Minute, func(ctx context.Context) selfhosted.ClusterValidatorResult {
		(<-quit)()
		<-ctx.Done()
		return selfhosted.ClusterValidatorResult{Err: ctx.Err(), RunID: "run1", LeftBehind: true}
	})
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	assert.Equal(t, true, finalEvent(t, stderr)["cancelled"])
	assert.Contains(t, stderr, "note: cluster-validator left objects in the cluster; the validator pod was still running")
	assert.Contains(t, stderr, "nvcf.nvidia.com/validator-run=run1")
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

// An unpinned validator image whose tag cannot be discovered is not launched:
// the kubelet would pull :latest, which the repository does not publish.
// Nothing was validated, so the run fails with a row that says to pin a tag,
// and the image's registry is still checked for credentials.
func TestCheck_UnresolvedValidatorTagFailsTheRun(t *testing.T) {
	var mu sync.Mutex
	var registries []string
	prev := newRegistryCredentialCheckerForSelfHosted
	newRegistryCredentialCheckerForSelfHosted = func() selfhosted.RegistryCredentialChecker {
		return func(_ context.Context, registry, _ string, _ bool) error {
			mu.Lock()
			defer mu.Unlock()
			registries = append(registries, registry)
			return nil
		}
	}
	t.Cleanup(func() { newRegistryCredentialCheckerForSelfHosted = prev })
	calls := 0
	err, stderr := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		calls++
		return selfhosted.ClusterValidatorResult{Passed: true}
	}, "--cluster-validator-image", "harbor.example.com/nvcf/cluster-validator")
	assert.Zero(t, calls, "the untagged image must not be launched")
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 2, exitErr.Code)
	row := validatorRow(t, stderr)
	assert.Equal(t, "error", row["severity"])
	assert.Contains(t, row["message"], "could not resolve a tag for harbor.example.com/nvcf/cluster-validator")
	assert.Equal(t, false, finalEvent(t, stderr)["success"])
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, registries, "harbor.example.com")
}

// The tag is resolved again on every --wait poll, so a discovery failure that
// clears lets the validator run.
func TestCheck_WaitResolvesTheValidatorTagAgainEachPoll(t *testing.T) {
	prev := resolveLatestValidatorTagForSelfHosted
	t.Cleanup(func() { resolveLatestValidatorTagForSelfHosted = prev })
	var resolves atomic.Int32
	resolveLatestValidatorTagForSelfHosted = func(_ context.Context, image string) (string, bool) {
		// Before the loop, and on the first poll.
		if resolves.Add(1) <= 2 {
			return "", false
		}
		return image + ":1.2.3", true
	}
	var images []string
	err, _ := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		images = append(images, "ran")
		return selfhosted.ClusterValidatorResult{Passed: true, Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
	}, "--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator", "--wait", "30s")
	require.NoError(t, err)
	assert.Len(t, images, 1, "the second poll ran the validator")
	assert.EqualValues(t, 3, resolves.Load())
}

func TestImageRefIsPinned(t *testing.T) {
	for ref, want := range map[string]bool{
		"nvcr.io/nvidia/validator:3.2.26":         true,
		"nvcr.io/nvidia/validator@sha256:abcd":    true,
		"localhost:5000/validator":                false,
		"nvcr.io/nvidia/validator":                false,
		"registry.example.com:443/team/validator": false,
	} {
		assert.Equal(t, want, selfhosted.ImageRefIsPinned(ref), ref)
	}
}

// The budget covers each validator's longest run, including the wait after its
// own timeout for the Job's deadline to end the pod, so a validator that times
// out is graded as its own failure and not as a spent budget. Both roles on one
// cluster run in turn and need a share each.
func TestCheck_BudgetCoversEachValidatorsFullRun(t *testing.T) {
	ceiling := selfhosted.ClusterValidatorRunCeiling()
	passNow := func(context.Context) selfhosted.ClusterValidatorResult {
		return selfhosted.ClusterValidatorResult{Passed: true}
	}
	for _, tc := range []struct {
		args       []string
		validators time.Duration
	}{
		{args: nil, validators: 1},
		{args: []string{"--pre"}, validators: 2},
	} {
		_, _ = runCheckWithBudget(t, time.Minute, passNow, tc.args...)
		assert.GreaterOrEqual(t, requestedBudget, tc.validators*ceiling+checkProbeShare, "args %v", tc.args)
	}
}
