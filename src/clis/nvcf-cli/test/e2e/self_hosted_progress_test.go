//go:build e2e
// +build e2e

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

// E2E smoke test for §6.4.2 plain-mode output shape (M+7.9).
// Run on mcamp-dev-vm via `make e2e-self-hosted-progress`. Slow (~10 min).
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelfHostedUp_PlainModeEvents verifies the --plain renderer emits the
// §6.4.2 mock shape during a real install on mcamp-dev-vm.
//
// Slow: this runs a full `up` (~7-13 minutes against a fresh k3d cluster).
// Run via `make e2e-self-hosted-progress` on mcamp-dev-vm.
//
// Asserts:
//   - At least one PhaseStarted + PhaseCompleted line per phase 1-8
//   - RFC3339 UTC timestamps prefix every line
//   - [NN/8] phase tag is zero-padded
//   - Final line emitted with success=true
//   - No legacy ">>>" Fprintln strings remain in stderr
func TestSelfHostedUp_PlainModeEvents(t *testing.T) {
	if os.Getenv("NVCF_E2E_SKIP_INSTALL") != "" {
		t.Skip("NVCF_E2E_SKIP_INSTALL set — skipping full install run")
	}

	// 15-minute deadline gives ample headroom over the typical 7-13 min run.
	// The test will timeout if helmfile genuinely hangs; that's a real bug.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	args := []string{
		"self-hosted", "up",
		"--cluster-name", "ncp-local-e2e-progress",
		"--plain",
	}
	if token := os.Getenv("NVCF_TOKEN"); token != "" {
		args = append(args, "--token", token, "--non-interactive")
	}

	cmd := exec.CommandContext(ctx, cliBin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// stdout carries helmfile/helm logs — let the operator see them live for debugging
	cmd.Stdout = os.Stdout

	err := cmd.Run()
	require.NoError(t, err, "self-hosted up failed; stderr: %s", stderr.String())

	out := stderr.String()
	t.Logf("stderr (last 4KB):\n%s", plainProgressLastN(out, 4096))

	// §6.4.2 line shape: <RFC3339> [NN/8] <phase>: starting | complete (...)
	// RFC3339 UTC: 2006-01-02T15:04:05Z
	rfc3339 := `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`

	// All 8 phases emit a "starting" line and a "complete" line.
	// Phases 3, 5, 8 are synthetic (emitted back-to-back) but still appear.
	expectedPhases := []struct {
		num  int
		name string
	}{
		{1, "preflight"},
		{2, "resolve-stack"},
		{3, "render-cp"},
		{4, "apply-cp"},
		{5, "check-cp"},
		{6, "register"},
		{7, "apply-compute-plane"},
		{8, "final-health"},
	}

	for _, p := range expectedPhases {
		startingRe := regexp.MustCompile(
			fmt.Sprintf(`%s \[%02d/8\] %s: starting`, rfc3339, p.num, regexp.QuoteMeta(p.name)),
		)
		assert.Regexp(t, startingRe, out,
			"phase %d (%s): missing 'starting' line", p.num, p.name)

		completeRe := regexp.MustCompile(
			fmt.Sprintf(`%s \[%02d/8\] %s: complete \(`, rfc3339, p.num, regexp.QuoteMeta(p.name)),
		)
		assert.Regexp(t, completeRe, out,
			"phase %d (%s): missing 'complete' line", p.num, p.name)
	}

	// Final line: "TIMESTAMP final: success=true cluster=..."
	finalRe := regexp.MustCompile(rfc3339 + ` final: success=true`)
	assert.Regexp(t, finalRe, out, "missing final success line")

	// No legacy ">>> ..." Fprintln stragglers should appear in stderr when
	// the plain renderer is active; those print paths are only in the install
	// subcommand path and should not bleed into self-hosted up's stderr.
	assert.NotContains(t, out, ">>>",
		"legacy >>> Fprintln output found in stderr — plain renderer should replace all such output")
}

// plainProgressLastN returns the last n bytes of s; entire s if shorter.
func plainProgressLastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
