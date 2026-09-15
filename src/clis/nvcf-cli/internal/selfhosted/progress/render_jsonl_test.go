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

package progress

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func runEmit(t *testing.T, clock *fakeClock, events []Event) string {
	t.Helper()
	var buf bytes.Buffer
	r := newJSONLRendererForTest(&buf, clock.Now)
	ctx := context.Background()
	for _, e := range events {
		require.NoError(t, r.Emit(ctx, e))
	}
	return buf.String()
}

func TestRenderJSONL_BasicSequence(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:23:45Z"),
		ts("2026-04-29T01:23:53Z"),
		ts("2026-04-29T01:23:53Z"),
		ts("2026-04-29T01:23:56Z"),
		ts("2026-04-29T01:35:25Z"),
	}}
	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight", StartedAt: ts("2026-04-29T01:23:45Z")},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 8 * time.Second},
		PhaseStarted{Num: 2, Name: "resolve-stack", StartedAt: ts("2026-04-29T01:23:53Z")},
		PhaseCompleted{Num: 2, Name: "resolve-stack", Duration: 3 * time.Second},
		Final{Success: true, ClusterID: "abc", NVCFBackendHealth: "healthy", Duration: 11*time.Minute + 40*time.Second, DroppedProgressEvents: 0},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_basic.golden", got)
}

func TestRenderJSONL_PhaseProgress(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:30:10Z"),
		ts("2026-04-29T01:30:12Z"),
		ts("2026-04-29T01:30:14Z"),
	}}
	events := []Event{
		PhaseProgress{Num: 4, Name: "apply-cp", Resource: "namespaces", Done: 3, Total: 5},
		PhaseProgress{Num: 4, Name: "apply-cp", Resource: "deployments", Done: 9, Total: 14},
		PhaseProgress{Num: 4, Name: "apply-cp", Resource: "statefulsets", Done: 2, Total: 4},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_phase_progress.golden", got)
}

func TestRenderJSONL_PhaseFailed(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:32:00Z"),
	}}
	events := []Event{
		PhaseFailed{
			Num: 4, Name: "apply-cp", Duration: 2 * time.Minute,
			ErrCategory: "helm_apply", ErrMessage: "helm install api-keys: timed out",
			RetryClass: "backoff", RetryAfterSec: 60,
			Remediation: []string{"kubectl describe pod -n cassandra-system cassandra-0", "Re-run with --debug"},
			Raw: RawFailure{Subprocess: "helmfile", ExitCode: 1, StderrTail: "Error: timeout reached", KubernetesReason: "FailedScheduling"},
		},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_phase_failed.golden", got)
}

func TestRenderJSONL_PhaseFailed_NoRemediationNoRaw(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:23:50Z"),
	}}
	events := []Event{
		PhaseFailed{
			Num: 1, Name: "preflight", Duration: 5 * time.Second,
			ErrCategory: "internal", ErrMessage: "fatal", RetryClass: "none",
		},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_phase_failed_minimal.golden", got)
}

func TestRenderJSONL_AllEventKinds(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:00:01Z"),
		ts("2026-04-29T01:00:02Z"),
		ts("2026-04-29T01:00:03Z"),
		ts("2026-04-29T01:00:04Z"),
		ts("2026-04-29T01:00:05Z"),
		ts("2026-04-29T01:00:06Z"),
		ts("2026-04-29T01:00:07Z"),
		ts("2026-04-29T01:00:08Z"),
	}}
	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight"},
		PhaseProgress{Num: 1, Name: "preflight", Resource: "crds", Done: 1, Total: 5},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 10 * time.Second},
		PhaseFailed{Num: 2, Name: "resolve-stack", Duration: 5 * time.Second, ErrCategory: "internal", ErrMessage: "err", RetryClass: "none"},
		PhaseCancelled{Num: 3, Name: "apply-cp", Reason: "SIGINT"},
		Waiting{Num: 3, Reason: "throttle"},
		LastProgress{Num: 3, Detail: "still running"},
		Final{Success: false, Duration: 30 * time.Second},
	}
	got := runEmit(t, clock, events)

	// Verify: schemaVersion line + 8 event lines
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 9 {
		t.Fatalf("expected 9 lines (1 schema + 8 events), got %d:\n%s", len(lines), got)
	}

	wantKinds := []string{
		"phase_started", "phase_progress", "phase_completed",
		"phase_failed", "phase_cancelled", "waiting", "last_progress", "final",
	}
	for i, kind := range wantKinds {
		line := lines[i+1]
		if !strings.Contains(line, `"event":"`+kind+`"`) {
			t.Errorf("line %d: expected event=%q, got: %s", i+1, kind, line)
		}
	}

	assertGolden(t, "testdata/jsonl_all_kinds.golden", got)
}

func TestRenderJSONL_FinalCancelled(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:35:00Z"),
	}}
	events := []Event{
		Final{Cancelled: true, ClusterID: "abc", Duration: 3*time.Minute + 12*time.Second, DroppedProgressEvents: 0},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_final_cancelled.golden", got)
}

// TestRenderJSONL_HTMLChars locks in SetEscapeHTML(false). The characters <, >,
// and & must appear literally in the output — NOT as &lt;, &gt;, &amp;. This
// guards against a future maintainer flipping the flag or removing the
// newJSONEncoder helper, which would silently corrupt kubectl/helm stderr output
// (e.g. "<unknown>", "< 0.1s", XML in error messages).
func TestRenderJSONL_HTMLChars(t *testing.T) {
	clock := &fakeClock{times: []time.Time{ts("2026-04-29T01:32:00Z")}}
	events := []Event{
		PhaseFailed{
			Num: 4, Name: "apply-cp", Duration: 90 * time.Second,
			ErrCategory: "helm_apply",
			ErrMessage:  "kubectl: <unknown> error & timeout",
			RetryClass:  "after_remediation",
			Raw: RawFailure{
				Subprocess: "kubectl",
				ExitCode:   1,
				StderrTail: "Error: <pod ready=false> & condition not met",
				HTTPStatus: 503,
			},
		},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_html_chars.golden", got)
}

// TestRenderJSONL_EscapesQuotesAndNewlines locks in stdlib's JSON-string
// escaping for embedded double-quotes and newlines. Embedded " must appear as
// \" and embedded newline must appear as \n (two-character escape), not a
// literal newline that would break JSONL line-orientation.
func TestRenderJSONL_EscapesQuotesAndNewlines(t *testing.T) {
	clock := &fakeClock{times: []time.Time{ts("2026-04-29T01:32:00Z")}}
	events := []Event{
		PhaseFailed{
			Num: 1, Name: "preflight", Duration: 5 * time.Second,
			ErrCategory: "internal",
			ErrMessage:  "helm: \"timeout\"" + "\nwaiting for pod",
			RetryClass:  "none",
		},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_escapes.golden", got)
}

// TestRenderJSONL_StatusSnapshot emits a full status snapshot (Snapshot +
// ComponentHealth + ClusterRow + RecentEvent) and pins the output to a golden.
// This mirrors TestRenderPlain_StatusSnapshot using the same event sequence.
func TestRenderJSONL_StatusSnapshot(t *testing.T) {
	// One clock tick per event: 1 Snapshot + 10 ComponentHealth + 2 ClusterRow + 3 RecentEvent = 16
	// schemaVersion is emitted before the first Emit call (no clock tick).
	clock := &fakeClock{times: func() []time.Time {
		const n = 16
		ticks := make([]time.Time, n)
		for i := range ticks {
			ticks[i] = ts("2026-04-29T03:45:12Z")
		}
		return ticks
	}()}

	events := []Event{
		Snapshot{
			Cluster:         "ncp-local",
			Verdict:         "healthy",
			ReconcileAgeSec: 18,
			Identity: SnapshotIdentity{
				ClusterID:    "cl-abc123",
				Target:       "https://nvcf.nvidia.com",
				StackVersion: "1.14.2",
				StackDigest:  "sha256:deadbeef",
				InstalledAt:  ts("2026-04-25T14:02:18Z"),
			},
		},
		ComponentHealth{Name: "sis", Ready: 2, Total: 2, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "nats", Ready: 3, Total: 3, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "cassandra", Ready: 1, Total: 1, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "openbao", Ready: 3, Total: 3, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "api-keys", Ready: 1, Total: 1, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "nvcf-api", Ready: 3, Total: 3, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "reval", Ready: 2, Total: 2, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "gateway", Ready: 1, Total: 1, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "nvca-operator", Ready: 1, Total: 1, UptimeSec: 604800, Healthy: true},
		ComponentHealth{Name: "nvca-worker", Cluster: "ncp-local", Ready: 2, Total: 2, UptimeSec: 604800, Healthy: true},
		ClusterRow{
			Name:              "ncp-local",
			GPU:               "A100",
			GPUCount:          4,
			ActiveDeployments: 2,
			LastSeenAgeSec:    45,
			Healthy:           true,
		},
		ClusterRow{
			Name:              "yotta-east-1",
			GPU:               "H100",
			GPUCount:          8,
			ActiveDeployments: 0,
			LastSeenAgeSec:    120,
			Healthy:           true,
		},
		RecentEvent{AgeSec: 300, Kind: "function-deploy", Status: "ACTIVE", Name: "my-fn", Version: "3"},
		RecentEvent{AgeSec: 3600, Kind: "cluster-registered", Name: "yotta-east-1"},
		RecentEvent{AgeSec: 86400, Kind: "node-pressure", Name: "ncp-local-worker-0"},
	}

	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_status.golden", got)
}

// TestRenderJSONL_CheckStream emits a pre-flight check sequence and pins to a golden.
// Mirrors TestRenderPlain_CheckStream using the same event sequence. The Final event
// carries verdict/totalChecks/passedCount/failedCount per §6.6.3 (M+8.11).
func TestRenderJSONL_CheckStream(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T03:45:01Z"), // CheckStarted kubectl-on-path
		ts("2026-04-29T03:45:01Z"), // CheckCompleted kubectl-on-path
		ts("2026-04-29T03:45:01Z"), // CheckStarted helmfile-on-path
		ts("2026-04-29T03:45:01Z"), // CheckCompleted helmfile-on-path
		ts("2026-04-29T03:45:08Z"), // CheckStarted gateway-api
		ts("2026-04-29T03:45:08Z"), // CheckCompleted gateway-api
		ts("2026-04-29T03:45:08Z"), // CategoryCompleted
		ts("2026-04-29T03:45:09Z"), // Final
	}}

	events := []Event{
		CheckStarted{Category: "local-host-tools", ID: "kubectl-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "kubectl-on-path", Passed: true, Severity: "info", Message: "kubectl 1.30.2 on PATH", Detail: "1.30.2"},
		CheckStarted{Category: "local-host-tools", ID: "helmfile-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "helmfile-on-path", Passed: true, Severity: "info", Message: "helmfile 0.162.0 on PATH", Detail: "0.162.0"},
		CheckStarted{Category: "pre-kubernetes-setup", ID: "gateway-api"},
		CheckCompleted{Category: "pre-kubernetes-setup", ID: "gateway-api", Passed: false, Severity: "error", Message: "Gateway API CRDs not installed", HintURL: "https://docs.nvidia.com/nvcf/self-hosted/gateway-api"},
		CategoryCompleted{Category: "pre-kubernetes-setup", PassedCount: 13, FailedCount: 1, DurationSec: 2.4},
		Final{Success: false, Verdict: "failed", TotalChecks: 14, PassedCount: 13, FailedCount: 1},
	}

	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_check.golden", got)
}

// TestRenderJSONL_StatusComposed emits a status snapshot sequence using the
// composed-mode renderer (NewJSONLRendererForStatus / Plan Deviation #19).
// The renderer buffers ComponentHealth/ClusterRow/RecentEvent and emits a
// single fat snapshot object per §6.5.4.
func TestRenderJSONL_StatusComposed(t *testing.T) {
	// Clock ticks: schemaVersion (no tick) + flushComposed on Final (2 ticks:
	// one for the composed snapshot, one for the Final event).
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T03:45:12Z"), // snapshot flush ts
		ts("2026-04-29T03:45:12Z"), // Final event ts
	}}

	var buf bytes.Buffer
	r := newJSONLRendererForStatusTest(&buf, clock.Now)
	ctx := context.Background()

	events := []Event{
		Snapshot{
			Cluster:         "my-cluster",
			Verdict:         "healthy",
			ReconcileAgeSec: 12,
			Identity: SnapshotIdentity{
				ClusterID:    "cc9f8a98-5853-4e3b-929c-9154d0ac0ecc",
				Target:       "yotta-prod-us-east-1",
				StackVersion: "v1.2.0",
				StackDigest:  "sha256:0180e2",
				InstalledAt:  ts("2026-04-25T14:02:18Z"),
			},
		},
		ComponentHealth{Name: "sis", Ready: 2, Total: 2, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "nats", Ready: 3, Total: 3, UptimeSec: 1209600, Healthy: true},
		ClusterRow{Name: "ncp-local", GPU: "H100", GPUCount: 16, ActiveDeployments: 14, LastSeenAgeSec: 12, Healthy: true},
		RecentEvent{AgeSec: 161, Kind: "function-deploy", Status: "ACTIVE", Name: "echo-test", Version: "v1"},
		Final{Success: true},
	}

	for _, e := range events {
		require.NoError(t, r.Emit(ctx, e))
	}

	assertGolden(t, "testdata/jsonl_status_composed.golden", buf.String())
}

// TestRenderJSONL_SplitContext feeds a split-cluster event sequence (M+9.5) and
// asserts that "context":"admin@cp" / "context":"admin@gpu1" appear in the JSONL
// output. Events without a Context field must omit the field entirely (omitempty).
func TestRenderJSONL_SplitContext(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:23:45Z"), // PhaseStarted   phase 1, ctx=admin@cp
		ts("2026-04-29T01:23:53Z"), // PhaseCompleted  phase 1, ctx=admin@cp
		ts("2026-04-29T01:30:08Z"), // PhaseStarted   phase 6, ctx=admin@gpu1
		ts("2026-04-29T01:30:12Z"), // PhaseCompleted  phase 6, ctx=admin@gpu1
	}}
	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight", StartedAt: ts("2026-04-29T01:23:45Z"), Context: "admin@cp"},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 8 * time.Second, Context: "admin@cp"},
		PhaseStarted{Num: 6, Name: "register", StartedAt: ts("2026-04-29T01:30:08Z"), Context: "admin@gpu1"},
		PhaseCompleted{Num: 6, Name: "register", Duration: 4 * time.Second, Context: "admin@gpu1"},
	}
	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_split_install.golden", got)
}

// TestRenderJSONL_PhaseFailed_Structured feeds a PhaseFailed with all Raw fields
// populated plus after_remediation retry class and 4 remediation lines.
// This extends the existing jsonl_phase_failed.golden test coverage.
func TestRenderJSONL_PhaseFailed_Structured(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T03:50:00Z"),
	}}
	events := []Event{
		PhaseFailed{
			Num:         4,
			Name:        "apply-cp",
			Duration:    3 * time.Minute,
			ErrCategory: "helm_apply",
			ErrMessage:  "helmfile apply failed",
			RetryClass:  "after_remediation",
			Remediation: []string{
				"kubectl describe pod -n nvcf-system",
				"kubectl logs -n nvcf-system deploy/nvcf-api --previous",
				"Check disk pressure on nodes",
				"Re-run after resolving pod issues",
			},
			Raw: RawFailure{
				Subprocess:       "helmfile",
				ExitCode:         1,
				StderrTail:       "Error: timed out waiting for the condition\n",
				HTTPStatus:       0,
				KubernetesReason: "FailedScheduling",
			},
		},
	}

	got := runEmit(t, clock, events)
	assertGolden(t, "testdata/jsonl_phase_failed_structured.golden", got)
}

// runEmitWithTotalPhases is like runEmit but constructs the renderer with a
// custom phase denominator. (M+11.9)
func runEmitWithTotalPhases(t *testing.T, clock *fakeClock, events []Event, total int) string {
	t.Helper()
	var buf bytes.Buffer
	r := newJSONLRendererWithTotalPhasesForTest(&buf, clock.Now, total)
	ctx := context.Background()
	for _, e := range events {
		require.NoError(t, r.Emit(ctx, e))
	}
	return buf.String()
}

// TestRenderJSONL_Down feeds a 7-phase down sequence with DrainProgress events
// and asserts totalPhases=7 appears in phase_started lines. (M+11.9)
func TestRenderJSONL_Down(t *testing.T) {
	const fixedTS = "2026-04-29T01:23:45Z"
	clock := &fakeClock{times: func() []time.Time {
		const n = 12
		ticks := make([]time.Time, n)
		for i := range ticks {
			ticks[i] = ts(fixedTS)
		}
		return ticks
	}()}

	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight"},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 0},
		PhaseStarted{Num: 2, Name: "drain-active"},
		DrainProgress{Num: 2, Deployment: "function-deploy-1", State: "REMOVING"},
		DrainProgress{Num: 2, Deployment: "function-deploy-1", State: "STOPPED"},
		PhaseCompleted{Num: 2, Name: "drain-active", Duration: 1 * time.Second},
		PhaseStarted{Num: 3, Name: "uninstall-compute-plane"},
		PhaseCompleted{Num: 3, Name: "uninstall-compute-plane", Duration: 60 * time.Second},
		PhaseStarted{Num: 4, Name: "remove-cluster-row"},
		PhaseCompleted{Num: 4, Name: "remove-cluster-row", Duration: 1 * time.Second},
		Final{Success: true, ClusterID: "test", Duration: 62 * time.Second, DroppedProgressEvents: 0},
	}

	got := runEmitWithTotalPhases(t, clock, events, 7)
	assertGolden(t, "testdata/jsonl_down.golden", got)
}

// TestRenderJSONL_Uninstall feeds a 3-phase uninstall sequence and asserts
// totalPhases=3 appears in phase_started lines. (M+11.9)
func TestRenderJSONL_Uninstall(t *testing.T) {
	const fixedTS = "2026-04-29T01:23:45Z"
	clock := &fakeClock{times: func() []time.Time {
		const n = 8
		ticks := make([]time.Time, n)
		for i := range ticks {
			ticks[i] = ts(fixedTS)
		}
		return ticks
	}()}

	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight"},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 0},
		PhaseStarted{Num: 2, Name: "render-uninstall"},
		PhaseCompleted{Num: 2, Name: "render-uninstall", Duration: 0},
		PhaseStarted{Num: 3, Name: "helmfile-destroy"},
		PhaseCompleted{Num: 3, Name: "helmfile-destroy", Duration: 45 * time.Second},
		Final{Success: true, ClusterID: "test", Duration: 45 * time.Second, DroppedProgressEvents: 0},
	}

	got := runEmitWithTotalPhases(t, clock, events, 3)
	assertGolden(t, "testdata/jsonl_uninstall.golden", got)
}
