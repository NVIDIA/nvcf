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

// E2E status smoke test (M+8.12) per SRD/SDD §6.5.
//
// Run on mcamp-dev-vm against a real k3d cluster:
//
//	NVCF_E2E=1 go test -tags=e2e ./test/e2e/... -run TestE2E_StatusHealthyAfterUp -v
//
// Or via the Makefile target: make e2e-self-hosted-status
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

// TestE2E_StatusHealthyAfterUp drives the full status smoke per §6.5:
//  1. nvcf self-hosted up --cluster-name=e2e-status (idempotent if already up)
//  2. nvcf self-hosted status --json
//  3. parse the fat snapshot line, assert verdict="healthy" and all 10 expected
//     components are present and healthy.
//
// Per the plan this runs on mcamp-dev-vm against a real k3d cluster.
// Skip in CI; run manually.
func TestE2E_StatusHealthyAfterUp(t *testing.T) {
	requireE2E(t)
	t.Skip("M+8.12 skeleton: full run on mcamp-dev-vm deferred to manual dev-VM iteration (requires live k3d cluster)")
	nvcfBin := requireNvcfBin(t)
	cluster := envOr("NVCF_E2E_CLUSTER", "e2e-status")

	// Bring up the cluster (idempotent — if already installed this is a fast
	// no-op re-run that confirms the renderer exits 0).
	upArgs := []string{
		"self-hosted", "up",
		"--cluster-name=" + cluster,
		"--non-interactive",
		"--plain",
	}
	if tok := os.Getenv("NVCF_TOKEN"); tok != "" {
		upArgs = append(upArgs, "--token="+tok)
	}
	upCmd := exec.Command(nvcfBin, upArgs...)
	upCmd.Stdout = os.Stderr // forward helmfile noise to test stderr for diagnostics
	upCmd.Stderr = os.Stderr
	if err := upCmd.Run(); err != nil {
		t.Fatalf("up failed: %v", err)
	}

	// Run status --json; the renderer writes JSONL to stderr.
	// Composed mode emits: schemaVersion line → fat snapshot line → final line.
	statusCmd := exec.Command(nvcfBin, "self-hosted", "status", "--json",
		"--cluster-name="+cluster)
	var statusStderr strings.Builder
	statusCmd.Stderr = &statusStderr
	// status stdout is not used; let it go to /dev/null.
	statusCmd.Stdout = os.Stdout
	if err := statusCmd.Run(); err != nil {
		t.Logf("status stderr: %s", statusStderr.String())
		t.Fatalf("status --json failed: %v", err)
	}

	// Parse JSONL: find the composed `snapshot` event (the fat-object per §6.5.4).
	out := statusStderr.String()
	t.Logf("status stderr (full):\n%s", out)

	var snapshot map[string]any
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if obj["event"] == "snapshot" {
			snapshot = obj
			break
		}
	}
	require.NotNil(t, snapshot, "no snapshot event in status output: %s", out)
	assert.Equal(t, "healthy", snapshot["verdict"], "expected verdict=healthy")

	// Components panel: composed snapshot includes components[] inline (§6.5.4).
	// Each element must have healthy=true. The canonical 10 components are:
	//   control-plane: cassandra, openbao, nats, nvcf-api, nvca-operator,
	//                  sis-api, nvca-migrator, nvcf-control-plane (8)
	//   compute-plane: nvca-worker, nvcf-backend (2)
	// Total: 10.
	components, ok := snapshot["components"].([]any)
	if !ok {
		t.Fatalf("snapshot.components is missing or not an array; snapshot: %v", snapshot)
	}
	assert.Len(t, components, 10,
		"expected 10 components in snapshot, got %d; components: %v", len(components), components)
	for i, c := range components {
		cm, ok := c.(map[string]any)
		require.True(t, ok, "components[%d] is not an object", i)
		name, _ := cm["name"].(string)
		healthy, _ := cm["healthy"].(bool)
		assert.True(t, healthy, "component %q (index %d) should be healthy", name, i)
	}
}
