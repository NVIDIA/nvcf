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

// Down idempotency E2E tests T18-T21 (M+11.J / M+11.10).
//
// These tests exercise `down` tear-down paths including idempotency, PVC
// preservation, mid-flight interrupt recovery, and --no-apply round-trips.
// They require a live k3d cluster with helm and helmfile on PATH; on developer
// workstations and in CI they skip immediately via requireE2E.
//
// Run on mcamp-dev-vm:
//
//	NVCF_E2E=1 make e2e-self-hosted-down
//
// Full runs land in M+11.K when the dev-VM smoke matrix runs.
package e2e

import (
	"testing"
)

// TestE2E_T18_DownIdempotent runs `up; down; down` and asserts:
//   - first down: phases run normally
//   - second down: phases 4 (uninstall control plane) + 6 (remove namespaces)
//     reported as "skipped (already clean)"
//   - second down: SIS row reports "not found; nothing to do"
func TestE2E_T18_DownIdempotent(t *testing.T) {
	requireE2E(t)
	t.Skip("T18: requires live k3d + helmfile + helm on mcamp-dev-vm")
	// TODO(M+11.K close-out): on mcamp-dev-vm:
	//   1. nvcf self-hosted up --cluster-name=t18 ...
	//   2. nvcf self-hosted down --cluster-name=t18
	//   3. assert: helm releases gone (helm ls -A | grep -v t18 should be empty)
	//   4. nvcf self-hosted down --cluster-name=t18 --confirm
	//   5. assert: exits 0 with "skipped" markers; cluster delete reports
	//              "cluster t18 not found; nothing to do" (M+11.A behavior)
}

// TestE2E_T19_UpAfterDown runs:
//  1. up cluster A
//  2. down A (preserves PVCs)
//  3. up A again — assert healthy + PVCs reused (no data loss)
//  4. down A --remove-persistent --confirm
//  5. up A again — assert healthy from clean state
func TestE2E_T19_UpAfterDown(t *testing.T) {
	requireE2E(t)
	t.Skip("T19: requires live k3d + multiple up/down cycles on mcamp-dev-vm")
	// TODO(M+11.K close-out): on mcamp-dev-vm.
}

// TestE2E_T20_InterruptedDownResumes runs `down` and SIGINTs after 2 of N
// helm uninstalls have completed. Re-running `down` should:
//   - skip already-uninstalled releases (helm uninstall reports "release: not found")
//   - finish remaining uninstalls
//   - delete the SIS row (with --ignore-missing tolerance)
//   - exit 0
func TestE2E_T20_InterruptedDownResumes(t *testing.T) {
	requireE2E(t)
	t.Skip("T20: requires SIGINT mid-flight on mcamp-dev-vm")
	// TODO(M+11.K close-out): on mcamp-dev-vm.
}

// TestE2E_T21_UninstallNoApplyRoundTrip runs:
//  1. up
//  2. uninstall --no-apply --compute-plane --cluster-name=X > a.yaml
//  3. kubectl delete -f a.yaml --kube-context=...
//  4. assert: helm releases gone; PVCs preserved
//  5. down --cluster-name=X cleans up the SIS row even though helm releases
//     were already removed externally (--ignore-missing on cluster delete +
//     "release: not found" tolerance in teardown.RenderUninstall paths)
func TestE2E_T21_UninstallNoApplyRoundTrip(t *testing.T) {
	requireE2E(t)
	t.Skip("T21: requires live k3d + kubectl + helm on mcamp-dev-vm")
	// TODO(M+11.K close-out): on mcamp-dev-vm.
}
