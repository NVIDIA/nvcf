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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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
// for a single-role run reported a stuck nvca-operator on --control-plane.
func TestCheck_StaleNamespacesFollowTheRequestedRoles(t *testing.T) {
	ns, _ := runCheckRecording(t, "--control-plane")
	assert.Contains(t, ns, "vault-system")
	assert.NotContains(t, ns, "nvca-operator", "--control-plane must not scan compute namespaces")

	ns, _ = runCheckRecording(t, "--compute-plane")
	assert.Contains(t, ns, "nvca-operator")
	assert.NotContains(t, ns, "vault-system", "--compute-plane must not scan control-plane namespaces")

	ns, _ = runCheckRecording(t, "--all")
	assert.Contains(t, ns, "vault-system")
	assert.Contains(t, ns, "nvca-operator", "--all scans both")
}

// The stale-namespace scan reads each plane's gates from the stack the
// install used, an extracted built-in oci:// stack included, and from the
// environment the install used: prod turns kai-scheduler on here.
func TestCheck_StaleNamespaceGatesFollowTheInstallsStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("HELMFILE_ENV", "")
	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	const ref = "oci://registry.example.com/nvcf/compute-plane:1.0.0@sha256:0123456789abcdef"
	extracted := filepath.Join(cache, "nvcf-cli", "stacks", "oci-0123456789ab")
	envDir := filepath.Join(extracted, "environments")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	for name, enabled := range map[string]string{"base": "false", "prod": "true"} {
		require.NoError(t, os.WriteFile(filepath.Join(envDir, name+".yaml"),
			[]byte("addons:\n  kaiScheduler:\n    enabled: "+enabled+"\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(extracted, ".extraction-complete"), nil, 0o644))
	t.Setenv("NVCF_CLI_DEFAULT_COMPUTE_PLANE_STACK", ref)

	ns, _ := runCheckRecording(t, "--compute-plane", "--env", "prod")
	assert.Contains(t, ns, "kai-scheduler")
	ns, _ = runCheckRecording(t, "--all", "--env", "prod")
	assert.Contains(t, ns, "kai-scheduler", "merged into the one scan of a shared cluster")
	ns, _ = runCheckRecording(t, "--compute-plane")
	assert.NotContains(t, ns, "kai-scheduler", "with no environment named, base.yaml decides")
}

// The env toggles are booleans, so a config that spells one out as "false"
// or "0" leaves its check on. Any other value fails the command naming it.
func TestCheck_EnvTogglesAreBooleans(t *testing.T) {
	t.Setenv(envLocalOnly, "false")
	ns, _ := runCheckRecording(t, "--control-plane")
	assert.NotEmpty(t, ns, "LOCAL_ONLY=false still checks the cluster")
	t.Setenv(envLocalOnly, "true")
	ns, _ = runCheckRecording(t, "--control-plane")
	assert.Empty(t, ns)
	t.Setenv(envLocalOnly, "")

	resetCheckFlags(t)
	for value, on := range map[string]bool{"0": false, "false": false, "": false, "1": true, "TRUE": true} {
		t.Setenv(envSkipClusterValidation, value)
		t.Setenv(envSkipInotify, value)
		assert.Equal(t, on, clusterValidationSkipped(), value)
		assert.Equal(t, on, inotifyCheckSkipped(), value)
	}

	t.Setenv(envSkipInotify, "maybe")
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json"})
	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `NVCF_CLI_SELFHOSTED_SKIP_INOTIFY="maybe": expected true or false`)
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
func runCheckWithBudget(t *testing.T, budget time.Duration,
	validator func(context.Context) selfhosted.ClusterValidatorResult, args ...string) (error, string) {
	t.Helper()
	return runCheckWith(t, budget, func(
		ctx context.Context, _ selfhosted.ClusterValidatorParams,
	) selfhosted.ClusterValidatorResult {
		return validator(ctx)
	}, &syncBuffer{}, args...)
}

// runCheckWith is runCheckWithBudget with a validator stub that sees its
// parameters, writing stderr to out.
func runCheckWith(t *testing.T, budget time.Duration, validator selfhosted.ClusterValidator, out *syncBuffer,
	args ...string) (error, string) {
	t.Helper()
	return executeCheck(t, budget, validator, out, args...), out.String()
}

// executeCheck runs `self-hosted check --control-plane --json` with the given
// budget and validator stub, writing stderr to errOut.
func executeCheck(t *testing.T, budget time.Duration, validator selfhosted.ClusterValidator, errOut io.Writer,
	args ...string) error {
	t.Helper()
	resetCheckFlags(t)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	prevBudget, prevCV := checkBudget, newClusterValidatorForSelfHosted
	checkBudget = func(d time.Duration) time.Duration { requestedBudget = d; return budget }
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator { return validator }
	t.Cleanup(func() { checkBudget, newClusterValidatorForSelfHosted = prevBudget, prevCV })
	rootCmd.SetErr(errOut)
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	rootCmd.SetArgs(append([]string{"self-hosted", "check", "--control-plane", "--json",
		"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"}, args...))
	return rootCmd.Execute()
}

// syncBuffer is a bytes.Buffer safe to write from the interrupt handler while
// the run writes its events.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
// final event that says it failed, not success on whatever had run. On a
// loaded machine the budget can run out before the validator starts, so its
// row is either cut short or not run; both are blocking.
func TestCheck_BudgetSpentIsATimeout(t *testing.T) {
	for _, args := range [][]string{nil, {"--wait", "1m"}} {
		err, stderr := runCheckWithBudget(t, 300*time.Millisecond, holdUntilDone, args...)
		var exitErr *ExitCodeError
		require.ErrorAs(t, err, &exitErr, "args %v", args)
		assert.Equal(t, 5, exitErr.Code, "args %v", args)
		final := finalEvent(t, stderr)
		assert.Equal(t, false, final["success"], "args %v", args)
		assert.Equal(t, "timeout", final["verdict"], "args %v", args)
		row := validatorRow(t, stderr)
		assert.Equal(t, false, row["passed"], "args %v", args)
		assert.EqualValues(t, selfhosted.SeverityError, row["severity"], "args %v", args)
		assert.Regexp(t, `^(cut short|not run): the check's time budget ran out`, row["message"], "args %v", args)
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

// A validator Job that succeeded is the validator's verdict even when its
// transcript cannot be read, so --wait accepts the first poll instead of
// starting a new Job on every poll until it times out.
func TestCheck_WaitAcceptsASucceededValidatorWhoseTranscriptCannotBeRead(t *testing.T) {
	calls := 0
	err, stderr := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		calls++
		return selfhosted.ClusterValidatorResult{Passed: true, JobName: "cv-1",
			LogsErr: errors.New(`pods "cv-1-abc" is forbidden: cannot get resource "pods/log"`)}
	}, "--wait", "30s")
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	row := validatorRow(t, stderr)
	assert.Equal(t, true, row["passed"])
	assert.Contains(t, row["message"], "its transcript could not be read")
	assert.Equal(t, "ok", finalEvent(t, stderr)["verdict"])
}

// The dashboard's quit key interrupts the run as a signal does: exit 130. A
// validator that kept objects in the cluster still says how to remove them:
// at once, before its teardown, and again on stderr once the dashboard is
// closed. Wiring the key to the budget's cancel instead read as a finished
// run.
func TestCheck_QuitKeyInterruptsAndPrintsTheCleanupCommand(t *testing.T) {
	prev := selectCheckRendererFn
	t.Cleanup(func() { selectCheckRendererFn = prev })
	quit := make(chan func(), 1)
	selectCheckRendererFn = func(w io.Writer, wait bool, onQuit func()) (progress.EventSink, error) {
		quit <- onQuit
		return prev(w, wait, onQuit)
	}
	err, stderr := runCheckWith(t, time.Minute, func(
		ctx context.Context, p selfhosted.ClusterValidatorParams,
	) selfhosted.ClusterValidatorResult {
		p.OnStart("run1")
		(<-quit)()
		<-ctx.Done()
		return selfhosted.ClusterValidatorResult{Err: ctx.Err(), RunID: "run1", LeftBehind: true}
	}, &syncBuffer{})
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	assert.Equal(t, true, finalEvent(t, stderr)["cancelled"])
	cmd := "kubectl get pods -n default -l nvcf.nvidia.com/validator-run=run1; " +
		"kubectl delete -n default job -l nvcf.nvidia.com/validator-run=run1 --cascade=foreground --wait; " +
		"kubectl delete clusterrolebinding,clusterrole -l nvcf.nvidia.com/validator-run=run1; " +
		"kubectl delete -n default rolebinding,role,serviceaccount,secret,configmap " +
		"-l nvcf.nvidia.com/validator-run=run1"
	assert.Equal(t, []any{cmd}, finalEvent(t, stderr)["cleanup"])
	interruptNote := strings.Index(stderr, "note: interrupted; ")
	require.GreaterOrEqual(t, interruptNote, 0, "the interrupt says at once how to remove the run's objects")
	assert.Less(t, interruptNote, strings.Index(stderr, `"event":"final"`), "the note comes before the final event")
	assert.Equal(t, cmd, validatorRow(t, stderr)["cleanup"])
	kept := strings.Index(stderr,
		"note: the cluster-validator left objects in the cluster; remove them with:\n  "+cmd+"\n")
	require.Positive(t, kept, "the removal command is repeated once the stream has ended")
	assert.Greater(t, kept, strings.Index(stderr, `"event":"final"`))
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

// Each --wait poll reads the local credentials again, so a docker login the
// operator renews while the run waits is the one the next poll sends.
func TestCheck_WaitReadsCredentialsAgainEachPoll(t *testing.T) {
	prevInterval := checkPollInterval
	checkPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { checkPollInterval = prevInterval })

	var mu sync.Mutex
	var sent []string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, pass, _ := r.BasicAuth()
		mu.Lock()
		sent = append(sent, pass)
		mu.Unlock()
		if pass != "renewed" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"token":"t"}`))
	}))
	t.Cleanup(srv.Close)
	prevTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = prevTransport })
	host := strings.TrimPrefix(srv.URL, "https://")

	dockerDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerDir)
	login := func(pass string) {
		cfg, err := json.Marshal(map[string]any{"auths": map[string]any{
			host: map[string]string{"username": "u", "password": pass},
		}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dockerDir, "config.json"), cfg, 0o600))
	}
	login("stale")

	prevChecker := newRegistryCredentialCheckerForSelfHosted
	t.Cleanup(func() { newRegistryCredentialCheckerForSelfHosted = prevChecker })
	probe := selfhosted.NewRegistryCredentialChecker()
	newRegistryCredentialCheckerForSelfHosted = func() selfhosted.RegistryCredentialChecker {
		return func(ctx context.Context, registry, repo string, critical bool) error {
			if registry != host {
				return nil
			}
			return probe(ctx, registry, repo, critical)
		}
	}

	polls := 0
	err, stderr := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		polls++
		if polls == 1 {
			login("renewed")
			return selfhosted.ClusterValidatorResult{ExitCode: 1, Logs: "Validator role: control-plane\n"}
		}
		return selfhosted.ClusterValidatorResult{Passed: true, Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
	}, "--cluster-validator-registries", host, "--wait", "30s")
	require.NoError(t, err, stderr)
	assert.Equal(t, 2, polls)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"stale", "renewed"}, sent, "the second poll sends the renewed login")
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

// removalCommand is the command that removes validator run runID's objects,
// with no context pinned.
func removalCommand(runID string) string {
	sel := "nvcf.nvidia.com/validator-run=" + runID
	return "kubectl get pods -n default -l " + sel + "; " +
		"kubectl delete -n default job -l " + sel + " --cascade=foreground --wait; " +
		"kubectl delete clusterrolebinding,clusterrole -l " + sel + "; " +
		"kubectl delete -n default rolebinding,role,serviceaccount,secret,configmap -l " + sel
}

// keptNote is what the command prints last when runs kept objects.
func keptNote(commands ...string) string {
	return "note: the cluster-validator left objects in the cluster; remove them with:\n  " +
		strings.Join(commands, "\n  ") + "\n"
}

// On an interrupt, the removal command of every run in progress is printed at
// once, before the teardown that may not finish: a second Ctrl-C or CI's
// follow-up SIGTERM can end the process at any time.
func TestCheck_InterruptPrintsTheRemovalCommandBeforeTheTeardown(t *testing.T) {
	prev := selectCheckRendererFn
	t.Cleanup(func() { selectCheckRendererFn = prev })
	quit := make(chan func(), 1)
	selectCheckRendererFn = func(w io.Writer, wait bool, onQuit func()) (progress.EventSink, error) {
		quit <- onQuit
		return prev(w, wait, onQuit)
	}
	out := &syncBuffer{}
	note := "note: interrupted; stopping the cluster-validator and removing its objects. A second Ctrl-C " +
		"exits at once and leaves them; remove them with:\n  " + removalCommand("run1") + "\n"
	printedFirst := false
	err, stderr := runCheckWith(t, time.Minute, func(
		ctx context.Context, p selfhosted.ClusterValidatorParams,
	) selfhosted.ClusterValidatorResult {
		p.OnStart("run1")
		(<-quit)()
		<-ctx.Done()
		// The teardown takes a while; the note must not wait for it.
		require.Eventually(t, func() bool { return strings.Contains(out.String(), note) }, 5*time.Second, 10*time.Millisecond)
		printedFirst = true
		return selfhosted.ClusterValidatorResult{Err: ctx.Err(), RunID: "run1", Created: true}
	}, out)
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	assert.True(t, printedFirst)
	assert.Equal(t, 1, strings.Count(stderr, note), "printed once")
	assert.NotContains(t, stderr, keptNote(removalCommand("run1")), "the teardown removed everything")
}

// closedStderrChild runs TestCheck_InterruptSurvivesTheEndOfItsStderrReader as
// the child process. Its value is the file the child writes once its
// validator's teardown has finished.
const closedStderrChild = "NVCF_CLI_TEST_CLOSED_STDERR_CHILD"

// A Ctrl-C or a CI cancel signals the whole process group, so a pipe reading
// stderr ends with the run. Writing the interrupt note, or anything after it,
// to that pipe must not kill the process before the validator's teardown has
// removed what the run created, nor after it, before the process exits 130.
func TestCheck_InterruptSurvivesTheEndOfItsStderrReader(t *testing.T) {
	const started = "validator started"
	if marker := os.Getenv(closedStderrChild); marker != "" {
		err := executeCheck(t, time.Minute, func(
			ctx context.Context, p selfhosted.ClusterValidatorParams,
		) selfhosted.ClusterValidatorResult {
			p.OnStart("run1")
			fmt.Fprintln(os.Stderr, started)
			<-ctx.Done()
			// The teardown takes a while; the note is written meanwhile.
			time.Sleep(time.Second)
			require.NoError(t, os.WriteFile(marker, nil, 0o600))
			return selfhosted.ClusterValidatorResult{Err: ctx.Err(), RunID: "run1", Created: true}
		}, os.Stderr)
		// As main does, after cobra has printed the error to the closed pipe.
		os.Exit(ExitCodeFromError(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	marker := filepath.Join(t.TempDir(), "torn-down")
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), closedStderrChild+"="+marker)
	child.Stderr = w
	var stdout bytes.Buffer
	child.Stdout = &stdout
	require.NoError(t, child.Start())
	require.NoError(t, w.Close())

	seen := false
	for lines := bufio.NewScanner(r); !seen && lines.Scan(); {
		seen = lines.Text() == started
	}
	require.NoError(t, r.Close())
	if !seen {
		_ = child.Wait()
		t.Fatalf("the validator never started:\n%s", stdout.String())
	}
	require.NoError(t, child.Process.Signal(os.Interrupt))
	waitErr := child.Wait()
	_, err = os.Stat(marker)
	assert.NoError(t, err, "the process died before the teardown finished: %v\n%s", waitErr, stdout.String())
	assert.Equal(t, 130, child.ProcessState.ExitCode(), "%v\n%s", waitErr, stdout.String())
}

// brokenPipeChild runs TestCheck_AnOutputReaderThatExitsEarlyDoesNotKillTheRun
// as the child process. Its value is the stream the parent reads, stdout or
// stderr, and the directory the two processes leave their markers in.
const brokenPipeChild = "NVCF_CLI_TEST_BROKEN_PIPE_CHILD"

// A reader that stops early, such as grep -m1, head or a jq that fails on a
// line, closes its pipe while the run goes on, with no interrupt. Writing an
// event to that pipe while a validator holds its cluster-wide RBAC must not
// kill the process before the teardown has removed it, nor after it, before
// the command returns.
func TestCheck_AnOutputReaderThatExitsEarlyDoesNotKillTheRun(t *testing.T) {
	const started = "validator started"
	if spec := os.Getenv(brokenPipeChild); spec != "" {
		stream, dir, _ := strings.Cut(spec, ":")
		out := os.Stderr
		if stream == "stdout" {
			out = os.Stdout
		}
		err := executeCheck(t, time.Minute, func(
			ctx context.Context, p selfhosted.ClusterValidatorParams,
		) selfhosted.ClusterValidatorResult {
			p.OnStart("run1")
			fmt.Fprintln(out, started)
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
				if _, err := os.Stat(filepath.Join(dir, "reader-gone")); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Another role's event, written while this run's objects exist.
			fmt.Fprintln(out, `{"event":"check_started"}`)
			time.Sleep(100 * time.Millisecond)
			_ = os.WriteFile(filepath.Join(dir, "teardown"), nil, 0o600)
			return selfhosted.ClusterValidatorResult{Err: ctx.Err(), Passed: ctx.Err() == nil, RunID: "run1",
				Created: true, Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
		}, os.Stderr)
		_ = os.WriteFile(filepath.Join(dir, "return"), nil, 0o600)
		os.Exit(ExitCodeFromError(err))
	}

	for _, stream := range []string{"stderr", "stdout"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		dir := t.TempDir()
		r, w, err := os.Pipe()
		require.NoError(t, err)
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$")
		child.Env = append(os.Environ(), brokenPipeChild+"="+stream+":"+dir)
		var other bytes.Buffer
		child.Stdout, child.Stderr = &other, w
		if stream == "stdout" {
			child.Stdout, child.Stderr = w, &other
		}
		require.NoError(t, child.Start(), stream)
		require.NoError(t, w.Close())

		seen := false
		for lines := bufio.NewScanner(r); !seen && lines.Scan(); {
			seen = lines.Text() == started
		}
		require.NoError(t, r.Close())
		require.NoError(t, os.WriteFile(filepath.Join(dir, "reader-gone"), nil, 0o600))
		waitErr := child.Wait()
		cancel()
		require.True(t, seen, "%s: the validator never started:\n%s", stream, other.String())
		status, _ := child.ProcessState.Sys().(syscall.WaitStatus)
		assert.False(t, status.Signaled(), "%s: killed by %v\n%s", stream, status.Signal(), other.String())
		for marker, what := range map[string]string{"teardown": "its teardown", "return": "the command returned"} {
			_, err = os.Stat(filepath.Join(dir, marker))
			assert.NoError(t, err, "%s: the process died before %s: %v\n%s", stream, what, waitErr, other.String())
		}
	}
}

// terminalSink stands in for the --wait dashboard, which draws on the
// alternate screen: what is written to the terminal before it closes is lost.
type terminalSink struct {
	progress.EventSink
	out    *syncBuffer
	closed atomic.Bool
}

func (s *terminalSink) OwnsTerminal() bool { return true }

func (s *terminalSink) Close() error {
	if !s.closed.Swap(true) {
		_, _ = s.out.Write([]byte("<dashboard closed>\n"))
	}
	return s.EventSink.Close()
}

// On an interrupt, a dashboard that owns the terminal is closed before the
// removal command is printed. Printed while it was open, the command went
// with the alternate screen, and a second Ctrl-C left nothing on screen.
func TestCheck_InterruptNoteIsPrintedAfterTheDashboardCloses(t *testing.T) {
	prev := selectCheckRendererFn
	t.Cleanup(func() { selectCheckRendererFn = prev })
	quit := make(chan func(), 1)
	out := &syncBuffer{}
	selectCheckRendererFn = func(w io.Writer, wait bool, onQuit func()) (progress.EventSink, error) {
		quit <- onQuit
		sink, err := prev(w, wait, onQuit)
		return &terminalSink{EventSink: sink, out: out}, err
	}
	const note, closed = "note: interrupted; ", "<dashboard closed>"
	err, stderr := runCheckWith(t, time.Minute, func(
		ctx context.Context, p selfhosted.ClusterValidatorParams,
	) selfhosted.ClusterValidatorResult {
		p.OnStart("run1")
		(<-quit)()
		<-ctx.Done()
		require.Eventually(t, func() bool { return strings.Contains(out.String(), note) },
			5*time.Second, 10*time.Millisecond)
		return selfhosted.ClusterValidatorResult{Err: ctx.Err(), RunID: "run1", Created: true}
	}, out)
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	require.Contains(t, stderr, closed)
	assert.Less(t, strings.Index(stderr, closed), strings.Index(stderr, note),
		"the note is printed on the restored screen")
	assert.Equal(t, 1, strings.Count(stderr, closed))
}

// A closed terminal or a dropped SSH session sends SIGHUP. It goes through
// the same teardown as Ctrl-C, instead of killing the process with the run's
// cluster-wide RBAC and NGC-key Secret in place.
func TestCheck_SIGHUPGoesThroughTheTeardown(t *testing.T) {
	err, stderr := runCheckWithBudget(t, time.Minute, func(ctx context.Context) selfhosted.ClusterValidatorResult {
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
		<-ctx.Done()
		return selfhosted.ClusterValidatorResult{Err: ctx.Err()}
	})
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	assert.Equal(t, true, finalEvent(t, stderr)["cancelled"])
}

// Every exit that leaves objects behind says how to remove them: in the row,
// in the final event, and on stderr after the stream ends, where no dashboard
// can wipe it and a pipe into jq does not swallow it.
func TestCheck_KeptObjectsAreReportedOnEveryExit(t *testing.T) {
	for name, tc := range map[string]struct {
		budget time.Duration
		result func(ctx context.Context) selfhosted.ClusterValidatorResult
		args   []string
		code   int
	}{
		"no-cleanup pass": {budget: time.Minute, args: []string{"--no-cleanup"},
			result: func(context.Context) selfhosted.ClusterValidatorResult {
				return selfhosted.ClusterValidatorResult{Passed: true, RunID: "run1", Created: true,
					Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
			}},
		"failed, pod kept": {budget: time.Minute, code: 2,
			result: func(context.Context) selfhosted.ClusterValidatorResult {
				return selfhosted.ClusterValidatorResult{Err: errors.New("did not finish"), RunID: "run1", LeftBehind: true}
			}},
		"budget spent": {budget: 300 * time.Millisecond, code: 5,
			result: func(ctx context.Context) selfhosted.ClusterValidatorResult {
				<-ctx.Done()
				return selfhosted.ClusterValidatorResult{Err: ctx.Err(), RunID: "run1", LeftBehind: true}
			}},
	} {
		err, stderr := runCheckWithBudget(t, tc.budget, tc.result, tc.args...)
		if tc.code == 0 {
			require.NoError(t, err, name)
		} else {
			var exitErr *ExitCodeError
			require.ErrorAs(t, err, &exitErr, name)
			assert.Equal(t, tc.code, exitErr.Code, name)
		}
		cmd := removalCommand("run1")
		assert.Equal(t, cmd, validatorRow(t, stderr)["cleanup"], name)
		assert.Contains(t, validatorRow(t, stderr)["detail"], ": "+cmd, name)
		if name == "no-cleanup pass" {
			assert.Contains(t, validatorRow(t, stderr)["detail"], "kept with --no-cleanup; remove with: "+cmd)
		}
		assert.Equal(t, []any{cmd}, finalEvent(t, stderr)["cleanup"], name)
		assert.True(t, strings.HasSuffix(strings.TrimSuffix(stderr, "Error: "+errText(err)+"\n"), keptNote(cmd)),
			"%s: the command is printed last:\n%s", name, stderr)
	}
}

// errText is err's message, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Under --wait, a poll whose run kept objects is not forgotten when a later
// poll passes: its command is still printed and still in the final event.
func TestCheck_WaitKeepsEarlierPollsRemovalCommands(t *testing.T) {
	calls := 0
	err, stderr := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		calls++
		if calls == 1 {
			return selfhosted.ClusterValidatorResult{Err: errors.New("did not finish"), RunID: "run1", LeftBehind: true}
		}
		return selfhosted.ClusterValidatorResult{Passed: true, RunID: "run2", Created: true,
			Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
	}, "--wait", "30s")
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []any{removalCommand("run1")}, finalEvent(t, stderr)["cleanup"])
	assert.True(t, strings.HasSuffix(stderr, keptNote(removalCommand("run1"))), stderr)
}

// An interrupt after the budget ran out reports the interrupt only: no
// synthetic cut-short or not-run rows ahead of the cancelled final event.
func TestCheck_InterruptAfterTheBudgetEmitsNoSyntheticRows(t *testing.T) {
	prev := selectCheckRendererFn
	t.Cleanup(func() { selectCheckRendererFn = prev })
	var onQuit atomic.Value
	selectCheckRendererFn = func(w io.Writer, wait bool, q func()) (progress.EventSink, error) {
		onQuit.Store(q)
		return prev(w, wait, q)
	}
	err, stderr := runCheckWithBudget(t, 300*time.Millisecond, func(
		ctx context.Context,
	) selfhosted.ClusterValidatorResult {
		<-ctx.Done()
		onQuit.Load().(func())()
		return selfhosted.ClusterValidatorResult{Err: ctx.Err()}
	}, "--all")
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	for _, row := range checkEvents(t, stderr, "check_completed") {
		msg, _ := row["message"].(string)
		assert.NotContains(t, msg, "cut short", row["id"])
		assert.NotContains(t, msg, "not run:", row["id"])
	}
	assert.Equal(t, true, finalEvent(t, stderr)["cancelled"])
}

// --show-logs prints the kept transcript on an interrupt too.
func TestCheck_ShowLogsOnInterrupt(t *testing.T) {
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
		return selfhosted.ClusterValidatorResult{Err: ctx.Err(), RunID: "run1", LeftBehind: true,
			Logs: "Validator role: control-plane\nTier-1: checking\n"}
	}, "--show-logs")
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
	assert.Contains(t, stderr,
		"--- cluster-validator logs (control-plane-cluster) ---\nValidator role: control-plane\nTier-1: checking\n")
}

// A check's own timeout under a live budget is its result: exit 2, not 5.
func TestCheck_ACheckOwnTimeoutIsAFailure(t *testing.T) {
	err, stderr := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
		return selfhosted.ClusterValidatorResult{Err: fmt.Errorf("reading the Job: %w", context.DeadlineExceeded)}
	})
	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 2, exitErr.Code)
	assert.Equal(t, "failed", finalEvent(t, stderr)["verdict"])
}

// Both TTY renderers get the quit key: in raw mode it is the only Ctrl-C the
// run sees, and without it the dashboard closes while the Jobs keep running.
func TestSelectCheckRenderer_TTYCarriesOnQuit(t *testing.T) {
	resetCheckFlags(t)
	prevTTY, prevSelect, prevOneShot := checkWriterIsTTY, selectProgressRenderer, newCheckOneShotRenderer
	t.Cleanup(func() {
		checkWriterIsTTY, selectProgressRenderer, newCheckOneShotRenderer = prevTTY, prevSelect, prevOneShot
	})
	checkWriterIsTTY = func(io.Writer) bool { return true }
	var got func()
	selectProgressRenderer = func(
		w io.Writer, opts progress.RenderOpts,
	) (progress.EventSink, progress.RendererKind, error) {
		got = opts.OnQuit
		return progress.NewPlainRenderer(w), progress.RendererTTYFull, nil
	}
	newCheckOneShotRenderer = func(w io.Writer, opts progress.ModelOpts) progress.EventSink {
		got = opts.OnQuit
		return progress.NewPlainRenderer(w)
	}
	for _, wait := range []bool{false, true} {
		got = nil
		quits := 0
		_, err := selectCheckRenderer(&bytes.Buffer{}, wait, func() { quits++ })
		require.NoError(t, err)
		require.NotNil(t, got, "wait=%v", wait)
		got()
		assert.Equal(t, 1, quits, "wait=%v", wait)
	}
}
