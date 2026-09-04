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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted/kubectx"
	"nvcf-cli/internal/selfhosted/progress"
)

// TestCheck_LegacyOutputJSONWarnsAndStreams verifies that passing the deprecated
// --output=json flag:
//   - prints a deprecation warning to stderr, AND
//   - falls through to JSONL streaming behaviour on stderr (same as --json).
func TestCheck_LegacyOutputJSONWarnsAndStreams(t *testing.T) {
	// Reset global flag state left over from other tests.
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{}) // discard stdout

	t.Setenv("NVCF_CLI_SELFHOSTED_LOCAL_ONLY", "1")
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--output", "json"})
	_ = rootCmd.Execute()

	got := stderr.String()
	// The deprecation warning must appear on stderr.
	assert.Contains(t, got, "deprecated; use --json", "expected deprecation warning on stderr")
	// The JSONL stream must also appear on stderr: first line is always schemaVersion.
	assert.Contains(t, got, `"event":"schemaVersion"`, "expected JSONL schemaVersion line on stderr")
}

// TestCheck_NewJSON verifies that --json emits a valid JSONL stream that matches
// the §6.6.3 schema: schemaVersion header, then check_started/check_completed/
// category_completed events per check, then a final event with verdict fields.
func TestCheck_NewJSON(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	t.Setenv("NVCF_CLI_SELFHOSTED_LOCAL_ONLY", "1")
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json"})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	require.NotEmpty(t, lines, "expected at least one JSONL line")

	// First line must be the schemaVersion header.
	assert.Equal(t, "schemaVersion", lines[0]["event"])
	assert.Equal(t, float64(1), lines[0]["version"])

	// Collect event kinds in order.
	var kinds []string
	for _, l := range lines[1:] {
		kinds = append(kinds, l["event"].(string))
	}

	// Must have at least check_started + check_completed events for each of the
	// 3 default tools, plus at least one category_completed and a final.
	assert.Contains(t, kinds, "check_started")
	assert.Contains(t, kinds, "check_completed")
	assert.Contains(t, kinds, "category_completed")
	assert.Contains(t, kinds, "final")

	// The final event must carry verdict fields.
	var finalLine map[string]any
	for _, l := range lines[1:] {
		if l["event"] == "final" {
			finalLine = l
			break
		}
	}
	require.NotNil(t, finalLine, "expected a final event")
	assert.Contains(t, finalLine, "verdict", "final event must carry verdict field")
	assert.Contains(t, finalLine, "totalChecks", "final event must carry totalChecks field")
	assert.Contains(t, finalLine, "passedCount", "final event must carry passedCount field")
}

// TestCheck_WaitTimesOutCleanly verifies that --wait honors the timeout duration.
func TestSelfHostedCheck_WaitTimesOutCleanly(t *testing.T) {
	t.Setenv("NVCF_CLI_SELFHOSTED_LOCAL_ONLY", "1")
	t.Setenv("NVCF_CLI_SELFHOSTED_FORCE_FAIL", "1") // seam: forces a failing check
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--wait", "2s"})
	start := time.Now()
	err := rootCmd.Execute()
	elapsed := time.Since(start)
	assert.Error(t, err)
	assert.True(t, elapsed >= 2*time.Second && elapsed < 5*time.Second,
		"wait should have honored 2s timeout, got %s", elapsed)
}

func TestCheck_OneShotTTYUsesStaticRenderer(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedPlain = false
		selfHostedAccessible = false
		selfHostedWait = ""
		checkWriterIsTTY = isWriterTTY
	})

	checkWriterIsTTY = func(io.Writer) bool { return true }
	var stderr bytes.Buffer

	sink, err := selectCheckRenderer(&stderr, false)
	require.NoError(t, err)
	require.IsType(t, &progress.CheckOneShotRenderer{}, sink)

	ctx := context.Background()
	require.NoError(t, sink.Emit(ctx, progress.CheckStarted{Category: "test", ID: "forced"}))
	require.NoError(t, sink.Emit(ctx, progress.CheckCompleted{
		Category: "test",
		ID:       "forced",
		Passed:   false,
		Severity: "error",
		Message:  "forced failure",
	}))
	require.NoError(t, sink.Emit(ctx, progress.CategoryCompleted{Category: "test", PassedCount: 0, FailedCount: 1}))
	require.NoError(t, sink.Emit(ctx, progress.Final{Success: false, Verdict: "failed", TotalChecks: 1, PassedCount: 0, FailedCount: 1}))

	out := stderr.String()
	assert.Contains(t, out, "Pre-flight checks for NVCF self-hosted install")
	assert.Contains(t, out, "[✘] forced failure")
	assert.Contains(t, out, "Status: ✘ failed  (0/1 passed, 1 failed)")
}

// TestCheck_PreflightStreamingOrder verifies that for each tool the events arrive
// in CheckStarted → CheckCompleted order, and CategoryCompleted follows all checks.
func TestCheck_PreflightStreamingOrder(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	t.Setenv("NVCF_CLI_SELFHOSTED_LOCAL_ONLY", "1")
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json"})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	// Skip schemaVersion header.
	var kinds []string
	for _, l := range lines[1:] {
		kinds = append(kinds, l["event"].(string))
	}

	// Verify strict interleaving: every check_started is immediately followed
	// by a check_completed before the next check_started or category_completed.
	lastStartIdx := -1
	for i, k := range kinds {
		switch k {
		case "check_started":
			lastStartIdx = i
		case "check_completed":
			if lastStartIdx == -1 {
				t.Fatalf("check_completed at index %d with no preceding check_started", i)
			}
			if i != lastStartIdx+1 {
				t.Fatalf("check_completed at index %d does not immediately follow check_started at %d", i, lastStartIdx)
			}
			lastStartIdx = -1
		}
	}

	// category_completed must come after all check events for its category.
	catCompIdx := -1
	for i, k := range kinds {
		if k == "category_completed" {
			catCompIdx = i
		}
	}
	finalIdx := -1
	for i, k := range kinds {
		if k == "final" {
			finalIdx = i
		}
	}
	require.NotEqual(t, -1, catCompIdx, "expected at least one category_completed")
	require.NotEqual(t, -1, finalIdx, "expected a final event")
	assert.Less(t, catCompIdx, finalIdx, "category_completed must precede final")
}

// TestCheck_LocalOnlyFlag verifies that --local-only causes only local-host-tools
// events (no control-plane-cluster or compute-plane-cluster events).
func TestCheck_LocalOnlyFlag(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkLocalOnly = false
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json", "--local-only"})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	require.NotEmpty(t, lines, "expected at least one JSONL line")

	// Collect category names from category_completed events.
	var categories []string
	for _, l := range lines[1:] {
		if l["event"] == "category_completed" {
			if cat, ok := l["category"].(string); ok {
				categories = append(categories, cat)
			}
		}
	}
	assert.Contains(t, categories, "local-host-tools", "expected local-host-tools category")
	for _, cat := range categories {
		assert.NotEqual(t, "control-plane-cluster", cat, "control-plane-cluster must not appear with --local-only")
		assert.NotEqual(t, "compute-plane-cluster", cat, "compute-plane-cluster must not appear with --local-only")
	}
}

// TestCheck_SingleClusterMode verifies that without context flags, both
// control-plane-cluster and compute-plane-cluster category events appear.
func TestCheck_SingleClusterMode(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkLocalOnly = false
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	// No --local-only, no context flags → single-cluster mode.
	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json"})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	require.NotEmpty(t, lines, "expected at least one JSONL line")

	var categories []string
	for _, l := range lines[1:] {
		if l["event"] == "category_completed" {
			if cat, ok := l["category"].(string); ok {
				categories = append(categories, cat)
			}
		}
	}
	assert.Contains(t, categories, "local-host-tools", "expected local-host-tools")
	assert.Contains(t, categories, "control-plane-cluster", "expected control-plane-cluster in single-cluster mode")
	assert.Contains(t, categories, "compute-plane-cluster", "expected compute-plane-cluster in single-cluster mode")
}

func TestCheck_PreDoesNotProbeSISReachability(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkLocalOnly = false
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	t.Setenv("NVCF_SIS_URL", "http://127.0.0.1:1")

	rootCmd.SetArgs([]string{"self-hosted", "check", "--pre", "--json"})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	require.NotEmpty(t, lines, "expected at least one JSONL line")
	for _, l := range lines {
		assert.NotEqual(t, "sis-reachability", l["id"], "--pre must not probe SIS reachability before install")
	}
}

// TestCheck_SplitClusterMode verifies that providing both context flags causes
// both control-plane and compute-plane category events (run in parallel).
func TestCheck_SplitClusterMode(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkLocalOnly = false
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	rootCmd.SetArgs([]string{
		"self-hosted", "check", "--pre", "--json",
		"--control-plane-context", "admin@cp",
		"--compute-plane-context", "admin@gpu1",
	})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	require.NotEmpty(t, lines, "expected at least one JSONL line")

	var categories []string
	for _, l := range lines[1:] {
		if l["event"] == "category_completed" {
			if cat, ok := l["category"].(string); ok {
				categories = append(categories, cat)
			}
		}
	}
	assert.Contains(t, categories, "control-plane-cluster", "expected control-plane-cluster in split mode")
	assert.Contains(t, categories, "compute-plane-cluster", "expected compute-plane-cluster in split mode")
}

// TestComputePlaneIsTargeted tests the predicate that gates the cluster-validator
// probe. The validator should run when the compute plane is explicitly targeted
// (--compute-plane, --all) or implicitly targeted by ModeSingle + --pre.
// It must NOT run for --pre alone in ModeSplit, where the two context flags
// identify separate clusters and --pre does not constitute targeting the compute plane.
func TestComputePlaneIsTargeted(t *testing.T) {
	t.Cleanup(func() {
		checkPre = false
		checkComputePlane = false
		checkAll = false
	})

	tests := []struct {
		name string
		pre  bool
		cp   bool
		all  bool
		mode kubectx.Mode
		want bool
	}{
		{"--compute-plane single", false, true, false, kubectx.ModeSingle, true},
		{"--compute-plane split", false, true, false, kubectx.ModeSplit, true},
		{"--all single", false, false, true, kubectx.ModeSingle, true},
		{"--all split", false, false, true, kubectx.ModeSplit, true},
		{"--pre single — implicit compute plane", true, false, false, kubectx.ModeSingle, true},
		{"--pre split — must not target compute plane", true, false, false, kubectx.ModeSplit, false},
		{"--control-plane only", false, false, false, kubectx.ModeSingle, false},
		{"no relevant flag", false, false, false, kubectx.ModeSingle, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkPre = tt.pre
			checkComputePlane = tt.cp
			checkAll = tt.all
			assert.Equal(t, tt.want, computePlaneIsTargeted(tt.mode))
		})
	}
}

func TestControlPlaneIsTargeted(t *testing.T) {
	t.Cleanup(func() {
		checkPre = false
		checkControlPlane = false
		checkAll = false
	})

	tests := []struct {
		name string
		pre  bool
		cp   bool
		all  bool
		mode kubectx.Mode
		want bool
	}{
		{"--control-plane single", false, true, false, kubectx.ModeSingle, true},
		{"--control-plane split", false, true, false, kubectx.ModeSplit, true},
		{"--all single", false, false, true, kubectx.ModeSingle, true},
		{"--all split", false, false, true, kubectx.ModeSplit, true},
		{"--pre single -- implicit control plane", true, false, false, kubectx.ModeSingle, true},
		{"--pre split -- must not target control plane", true, false, false, kubectx.ModeSplit, false},
		{"--compute-plane only", false, false, false, kubectx.ModeSingle, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkPre = tt.pre
			checkControlPlane = tt.cp
			checkAll = tt.all
			assert.Equal(t, tt.want, controlPlaneIsTargeted(tt.mode))
		})
	}
}

// TestCheck_ComputePlaneFlagRunsChecks verifies that --compute-plane alone
// produces compute-plane-cluster category events. Before the gating fix this
// flag was a complete no-op and produced no check events at all.
func TestCheck_ComputePlaneFlagRunsChecks(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkComputePlane = false
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	rootCmd.SetArgs([]string{"self-hosted", "check", "--compute-plane", "--skip-cluster-validation", "--json"})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	require.NotEmpty(t, lines, "expected at least one JSONL line")

	var categories []string
	for _, l := range lines[1:] {
		if l["event"] == "category_completed" {
			if cat, ok := l["category"].(string); ok {
				categories = append(categories, cat)
			}
		}
	}
	assert.Contains(t, categories, "compute-plane-cluster",
		"--compute-plane must produce compute-plane-cluster events")
}

// TestCheck_ControlPlaneFlagRunsChecks verifies that --control-plane alone
// produces control-plane-cluster category events.
func TestCheck_ControlPlaneFlagRunsChecks(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkControlPlane = false
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	rootCmd.SetArgs([]string{"self-hosted", "check", "--control-plane", "--skip-cluster-validation", "--json"})
	_ = rootCmd.Execute()

	lines := parseJSONLLines(t, stderr.String())
	require.NotEmpty(t, lines, "expected at least one JSONL line")

	var categories []string
	for _, l := range lines[1:] {
		if l["event"] == "category_completed" {
			if cat, ok := l["category"].(string); ok {
				categories = append(categories, cat)
			}
		}
	}
	assert.Contains(t, categories, "control-plane-cluster",
		"--control-plane must produce control-plane-cluster events")
}

// TestCheck_ValidatorSkipNoteAppearsOnComputePlane verifies that the
// "cluster-validator skipped" note appears on stderr when --compute-plane is
// used with --skip-cluster-validation (compute plane is targeted, validator is
// suppressed).
func TestCheck_ValidatorSkipNoteAppearsOnComputePlane(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkComputePlane = false
		checkSkipClusterValidation = false
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	rootCmd.SetArgs([]string{
		"self-hosted", "check", "--compute-plane",
		"--skip-cluster-validation", "--json",
	})
	_ = rootCmd.Execute()

	assert.Contains(t, stderr.String(), "cluster-validator skipped",
		"expected skip note when compute plane is targeted and --skip-cluster-validation is set")
}

// TestCheck_ValidatorSkipNoteAbsentForPreInSplitMode verifies that the
// "cluster-validator skipped" note does NOT appear when --pre is used in
// split mode, because the compute plane is not explicitly targeted and
// ModeSplit + --pre does not implicitly make either context the compute plane.
func TestCheck_ValidatorSkipNoteAbsentForPreInSplitMode(t *testing.T) {
	t.Cleanup(func() {
		selfHostedJSON = false
		selfHostedOutput = "text"
		checkPre = false
		checkSkipClusterValidation = false
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
	})

	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})

	rootCmd.SetArgs([]string{
		"self-hosted", "check", "--pre",
		"--skip-cluster-validation", "--json",
		"--control-plane-context", "admin@cp",
		"--compute-plane-context", "admin@gpu1",
	})
	_ = rootCmd.Execute()

	assert.NotContains(t, stderr.String(), "cluster-validator skipped",
		"skip note must not appear for --pre in split mode (compute plane not targeted)")
}

// parseJSONLLines splits s into non-empty lines, skips any non-JSON lines
// (e.g. cobra error messages written to stderr), and unmarshals each JSON line
// as an object. Returns them in order.
func parseJSONLLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue // skip non-JSON lines (warnings, cobra error messages, etc.)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("JSONL line is not valid JSON: %s\nerr: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}
