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

// E2E plan-only smoke test (M+8.12) per SRD/SDD §6.4.
//
// Run on mcamp-dev-vm against a real k3d cluster:
//
//	NVCF_E2E=1 go test -tags=e2e ./test/e2e/... -run TestE2E_PlanOnlyMakesNoChanges -v
//
// Or via the Makefile target: make e2e-self-hosted-plan-only
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_PlanOnlyMakesNoChanges drives the plan-only smoke per §6.4:
//  1. snapshot `helm ls -A -q` release count before
//  2. nvcf self-hosted up --plan-only --cluster-name=plan-only-smoke --json
//  3. snapshot release count after
//  4. assert no new releases; assert `planned` event lists 8 phases
//
// --plan-only exits 0 without modifying cluster state (REQ-17). Run on
// mcamp-dev-vm; skip in CI.
func TestE2E_PlanOnlyMakesNoChanges(t *testing.T) {
	requireE2E(t)
	t.Skip("M+8.12 skeleton: full run on mcamp-dev-vm deferred to manual dev-VM iteration (requires live k3d cluster, helm, kubectl)")
	nvcfBin := requireNvcfBin(t)

	// countReleases returns the number of helm releases across all namespaces.
	countReleases := func() int {
		out, err := exec.Command("helm", "ls", "-A", "-q").Output()
		if err != nil {
			return -1
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			return 0
		}
		return strings.Count(s, "\n") + 1
	}

	before := countReleases()
	require.NotEqual(t, -1, before, "helm ls failed before plan-only run")

	// --plan-only exits 0 and emits a `planned` JSONL event on stderr.
	// No helmfile / kubectl mutating calls are made.
	args := []string{
		"self-hosted", "up",
		"--plan-only",
		"--cluster-name=plan-only-smoke",
		"--non-interactive",
		"--json",
	}
	if tok := os.Getenv("NVCF_TOKEN"); tok != "" {
		args = append(args, "--token="+tok)
	} else {
		// plan-only skips the token exchange — any non-empty placeholder works.
		args = append(args, "--token=plan-only-smoke-noop")
	}
	cmd := exec.Command(nvcfBin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = os.Stdout // helmfile-mode stdout (usually empty for plan-only)
	if err := cmd.Run(); err != nil {
		t.Logf("plan-only stderr: %s", stderr.String())
		t.Fatalf("plan-only must exit 0; err: %v", err)
	}

	// Assert: no new helm releases were created.
	after := countReleases()
	assert.Equal(t, before, after,
		"plan-only must not create helm releases (before=%d, after=%d)", before, after)

	// Assert: find the `planned` event; it must list all 8 phases.
	stderrOut := stderr.String()
	t.Logf("plan-only stderr (full):\n%s", stderrOut)

	var plannedEvent map[string]any
	for _, line := range strings.Split(stderrOut, "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if obj["event"] == "planned" {
			plannedEvent = obj
			break
		}
	}
	require.NotNil(t, plannedEvent,
		"no planned event in stderr; full output:\n%s", stderrOut)

	phases, ok := plannedEvent["phases"].([]any)
	require.True(t, ok, "planned.phases is not an array; event: %v", plannedEvent)
	assert.Len(t, phases, 8,
		"planned event must list all 8 phases, got %d; phases: %v", len(phases), phases)
}
