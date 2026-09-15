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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRenderPlain_BasicSequence(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:23:45Z"),
		ts("2026-04-29T01:23:53Z"),
		ts("2026-04-29T01:23:53Z"),
		ts("2026-04-29T01:23:56Z"),
	}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight", StartedAt: ts("2026-04-29T01:23:45Z")},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 8 * time.Second},
		PhaseStarted{Num: 2, Name: "resolve-stack", StartedAt: ts("2026-04-29T01:23:53Z")},
		PhaseCompleted{Num: 2, Name: "resolve-stack", Duration: 3 * time.Second},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_basic.golden", buf.String())
}

func TestRenderPlain_WithWaiting(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:24:00Z"),
		ts("2026-04-29T01:24:05Z"),
		ts("2026-04-29T01:24:10Z"),
		ts("2026-04-29T01:24:20Z"),
	}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		PhaseStarted{Num: 3, Name: "apply-ns", StartedAt: ts("2026-04-29T01:24:00Z")},
		Waiting{Num: 3, Reason: "cassandra CRD stabilization"},
		LastProgress{Num: 3, Detail: "namespaces 3/5", At: ts("2026-04-29T01:24:10Z")},
		PhaseCompleted{Num: 3, Name: "apply-ns", Duration: 20 * time.Second},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_with_waiting.golden", buf.String())
}

func TestRenderPlain_PhaseFailed(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:25:00Z"),
		ts("2026-04-29T01:25:00Z"),
		ts("2026-04-29T01:25:00Z"),
	}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		PhaseFailed{
			Num:           4,
			Name:          "apply-cp",
			ErrCategory:   "helm_apply",
			ErrMessage:    "timed out",
			RetryClass:    "backoff",
			RetryAfterSec: 60,
			Remediation: []string{
				"kubectl describe pod -n cassandra-system cassandra-0",
				"Re-run with --debug",
			},
		},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_phase_failed.golden", buf.String())
}

func TestRenderPlain_Cancelled(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:26:00Z"),
	}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		PhaseCancelled{Num: 4, Name: "apply-cp", Reason: "sigint"},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_cancelled.golden", buf.String())
}

func TestRenderPlain_Final(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:35:00Z"),
	}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		Final{
			Success:               true,
			ClusterID:             "abc",
			NVCFBackendHealth:     "healthy",
			Duration:              11*time.Minute + 40*time.Second,
			DroppedProgressEvents: 0,
		},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_final.golden", buf.String())
}

func TestRenderPlain_PhaseProgress(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:30:07Z"),
		ts("2026-04-29T01:30:12Z"),
		ts("2026-04-29T01:30:18Z"),
		ts("2026-04-29T01:30:24Z"),
		ts("2026-04-29T01:30:31Z"),
	}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		PhaseStarted{Num: 4, Name: "apply-cp", StartedAt: ts("2026-04-29T01:30:07Z")},
		PhaseProgress{Num: 4, Name: "apply-cp", Resource: "namespaces", Done: 8, Total: 8},
		PhaseProgress{Num: 4, Name: "apply-cp", Resource: "deployments", Done: 9, Total: 14},
		PhaseProgress{Num: 4, Name: "apply-cp", Resource: "statefulsets", Done: 2, Total: 5},
		PhaseCompleted{Num: 4, Name: "apply-cp", Duration: 5*time.Minute + 18*time.Second},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_phase_progress.golden", buf.String())
}

func TestRenderPlain_FinalCancelled(t *testing.T) {
	clock := &fakeClock{times: []time.Time{ts("2026-04-29T01:35:00Z")}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		Final{
			Cancelled:             true,
			ClusterID:             "abc",
			Duration:              3*time.Minute + 12*time.Second,
			DroppedProgressEvents: 0,
		},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_final_cancelled.golden", buf.String())
}

// TestRenderPlain_StatusSnapshot feeds a full status snapshot (matching §6.5.3)
// and pins all timestamps to 2026-04-29T03:45:12Z.
func TestRenderPlain_StatusSnapshot(t *testing.T) {
	// One clock tick per event: 1 Snapshot + 10 ComponentHealth + 2 ClusterRow + 3 RecentEvent = 16
	clock := &fakeClock{times: func() []time.Time {
		const n = 16
		ticks := make([]time.Time, n)
		for i := range ticks {
			ticks[i] = ts("2026-04-29T03:45:12Z")
		}
		return ticks
	}()}

	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)
	ctx := context.Background()

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
		// Control-plane components (no cluster context)
		ComponentHealth{Name: "sis", Ready: 2, Total: 2, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "nats", Ready: 3, Total: 3, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "cassandra", Ready: 1, Total: 1, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "openbao", Ready: 3, Total: 3, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "api-keys", Ready: 1, Total: 1, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "nvcf-api", Ready: 3, Total: 3, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "reval", Ready: 2, Total: 2, UptimeSec: 1209600, Healthy: true},
		ComponentHealth{Name: "gateway", Ready: 1, Total: 1, UptimeSec: 1209600, Healthy: true},
		// Compute-plane operator (no cluster context)
		ComponentHealth{Name: "nvca-operator", Ready: 1, Total: 1, UptimeSec: 604800, Healthy: true},
		// Compute-plane worker (cluster context)
		ComponentHealth{Name: "nvca-worker", Cluster: "ncp-local", Ready: 2, Total: 2, UptimeSec: 604800, Healthy: true},
		// Compute clusters
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
		// Recent events — ages match §6.5.3 mock: 02m41s=161s, 04m12s=252s, 12m38s=758s
		RecentEvent{AgeSec: 161, Kind: "function-deploy", Status: "ACTIVE", Name: "my-fn", Version: "3"},
		RecentEvent{AgeSec: 252, Kind: "cluster-registered", Name: "yotta-east-1"},
		RecentEvent{AgeSec: 758, Kind: "node-pressure", Name: "ncp-local-worker-0"},
	}

	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_status.golden", buf.String())
}

// TestRenderPlain_CheckStream feeds a pre-flight check sequence (matching §6.6.2)
// and asserts the golden output. The Final event carries verdict/totalChecks/
// passed/failed fields per §6.6.2 (M+8.11).
func TestRenderPlain_CheckStream(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T03:45:01Z"), // CheckStarted kubectl-on-path
		ts("2026-04-29T03:45:01Z"), // CheckCompleted kubectl-on-path
		ts("2026-04-29T03:45:01Z"), // CheckStarted helmfile-on-path
		ts("2026-04-29T03:45:01Z"), // CheckCompleted helmfile-on-path
		ts("2026-04-29T03:45:08Z"), // CheckStarted gateway-api (started before completed — same ts in mock)
		ts("2026-04-29T03:45:08Z"), // CheckCompleted gateway-api
		ts("2026-04-29T03:45:08Z"), // CategoryCompleted
		ts("2026-04-29T03:45:09Z"), // Final
	}}

	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)
	ctx := context.Background()

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

	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_check.golden", buf.String())
}

// TestRenderPlain_SplitContext feeds a split-cluster event sequence (M+9.5) and
// asserts that ctx=<value> is injected after the phase prefix for each event.
// Single-cluster events (no Context field) must produce byte-identical output
// to the pre-M+9 form so existing CI integrations aren't broken.
func TestRenderPlain_SplitContext(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T01:23:45Z"), // PhaseStarted   phase 1, ctx=admin@cp
		ts("2026-04-29T01:23:53Z"), // PhaseCompleted  phase 1, ctx=admin@cp
		ts("2026-04-29T01:30:08Z"), // PhaseStarted   phase 6, ctx=admin@gpu1
		ts("2026-04-29T01:30:12Z"), // PhaseCompleted  phase 6, ctx=admin@gpu1
	}}
	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)

	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight", StartedAt: ts("2026-04-29T01:23:45Z"), Context: "admin@cp"},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 8 * time.Second, Context: "admin@cp"},
		PhaseStarted{Num: 6, Name: "register", StartedAt: ts("2026-04-29T01:30:08Z"), Context: "admin@gpu1"},
		PhaseCompleted{Num: 6, Name: "register", Duration: 4 * time.Second, Context: "admin@gpu1"},
	}
	ctx := context.Background()
	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_split_install.golden", buf.String())
}

// TestRenderPlain_PhaseFailedRemediation exercises PhaseFailed with after_remediation
// retry class and a full Raw struct. The plain renderer ignores Raw (only JSONL surfaces it).
func TestRenderPlain_PhaseFailedRemediation(t *testing.T) {
	clock := &fakeClock{times: []time.Time{
		ts("2026-04-29T03:50:00Z"),
		ts("2026-04-29T03:50:00Z"),
		ts("2026-04-29T03:50:00Z"),
		ts("2026-04-29T03:50:00Z"),
		ts("2026-04-29T03:50:00Z"),
	}}

	var buf bytes.Buffer
	r := newPlainRendererForTest(&buf, clock.Now)
	ctx := context.Background()

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
				Subprocess: "helmfile",
				ExitCode:   1,
				StderrTail: "Error: timed out waiting for the condition\n",
			},
		},
	}

	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_phase_failed_remediation.golden", buf.String())
}

// TestRenderPlain_Down feeds a full 7-phase down sequence with DrainProgress
// and asserts the golden output. Phase denominator must be 7 throughout. (M+11.9)
func TestRenderPlain_Down(t *testing.T) {
	// One clock tick per line of output: enough pinned timestamps.
	const fixedTS = "2026-04-29T01:23:45Z"
	clock := &fakeClock{times: func() []time.Time {
		const n = 12
		ticks := make([]time.Time, n)
		for i := range ticks {
			ticks[i] = ts(fixedTS)
		}
		return ticks
	}()}

	var buf bytes.Buffer
	r := newPlainRendererWithTotalPhasesForTest(&buf, clock.Now, 7)
	ctx := context.Background()

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

	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_down.golden", buf.String())
}

// TestRenderPlain_Uninstall feeds a 3-phase uninstall sequence and asserts the
// golden output. Phase denominator must be 3 throughout. (M+11.9)
func TestRenderPlain_Uninstall(t *testing.T) {
	const fixedTS = "2026-04-29T01:23:45Z"
	clock := &fakeClock{times: func() []time.Time {
		const n = 8
		ticks := make([]time.Time, n)
		for i := range ticks {
			ticks[i] = ts(fixedTS)
		}
		return ticks
	}()}

	var buf bytes.Buffer
	r := newPlainRendererWithTotalPhasesForTest(&buf, clock.Now, 3)
	ctx := context.Background()

	events := []Event{
		PhaseStarted{Num: 1, Name: "preflight"},
		PhaseCompleted{Num: 1, Name: "preflight", Duration: 0},
		PhaseStarted{Num: 2, Name: "render-uninstall"},
		PhaseCompleted{Num: 2, Name: "render-uninstall", Duration: 0},
		PhaseStarted{Num: 3, Name: "helmfile-destroy"},
		PhaseCompleted{Num: 3, Name: "helmfile-destroy", Duration: 45 * time.Second},
		Final{Success: true, ClusterID: "test", Duration: 45 * time.Second, DroppedProgressEvents: 0},
	}

	for _, ev := range events {
		require.NoError(t, r.Emit(ctx, ev))
	}
	require.NoError(t, r.Close())
	assertGolden(t, "testdata/plain_uninstall.golden", buf.String())
}
