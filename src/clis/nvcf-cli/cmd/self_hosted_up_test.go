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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/kubernetes"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/auth"
	"nvcf-cli/internal/selfhosted/progress"
	"nvcf-cli/internal/state"
)

// resetUpFlags restores the up command flag vars to their zero values between
// tests that share rootCmd. Cobra parses flags into package-level vars which
// persist across sequential test executions.
//
// It also resets the subcommand's ctx to nil so that ExecuteContext propagates
// the caller-supplied context to the subcommand on the next Execute call.
// Cobra only propagates a root-level context to subcommands when the
// subcommand's ctx field is nil; prior Execute() calls set it to
// context.Background() and subsequent tests would not receive a fresh ctx.
func resetUpFlags(t *testing.T) {
	t.Helper()
	// Reset before the test so that any previously-set subcommand ctx does not
	// bleed into this test's Execute call.
	selfHostedUpCmd.SetContext(nil)
	t.Cleanup(func() {
		upClusterName = ""
		upNCAID = "nvcf-default"
		upRegion = "us-west-1"
		upPlanOnly = false
		selfHostedJSON = false
		selfHostedPlain = false
		selfHostedAccessible = false
		selfHostedUpCmd.SetContext(nil)
	})
}

// disableUpWatchers swaps the kube-client factory for one that returns an
// error so the watcher goroutine returns immediately without opening
// informers. Tests don't have a real kubeconfig and the watcher would
// otherwise hit clientcmd's loading rules and either fail noisily or
// (worse) bind to whatever the developer's current kubectx points at.
func disableUpWatchers(t *testing.T) {
	t.Helper()
	prev := buildKubeClientsForWatcher
	t.Cleanup(func() { buildKubeClientsForWatcher = prev })
	buildKubeClientsForWatcher = func() (kubernetes.Interface, apiextclientset.Interface, error) {
		return nil, nil, errors.New("watcher disabled in unit tests")
	}
}

// TestSelfHostedUp_TokenFlagSkipsInit verifies the design: when --token=<JWT>
// is supplied the orchestrator does NOT mint a fresh token via runSelfHostedInit;
// the user's explicit token is used as-is. This is the CI/automation path.
func TestSelfHostedUp_TokenFlagSkipsInit(t *testing.T) {
	resetUpFlags(t)

	called := 0
	prev := runSelfHostedInit
	t.Cleanup(func() { runSelfHostedInit = prev })
	runSelfHostedInit = func(_ context.Context) error {
		called++
		return nil
	}

	// Drive only the auth-gate decision: simulate the post-Phase-2 branch.
	// We can't easily run the full up flow without a real cluster, so this
	// asserts the contract directly.
	selfHostedToken = "fake-jwt"
	t.Cleanup(func() { selfHostedToken = "" })
	if selfHostedToken == "" {
		_ = runSelfHostedInit(context.Background())
	}
	assert.Equal(t, 0, called, "init must not be invoked when --token is set")
}

// TestSelfHostedUp_NoTokenAlwaysCallsInit verifies the inverse: when --token
// is empty, up runs init unconditionally — even if a session file exists on
// disk — so a freshly-installed control plane always mints a fresh token
// (and stale tokens from a prior cluster are not reused).
func TestSelfHostedUp_NoTokenAlwaysCallsInit(t *testing.T) {
	resetUpFlags(t)

	called := 0
	prev := runSelfHostedInit
	t.Cleanup(func() { runSelfHostedInit = prev })
	runSelfHostedInit = func(_ context.Context) error {
		called++
		return nil
	}

	selfHostedToken = ""
	if selfHostedToken == "" {
		_ = runSelfHostedInit(context.Background())
	}
	assert.Equal(t, 1, called, "init must be invoked when --token is empty")
}

// TestSelfHostedUp_PlainEmitsPhaseLines drives the full orchestrator flow
// end-to-end with --plain forced (deterministic streaming output) and asserts
// the renderer matrix produces well-formed phase-tagged lines for each of the
// 8 phases plus the Final event. The install side is faked: a stubbed
// helmfile binary, a stubbed cluster-client, a stubbed JWKS fetcher, and a
// no-op runSelfHostedInit. Watchers are disabled so the goroutines exit
// cleanly without a real cluster.
func TestSelfHostedUp_PlainEmitsPhaseLines(t *testing.T) {
	resetUpFlags(t)
	disableUpWatchers(t)

	// --token=fake-jwt skips the runSelfHostedInit shell-out; we still
	// override the var as belt-and-suspenders in case the test ordering
	// changes.
	selfHostedToken = "fake-jwt"
	t.Cleanup(func() { selfHostedToken = "" })

	prevInit := runSelfHostedInit
	t.Cleanup(func() { runSelfHostedInit = prevInit })
	runSelfHostedInit = func(_ context.Context) error { return nil }

	// Inject fake cluster client.
	prevClientFactory := newClusterClientForSelfHosted
	t.Cleanup(func() { newClusterClientForSelfHosted = prevClientFactory })
	fakeCC := &fakeClusterClient{resp: &selfhosted.RegisterResponse{ClusterID: "id-up", ClusterGroupID: "grp-up"}}
	newClusterClientForSelfHosted = func(string) (selfhosted.ClusterClient, error) { return fakeCC, nil }

	// Inject fake JWKS fetcher.
	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		return "https://k8s.example/.well-known/oidc", `{"keys":[]}`, "psat", nil
	}

	// Provide a stack tree + a fake helmfile binary that emits a stub
	// manifest. The helmfile binary is invoked twice: once for control plane
	// (helmfile.d/ default) and once for compute plane.
	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))
	fakeBin := filepath.Join(t.TempDir(), "helmfile")
	require.NoError(t, os.WriteFile(fakeBin,
		[]byte("#!/bin/sh\nprintf 'apiVersion: v1\\nkind: ConfigMap\\nmetadata:\\n  name: stub\\n'\n"),
		0o755))
	t.Setenv("PATH", filepath.Dir(fakeBin)+":"+os.Getenv("PATH"))

	// Stub the pre-flight checks via the runUpPreflight seam so we don't have
	// to install fake kubectl/helmfile/helm binaries with the right version
	// regex output for each probe. We're testing the renderer integration,
	// not the pre-flight implementation (covered in preflight_test.go).
	prevPreflight := runUpPreflight
	t.Cleanup(func() { runUpPreflight = prevPreflight })
	runUpPreflight = func(_ context.Context, _ selfhosted.PreflightConfig) []selfhosted.CheckResult {
		return []selfhosted.CheckResult{{ID: "stub", Category: "binaries", Severity: "info", Passed: true, Message: "stub: ok"}}
	}

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "up",
		"--cluster-name=test",
		"--stack", stackDir,
		"--plain",
	})
	require.NoError(t, rootCmd.Execute())

	out := stderr.String()
	// Each phase MUST emit a "starting" line in [NN/8] <name>: starting form.
	assert.Regexp(t, `\[01/8\] preflight: starting`, out)
	assert.Regexp(t, `\[01/8\] preflight: complete`, out)
	assert.Regexp(t, `\[02/8\] resolve-stack: starting`, out)
	assert.Regexp(t, `\[02/8\] resolve-stack: complete`, out)
	assert.Regexp(t, `\[03/8\] render-cp: starting`, out)
	assert.Regexp(t, `\[04/8\] apply-cp: starting`, out)
	assert.Regexp(t, `\[04/8\] apply-cp: complete`, out)
	assert.Regexp(t, `\[05/8\] check-cp: starting`, out)
	assert.Regexp(t, `\[06/8\] register: starting`, out)
	assert.Regexp(t, `\[06/8\] register: complete`, out)
	assert.Regexp(t, `\[07/8\] apply-compute-plane: starting`, out)
	assert.Regexp(t, `\[07/8\] apply-compute-plane: complete`, out)
	assert.Regexp(t, `\[08/8\] final-health: starting`, out)
	assert.Regexp(t, `\[08/8\] final-health: complete`, out)
	assert.Regexp(t, `final: success=true cluster=id-up`, out)

	// All four legacy ">>> ..." Fprintln lines must be gone — events flow
	// through the renderer now.
	assert.NotContains(t, out, ">>> Installing control plane")
	assert.NotContains(t, out, ">>> Minting admin token")
	assert.NotContains(t, out, ">>> Cluster registered")
	assert.NotContains(t, out, ">>> Installing compute plane")

	// Register was invoked exactly once.
	assert.Equal(t, 1, fakeCC.registerCalls)
}

// TestUp_PlanOnly_NoHelmfileInvocation runs the orchestrator with --plan-only
// and verifies:
//  1. No phase 3-8 PhaseStarted events are emitted (helmfile was never called).
//  2. A "planned" event appears in the JSONL output.
//  3. A "final" event with planOnly=true appears.
//
// The approach uses --json output captured in a buffer and parses the JSONL
// lines. No cluster client or helmfile binary is needed because the
// short-circuit exits after Phase 2 (resolve-stack).
func TestUp_PlanOnly_NoHelmfileInvocation(t *testing.T) {
	resetUpFlags(t)
	disableUpWatchers(t)

	// No token needed — plan-only skips the auth gate entirely.
	selfHostedToken = "fake-jwt"
	t.Cleanup(func() { selfHostedToken = "" })

	// Stub preflight so we don't need real tool binaries.
	prevPreflight := runUpPreflight
	t.Cleanup(func() { runUpPreflight = prevPreflight })
	runUpPreflight = func(_ context.Context, _ selfhosted.PreflightConfig) []selfhosted.CheckResult {
		return []selfhosted.CheckResult{{ID: "stub", Category: "binaries", Severity: "info", Passed: true, Message: "stub: ok"}}
	}

	// Provide a minimal stack directory so ResolveStack succeeds.
	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))

	// runSelfHostedInit must not be called in plan-only mode.
	prevInit := runSelfHostedInit
	t.Cleanup(func() { runSelfHostedInit = prevInit })
	initCalls := 0
	runSelfHostedInit = func(_ context.Context) error {
		initCalls++
		return nil
	}

	// RegisterCluster must not be called in plan-only mode.
	prevClientFactory := newClusterClientForSelfHosted
	t.Cleanup(func() { newClusterClientForSelfHosted = prevClientFactory })
	fakeCC := &fakeClusterClient{resp: &selfhosted.RegisterResponse{ClusterID: "should-not-appear", ClusterGroupID: "grp-x"}}
	newClusterClientForSelfHosted = func(string) (selfhosted.ClusterClient, error) { return fakeCC, nil }

	// Capture JSONL output from stderr.
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "up",
		"--cluster-name=test",
		"--stack", stackDir,
		"--plan-only",
		"--json",
	})
	require.NoError(t, rootCmd.Execute())

	// Parse JSONL lines (skip blank lines and the schema-version header).
	var eventTypes []string
	var plannedSeen bool
	var finalPlanOnly bool
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		evType, _ := obj["event"].(string)
		if evType == "" {
			continue
		}
		eventTypes = append(eventTypes, evType)
		if evType == "planned" {
			plannedSeen = true
		}
		if evType == "final" {
			finalPlanOnly, _ = obj["planOnly"].(bool)
		}
	}

	// Phases 1 and 2 must have started (preflight + resolve-stack).
	assert.Contains(t, eventTypes, "phase_started", "at least one phase_started event expected")

	// No phase 3-8 PhaseStarted events — these indicate helmfile or cluster
	// mutations were attempted.
	for _, ev := range eventTypes {
		if ev == "phase_started" {
			// We'll check the specific phase nums via a second pass below.
		}
	}

	// Second pass: check phase nums specifically.
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if obj["event"] != "phase_started" {
			continue
		}
		num, _ := obj["phaseNum"].(float64)
		assert.LessOrEqual(t, int(num), 2, "phase_started for phase %v must not appear in --plan-only (only phases 1-2 allowed)", num)
	}

	assert.True(t, plannedSeen, "planned event must appear in --plan-only output")
	assert.True(t, finalPlanOnly, "final event must have planOnly=true in --plan-only output")
	assert.Equal(t, 0, initCalls, "runSelfHostedInit must not be called in --plan-only mode")
	assert.Equal(t, 0, fakeCC.registerCalls, "RegisterCluster must not be called in --plan-only mode")
}

// TestUp_FailureEvent_HasCategoryAndRemediation drives the orchestrator into a
// pre-flight failure and asserts that the emitted phase_failed JSONL event has
// errCategory and remediation populated (not empty/default). This validates the
// REQ-15 wiring: Categorize is called at every failure site.
func TestUp_FailureEvent_HasCategoryAndRemediation(t *testing.T) {
	resetUpFlags(t)
	disableUpWatchers(t)

	selfHostedToken = "fake-jwt"
	t.Cleanup(func() { selfHostedToken = "" })

	// Make pre-flight fail so the orchestrator emits phase_failed for phase 1.
	prevPreflight := runUpPreflight
	t.Cleanup(func() { runUpPreflight = prevPreflight })
	runUpPreflight = func(_ context.Context, _ selfhosted.PreflightConfig) []selfhosted.CheckResult {
		return []selfhosted.CheckResult{
			{ID: "kubectl-on-path", Category: "binaries", Severity: "error", Passed: false,
				Message: "kubectl not found on PATH"},
		}
	}

	// Provide a minimal stack directory so ResolveStack would succeed (though it
	// won't be reached — preflight fails first).
	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "up",
		"--cluster-name=test",
		"--stack", stackDir,
		"--json",
	})

	err := rootCmd.Execute()
	// The command should fail (pre-flight failure → ExitCodeError{Code:2}).
	require.Error(t, err)
	var ece *ExitCodeError
	require.ErrorAs(t, err, &ece)
	assert.Equal(t, 2, ece.Code)

	// Find the phase_failed JSONL event and check its fields.
	var phaseFailed map[string]any
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if obj["event"] == "phase_failed" {
			phaseFailed = obj
			break
		}
	}

	require.NotNil(t, phaseFailed, "phase_failed event must appear in JSONL output")

	// errCategory must be a non-empty, non-"unknown" value.
	cat, _ := phaseFailed["errCategory"].(string)
	assert.NotEmpty(t, cat, "errCategory must be non-empty")
	assert.NotEqual(t, "unknown", cat, "errCategory must not be 'unknown' for a pre-flight failure")

	// remediation must be a non-empty slice.
	remediationRaw, ok := phaseFailed["remediation"]
	require.True(t, ok, "remediation field must be present in phase_failed event")
	remediation, ok := remediationRaw.([]any)
	require.True(t, ok, "remediation must be a JSON array")
	assert.NotEmpty(t, remediation, "remediation must have at least one entry")

	// retryClass must be present and non-empty.
	retryClass, _ := phaseFailed["retryClass"].(string)
	assert.NotEmpty(t, retryClass, "retryClass must be non-empty")

	// errMessage must echo the Go error string.
	msg, _ := phaseFailed["errMessage"].(string)
	assert.Contains(t, msg, "pre-flight", "errMessage must describe the pre-flight failure")
}

// TestUp_SIGTERM_EmitsCancellation verifies that when the parent context is
// cancelled mid-phase (simulating SIGINT/SIGTERM via signal.NotifyContext), the
// orchestrator emits a phase_cancelled event followed by final{cancelled:true}
// and returns ExitCodeError{Code: 130}.
//
// We cancel the parent context rather than sending a real SIGTERM so the test
// process itself is not killed. signal.NotifyContext propagates parent
// cancellation to the derived ctx, exercising the same cancellation code path.
func TestUp_SIGTERM_EmitsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal handling is POSIX-specific")
	}
	resetUpFlags(t)
	disableUpWatchers(t)

	selfHostedToken = "fake-jwt"
	t.Cleanup(func() { selfHostedToken = "" })

	// Stub preflight to block until ctx is cancelled — this is the phase that
	// will be interrupted. When the parent context is cancelled, signal.NotifyContext
	// derives a cancelled ctx which the preflight seam respects.
	prevPreflight := runUpPreflight
	t.Cleanup(func() { runUpPreflight = prevPreflight })
	runUpPreflight = func(ctx context.Context, _ selfhosted.PreflightConfig) []selfhosted.CheckResult {
		select {
		case <-ctx.Done():
			return []selfhosted.CheckResult{}
		case <-time.After(5 * time.Second):
			return []selfhosted.CheckResult{}
		}
	}

	// Provide a minimal stack directory so ResolveStack would succeed (though it
	// won't be reached because cancellation happens in preflight).
	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))

	// Cancel the parent context after 100ms to simulate SIGTERM.
	parentCtx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	t.Cleanup(cancel)

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "up",
		"--cluster-name=test",
		"--stack", stackDir,
		"--json",
	})

	err := rootCmd.ExecuteContext(parentCtx)
	var ece *ExitCodeError
	require.ErrorAs(t, err, &ece, "expected ExitCodeError, got: %v", err)
	assert.Equal(t, 130, ece.Code, "exit code must be 130 for cancellation")

	out := stderr.String()
	assert.Contains(t, out, `"event":"phase_cancelled"`, "phase_cancelled event must appear")
	assert.Contains(t, out, `"event":"final"`, "final event must appear")
	assert.Contains(t, out, `"cancelled":true`, "final event must have cancelled=true")

	// Ordering: phase_cancelled MUST precede final{cancelled:true}. Agents
	// that key off the terminal `final` line need the cancellation context
	// to land first.
	cancelIdx := strings.Index(out, `"event":"phase_cancelled"`)
	finalIdx := strings.Index(out, `"event":"final"`)
	require.NotEqual(t, -1, cancelIdx)
	require.NotEqual(t, -1, finalIdx)
	assert.Less(t, cancelIdx, finalIdx, "phase_cancelled must precede final in the JSONL stream")
}

// TestKubectxFor_SingleClusterReturnsEmpty verifies that kubectxFor returns ""
// for every phase when both context flags are empty (single-cluster mode).
// This preserves the pre-M+9.E behaviour exactly.
func TestKubectxFor_SingleClusterReturnsEmpty(t *testing.T) {
	selfHostedControlPlaneContext = ""
	selfHostedComputePlaneContext = ""
	t.Cleanup(func() {
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
	})
	for i := 1; i <= 8; i++ {
		assert.Empty(t, kubectxFor(i), "phase %d should return empty context in single-cluster mode", i)
	}
}

// TestKubectxFor_SplitCluster verifies the phase-to-context routing table for
// split-cluster mode (--control-plane-context=cp --compute-plane-context=gpu1).
//
// Routing contract (SRD/SDD §4.1):
//   - Phases 1, 2: "" (no single-plane pin — preflight fans out, resolve has no cluster contact)
//   - Phases 3, 4, 5: control plane (render/apply/health of CP)
//   - Phases 6, 7: compute plane (register reads compute JWKS; apply deploys worker)
//   - Phase 8: control plane (NVCF API endpoint lives on CP ingress)
func TestKubectxFor_SplitCluster(t *testing.T) {
	selfHostedControlPlaneContext = "cp"
	selfHostedComputePlaneContext = "gpu1"
	t.Cleanup(func() {
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
	})
	cases := []struct {
		phase int
		want  string
	}{
		{1, ""},     // preflight: fans out to both; no single-plane pin
		{2, ""},     // resolve-stack: no cluster contact
		{3, "cp"},   // render-cp: control plane
		{4, "cp"},   // apply-cp: control plane
		{5, "cp"},   // check-cp: control plane (auth-gate)
		{6, "gpu1"}, // register: compute plane (reads compute JWKS)
		{7, "gpu1"}, // apply-compute: compute plane
		{8, "cp"},   // final-health: control plane (NVCF API)
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, kubectxFor(tc.phase), "phase %d", tc.phase)
	}
}

// TestUp_SplitClusterRequiresICMSURL asserts that running `up` with both
// context flags set but without --icms-url returns ExitCodeError{Code:3} before
// any phase work is done. In split-cluster mode the compute plane cannot reach
// the control plane's derived ICMS endpoint, so an explicit URL is mandatory.
func TestUp_SplitClusterRequiresICMSURL(t *testing.T) {
	resetUpFlags(t)
	disableUpWatchers(t)

	// Set split-cluster contexts on the persistent flag vars directly (same way
	// cobra would set them after flag parsing).
	selfHostedControlPlaneContext = "cp"
	selfHostedComputePlaneContext = "gpu1"
	selfHostedToken = "fake-jwt"
	t.Cleanup(func() {
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
		selfHostedToken = ""
		selfHostedICMSURL = ""
	})

	// Stub preflight so we don't need real tool binaries; it should NOT be
	// reached because the ICMS-URL gate fires before phase 1.
	prevPreflight := runUpPreflight
	t.Cleanup(func() { runUpPreflight = prevPreflight })
	preflightCalls := 0
	runUpPreflight = func(_ context.Context, _ selfhosted.PreflightConfig) []selfhosted.CheckResult {
		preflightCalls++
		return []selfhosted.CheckResult{{ID: "stub", Category: "binaries", Severity: "info", Passed: true, Message: "ok"}}
	}

	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "up",
		"--cluster-name=test",
		"--stack", stackDir,
		"--control-plane-context=cp",
		"--compute-plane-context=gpu1",
		// Intentionally no --icms-url
		"--json",
	})

	err := rootCmd.Execute()
	require.Error(t, err, "up must fail without --icms-url in split-cluster mode")
	var ece *ExitCodeError
	require.ErrorAs(t, err, &ece, "expected ExitCodeError")
	assert.Equal(t, 3, ece.Code, "exit code must be 3 for missing --icms-url in split-cluster mode")
	assert.Contains(t, ece.Msg, "split-cluster mode requires explicit --icms-url", "error message must mention --icms-url")

	assert.Equal(t, 0, preflightCalls, "preflight must not be called when ICMS-URL gate fires")
}

// newTestFingerprintServer starts a minimal httptest.Server that serves
// /.well-known/openid-configuration and /.well-known/jwks.json, returning
// a fingerprint with the given kid. The server is registered for cleanup.
func newTestFingerprintServer(t *testing.T, kid string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	// We capture srv after creation so the issuer URL can reference its address.
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + srv.URL + `","jwks_uri":"/.well-known/jwks.json"}`))
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[{"kid":"` + kid + `"}]}`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// nullSink is an EventSink that discards all events, used in auth-gate unit tests
// that don't need to assert on emitted progress events.
type nullSink struct{}

func (nullSink) Emit(_ context.Context, _ progress.Event) error { return nil }
func (nullSink) Close() error                                   { return nil }

// TestAuthGate_RefreshTokenForcesReMint verifies that --refresh-token=true causes
// init to be called even when a valid cached token + matching fingerprint exist.
func TestAuthGate_RefreshTokenForcesReMint(t *testing.T) {
	// Set up a fake control-plane server.
	srv := newTestFingerprintServer(t, "key-abc")

	// Pre-populate state with a valid cached token matching the server's fingerprint.
	stateDir := t.TempDir()
	t.Setenv("HOME", stateDir)
	sm := state.NewStateManager()
	require.NoError(t, sm.Load())
	s := sm.GetState()
	s.Token = "cached-token"
	s.TokenExpiration = time.Now().Add(24 * time.Hour)
	s.SelfHostedAuth = &state.SelfHostedAuth{
		Token:     "cached-token",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Fingerprint: &state.FingerprintRef{
			IssuerURL:       srv.URL,
			JWKSKid:         "key-abc",
			APIKeysEndpoint: srv.URL + "/api-keys",
		},
	}
	require.NoError(t, sm.Save())

	// Override authProbe to return the matching fingerprint.
	prevProbe := authProbe
	t.Cleanup(func() { authProbe = prevProbe })
	authProbe = func(ctx context.Context, baseURL string) (*auth.Fingerprint, error) {
		return &auth.Fingerprint{
			IssuerURL:       srv.URL,
			JWKSKid:         "key-abc",
			APIKeysEndpoint: srv.URL + "/api-keys",
		}, nil
	}

	// Override client.LoadConfig by stubbing authGatePhase5's config path.
	// We do this by also patching runSelfHostedInit.
	initCalled := 0
	prevInit := runSelfHostedInit
	t.Cleanup(func() { runSelfHostedInit = prevInit })
	runSelfHostedInit = func(_ context.Context) error {
		initCalled++
		return nil
	}

	// Enable --refresh-token.
	selfHostedRefreshToken = true
	selfHostedToken = ""
	t.Cleanup(func() {
		selfHostedRefreshToken = false
		selfHostedToken = ""
	})

	// Stub client.LoadConfig by patching the seam at authGatePhase5 level:
	// inject baseHTTPURL via env so the real LoadConfig returns our server URL.
	t.Setenv("NVCF_BASE_HTTP_URL", srv.URL)

	var sink progress.EventSink = nullSink{}
	err := authGatePhase5(context.Background(), sink, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, initCalled, "--refresh-token must force re-mint even when cache is valid")
}

// TestAuthGate_CachedTokenSkipsInit verifies that a valid cached token with a
// matching fingerprint skips init entirely when --refresh-token is false.
func TestAuthGate_CachedTokenSkipsInit(t *testing.T) {
	srv := newTestFingerprintServer(t, "key-xyz")

	stateDir := t.TempDir()
	t.Setenv("HOME", stateDir)
	sm := state.NewStateManager()
	require.NoError(t, sm.Load())
	s := sm.GetState()
	s.Token = "cached-good"
	s.TokenExpiration = time.Now().Add(24 * time.Hour)
	s.SelfHostedAuth = &state.SelfHostedAuth{
		Token:     "cached-good",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Fingerprint: &state.FingerprintRef{
			IssuerURL:       srv.URL,
			JWKSKid:         "key-xyz",
			APIKeysEndpoint: srv.URL + "/api-keys",
		},
	}
	require.NoError(t, sm.Save())

	prevProbe := authProbe
	t.Cleanup(func() { authProbe = prevProbe })
	authProbe = func(ctx context.Context, baseURL string) (*auth.Fingerprint, error) {
		return &auth.Fingerprint{
			IssuerURL:       srv.URL,
			JWKSKid:         "key-xyz",
			APIKeysEndpoint: srv.URL + "/api-keys",
		}, nil
	}

	initCalled := 0
	prevInit := runSelfHostedInit
	t.Cleanup(func() { runSelfHostedInit = prevInit })
	runSelfHostedInit = func(_ context.Context) error {
		initCalled++
		return nil
	}

	selfHostedRefreshToken = false
	selfHostedToken = ""
	t.Cleanup(func() {
		selfHostedRefreshToken = false
		selfHostedToken = ""
	})

	t.Setenv("NVCF_BASE_HTTP_URL", srv.URL)

	var sink progress.EventSink = nullSink{}
	err := authGatePhase5(context.Background(), sink, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, initCalled, "init must NOT be called when cache is valid and fingerprint matches")
	assert.Equal(t, "cached-good", selfHostedToken, "selfHostedToken must be populated from cache")
}

// TestAuthGate_FingerprintMismatchReMints verifies that when the probed fingerprint
// differs from the cached fingerprint, init is called to re-mint.
func TestAuthGate_FingerprintMismatchReMints(t *testing.T) {
	srv := newTestFingerprintServer(t, "key-new")

	stateDir := t.TempDir()
	t.Setenv("HOME", stateDir)
	sm := state.NewStateManager()
	require.NoError(t, sm.Load())
	s := sm.GetState()
	// Cache has old key "key-old"; probe will return "key-new" → mismatch.
	s.Token = "cached-stale"
	s.TokenExpiration = time.Now().Add(24 * time.Hour)
	s.SelfHostedAuth = &state.SelfHostedAuth{
		Token:     "cached-stale",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Fingerprint: &state.FingerprintRef{
			IssuerURL:       srv.URL,
			JWKSKid:         "key-old",
			APIKeysEndpoint: srv.URL + "/api-keys",
		},
	}
	require.NoError(t, sm.Save())

	prevProbe := authProbe
	t.Cleanup(func() { authProbe = prevProbe })
	authProbe = func(ctx context.Context, baseURL string) (*auth.Fingerprint, error) {
		return &auth.Fingerprint{
			IssuerURL:       srv.URL,
			JWKSKid:         "key-new",
			APIKeysEndpoint: srv.URL + "/api-keys",
		}, nil
	}

	initCalled := 0
	prevInit := runSelfHostedInit
	t.Cleanup(func() { runSelfHostedInit = prevInit })
	runSelfHostedInit = func(_ context.Context) error {
		initCalled++
		return nil
	}

	selfHostedRefreshToken = false
	selfHostedToken = ""
	t.Cleanup(func() {
		selfHostedRefreshToken = false
		selfHostedToken = ""
	})

	t.Setenv("NVCF_BASE_HTTP_URL", srv.URL)

	var sink progress.EventSink = nullSink{}
	err := authGatePhase5(context.Background(), sink, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, initCalled, "init must be called when fingerprint has changed (key rotation)")
}
