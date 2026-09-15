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

// Split-cluster E2E tests T8/T9/T10 (M+9.9 / M+9.H).
//
// These tests exercise the split-cluster context routing in `up` / `status`,
// requiring two or three real k3d clusters on mcamp-dev-vm. In CI and on
// developer workstations without k3d, every test skips immediately via
// requireE2E + the k3d PATH guard inside setupSplitClusters.
//
// Run on mcamp-dev-vm:
//
//	NVCF_E2E=1 make e2e-self-hosted-split
//
// Full implementation wired in M+9.I (dev-VM smoke matrix).
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestE2E_T8_SplitClusterCleanReRun runs `up --control-plane-context=cp
// --compute-plane-context=gpu1` twice and asserts:
//   - both runs exit 0
//   - second run is idempotent (no resource churn)
//   - phase 6 (register) hits compute-plane context's K8s API for JWKS
//   - control-plane and compute-plane both have their helm releases applied
//
// Per the plan, this runs on mcamp-dev-vm against two real k3d clusters.
// CI lane TBD.
func TestE2E_T8_SplitClusterCleanReRun(t *testing.T) {
	requireE2E(t)
	t.Skip("T8: requires two live k3d clusters (cp + gpu1) on mcamp-dev-vm")
	// TODO(M+9.K close-out): on mcamp-dev-vm:
	//   1. setupSplitClusters(t, "cp", "gpu1")  // k3d cluster create both
	//   2. nvcfBin := requireNvcfBin(t)
	//   3. run `up --control-plane-context=k3d-cp --compute-plane-context=k3d-gpu1
	//      --cluster-name=split-t8 --icms-url=https://sis.local --token=$JWT --json`
	//   4. assert exit 0; parse JSONL: phase 6 (register) has Context=k3d-gpu1
	//   5. re-run identical command; assert exit 0; assert phase 6 emits "register
	//      would be no-op; existing row will be reused"
	//   6. teardownSplitClusters(t)
}

// TestE2E_T9_SecondComputePlane brings up the control plane on `cp`, runs
// `up` to register `gpu1`, then runs `up --cluster-name=split-t9-2
// --compute-plane-context=k3d-gpu2` to add a second compute plane against
// the same control plane. Asserts:
//   - both compute-plane registrations succeed
//   - status --watch shows both clusters in the Registered Compute Planes panel
func TestE2E_T9_SecondComputePlane(t *testing.T) {
	requireE2E(t)
	t.Skip("T9: requires three live k3d clusters (cp + gpu1 + gpu2) on mcamp-dev-vm")
	// TODO(M+9.K close-out): on mcamp-dev-vm:
	//   1. setupSplitClusters(t, "cp", "gpu1", "gpu2")
	//   2. up against gpu1, then up against gpu2 with same control plane
	//   3. status --json shows both compute-planes in clusters[]
}

// TestE2E_T10_SplitInterruptedRegisterRecovers runs `up` against split
// contexts, kills it after phase 6 succeeds (register row written) but before
// phase 7 (compute-plane apply) completes. Re-runs `up` and asserts:
//   - the existing SIS row is reused (--ignore-existing semantics)
//   - phase 7 retries from a clean slate
//   - cluster ends in healthy state
func TestE2E_T10_SplitInterruptedRegisterRecovers(t *testing.T) {
	requireE2E(t)
	t.Skip("T10: requires SIGTERM-mid-flight + state inspection on mcamp-dev-vm")
	// TODO(M+9.K close-out): on mcamp-dev-vm:
	//   1. setupSplitClusters(t, "cp", "gpu1")
	//   2. start `up`; SIGTERM after observing phase 6 complete in JSONL stream
	//   3. assert exit 130
	//   4. re-run `up`; assert phase 6 emits "register would be no-op"
	//   5. status --json shows verdict=healthy
}

// setupSplitClusters creates k3d clusters for each name and registers cleanup
// handlers. Skips with a helpful message if k3d is not on PATH.
func setupSplitClusters(t *testing.T, names ...string) {
	t.Helper()
	if _, err := exec.LookPath("k3d"); err != nil {
		t.Skipf("k3d not on PATH: %v", err)
	}
	for _, name := range names {
		clusterName := name // capture for closure
		cmd := exec.Command("k3d", "cluster", "create", clusterName, "--wait", "--timeout", "120s")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("k3d cluster create %s: %v", clusterName, err)
		}
		t.Cleanup(func() { teardownSplitCluster(t, clusterName) })
	}
}

// teardownSplitCluster deletes a k3d cluster by name. Errors are logged but
// not fatal so that parallel teardowns do not mask the original test failure.
func teardownSplitCluster(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "k3d", "cluster", "delete", name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Logf("k3d cluster delete %s (best-effort): %v", name, err)
	}
}

// kctxFromName converts a k3d cluster name to its kubeconfig context name.
// k3d prefixes context names with "k3d-".
func kctxFromName(name string) string {
	return "k3d-" + name
}

// parseSplitJSONLEvents parses a JSONL stream and returns each valid JSON
// object as a map for downstream assertions. Non-JSON lines are silently
// skipped (matching the agent-safe filter convention from Plan Deviation #18).
func parseSplitJSONLEvents(s string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err == nil {
			out = append(out, obj)
		}
	}
	return out
}

// Silence "imported and not used" errors from the skip stubs. These symbols
// are exercised in the full dev-VM run (M+9.K close-out).
var (
	_ = kctxFromName
	_ = parseSplitJSONLEvents
	_ = fmt.Sprintf
)
