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

// Fault-injection E2E tests T11-T17 (spec §8.4 adversarial matrix).
//
// These tests require significant external infrastructure (toxiproxy-server,
// live k3d cluster, Cassandra direct access, helmfile) and are intended to run
// on mcamp-dev-vm via `make e2e-self-hosted-faults`. In CI, each test skips
// immediately when NVCF_E2E_FAULTS is unset.
//
// Harness scaffolded in M+8.I; full integration runs deferred to M+8.K
// per the plan deviation documented in the implementation plan §Plan Deviation N.
package e2e

import (
	"os"
	"testing"
)

// faultTestsEnabled is a test helper that skips the calling test unless
// NVCF_E2E_FAULTS=1 is set in the environment. Call at the top of every
// T11-T17 test before any setup that requires live infrastructure.
func faultTestsEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("NVCF_E2E_FAULTS") == "" {
		t.Skip("set NVCF_E2E_FAULTS=1 to run fault-injection tests (requires toxiproxy-server, live cluster, etc.)")
	}
}

// TestT11_SISFiveOhThreeMidInstall verifies that a SIS 503 mid-install causes
// the orchestrator to emit phase_failed{errCategory:"network",
// retryClass:"backoff", retryAfterSec≥1} and then converge once the fault
// is cleared. Per SRD/SDD §8.4 T11.
func TestT11_SISFiveOhThreeMidInstall(t *testing.T) {
	faultTestsEnabled(t)
	t.Skip("T11: full run requires toxiproxy-server + live SIS endpoint (M+8.K close-out)")
	// TODO(M+8.K close-out): run on mcamp-dev-vm:
	//   1. Build control plane up to the SIS-register phase (vanilla up).
	//   2. Start toxiproxy: tp, _ := faults.Start(ctx, 8474); t.Cleanup(func() { tp.Stop() })
	//   3. AddProxy: tp.AddProxy("sis", "127.0.0.1:8888", "<real-SIS-host>:<port>")
	//   4. Inject TCP-level fault: tp.AddToxic("sis", "drop", "limit_data", map[string]any{"bytes": 0})
	//   5. Run `up --icms-url=http://127.0.0.1:8888 ...`.
	//   6. Parse JSONL stderr; assert phase_failed event with errCategory="network" and retryClass="backoff".
	//   7. tp.RemoveToxic("sis", "drop"); re-run `up`; assert convergence.
}

// TestT12_APIKeysRateLimitBackoff verifies that a 429 from the API Keys
// service with Retry-After: 5 causes the orchestrator to emit exactly two
// Waiting{reason:"api-keys 429; retry in 5s"} events and then succeed on the
// third attempt within ~12 s. Per SRD/SDD §8.4 T12.
func TestT12_APIKeysRateLimitBackoff(t *testing.T) {
	faultTestsEnabled(t)
	t.Skip("T12: full run requires httptest.Server middleware seam in API Keys client (M+8.K close-out)")
	// TODO(M+8.K close-out): run on mcamp-dev-vm:
	//   1. Stand up an httptest.Server with a request counter: return 429 with
	//      "Retry-After: 5" for the first two requests, then 200 on the third.
	//   2. Inject the test server URL via the API Keys client init seam or env override.
	//   3. Run `up`; collect JSONL events via captureSink or stderr parse.
	//   4. Assert exactly two Waiting{reason:"api-keys 429; retry in 5s"} events.
	//   5. Assert PhaseCompleted within 12 s of the first 429.
}

// TestT13_KubeconfigExecTokenExpiry verifies that a kubeconfig whose exec
// credential plugin returns a token expiring 30 s in the future causes the
// orchestrator to emit phase_failed{errCategory:"token_expiry",
// retryClass:"after_remediation"} when helmfile runs mid-deploy.
// Per SRD/SDD §8.4 T13.
func TestT13_KubeconfigExecTokenExpiry(t *testing.T) {
	faultTestsEnabled(t)
	t.Skip("T13: full run requires custom exec-credential plugin binary + timing coordination (M+8.K close-out)")
	// TODO(M+8.K close-out): run on mcamp-dev-vm:
	//   1. Compile a small Go binary that writes an ExecCredential JSON with
	//      status.expirationTimestamp = now+30s.
	//   2. Construct a kubeconfig with an exec block pointing at that binary.
	//   3. Run `up` with KUBECONFIG pointing at the crafted config.
	//   4. Helmfile will hit token expiration mid-apply; assert phase_failed event
	//      with errCategory="token_expiry" and retryClass="after_remediation".
}

// TestT14_OCICacheCorruption verifies that a truncated OCI cache entry causes
// the orchestrator to emit phase_failed{errCategory:"cache_corruption",
// retryClass:"immediate"} and then remove the bad entry and re-fetch on retry.
// Per SRD/SDD §8.4 T14.
func TestT14_OCICacheCorruption(t *testing.T) {
	faultTestsEnabled(t)
	t.Skip("T14: full run requires a prior successful up + populated OCI cache (M+8.K close-out)")
	// TODO(M+8.K close-out): run on mcamp-dev-vm:
	//   1. Run `up` once to populate the OCI cache.
	//   2. Determine the digest from STACK_PATH (oci:// ref or local bundle).
	//   3. homeDir, _ := os.UserHomeDir()
	//      faults.TruncateOCICache(homeDir, digest)
	//   4. Run `up` again; assert phase_failed{errCategory:"cache_corruption", retryClass:"immediate"}.
	//   5. Verify the cache file was removed (or re-populated to full size) on retry.
}

// TestT15_HelmPendingUpgradeStuck verifies that a release left in
// pending-upgrade state (from a killed helm upgrade) causes the orchestrator
// to emit phase_failed{errCategory:"helm_pending_upgrade",
// retryClass:"after_remediation"} with rollback instructions in the remediation
// field. Per SRD/SDD §8.4 T15.
func TestT15_HelmPendingUpgradeStuck(t *testing.T) {
	faultTestsEnabled(t)
	t.Skip("T15: full run requires helmfile + installed control plane + helm CLI (M+8.K close-out)")
	// TODO(M+8.K close-out): run on mcamp-dev-vm:
	//   1. Ensure control plane is installed (successful prior up).
	//   2. faults.ForceHelmPendingUpgrade("nvcf-control-plane", chartPath, "nvcf-system")
	//   3. Run `up`; assert phase_failed{errCategory:"helm_pending_upgrade"} with
	//      remediation containing "helm rollback nvcf-control-plane".
	//   4. Run `helm rollback nvcf-control-plane -n nvcf-system`.
	//   5. Re-run `up`; assert convergence.
}

// TestT16_CassandraMigrationLock verifies that a Cassandra migration job pod
// that is force-deleted (simulating a stuck migration lock) causes helmfile's
// waitForJobs to time out and the orchestrator to emit phase_failed{
// errCategory:"cassandra_migration_lock", retryClass:"after_remediation"}.
// Per SRD/SDD §8.4 T16.
func TestT16_CassandraMigrationLock(t *testing.T) {
	faultTestsEnabled(t)
	t.Skip("T16: full run requires kubectl + control plane mid-install + timing (M+8.K close-out)")
	// TODO(M+8.K close-out): run on mcamp-dev-vm:
	//   1. Start `up` in a goroutine (or background process).
	//   2. Poll until the Cassandra migration Job pod reaches Running.
	//   3. faults.KillCassandraMigrationJob("cassandra-system", "<job-name>")
	//   4. Wait for `up` to emit phase_failed{errCategory:"cassandra_migration_lock"}.
	//   5. (Optional) Re-run `up` after manual cleanup; assert convergence.
}

// TestT17_HalfRegisteredSISRow verifies that a pre-existing
// clusters_by_account_id row without a corresponding cluster_oidc_by_cluster_id
// row (simulating a partial SIS write) causes the orchestrator to emit
// phase_progress{reason:"partial_sis_write detected; reconciling"} and
// converge without any manual cleanup. Per SRD/SDD §8.4 T17.
func TestT17_HalfRegisteredSISRow(t *testing.T) {
	faultTestsEnabled(t)
	t.Skip("T17: full run requires cqlsh + Cassandra NodePort exposed + control plane (M+8.K close-out)")
	// TODO(M+8.K close-out): run on mcamp-dev-vm:
	//   1. cassHost := "127.0.0.1"; cassPort := "9042" (or NodePort-forwarded)
	//      ncaID := os.Getenv("NVCF_NCA_ID")
	//      faults.InsertHalfRegisteredSISRow(cassHost, cassPort, ncaID, clusterName)
	//   2. Run `up --cluster-name=<clusterName> ...`
	//   3. Assert phase_progress event with reason="partial_sis_write detected; reconciling".
	//   4. Assert up exits 0 (convergence without manual cleanup).
	//   5. Verify: cassandraRowCount(t, "sis_api", "cluster_oidc_by_cluster_id", "") == 1
}
