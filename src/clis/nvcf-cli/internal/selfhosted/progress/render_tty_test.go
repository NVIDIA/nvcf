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
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ansiRE matches CSI/OSC ANSI escape sequences so tests can verify the
// ASCII-only renderer emits zero of them.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// runningEvents constructs the canonical "phase 4 mid-running" event sequence
// reused by several tests so the goldens stay aligned.
func runningEvents() []tea.Msg {
	return []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, Name: "Preflight checks", StartedAt: ts("2026-04-29T01:23:43Z")}},
		eventMsg{PhaseCompleted{Num: 1, Name: "Preflight checks", Duration: 8 * time.Second}},
		eventMsg{PhaseStarted{Num: 2, Name: "Resolve stack bundle", StartedAt: ts("2026-04-29T01:23:51Z")}},
		eventMsg{PhaseCompleted{Num: 2, Name: "Resolve stack bundle", Duration: 3 * time.Second}},
		eventMsg{PhaseStarted{Num: 3, Name: "Render control plane", StartedAt: ts("2026-04-29T01:23:54Z")}},
		eventMsg{PhaseCompleted{Num: 3, Name: "Render control plane", Duration: 11 * time.Second}},
		eventMsg{PhaseStarted{Num: 4, Name: "Apply control plane", StartedAt: ts("2026-04-29T01:24:05Z")}},
		eventMsg{PhaseProgress{Num: 4, Resource: "Namespaces", Done: 8, Total: 8}},
		eventMsg{PhaseProgress{Num: 4, Resource: "CRDs", Done: 12, Total: 12}},
		eventMsg{PhaseProgress{Num: 4, Resource: "Deployments", Done: 9, Total: 14}},
		eventMsg{PhaseProgress{Num: 4, Resource: "StatefulSets", Done: 2, Total: 5}},
		eventMsg{PhaseProgress{Num: 4, Resource: "Jobs", Done: 4, Total: 5}},
		// LastProgress fires first; the subsequent Waiting represents the
		// "currently blocked on cassandra" state shown in §6.4.1's mockup.
		// Both lines coexist in the panel — Waiting is the active beat,
		// LastProgress is the most recent forward-progress signal.
		eventMsg{LastProgress{Num: 4, Detail: "nvcf-api deployment became available", At: ts("2026-04-29T01:31:08Z")}},
		eventMsg{Waiting{Num: 4, Reason: "cassandra-system/cassandra-0 PodInitializing"}},
	}
}

// pinnedClock returns a fakeClock whose only timestamp is "elapsed pinned at
// 07m42s after start" — used for deterministic View() output.
func pinnedClock() *fakeClock {
	return &fakeClock{times: []time.Time{
		ts("2026-04-29T01:31:25Z"),
	}}
}

// applyAll feeds a slice of bubbletea messages into the model in order and
// returns the final Model.
func applyAll(t *testing.T, m Model, msgs []tea.Msg) Model {
	t.Helper()
	var model tea.Model = m
	for _, msg := range msgs {
		model, _ = model.Update(msg)
	}
	out, ok := model.(Model)
	require.True(t, ok, "model is not progress.Model after Update chain")
	return out
}

func TestTTY_RunningFrame(t *testing.T) {
	clock := pinnedClock()
	m := NewModel(ModelOpts{
		Cluster: "my-cluster",
		Target:  "yotta-prod-us-east-1",
		Stack:   "v1.2.0@sha256:0180e2",
		NowFunc: clock.Now,
		Started: ts("2026-04-29T01:23:43Z"),
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:31:25Z") // pin the View()-time clock too

	final := applyAll(t, m, runningEvents())
	final.now = ts("2026-04-29T01:31:25Z")
	assertGolden(t, "testdata/tty_running_frame.golden", stripSpinner(final.View()))
}

func TestTTY_FinalSuccessFrame(t *testing.T) {
	clock := &fakeClock{times: []time.Time{ts("2026-04-29T01:35:23Z")}}
	m := NewModel(ModelOpts{
		Cluster: "my-cluster",
		Target:  "yotta-prod-us-east-1",
		Stack:   "v1.2.0@sha256:0180e2",
		NowFunc: clock.Now,
		Started: ts("2026-04-29T01:23:43Z"),
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:35:23Z")

	msgs := []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, StartedAt: ts("2026-04-29T01:23:43Z")}},
		eventMsg{PhaseCompleted{Num: 1, Duration: 8 * time.Second}},
		eventMsg{PhaseStarted{Num: 2, StartedAt: ts("2026-04-29T01:23:51Z")}},
		eventMsg{PhaseCompleted{Num: 2, Duration: 3 * time.Second}},
		eventMsg{PhaseStarted{Num: 3, StartedAt: ts("2026-04-29T01:23:54Z")}},
		eventMsg{PhaseCompleted{Num: 3, Duration: 11 * time.Second}},
		eventMsg{PhaseStarted{Num: 4, StartedAt: ts("2026-04-29T01:24:05Z")}},
		eventMsg{PhaseCompleted{Num: 4, Duration: 5*time.Minute + 18*time.Second}},
		eventMsg{PhaseStarted{Num: 5, StartedAt: ts("2026-04-29T01:29:23Z")}},
		eventMsg{PhaseCompleted{Num: 5, Duration: 22 * time.Second}},
		eventMsg{PhaseStarted{Num: 6, StartedAt: ts("2026-04-29T01:29:45Z")}},
		eventMsg{PhaseCompleted{Num: 6, Duration: 4 * time.Second}},
		eventMsg{PhaseStarted{Num: 7, StartedAt: ts("2026-04-29T01:29:49Z")}},
		eventMsg{PhaseCompleted{Num: 7, Duration: 4*time.Minute + 21*time.Second}},
		eventMsg{PhaseStarted{Num: 8, StartedAt: ts("2026-04-29T01:34:10Z")}},
		eventMsg{PhaseCompleted{Num: 8, Duration: 53 * time.Second}},
		eventMsg{Final{
			Success:           true,
			ClusterID:         "cc9f8a98-5853-4e3b-929c-9154d0ac0ecc",
			ClusterGroupID:    "cb0ca171-2363-4c3f-b03a-1fc7d5743dea",
			NVCFBackendHealth: "ncp-local",
			Duration:          11*time.Minute + 40*time.Second,
		}},
	}

	final := applyAll(t, m, msgs)
	final.now = ts("2026-04-29T01:35:23Z")
	assertGolden(t, "testdata/tty_final_success_frame.golden", stripSpinner(final.View()))
}

func TestTTY_FinalFailureFrame(t *testing.T) {
	clock := &fakeClock{times: []time.Time{ts("2026-04-29T01:31:25Z")}}
	m := NewModel(ModelOpts{
		Cluster: "my-cluster",
		Target:  "yotta-prod-us-east-1",
		Stack:   "v1.2.0@sha256:0180e2",
		NowFunc: clock.Now,
		Started: ts("2026-04-29T01:23:43Z"),
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:31:25Z")

	msgs := []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, StartedAt: ts("2026-04-29T01:23:43Z")}},
		eventMsg{PhaseCompleted{Num: 1, Duration: 8 * time.Second}},
		eventMsg{PhaseStarted{Num: 2, StartedAt: ts("2026-04-29T01:23:51Z")}},
		eventMsg{PhaseCompleted{Num: 2, Duration: 3 * time.Second}},
		eventMsg{PhaseStarted{Num: 3, StartedAt: ts("2026-04-29T01:23:54Z")}},
		eventMsg{PhaseCompleted{Num: 3, Duration: 11 * time.Second}},
		eventMsg{PhaseStarted{Num: 4, StartedAt: ts("2026-04-29T01:24:05Z")}},
		eventMsg{PhaseFailed{
			Num:           4,
			Name:          "Apply control plane",
			Duration:      7*time.Minute + 20*time.Second,
			ErrCategory:   "helm_apply",
			ErrMessage:    "timed out waiting for cassandra-system/cassandra-0 to become Ready",
			RetryClass:    "backoff",
			RetryAfterSec: 60,
			Remediation: []string{
				"kubectl describe pod -n cassandra-system cassandra-0",
				"Inspect prior PodInitializing events for image-pull or PVC issues",
				"Re-run `nvcf self-hosted up --cluster-name my-cluster` after 60s",
			},
		}},
	}

	final := applyAll(t, m, msgs)
	final.now = ts("2026-04-29T01:31:25Z")
	assertGolden(t, "testdata/tty_final_failure_frame.golden", stripSpinner(final.View()))
}

func TestTTY_CompactFrame(t *testing.T) {
	clock := pinnedClock()
	m := NewModel(ModelOpts{
		Cluster: "my-cluster",
		Target:  "yotta-prod-us-east-1",
		Stack:   "v1.2.0@sha256:0180e2",
		NowFunc: clock.Now,
		Started: ts("2026-04-29T01:23:43Z"),
	})
	m.SetSize(80, 24)
	m.now = ts("2026-04-29T01:31:25Z")

	final := applyAll(t, m, runningEvents())
	final.now = ts("2026-04-29T01:31:25Z")
	assertGolden(t, "testdata/tty_compact_frame.golden", stripSpinner(final.View()))
}

func TestTTY_AsciiFrame(t *testing.T) {
	clock := pinnedClock()
	m := NewModel(ModelOpts{
		Cluster:   "my-cluster",
		Target:    "yotta-prod-us-east-1",
		Stack:     "v1.2.0@sha256:0180e2",
		NowFunc:   clock.Now,
		Started:   ts("2026-04-29T01:23:43Z"),
		AsciiOnly: true,
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:31:25Z")

	final := applyAll(t, m, runningEvents())
	final.now = ts("2026-04-29T01:31:25Z")
	out := stripSpinner(final.View())

	// Hard invariant: zero ANSI escapes when asciiOnly is set. If this trips,
	// some style helper bypassed the per-Model lipgloss.Renderer.
	require.Empty(t, ansiRE.FindAllString(out, -1),
		"expected zero ANSI escape sequences in ASCII-only render, got: %q", ansiRE.FindAllString(out, -1))

	// Glyph markers MUST carry phase state on their own.
	require.Contains(t, out, glyphDone, "ASCII frame missing completed marker")
	require.Contains(t, out, glyphRun, "ASCII frame missing running marker")
	require.Contains(t, out, glyphPending, "ASCII frame missing pending marker")

	assertGolden(t, "testdata/tty_ascii_frame.golden", out)
}

func TestTTY_QuitOnKeypress(t *testing.T) {
	m := NewModel(ModelOpts{NowFunc: func() time.Time { return ts("2026-04-29T00:00:00Z") }})
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
	} {
		_, cmd := m.Update(k)
		require.NotNil(t, cmd, "expected non-nil cmd for key %v", k)
		assert.IsType(t, tea.QuitMsg{}, cmd(), "key %v should produce tea.QuitMsg", k)
	}
}

func TestTTY_GlyphMarkersStripAnsi(t *testing.T) {
	clock := pinnedClock()
	m := NewModel(ModelOpts{
		Cluster: "my-cluster",
		Target:  "yotta-prod-us-east-1",
		Stack:   "v1.2.0@sha256:0180e2",
		NowFunc: clock.Now,
		Started: ts("2026-04-29T01:23:43Z"),
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:31:25Z")

	final := applyAll(t, m, runningEvents())
	final.now = ts("2026-04-29T01:31:25Z")
	stripped := ansiRE.ReplaceAllString(final.View(), "")

	// Phases 1-3 completed → [✓] expected.
	assert.Contains(t, stripped, glyphDone, "completed marker missing after ANSI strip")
	// Phase 4 running → [▶] expected.
	assert.Contains(t, stripped, glyphRun, "running marker missing after ANSI strip")
	// Phases 5-8 pending → [ ] expected.
	assert.Contains(t, stripped, glyphPending, "pending marker missing after ANSI strip")
}

func TestTTY_FailureGlyphAfterStripAnsi(t *testing.T) {
	clock := &fakeClock{times: []time.Time{ts("2026-04-29T01:31:25Z")}}
	m := NewModel(ModelOpts{
		Cluster: "my-cluster",
		Target:  "yotta-prod-us-east-1",
		Stack:   "v1.2.0@sha256:0180e2",
		NowFunc: clock.Now,
		Started: ts("2026-04-29T01:23:43Z"),
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:31:25Z")

	final := applyAll(t, m, []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, StartedAt: ts("2026-04-29T01:23:43Z")}},
		eventMsg{PhaseFailed{Num: 1, ErrCategory: "auth", ErrMessage: "boom", RetryClass: "none"}},
	})
	final.now = ts("2026-04-29T01:31:25Z")
	stripped := ansiRE.ReplaceAllString(final.View(), "")
	assert.Contains(t, stripped, glyphFailed, "failed marker missing after ANSI strip")
}

// TestTTY_ColorToggleProvable proves the color seam works end-to-end: with a
// non-Ascii lipgloss profile forced, AsciiOnly=false MUST emit ANSI escapes
// and AsciiOnly=true MUST strip them. Without this, the prior
// TestTTY_AsciiFrame passed regardless of bug state because `go test` stdout
// is non-TTY and lipgloss auto-detected to Ascii anyway.
//
// Mechanism: ModelOpts.ForceColorProfile overrides the renderer's auto-
// detected profile so the test doesn't depend on the host terminal's
// capabilities (CI VMs are non-TTY; dev laptops are TrueColor). AsciiOnly
// still wins inside NewModel so we can prove the toggle.
func TestTTY_ColorToggleProvable(t *testing.T) {
	trueColor := termenv.TrueColor

	mkModel := func(asciiOnly bool) Model {
		clock := pinnedClock()
		m := NewModel(ModelOpts{
			Cluster:           "my-cluster",
			Target:            "yotta-prod-us-east-1",
			Stack:             "v1.2.0@sha256:0180e2",
			NowFunc:           clock.Now,
			Started:           ts("2026-04-29T01:23:43Z"),
			AsciiOnly:         asciiOnly,
			ForceColorProfile: &trueColor,
		})
		m.SetSize(130, 36)
		m.now = ts("2026-04-29T01:31:25Z")
		final := applyAll(t, m, runningEvents())
		final.now = ts("2026-04-29T01:31:25Z")
		return final
	}

	colorOn := mkModel(false)
	colorOff := mkModel(true)

	onOut := stripSpinner(colorOn.View())
	offOut := stripSpinner(colorOff.View())

	require.Regexp(t, ansiRE, onOut,
		"AsciiOnly=false with TrueColor profile MUST emit ANSI escapes; "+
			"if this fails, the lipgloss.Renderer is being constructed against "+
			"a writer that auto-detects to Ascii (the original bug) or the "+
			"ForceColorProfile seam was bypassed.")

	require.NotRegexp(t, ansiRE, offOut,
		"AsciiOnly=true MUST strip ANSI escapes even when the underlying "+
			"profile would emit color; if this fails, some style helper "+
			"bypassed the per-Model lipgloss.Renderer or a sub-progress bar "+
			"is reading from the global termenv (the second bug).")

	// And the structural glyphs must survive both branches — color is
	// decoration, not the source of truth for phase state.
	for _, out := range []string{onOut, offOut} {
		stripped := ansiRE.ReplaceAllString(out, "")
		assert.Contains(t, stripped, glyphDone)
		assert.Contains(t, stripped, glyphRun)
		assert.Contains(t, stripped, glyphPending)
	}
}

// stripSpinner replaces the bubbletea spinner glyph with a placeholder so
// goldens are deterministic across invocations. The spinner.Dot frame cycles
// through ⣾⣽⣻⢿⡿⣟⣯⣷; we collapse them all to a single literal.
//
// The replacement string is "·" (middle dot) so the visible width is preserved
// and the surrounding lipgloss padding doesn't drift.
func stripSpinner(s string) string {
	for _, frame := range []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"} {
		s = strings.ReplaceAll(s, frame, "·")
	}
	return s
}

// ─── ModeStatus tests ─────────────────────────────────────────────────────────

// healthyStatusEvents returns the canonical status-healthy event sequence used
// by TestTTY_StatusHealthy. Mirrors §6.5.1 exactly.
func healthyStatusEvents() []Event {
	return []Event{
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
		ComponentHealth{Name: "sis", Ready: 2, Total: 2, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "nats", Ready: 3, Total: 3, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "cassandra", Ready: 1, Total: 1, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "openbao", Ready: 3, Total: 3, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "api-keys", Ready: 1, Total: 1, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "nvcf-api", Ready: 3, Total: 3, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "reval", Ready: 2, Total: 2, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "gateway", Ready: 1, Total: 1, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "nvca-operator", Ready: 1, Total: 1, UptimeSec: 7 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "nvca-worker", Cluster: "ncp-local", Ready: 2, Total: 2, UptimeSec: 7 * 24 * 3600, Healthy: true},
		ClusterRow{Name: "ncp-local", GPU: "H100", GPUCount: 16, ActiveDeployments: 14, LastSeenAgeSec: 12, Healthy: true},
		ClusterRow{Name: "yotta-east-1", GPU: "H100", GPUCount: 32, ActiveDeployments: 0, LastSeenAgeSec: 41, Healthy: true},
		RecentEvent{AgeSec: 161, Kind: "function-deploy", Status: "ACTIVE", Name: "echo-test", Version: "v1"},
		RecentEvent{AgeSec: 252, Kind: "cluster-registered", Name: "yotta-east-1"},
		RecentEvent{AgeSec: 758, Kind: "function-deploy", Status: "DELETED", Name: "echo-test", Version: "v1"},
	}
}

func TestTTY_StatusHealthy(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel(ModelOpts{
		Mode:      ModeStatus,
		Cluster:   "my-cluster",
		AsciiOnly: true,
		NowFunc:   func() time.Time { return ts("2026-04-29T03:45:12Z") },
	})
	m.SetSize(120, 40)

	var model tea.Model = m
	for _, e := range healthyStatusEvents() {
		model, _ = model.(Model).applyStatusEvent(e)
	}
	final := model.(Model)
	assertGolden(t, "testdata/tty_status_healthy.golden", final.View())
}

func TestTTY_StatusDegraded(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel(ModelOpts{
		Mode:      ModeStatus,
		Cluster:   "my-cluster",
		AsciiOnly: true,
		NowFunc:   func() time.Time { return ts("2026-04-29T03:45:12Z") },
	})
	m.SetSize(120, 40)

	events := []Event{
		Snapshot{
			Cluster:         "my-cluster",
			Verdict:         "degraded",
			ReconcileAgeSec: 5,
			Identity: SnapshotIdentity{
				ClusterID:    "cc9f8a98-5853-4e3b-929c-9154d0ac0ecc",
				Target:       "yotta-prod-us-east-1",
				StackVersion: "v1.2.0",
				StackDigest:  "sha256:0180e2",
				InstalledAt:  ts("2026-04-25T14:02:18Z"),
			},
		},
		ComponentHealth{Name: "sis", Ready: 2, Total: 2, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "nats", Ready: 3, Total: 3, UptimeSec: 14 * 24 * 3600, Healthy: true},
		ComponentHealth{Name: "cassandra", Ready: 0, Total: 1, UptimeSec: 0, Healthy: false, Message: "cassandra-0 PodInitializing 6m23s"},
		ComponentHealth{Name: "openbao", Ready: 3, Total: 3, UptimeSec: 14 * 24 * 3600, Healthy: true},
		RecentEvent{AgeSec: 383, Kind: "readiness-lost", Name: "cassandra-0", Status: "PodInitializing"},
	}

	var model tea.Model = m
	for _, e := range events {
		model, _ = model.(Model).applyStatusEvent(e)
	}
	final := model.(Model)
	assertGolden(t, "testdata/tty_status_degraded.golden", final.View())
}

// ─── ModeCheck tests ──────────────────────────────────────────────────────────

func TestTTY_CheckInflight(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel(ModelOpts{
		Mode:        ModeCheck,
		AsciiOnly:   true,
		TotalChecks: 14,
		NowFunc:     func() time.Time { return ts("2026-04-29T03:45:12Z") },
	})
	m.SetSize(120, 40)

	// local-host-tools: 3 passed (kubectl, helmfile, helm); each with detail.
	// kubernetes-api: kubectl-init passed, querying-api-version started but not finished.
	// pre-kubernetes-setup: category started but no checks yet.
	events := []Event{
		CheckStarted{Category: "local-host-tools", ID: "kubectl-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "kubectl-on-path", Passed: true, Severity: "info", Message: "kubectl 1.30.2 on PATH", Detail: "≥1.28"},
		CheckStarted{Category: "local-host-tools", ID: "helmfile-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "helmfile-on-path", Passed: true, Severity: "info", Message: "helmfile 1.1.9 on PATH", Detail: "≥1.0"},
		CheckStarted{Category: "local-host-tools", ID: "helm-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "helm-on-path", Passed: true, Severity: "info", Message: "helm 3.15.4 on PATH", Detail: "≥3.14"},
		CheckStarted{Category: "kubernetes-api", ID: "kubectl-init"},
		CheckCompleted{Category: "kubernetes-api", ID: "kubectl-init", Passed: true, Severity: "info", Message: "can initialize the client"},
		CheckStarted{Category: "kubernetes-api", ID: "querying-api-version", Message: "querying API version…"},
		// querying-api-version: started but NOT completed (in flight)
		CheckStarted{Category: "pre-kubernetes-setup", ID: "create-non-namespaced"},
		// create-non-namespaced: started but not completed either (pending-ish)
	}

	var model tea.Model = m
	for _, e := range events {
		model, _ = model.(Model).applyCheckEvent(e)
	}
	final := model.(Model)
	assertGolden(t, "testdata/tty_check_inflight.golden", final.View())
}

func TestTTY_CheckComplete(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel(ModelOpts{
		Mode:        ModeCheck,
		AsciiOnly:   true,
		TotalChecks: 14,
		NowFunc:     func() time.Time { return ts("2026-04-29T03:45:12Z") },
	})
	m.SetSize(120, 40)

	// 14 checks: 13 passed, 1 failed (gateway-api in pre-kubernetes-setup).
	events := []Event{
		CheckStarted{Category: "local-host-tools", ID: "kubectl-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "kubectl-on-path", Passed: true, Severity: "info", Message: "kubectl 1.30.2 on PATH", Detail: "1.30.2"},
		CheckStarted{Category: "local-host-tools", ID: "helmfile-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "helmfile-on-path", Passed: true, Severity: "info", Message: "helmfile 1.1.9 on PATH", Detail: "1.1.9"},
		CheckStarted{Category: "local-host-tools", ID: "helm-on-path"},
		CheckCompleted{Category: "local-host-tools", ID: "helm-on-path", Passed: true, Severity: "info", Message: "helm 3.15.4 on PATH", Detail: "3.15.4"},
		CategoryCompleted{Category: "local-host-tools", PassedCount: 3, FailedCount: 0, DurationSec: 0.1},
		CheckStarted{Category: "kubernetes-api", ID: "kubectl-init"},
		CheckCompleted{Category: "kubernetes-api", ID: "kubectl-init", Passed: true, Severity: "info", Message: "can initialize the client"},
		CheckStarted{Category: "kubernetes-api", ID: "querying-api-version"},
		CheckCompleted{Category: "kubernetes-api", ID: "querying-api-version", Passed: true, Severity: "info", Message: "API server version: 1.30.2"},
		CategoryCompleted{Category: "kubernetes-api", PassedCount: 2, FailedCount: 0, DurationSec: 0.5},
		CheckStarted{Category: "pre-kubernetes-setup", ID: "gateway-api"},
		CheckCompleted{Category: "pre-kubernetes-setup", ID: "gateway-api", Passed: false, Severity: "error", Message: "Gateway API CRDs not installed", HintURL: "https://docs.nvidia.com/nvcf/self-hosted/gateway-api"},
		CategoryCompleted{Category: "pre-kubernetes-setup", PassedCount: 0, FailedCount: 1, DurationSec: 2.4},
		Final{Success: false},
	}

	var model tea.Model = m
	for _, e := range events {
		model, _ = model.(Model).applyCheckEvent(e)
	}
	final := model.(Model)
	assertGolden(t, "testdata/tty_check_complete.golden", final.View())
}

func TestCheckOneShotRenderer_FlushesFailedSummaryOnFinal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	r := NewCheckOneShotRenderer(&buf, ModelOpts{
		Mode:      ModeCheck,
		Output:    &buf,
		AsciiOnly: true,
	})

	ctx := context.Background()
	require.NoError(t, r.Emit(ctx, CheckStarted{Category: "local-host-tools", ID: "kubectl-on-path"}))
	require.NoError(t, r.Emit(ctx, CheckCompleted{
		Category: "local-host-tools",
		ID:       "kubectl-on-path",
		Passed:   true,
		Severity: "info",
		Message:  "kubectl 1.30.2 on PATH",
		Detail:   "1.30.2",
	}))
	require.NoError(t, r.Emit(ctx, CategoryCompleted{Category: "local-host-tools", PassedCount: 1, FailedCount: 0}))
	require.NoError(t, r.Emit(ctx, CheckStarted{Category: "pre-kubernetes-setup", ID: "gateway-api"}))
	require.NoError(t, r.Emit(ctx, CheckCompleted{
		Category: "pre-kubernetes-setup",
		ID:       "gateway-api",
		Passed:   false,
		Severity: "error",
		Message:  "Gateway API CRDs not installed",
	}))
	require.NoError(t, r.Emit(ctx, CategoryCompleted{Category: "pre-kubernetes-setup", PassedCount: 0, FailedCount: 1}))
	require.NoError(t, r.Emit(ctx, Final{Success: false, Verdict: "failed", TotalChecks: 2, PassedCount: 1, FailedCount: 1}))

	out := buf.String()
	assert.Contains(t, out, "Pre-flight checks for NVCF self-hosted install")
	assert.Contains(t, out, "[✓] kubectl 1.30.2 on PATH (1.30.2)")
	assert.Contains(t, out, "[✘] Gateway API CRDs not installed")
	assert.Contains(t, out, "Status: ✘ failed  (1/2 passed, 1 failed)")
}

func TestCheckOneShotRenderer_UsesFinalTallyForStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	r := NewCheckOneShotRenderer(&buf, ModelOpts{
		Mode:      ModeCheck,
		Output:    &buf,
		AsciiOnly: true,
	})

	ctx := context.Background()
	require.NoError(t, r.Emit(ctx, CheckStarted{Category: "local-host-tools", ID: "kubectl-on-path"}))
	require.NoError(t, r.Emit(ctx, CheckCompleted{
		Category: "local-host-tools",
		ID:       "kubectl-on-path",
		Passed:   true,
		Severity: "info",
		Message:  "kubectl 1.30.2 on PATH",
	}))
	require.NoError(t, r.Emit(ctx, CategoryCompleted{Category: "local-host-tools", PassedCount: 1, FailedCount: 0}))
	require.NoError(t, r.Emit(ctx, Final{Success: false, Verdict: "failed", TotalChecks: 2, PassedCount: 1, FailedCount: 1}))

	assert.Contains(t, buf.String(), "Status: ✘ failed  (1/2 passed, 1 failed)")
}

func TestCheckOneShotRenderer_SerializesConcurrentCheckEvents(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	r := NewCheckOneShotRenderer(&buf, ModelOpts{
		Mode:      ModeCheck,
		Output:    &buf,
		AsciiOnly: true,
	})

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("cp-%d", i)
			require.NoError(t, r.Emit(ctx, CheckStarted{Category: "control-plane-cluster", ID: id}))
			require.NoError(t, r.Emit(ctx, CheckCompleted{
				Category: "control-plane-cluster",
				ID:       id,
				Passed:   true,
				Severity: "info",
				Message:  id + " passed",
			}))
		}(i)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("gpu-%d", i)
			require.NoError(t, r.Emit(ctx, CheckStarted{Category: "compute-plane-cluster", ID: id}))
			require.NoError(t, r.Emit(ctx, CheckCompleted{
				Category: "compute-plane-cluster",
				ID:       id,
				Passed:   true,
				Severity: "info",
				Message:  id + " passed",
			}))
		}(i)
	}
	wg.Wait()
	require.NoError(t, r.Emit(ctx, CategoryCompleted{Category: "control-plane-cluster", PassedCount: 100, FailedCount: 0}))
	require.NoError(t, r.Emit(ctx, CategoryCompleted{Category: "compute-plane-cluster", PassedCount: 100, FailedCount: 0}))
	require.NoError(t, r.Emit(ctx, Final{Success: true, Verdict: "ok", TotalChecks: 200, PassedCount: 200, FailedCount: 0}))

	out := buf.String()
	assert.Contains(t, out, "control-plane-cluster")
	assert.Contains(t, out, "compute-plane-cluster")
	assert.Contains(t, out, "Status: ✓ ok  (200/200 passed, 0 failed)")
}

// ─── M+9 split-cluster TTY tests ─────────────────────────────────────────────

// TestTTY_DownRunning verifies the ModeDown header label ("teardown" not
// "install") and checklist shows only 7 phases with correct names. (M+11.9)
func TestTTY_DownRunning(t *testing.T) {
	m := NewModel(ModelOpts{
		Mode:        ModeDown,
		Cluster:     "ncp-local",
		Target:      "https://nvcf.nvidia.com",
		Stack:       "v1.14.2@sha256:deadbeef",
		TotalPhases: 7,
		AsciiOnly:   true,
		Started:     ts("2026-04-29T01:23:43Z"),
		NowFunc:     func() time.Time { return ts("2026-04-29T01:24:15Z") },
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:24:15Z")

	msgs := []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, Name: "Pre-flight checks", StartedAt: ts("2026-04-29T01:23:43Z")}},
		eventMsg{PhaseCompleted{Num: 1, Name: "Pre-flight checks", Duration: 2 * time.Second}},
		eventMsg{PhaseStarted{Num: 2, Name: "Drain active deployments", StartedAt: ts("2026-04-29T01:23:45Z")}},
		eventMsg{DrainProgress{Num: 2, Deployment: "function-deploy-1", State: "REMOVING"}},
	}
	final := applyAll(t, m, msgs)
	final.now = ts("2026-04-29T01:24:15Z")
	out := stripSpinner(final.View())

	// Hard invariant: header must say "teardown" not "self-hosted install".
	require.Contains(t, out, "teardown", "ModeDown header must say teardown")
	require.NotContains(t, out, "self-hosted install", "ModeDown header must not say self-hosted install")

	assertGolden(t, "testdata/tty_down_running.golden", out)
}

// TestTTY_DownComplete verifies the ModeDown final success rendering. (M+11.9)
func TestTTY_DownComplete(t *testing.T) {
	m := NewModel(ModelOpts{
		Mode:        ModeDown,
		Cluster:     "ncp-local",
		Target:      "https://nvcf.nvidia.com",
		Stack:       "v1.14.2@sha256:deadbeef",
		TotalPhases: 7,
		AsciiOnly:   true,
		Started:     ts("2026-04-29T01:23:43Z"),
		NowFunc:     func() time.Time { return ts("2026-04-29T01:24:52Z") },
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:24:52Z")

	msgs := []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, Name: "Pre-flight checks", StartedAt: ts("2026-04-29T01:23:43Z")}},
		eventMsg{PhaseCompleted{Num: 1, Name: "Pre-flight checks", Duration: 2 * time.Second}},
		eventMsg{PhaseStarted{Num: 2, Name: "Drain active deployments", StartedAt: ts("2026-04-29T01:23:45Z")}},
		eventMsg{PhaseCompleted{Num: 2, Name: "Drain active deployments", Duration: 5 * time.Second}},
		eventMsg{PhaseStarted{Num: 3, Name: "Uninstall compute plane", StartedAt: ts("2026-04-29T01:23:50Z")}},
		eventMsg{PhaseCompleted{Num: 3, Name: "Uninstall compute plane", Duration: 45 * time.Second}},
		eventMsg{Final{Success: true, Duration: 52 * time.Second}},
	}
	final := applyAll(t, m, msgs)
	final.now = ts("2026-04-29T01:24:52Z")
	out := stripSpinner(final.View())

	require.Contains(t, out, "teardown", "ModeDown header must say teardown")
	require.Contains(t, out, "Teardown complete", "ModeDown success must say Teardown complete")

	assertGolden(t, "testdata/tty_down_complete.golden", out)
}

// TestTTY_UninstallRunning verifies ModeDown with TotalPhases=3 (uninstall)
// shows only 3 phases in the checklist. (M+11.9)
func TestTTY_UninstallRunning(t *testing.T) {
	m := NewModel(ModelOpts{
		Mode:        ModeDown,
		Cluster:     "ncp-local",
		Target:      "https://nvcf.nvidia.com",
		Stack:       "v1.14.2@sha256:deadbeef",
		TotalPhases: 3,
		AsciiOnly:   true,
		Started:     ts("2026-04-29T01:23:43Z"),
		NowFunc:     func() time.Time { return ts("2026-04-29T01:23:50Z") },
	})
	m.SetSize(130, 36)
	m.now = ts("2026-04-29T01:23:50Z")

	msgs := []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, Name: "Pre-flight checks", StartedAt: ts("2026-04-29T01:23:43Z")}},
		eventMsg{PhaseCompleted{Num: 1, Name: "Pre-flight checks", Duration: 2 * time.Second}},
		eventMsg{PhaseStarted{Num: 2, Name: "Render uninstall manifests", StartedAt: ts("2026-04-29T01:23:45Z")}},
		eventMsg{PhaseCompleted{Num: 2, Name: "Render uninstall manifests", Duration: 1 * time.Second}},
		eventMsg{PhaseStarted{Num: 3, Name: "Helmfile destroy", StartedAt: ts("2026-04-29T01:23:46Z")}},
	}
	final := applyAll(t, m, msgs)
	final.now = ts("2026-04-29T01:23:50Z")
	out := stripSpinner(final.View())

	require.Contains(t, out, "teardown", "ModeDown uninstall header must say teardown")
	// Verify exactly 3 phase rows (no phase 4-8 leaking through).
	require.Contains(t, out, "1. Pre-flight checks", "phase 1 missing")
	require.Contains(t, out, "2. Render uninstall manifests", "phase 2 missing")
	require.Contains(t, out, "3. Helmfile destroy", "phase 3 missing")

	assertGolden(t, "testdata/tty_uninstall_running.golden", out)
}

// TestTTY_SplitInstallHeader verifies the split-cluster header (M+9.5, §6.4.1).
// When ControlPlaneContext and ComputePlaneContext are both set and differ, the
// header gains "Control:" and "Compute:" lines, and per-phase rows with a
// non-empty ctx field show a "→ <ctx>" annotation.
func TestTTY_SplitInstallHeader(t *testing.T) {
	m := NewModel(ModelOpts{
		Mode:                ModeInstall,
		Cluster:             "ncp-local",
		Stack:               "v1.2.0@sha256:0180e2",
		ControlPlaneContext: "admin@cp",
		ComputePlaneContext: "admin@gpu1",
		AsciiOnly:           true,
		Started:             ts("2026-04-29T14:02:18Z"),
		NowFunc:             func() time.Time { return ts("2026-04-29T14:02:30Z") },
	})
	m.SetSize(120, 40)
	m.now = ts("2026-04-29T14:02:30Z")

	// Feed a few phase events with Context so the checklist rows show → <ctx>.
	msgs := []tea.Msg{
		eventMsg{PhaseStarted{Num: 1, Name: "Preflight checks", StartedAt: ts("2026-04-29T14:02:18Z"), Context: "admin@cp"}},
		eventMsg{PhaseCompleted{Num: 1, Name: "Preflight checks", Duration: 8 * time.Second, Context: "admin@cp"}},
		eventMsg{PhaseStarted{Num: 2, Name: "Resolve stack bundle", StartedAt: ts("2026-04-29T14:02:26Z"), Context: "admin@cp"}},
		eventMsg{PhaseCompleted{Num: 2, Name: "Resolve stack bundle", Duration: 3 * time.Second, Context: "admin@cp"}},
		eventMsg{PhaseStarted{Num: 3, Name: "Render control plane", StartedAt: ts("2026-04-29T14:02:29Z"), Context: "admin@cp"}},
	}
	final := applyAll(t, m, msgs)
	final.now = ts("2026-04-29T14:02:30Z")
	assertGolden(t, "testdata/tty_split_install.golden", stripSpinner(final.View()))
}

// TestTTY_RecentLogPanel asserts the bubbletea Model accumulates LogLine
// events in its ring buffer and renders them as a "Recent" panel beneath
// "Next:". The panel:
//   - is hidden when no LogLine events have arrived (empty logTail)
//   - shows oldest-first ordering up to recentLogCapacity entries
//   - drops bare/whitespace-only lines so subprocess stray newlines don't
//     push real activity out of the ring
//   - rolls over FIFO when the ring is full (newest entry replaces oldest)
func TestTTY_RecentLogPanel(t *testing.T) {
	clock := pinnedClock()
	m := NewModel(ModelOpts{
		Cluster:   "ncp-local",
		NowFunc:   clock.Now,
		Started:   ts("2026-04-29T01:23:43Z"),
		AsciiOnly: true,
	})
	m.SetSize(120, 40)

	// No LogLine events yet → renderRecentPanel returns "" and View has
	// no "Recent:" section.
	require.Empty(t, m.renderRecentPanel(),
		"recent panel must be hidden when no log lines have arrived")

	// Apply a few LogLine events.
	updateModel(&m, LogLine{Stream: "stdout", Source: "helmfile", Line: "Pulling cassandra:0.12.0"})
	updateModel(&m, LogLine{Stream: "stdout", Source: "helmfile", Line: "Comparing release=cassandra"})
	updateModel(&m, LogLine{Stream: "stdout", Source: "helmfile", Line: ""})       // dropped: blank line
	updateModel(&m, LogLine{Stream: "stdout", Source: "helmfile", Line: "  \t  "}) // dropped: whitespace
	updateModel(&m, LogLine{Stream: "stdout", Source: "helmfile", Line: "Upgrading release=nats"})

	panel := m.renderRecentPanel()
	require.NotEmpty(t, panel, "recent panel must render after LogLine events")
	assert.Contains(t, panel, "Recent:")
	assert.Contains(t, panel, "Pulling cassandra:0.12.0")
	assert.Contains(t, panel, "Comparing release=cassandra")
	assert.Contains(t, panel, "Upgrading release=nats")

	// Roll-over: push capacity+3 more; the oldest 3 (Pulling cassandra,
	// Comparing release=cassandra, Upgrading release=nats) should fall
	// off, leaving the most-recent recentLogCapacity entries.
	for i := 0; i < recentLogCapacity+3; i++ {
		updateModel(&m, LogLine{Source: "helmfile", Line: fmt.Sprintf("rolled-line-%d", i)})
	}
	panel = m.renderRecentPanel()
	assert.NotContains(t, panel, "Pulling cassandra:0.12.0",
		"oldest entries must roll off when ring fills")
	assert.NotContains(t, panel, "Comparing release=cassandra")
	assert.Contains(t, panel, fmt.Sprintf("rolled-line-%d", recentLogCapacity+2),
		"newest entry must be present after roll-over")
}

// updateModel applies one event to a Model in place. Helper for tests that
// don't care about the tea.Cmd return value from Update.
func updateModel(m *Model, e Event) {
	next, _ := m.Update(eventMsg{e: e})
	*m = next.(Model)
}

// TestTTY_StatusWatchTickResetsBetweenSnapshots locks in the fix for the
// --watch-mode bug where each tick re-emitted Snapshot+ComponentHealth
// events but the Model appended instead of replacing — causing the
// component table to grow unboundedly across watch ticks (after 5 ticks
// the user saw 9 × 5 = 45 component rows; over an hour at 5s intervals
// that grew to ~6500 rows). Each new Snapshot now resets the components/
// clusters/recentEvents slices before the next tick's events arrive.
func TestTTY_StatusWatchTickResetsBetweenSnapshots(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel(ModelOpts{
		Mode:      ModeStatus,
		Cluster:   "ncp-local",
		AsciiOnly: true,
		NowFunc:   func() time.Time { return ts("2026-04-29T03:45:12Z") },
	})
	m.SetSize(120, 40)

	tickEvents := []tea.Msg{
		eventMsg{Snapshot{Cluster: "ncp-local", Verdict: "healthy"}},
		eventMsg{ComponentHealth{Name: "SIS", Ready: 1, Total: 1, Healthy: true}},
		eventMsg{ComponentHealth{Name: "NATS", Ready: 3, Total: 3, Healthy: true}},
		eventMsg{ClusterRow{Name: "ncp-local", Healthy: true, IsCurrent: true}},
	}

	// Apply 5 ticks back-to-back, mimicking a 5-iteration --watch session.
	final := applyAll(t, m, tickEvents)
	for i := 0; i < 4; i++ {
		final = applyAll(t, final, tickEvents)
	}

	// After 5 ticks, the buffers should hold exactly one tick's worth of
	// data — not 5×.
	assert.Len(t, final.components, 2,
		"each Snapshot must reset components; after 5 ticks expect 2 rows, not %d", len(final.components))
	assert.Len(t, final.clusters, 1,
		"each Snapshot must reset clusters; after 5 ticks expect 1 row, not %d", len(final.clusters))

	// The rendered View should also have just two component rows and one
	// cluster row, not the multiplied accumulation that prompted the fix.
	view := final.View()
	sisCount := strings.Count(view, "SIS")
	assert.Equal(t, 1, sisCount,
		"rendered View should show SIS exactly once after 5 watch ticks, not %d times", sisCount)
}
