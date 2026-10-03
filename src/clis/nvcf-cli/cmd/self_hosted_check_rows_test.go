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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/kubectx"
	"nvcf-cli/internal/selfhosted/progress"
)

const pinnedValidatorImage = "nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0"

// checkStubs replaces what a recorded check run would reach outside the test.
type checkStubs struct {
	stale     func(ctx context.Context) ([]selfhosted.StaleNamespace, error)
	validator func(ctx context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult
	tools     func() []selfhosted.BinarySpec
	budget    time.Duration // 0 keeps the requested budget
	noSISURL  bool          // leave the ICMS URL unconfigured
}

type staleCall struct {
	context    string
	namespaces []string
}

type validatorCall struct {
	context, role, postInstall string
}

// checkRun is what one recorded `self-hosted check --json` run did.
type checkRun struct {
	err        error
	stderr     string
	budget     time.Duration
	stale      []staleCall
	inotify    []string
	validators []validatorCall
	sisHits    int64
}

func (r checkRun) code() int {
	if r.err == nil {
		return 0
	}
	var exitErr *ExitCodeError
	if errors.As(r.err, &exitErr) {
		return exitErr.Code
	}
	return 1
}

// row returns the check_completed event with category and id, or nil.
func (r checkRun) row(t *testing.T, category, id string) map[string]any {
	t.Helper()
	for _, e := range checkEvents(t, r.stderr, "check_completed") {
		if e["category"] == category && e["id"] == id {
			return e
		}
	}
	return nil
}

// runCheck runs `self-hosted check --json` with args, recording every probe
// it makes. Each prober passes unless stubs says otherwise.
func runCheck(t *testing.T, stubs checkStubs, args ...string) checkRun {
	t.Helper()
	resetCheckFlags(t)
	var run checkRun
	var mu sync.Mutex
	var hits atomic.Int64
	sis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sis.Close)
	if stubs.noSISURL {
		t.Setenv("NVCF_ICMS_URL", "")
		t.Setenv("NVCF_SIS_URL", "")
	} else {
		t.Setenv("NVCF_ICMS_URL", sis.URL)
	}

	prevStale, prevInotify, prevCV := newStaleNamespaceProberForSelfHosted, newInotifyProberForSelfHosted,
		newClusterValidatorForSelfHosted
	prevTools, prevBudget := checkPreflightTools, checkBudget
	t.Cleanup(func() {
		newStaleNamespaceProberForSelfHosted, newInotifyProberForSelfHosted = prevStale, prevInotify
		newClusterValidatorForSelfHosted, checkPreflightTools, checkBudget = prevCV, prevTools, prevBudget
	})
	checkPreflightTools = passingPreflightTools
	if stubs.tools != nil {
		checkPreflightTools = stubs.tools
	}
	checkBudget = func(d time.Duration) time.Duration {
		run.budget = d
		if stubs.budget > 0 {
			return stubs.budget
		}
		return d
	}
	newStaleNamespaceProberForSelfHosted = func() selfhosted.StaleNamespaceProber {
		return func(ctx context.Context, kubeContext string, ns []string) ([]selfhosted.StaleNamespace, error) {
			mu.Lock()
			run.stale = append(run.stale, staleCall{kubeContext, ns})
			mu.Unlock()
			if stubs.stale != nil {
				return stubs.stale(ctx)
			}
			return nil, nil
		}
	}
	newInotifyProberForSelfHosted = func(string) selfhosted.NodeInotifyProber {
		return func(_ context.Context, kubeContext string) ([]selfhosted.NodeInotifyLimits, error) {
			mu.Lock()
			run.inotify = append(run.inotify, kubeContext)
			mu.Unlock()
			return []selfhosted.NodeInotifyLimits{{NodeName: "n1", MaxUserInstances: 8192, MaxUserWatches: 524288}}, nil
		}
	}
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(ctx context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			mu.Lock()
			run.validators = append(run.validators, validatorCall{p.KubeContext, p.Role, p.Env["VALIDATOR_POST_INSTALL"]})
			mu.Unlock()
			if stubs.validator != nil {
				return stubs.validator(ctx, p)
			}
			return selfhosted.ClusterValidatorResult{Passed: true,
				Logs: "Validator role: " + p.Role + "\nCluster is NVCF-Ready\n"}
		}
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	rootCmd.SetArgs(append([]string{"self-hosted", "check", "--json"}, args...))
	run.err = rootCmd.Execute()
	run.stderr = stderr.String()
	run.sisHits = hits.Load()
	return run
}

// Every check the flags select ends as a row, so a consumer of the stream can
// tell what was not checked. An explicit opt-out passes at info; a validator
// left unconfigured warns, so the verdict says the cluster was not validated.
func TestCheck_SkippedChecksAreRows(t *testing.T) {
	type wantRow struct {
		category, id string
		passed       bool
		severity     string
		message      string
	}
	for name, tc := range map[string]struct {
		env     string
		args    []string
		rows    []wantRow
		verdict string
	}{
		"no validator image": {
			args: []string{"--control-plane"},
			rows: []wantRow{{"control-plane-cluster", "cluster-validator", false, "warning",
				"cluster_validator_image is not set"}},
			verdict: "warnings",
		},
		"--skip-cluster-validation": {
			args: []string{"--control-plane", "--skip-cluster-validation", "--cluster-validator-image", pinnedValidatorImage},
			rows: []wantRow{{"control-plane-cluster", "cluster-validator", true, "info",
				"skipped (--skip-cluster-validation)"}},
			verdict: "ok",
		},
		"--skip-inotify-check": {
			args: []string{"--compute-plane", "--skip-cluster-validation", "--skip-inotify-check"},
			rows: []wantRow{{"compute-plane-cluster", "node-inotify-limits", true, "info",
				"skipped (--skip-inotify-check)"}},
			verdict: "ok",
		},
		"NVCF_CLI_SELFHOSTED_SKIP_INOTIFY": {
			env:  "NVCF_CLI_SELFHOSTED_SKIP_INOTIFY",
			args: []string{"--compute-plane", "--skip-cluster-validation"},
			rows: []wantRow{{"compute-plane-cluster", "node-inotify-limits", true, "info",
				"skipped (--skip-inotify-check)"}},
			verdict: "ok",
		},
		"--local-only": {
			args: []string{"--all", "--local-only", "--cluster-validator-image", pinnedValidatorImage},
			rows: []wantRow{
				{"registry-credentials", "registry-credentials", true, "info", "skipped (--local-only)"},
				{"control-plane-cluster", "control-plane-cluster", true, "info", "skipped (--local-only)"},
				{"compute-plane-cluster", "compute-plane-cluster", true, "info", "skipped (--local-only)"},
			},
			verdict: "ok",
		},
	} {
		if tc.env != "" {
			t.Setenv(tc.env, "1")
		}
		run := runCheck(t, checkStubs{}, tc.args...)
		if tc.env != "" {
			t.Setenv(tc.env, "")
		}
		assert.Equal(t, 0, run.code(), name)
		for _, want := range tc.rows {
			row := run.row(t, want.category, want.id)
			require.NotNil(t, row, "%s: no %s/%s row in\n%s", name, want.category, want.id, run.stderr)
			assert.Equal(t, want.passed, row["passed"], name)
			assert.Equal(t, want.severity, row["severity"], name)
			assert.Contains(t, row["message"], want.message, name)
		}
		final := finalEvent(t, run.stderr)
		assert.Equal(t, tc.verdict, final["verdict"], name)
		assert.Equal(t, true, final["success"], name)
		if name == "--local-only" {
			assert.Empty(t, run.stale, "--local-only contacts no cluster")
			assert.Empty(t, run.validators)
			assert.Empty(t, run.inotify)
			assert.Zero(t, run.sisHits)
		}
		if strings.Contains(name, "INOTIFY") || strings.Contains(name, "inotify") {
			assert.Empty(t, run.inotify, name)
		}
	}
}

// The final event counts the warnings, as category_completed and the
// dashboard do.
func TestCheck_FinalEventCountsWarnings(t *testing.T) {
	run := runCheck(t, checkStubs{}, "--control-plane")
	final := finalEvent(t, run.stderr)
	assert.EqualValues(t, 1, final["warningCount"])
	assert.EqualValues(t, 0, final["failedCount"])
}

// Every passing row has severity info on the wire.
func TestCheck_EveryPassingRowIsInfo(t *testing.T) {
	run := runCheck(t, checkStubs{}, "--all", "--cluster-validator-image", pinnedValidatorImage)
	require.Equal(t, 0, run.code(), run.stderr)
	rows := checkEvents(t, run.stderr, "check_completed")
	require.NotEmpty(t, rows)
	for _, row := range rows {
		if row["passed"] == true {
			assert.Equal(t, "info", row["severity"], "%s/%s", row["category"], row["id"])
		}
	}
}

// helmfile, helm and the Helm runtime are what the install runs. Checking an
// installed stack runs none of them, so a workstation without them warns;
// before an install it fails.
func TestCheck_LocalToolsWarnAfterInstall(t *testing.T) {
	withoutHelmfile := func() []selfhosted.BinarySpec {
		specs := passingPreflightTools()
		for i := range specs {
			if specs[i].Name == "helmfile" {
				specs[i].LookPath = func(string) (string, error) { return "", errors.New("not found") }
			}
		}
		return specs
	}
	for args, want := range map[string]struct {
		code     int
		severity string
	}{
		"--control-plane": {0, "warning"},
		"--all":           {0, "warning"},
		"--pre":           {2, "error"},
	} {
		run := runCheck(t, checkStubs{tools: withoutHelmfile}, args, "--skip-cluster-validation")
		assert.Equal(t, want.code, run.code(), args)
		row := run.row(t, "local-host-tools", "local-host-tools-helmfile")
		require.NotNil(t, row, args)
		assert.Equal(t, want.severity, row["severity"], args)
	}
}

// SIS is probed only at a URL the operator configured. Without one, the row
// says so rather than probing the client's default, NVIDIA's hosted service.
func TestCheck_SISOnlyAtAConfiguredURL(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	run := runCheck(t, checkStubs{noSISURL: true}, "--compute-plane", "--skip-cluster-validation")
	assert.Equal(t, 0, run.code())
	row := run.row(t, "compute-plane-cluster", "sis-reachability")
	require.NotNil(t, row, run.stderr)
	assert.Equal(t, false, row["passed"])
	assert.Equal(t, "warning", row["severity"])
	assert.Contains(t, row["message"], "no ICMS URL is configured")

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	viper.Set("base_http_url", srv.URL)
	run = runCheck(t, checkStubs{noSISURL: true}, "--compute-plane", "--skip-cluster-validation")
	assert.Equal(t, 0, run.code())
	assert.EqualValues(t, 1, hits.Load(), "a base_http_url the operator set is the install's")
	assert.Equal(t, true, run.row(t, "compute-plane-cluster", "sis-reachability")["passed"])
}

// A blocking finding exits 2 even when the budget cut another row short: no
// retry with more time passes it.
func TestCheck_BlockingFailureBeatsATimeout(t *testing.T) {
	stuck := func(context.Context) ([]selfhosted.StaleNamespace, error) {
		return []selfhosted.StaleNamespace{{Name: "nvcf", Reason: "stuck Terminating"}}, nil
	}
	hold := func(ctx context.Context, _ selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
		return holdUntilDone(ctx)
	}
	for _, extra := range [][]string{nil, {"--wait", "1m"}} {
		run := runCheck(t, checkStubs{stale: stuck, validator: hold, budget: 300 * time.Millisecond},
			append([]string{"--control-plane", "--cluster-validator-image", pinnedValidatorImage}, extra...)...)
		assert.Equal(t, 2, run.code(), "%v", extra)
		final := finalEvent(t, run.stderr)
		assert.Equal(t, "failed", final["verdict"], "%v", extra)
		assert.Equal(t, false, final["success"], "%v", extra)
		assert.Contains(t, run.row(t, "control-plane-cluster", "cluster-validator")["message"], "cut short: ")
	}
}

// A cluster that cannot be contacted at all fails the run, with or without a
// validator: nothing about it was checked.
func TestCheck_UnreachableClusterFailsTheRun(t *testing.T) {
	unreachable := func(context.Context) ([]selfhosted.StaleNamespace, error) {
		return nil, &selfhosted.ClusterUnreachableError{
			Context: "typo-ctx", Err: errors.New(`context "typo-ctx" does not exist`),
		}
	}
	for _, args := range [][]string{{"--control-plane"}, {"--pre"}} {
		run := runCheck(t, checkStubs{stale: unreachable}, args...)
		assert.Equal(t, 2, run.code(), "%v", args)
		assert.Equal(t, false, finalEvent(t, run.stderr)["success"], "%v", args)
		row := run.row(t, "control-plane-cluster", "stale-namespaces")
		require.NotNil(t, row, "%v", args)
		assert.Equal(t, "error", row["severity"])
		assert.Contains(t, row["message"], "cannot reach typo-ctx")
	}
}

// A malformed or non-positive --wait is a usage error, reported before any
// work: no tag discovery, no events.
func TestCheck_WaitIsParsedBeforeAnyWork(t *testing.T) {
	prev := resolveLatestValidatorTagForSelfHosted
	t.Cleanup(func() { resolveLatestValidatorTagForSelfHosted = prev })
	var discoveries atomic.Int32
	resolveLatestValidatorTagForSelfHosted = func(context.Context, string) (string, bool) {
		discoveries.Add(1)
		return "", false
	}
	for _, wait := range []string{"10x", "-5m", "0s"} {
		run := runCheck(t, checkStubs{}, "--control-plane", "--wait", wait,
			"--cluster-validator-image", "nvcr.io/nvidia/nvcf-byoc/cluster-validator")
		require.Error(t, run.err, wait)
		assert.Contains(t, run.err.Error(), "invalid --wait duration", wait)
		assert.Equal(t, 1, run.code(), wait)
		assert.NotContains(t, run.stderr, `"event"`, wait)
	}
	assert.Zero(t, discoveries.Load())
}

// The force-fail switch tests once used is gone: no environment variable can
// add a row to a production run.
func TestCheck_ForceFailEnvIsIgnored(t *testing.T) {
	t.Setenv("NVCF_CLI_SELFHOSTED_FORCE_FAIL", "1")
	run := runCheck(t, checkStubs{}, "--pre", "--local-only")
	assert.Equal(t, 0, run.code(), run.stderr)
	assert.NotContains(t, run.stderr, "force-fail")
}

// The outer budget, term by term: each role invocation gets the probe share
// plus the validator's run ceiling when one is configured; ModeSingle runs two
// roles in turn, ModeSplit in parallel; --wait adds its duration.
func TestCheck_BudgetTerms(t *testing.T) {
	share, ceiling := selfhosted.CheckProbeShare(), selfhosted.ClusterValidatorRunCeiling()
	image := []string{"--cluster-validator-image", pinnedValidatorImage}
	split := []string{"--control-plane-context", "cp-ctx", "--compute-plane-context", "gpu-ctx"}
	with := func(lists ...[]string) []string {
		var out []string
		for _, l := range lists {
			out = append(out, l...)
		}
		return out
	}
	for name, tc := range map[string]struct {
		args []string
		want time.Duration
	}{
		"no validator, one role":     {[]string{"--control-plane", "--skip-cluster-validation"}, share},
		"no validator, two in turn":  {[]string{"--pre", "--skip-cluster-validation"}, 2 * share},
		"validator, one role":        {with([]string{"--control-plane"}, image), share + ceiling},
		"validator, two in turn":     {with([]string{"--pre"}, image), 2 * (share + ceiling)},
		"validator, two in parallel": {with([]string{"--pre"}, image, split), share + ceiling},
		"unresolved image": {[]string{"--control-plane", "--cluster-validator-image",
			"nvcr.io/nvidia/nvcf-byoc/cluster-validator"}, share + ceiling},
		"--local-only": {with([]string{"--all", "--local-only"}, image), share},
		"--wait":       {with([]string{"--control-plane", "--wait", "15m"}, image), share + ceiling + 15*time.Minute},
	} {
		run := runCheck(t, checkStubs{}, tc.args...)
		assert.Equal(t, tc.want, run.budget, name)
	}
}

// An interrupt during --wait exits 130 with one cancelled final event, whether
// it lands during a poll or between polls.
func TestCheck_WaitInterrupt(t *testing.T) {
	ready := selfhosted.ClusterValidatorResult{Passed: true,
		Logs: "Validator role: control-plane\nCluster is NVCF-Ready\n"}
	for name, poll := range map[string]func(quit func()) selfhosted.ClusterValidatorResult{
		"during a poll": func(quit func()) selfhosted.ClusterValidatorResult {
			quit()
			return ready
		},
		"between polls": func(quit func()) selfhosted.ClusterValidatorResult {
			time.AfterFunc(100*time.Millisecond, quit)
			return selfhosted.ClusterValidatorResult{ExitCode: 1, Logs: "Validator role: control-plane\n"}
		},
	} {
		prev := selectCheckRendererFn
		quit := make(chan func(), 1)
		selectCheckRendererFn = func(w io.Writer, wait bool, onQuit func()) (progress.EventSink, error) {
			quit <- onQuit
			return prev(w, wait, onQuit)
		}
		var onQuit func()
		start := time.Now()
		err, stderr := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
			if onQuit == nil {
				onQuit = <-quit
			}
			return poll(onQuit)
		}, "--wait", "10m")
		selectCheckRendererFn = prev
		var exitErr *ExitCodeError
		require.ErrorAs(t, err, &exitErr, name)
		assert.Equal(t, 130, exitErr.Code, name)
		assert.Equal(t, true, finalEvent(t, stderr)["cancelled"], name)
		assert.Less(t, time.Since(start), 5*time.Second, "%s: did not wait for the next poll", name)
	}
}

// A poll that outlasts both the --wait duration and the poll interval ends
// the wait: the deadline is checked before the ticker, which select would
// otherwise pick half the time and run the checks again on a spent wait.
func TestCheck_WaitDeadlineBeatsTheTicker(t *testing.T) {
	prev := checkPollInterval
	checkPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { checkPollInterval = prev })
	for i := 0; i < 20; i++ {
		calls := 0
		err, _ := runCheckWithBudget(t, time.Minute, func(context.Context) selfhosted.ClusterValidatorResult {
			calls++
			time.Sleep(50 * time.Millisecond)
			return selfhosted.ClusterValidatorResult{ExitCode: 1, Logs: "Validator role: control-plane\n"}
		}, "--wait", "1ms")
		var exitErr *ExitCodeError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 5, exitErr.Code)
		require.Equal(t, 1, calls, "run %d polled again after the wait ended", i)
	}
}

// Every flag set in both topologies: which roles run and where, what each
// validator is told, which clusters are probed, and the budget.
func TestCheck_FlagAndTopologyMatrix(t *testing.T) {
	share, ceiling := selfhosted.CheckProbeShare(), selfhosted.ClusterValidatorRunCeiling()
	for _, tc := range []struct {
		args            []string
		cp, gpu         bool   // the clusters visited
		postCP, postGPU string // VALIDATOR_POST_INSTALL per role
		sis             bool
	}{
		{args: []string{"--pre"}, cp: true, gpu: true},
		{args: []string{"--control-plane"}, cp: true, postCP: "true"},
		{args: []string{"--compute-plane"}, gpu: true, postGPU: "true", sis: true},
		{args: []string{"--all"}, cp: true, gpu: true, postCP: "true", postGPU: "true", sis: true},
		{args: []string{"--pre", "--control-plane"}, cp: true, gpu: true, postCP: "true"},
		{args: []string{"--pre", "--compute-plane"}, cp: true, gpu: true, postGPU: "true", sis: true},
		{
			args: []string{"--control-plane", "--compute-plane"}, cp: true, gpu: true,
			postCP: "true", postGPU: "true", sis: true,
		},
		{args: []string{"--pre", "--all"}, cp: true, gpu: true, postCP: "true", postGPU: "true", sis: true},
	} {
		for _, mode := range []kubectx.Mode{kubectx.ModeSingle, kubectx.ModeSplit} {
			cpCtx, gpuCtx := "", ""
			args := append([]string{"--cluster-validator-image", pinnedValidatorImage}, tc.args...)
			if mode == kubectx.ModeSplit {
				cpCtx, gpuCtx = "cp-ctx", "gpu-ctx"
				args = append(args, "--control-plane-context", cpCtx, "--compute-plane-context", gpuCtx)
			}
			name := strings.Join(args[2:], " ")
			run := runCheck(t, checkStubs{}, args...)
			require.Equal(t, 0, run.code(), "%s\n%s", name, run.stderr)

			var wantValidators []validatorCall
			if tc.cp {
				wantValidators = append(wantValidators, validatorCall{cpCtx, "control-plane", tc.postCP})
			}
			if tc.gpu {
				wantValidators = append(wantValidators, validatorCall{gpuCtx, "compute-plane", tc.postGPU})
			}
			assert.ElementsMatch(t, wantValidators, run.validators, name)

			if tc.gpu {
				assert.Equal(t, []string{gpuCtx}, run.inotify, name)
			} else {
				assert.Empty(t, run.inotify, name)
			}

			wantSIS := int64(0)
			if tc.sis {
				wantSIS = 1
			}
			assert.Equal(t, wantSIS, run.sisHits, name)

			if mode == kubectx.ModeSingle {
				require.Len(t, run.stale, 1, "%s: one cluster, one probe", name)
				assert.Equal(t, tc.cp, contains(run.stale[0].namespaces, "vault-system"), name)
				assert.Equal(t, tc.gpu, contains(run.stale[0].namespaces, "kai-scheduler"), name)
			} else {
				var contexts []string
				for _, c := range run.stale {
					contexts = append(contexts, c.context)
					assert.Equal(t, c.context == gpuCtx, contains(c.namespaces, "kai-scheduler"), name)
				}
				var want []string
				if tc.cp {
					want = append(want, cpCtx)
				}
				if tc.gpu {
					want = append(want, gpuCtx)
				}
				assert.ElementsMatch(t, want, contexts, name)
			}

			hostRows := map[string]int{}
			for _, row := range checkEvents(t, run.stderr, "check_completed") {
				if row["category"] == "local-host-tools" {
					hostRows[row["id"].(string)]++
				}
			}
			assert.Len(t, hostRows, len(passingPreflightTools())+1, "%s: each tool and the Helm runtime", name)
			for id, n := range hostRows {
				assert.Equal(t, 1, n, "%s: %s", name, id)
			}

			wantBudget := share + ceiling
			if mode == kubectx.ModeSingle && tc.cp && tc.gpu {
				wantBudget = 2 * (share + ceiling)
			}
			assert.Equal(t, wantBudget, run.budget, name)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
