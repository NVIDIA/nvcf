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

// Manual E2E runner for spec §8.4 idempotency matrix (T1-T7).
// Run on mcamp-dev-vm against a working k3d cluster: `make e2e-self-hosted`.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	clusterName = "ncp-local-e2e"
	cliBin      = "/tmp/nvcf-cli"
)

func init() {
	if os.Getenv("NVCF_E2E") == "" {
		panic("self_hosted_idempotency_test must be run via `make e2e-self-hosted` — NVCF_E2E=1 is the gate")
	}
}

func stackPaths(t *testing.T) (controlPlane, computePlane string) {
	t.Helper()
	controlPlane = os.Getenv("CONTROL_PLANE_STACK_PATH")
	computePlane = os.Getenv("COMPUTE_PLANE_STACK_PATH")
	require.NotEmpty(t, controlPlane, "set CONTROL_PLANE_STACK_PATH")
	require.NotEmpty(t, computePlane, "set COMPUTE_PLANE_STACK_PATH")
	return controlPlane, computePlane
}

func nextStackPaths() (controlPlane, computePlane string) {
	controlPlane = os.Getenv("CONTROL_PLANE_STACK_PATH_NEXT")
	computePlane = os.Getenv("COMPUTE_PLANE_STACK_PATH_NEXT")
	return controlPlane, computePlane
}

func upArgs(cluster, controlPlaneStack, computePlaneStack string) []string {
	return []string{
		"self-hosted", "up",
		"--cluster-name=" + cluster,
		"--control-plane-stack=" + controlPlaneStack,
		"--compute-plane-stack=" + computePlaneStack,
		"--env=local",
		"--token=" + os.Getenv("NVCF_TOKEN"),
	}
}

// nvcf runs the CLI with the supplied args; returns stdout, stderr, exit code.
func nvcf(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, args...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	exitCode := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("nvcf %v: %v", args, err)
	}
	return out.String(), errb.String(), exitCode
}

// podUIDs returns the set of UIDs of pods in {nvcf, nvca-system, nvca-operator, sis,
// nats-system, vault-system, cassandra-system}.
func podUIDs(t *testing.T) map[string]string {
	t.Helper()
	out, err := exec.Command("kubectl", "get", "pods",
		"-A",
		"-l", "app.kubernetes.io/managed-by=Helm",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}={.metadata.uid}\n{end}",
	).Output()
	require.NoError(t, err)
	uids := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i := strings.LastIndex(line, "="); i > 0 {
			uids[line[:i]] = line[i+1:]
		}
	}
	return uids
}

// teardownCluster destroys + recreates the e2e k3d cluster to provide a clean
// slate for T2/T3/T4. Requires k3d on PATH and a cluster config file at the
// canonical location. The runbook in §10 documents the manual setup; this
// helper is the in-test equivalent.
func teardownCluster(t *testing.T) {
	t.Helper()
	// Best-effort destroy; ignore "cluster not found" errors.
	delCmd := exec.Command("k3d", "cluster", "delete", clusterName)
	if out, err := delCmd.CombinedOutput(); err != nil {
		t.Logf("k3d cluster delete %s: %v\n%s", clusterName, err, out)
	}
	// Recreate. Cluster config path is environment-specific; prefer the runbook's
	// build-and-deploy-cluster Makefile target if available, otherwise fall back
	// to a minimal k3d create. Operators can override via E2E_CLUSTER_CREATE_CMD.
	createCmd := os.Getenv("E2E_CLUSTER_CREATE_CMD")
	if createCmd == "" {
		createCmd = "k3d cluster create " + clusterName + " --servers 1 --agents 5"
	}
	out, err := exec.Command("sh", "-c", createCmd).CombinedOutput()
	require.NoError(t, err, "k3d cluster create failed: %s", out)
}

// requireBackendHealthy asserts that the NVCFBackend custom resource for the given cluster
// name reports status.health == "healthy".
func requireBackendHealthy(t *testing.T, name string) {
	t.Helper()
	out, err := exec.Command("kubectl", "get", "nvcfbackend", name,
		"-n", "nvca-operator",
		"-o", "jsonpath={.status.health}").Output()
	require.NoError(t, err)
	require.Equal(t, "healthy", strings.TrimSpace(string(out)))
}

// cassandraRowCount returns the number of rows in the given Cassandra table matching the
// optional WHERE clause. Connects via kubectl exec into cassandra-0 in cassandra-system.
// Pass whereClause="" for a plain SELECT COUNT(*).
// Hardcodes the helm-nvcf-cassandra default (cassandra/cassandra) credentials.
// If openbao-migrations rotates the password in production, this helper breaks
// silently with cqlsh auth failure. Acceptable for v1 dev-VM-only.
func cassandraRowCount(t *testing.T, keyspace, table, whereClause string) int {
	t.Helper()
	cql := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s", keyspace, table)
	if whereClause != "" {
		cql += " WHERE " + whereClause
	}
	cql += ";"
	out, err := exec.Command("kubectl", "exec",
		"-n", "cassandra-system", "cassandra-0",
		"-c", "cassandra",
		"--",
		"cqlsh", "-u", "cassandra", "-p", "cassandra",
		"-e", cql,
	).CombinedOutput()
	require.NoError(t, err, "cqlsh query failed: cql=%q stderr=%s", cql, string(out))
	// cqlsh output format:
	//  count
	// -------
	//      1
	// (1 rows)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "count") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "(") {
			continue
		}
		var count int
		if _, err := fmt.Sscanf(line, "%d", &count); err == nil {
			return count
		}
	}
	t.Fatalf("cassandraRowCount: could not parse count from cqlsh output: %q", string(out))
	return 0
}

// requireNoOrphanPodsInSRNamespaces asserts that no pods remain in any sr-* namespace
// that are not in Running/Succeeded state. Orphan pods (Pending/Unknown/Failed) indicate
// a botched partial deploy.
func requireNoOrphanPodsInSRNamespaces(t *testing.T) {
	t.Helper()
	out, err := exec.Command("kubectl", "get", "pods",
		"--all-namespaces",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace} {.metadata.name} {.status.phase}\n{end}",
	).Output()
	require.NoError(t, err)
	var orphans []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		ns, name, phase := fields[0], fields[1], fields[2]
		if !strings.HasPrefix(ns, "sr-") {
			continue
		}
		if phase != "Running" && phase != "Succeeded" {
			orphans = append(orphans, fmt.Sprintf("%s/%s (phase=%s)", ns, name, phase))
		}
	}
	require.Empty(t, orphans, "orphan pods found in sr-* namespaces: %v", orphans)
}

func TestT1_CleanRerun(t *testing.T) {
	// Pre-condition: up has succeeded once. We run it again and assert idempotency.
	controlPlaneStack, computePlaneStack := stackPaths(t)
	out, errb, exit := nvcf(t, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.Zero(t, exit, "stdout=%q stderr=%q", out, errb)

	// Capture pod UIDs.
	preUIDs := podUIDs(t)

	// Re-run.
	out2, errb2, exit2 := nvcf(t, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.Zero(t, exit2, "stdout=%q stderr=%q", out2, errb2)

	// Assert: helmfile reports nothing changed.
	assert.Contains(t, errb2, "Identical, nothing to update", "expected helmfile no-op summary")

	// Assert: no pods rolled (UIDs unchanged).
	postUIDs := podUIDs(t)
	assert.Equal(t, preUIDs, postUIDs, "pod UIDs changed across idempotent re-run")
}

func TestT2_InterruptedControlPlane(t *testing.T) {
	// Setup: tear down to a clean slate.
	teardownCluster(t)
	controlPlaneStack, computePlaneStack := stackPaths(t)

	// Action: start `up`, wait for the control-plane phase to begin, kill it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.NoError(t, cmd.Start())
	// Give it time to start at least one helmfile-apply phase, then kill.
	time.Sleep(45 * time.Second)
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	_ = cmd.Wait()

	// Assert: re-run converges.
	_, errb, exit := nvcf(t, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.Zero(t, exit, "errb=%q", errb)
	// Verify NVCFBackend reaches healthy.
	requireBackendHealthy(t, clusterName)
}

func TestT3_InterruptedRegister(t *testing.T) {
	// Setup: tear down to a clean slate.
	teardownCluster(t)
	controlPlaneStack, computePlaneStack := stackPaths(t)

	// Action: start `up`, wait long enough for the control-plane apply to finish
	// but interrupt before the register call completes (~90s is past helmfile-apply
	// but before the OIDC registration RPC returns).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.NoError(t, cmd.Start())
	// SIGINT timing is environment-specific (depends on cassandra/openbao bootstrap speed).
	// 90s targets the post-control-plane register window on mcamp-dev-vm but may
	// land mid-control-plane on slower hosts. First-run failures should adjust this
	// constant or replace with a deterministic readiness poll (e.g., wait for
	// `kubectl get nvcfbackend` to exist before SIGINT).
	time.Sleep(90 * time.Second)
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	_ = cmd.Wait()

	// Assert: re-run converges.
	_, errb, exit := nvcf(t, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.Zero(t, exit, "errb=%q", errb)

	// Assert: exactly one OIDC registration row (idempotent upsert, not duplicate insert).
	count := cassandraRowCount(t, "sis_api", "cluster_oidc_by_cluster_id", "")
	require.Equal(t, 1, count, "expected exactly 1 cluster_oidc_by_cluster_id row; got %d", count)

	// Assert: backend healthy.
	requireBackendHealthy(t, clusterName)
}

func TestT4_InterruptedComputePlane(t *testing.T) {
	// Setup: tear down to a clean slate.
	teardownCluster(t)
	controlPlaneStack, computePlaneStack := stackPaths(t)

	// Action: start `up`, wait long enough for the control-plane + register phases to
	// finish, then interrupt during compute-plane helmfile apply (~150s).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.NoError(t, cmd.Start())
	// SIGINT timing is environment-specific (depends on cassandra/openbao bootstrap speed).
	// 150s targets the post-control-plane+register window on mcamp-dev-vm but may
	// land mid-register on slower hosts. First-run failures should adjust this
	// constant or replace with a deterministic readiness poll (e.g., wait for
	// `kubectl get nvcfbackend` to exist before SIGINT).
	time.Sleep(150 * time.Second)
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	_ = cmd.Wait()

	// Assert: re-run converges.
	_, errb, exit := nvcf(t, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
	require.Zero(t, exit, "errb=%q", errb)

	// Assert: nvca operator + agent reach Ready in nvca-operator namespace.
	out, err := exec.Command("kubectl", "get", "pods",
		"-n", "nvca-operator",
		"-o", "jsonpath={range .items[*]}{.metadata.name} {.status.conditions[?(@.type==\"Ready\")].status}\n{end}",
	).Output()
	require.NoError(t, err)
	notReady := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] != "True" {
			notReady = append(notReady, fields[0])
		}
	}
	require.Empty(t, notReady, "pods not Ready in nvca-operator after re-run: %v", notReady)

	// Assert: no orphan pods remain in sr-* namespaces.
	requireNoOrphanPodsInSRNamespaces(t)

	// Assert: backend healthy.
	requireBackendHealthy(t, clusterName)
}

func TestT5_VersionUpgrade(t *testing.T) {
	controlPlaneV1, computePlaneV1 := stackPaths(t)
	controlPlaneV2, computePlaneV2 := nextStackPaths()
	if controlPlaneV2 == "" || computePlaneV2 == "" {
		t.Skip("set CONTROL_PLANE_STACK_PATH_NEXT and COMPUTE_PLANE_STACK_PATH_NEXT to run version-upgrade test")
		return
	}

	// Step 1: baseline deploy with v1 stack.
	_, errb, exit := nvcf(t, upArgs(clusterName, controlPlaneV1, computePlaneV1)...)
	require.Zero(t, exit, "baseline up failed: %s", errb)

	// Capture pre-upgrade pod UIDs to detect which pods rolled.
	preUIDs := podUIDs(t)

	// Step 2: upgrade to v2 stack.
	_, errb2, exit2 := nvcf(t, upArgs(clusterName, controlPlaneV2, computePlaneV2)...)
	require.Zero(t, exit2, "upgrade up failed: %s", errb2)

	// Assert: helmfile reports "Updated" for at least one release (upgrade happened).
	assert.Contains(t, errb2, "Updated", "expected at least one helmfile 'Updated' on version upgrade")

	// Assert: pods that didn't change don't roll (UIDs stable for unchanged pods).
	// We verify that not ALL pods rolled — at least the non-upgraded components are stable.
	postUIDs := podUIDs(t)
	rolledCount := 0
	for key, uid := range preUIDs {
		if postUIDs[key] != uid {
			rolledCount++
		}
	}
	t.Logf("version upgrade rolled %d pod(s) out of %d", rolledCount, len(preUIDs))
	// At least one pod should roll (the upgrade did something), but not all
	// (stable components should be unchanged).
	require.Greater(t, rolledCount, 0, "expected at least one pod to roll on upgrade")
	require.Less(t, rolledCount, len(preUIDs), "expected some pods to remain stable on upgrade")

	// Assert: backend healthy after upgrade.
	requireBackendHealthy(t, clusterName)
}

// TestT6_ConcurrentUp verifies that when two `nvcf-cli self-hosted up` invocations
// are launched concurrently against the same cluster, exactly one wins (exits 0)
// and the other is rejected immediately with exit code 1 and an error message
// containing "another nvcf-cli self-hosted up is in progress". The cluster
// ends in a healthy state after the winner finishes.
//
// Implementation: the orchestrator acquires a Kubernetes Lease-based lock
// (internal/selfhosted/installlock) after preflight and before any cluster
// mutations. The loser detects ErrAlreadyHeld and exits 1 with a user-friendly
// message containing the winner's hostname, PID, and start-time.
func TestT6_ConcurrentUp(t *testing.T) {
	controlPlaneStack, computePlaneStack := stackPaths(t)

	type result struct {
		stdout, stderr string
		exit           int
	}
	run := func() result {
		out, errb, exit := nvcf(t, upArgs(clusterName, controlPlaneStack, computePlaneStack)...)
		return result{out, errb, exit}
	}

	resultCh := make(chan result, 2)
	go func() { resultCh <- run() }()
	go func() { resultCh <- run() }()

	r1 := <-resultCh
	r2 := <-resultCh

	winners := 0
	losers := 0
	for _, r := range []result{r1, r2} {
		switch r.exit {
		case 0:
			winners++
		case 1:
			losers++
			// The loser must surface the holder identity so operators can diagnose.
			assert.Contains(t, r.stderr+r.stdout, "another nvcf-cli self-hosted up is in progress",
				"loser exit-1 message should explain the lock conflict")
		default:
			t.Errorf("unexpected exit code %d (stdout=%q stderr=%q)", r.exit, r.stdout, r.stderr)
		}
	}

	assert.Equal(t, 1, winners, "exactly one concurrent up should succeed")
	assert.Equal(t, 1, losers, "exactly one concurrent up should be rejected by the lock")

	// Cluster must be healthy after the winner completes.
	requireBackendHealthy(t, clusterName)
}

func TestT7_NoApplyRoundTrip(t *testing.T) {
	// Pre-condition: `up` has succeeded. We render manifests with --no-apply and
	// assert that `kubectl apply` reports no changes (YAML-layer idempotency).
	controlPlaneStack, computePlaneStack := stackPaths(t)

	// Render control-plane manifests.
	cpFile := t.TempDir() + "/control-plane.yaml"
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, cliBin,
			"self-hosted", "install",
			"--control-plane",
			"--no-apply",
			"--cluster-name="+clusterName,
			"--control-plane-stack="+controlPlaneStack,
			"--compute-plane-stack="+computePlaneStack,
			"--env=local",
			"--token="+os.Getenv("NVCF_TOKEN"),
		)
		outBytes, err := cmd.Output()
		require.NoError(t, err, "render control-plane --no-apply failed")
		require.NoError(t, os.WriteFile(cpFile, outBytes, 0o600))
	}

	// Render compute-plane manifests.
	compFile := t.TempDir() + "/compute-plane.yaml"
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, cliBin,
			"self-hosted", "install",
			"--compute-plane",
			"--no-apply",
			"--cluster-name="+clusterName,
			"--control-plane-stack="+controlPlaneStack,
			"--compute-plane-stack="+computePlaneStack,
			"--env=local",
			"--token="+os.Getenv("NVCF_TOKEN"),
		)
		outBytes, err := cmd.Output()
		require.NoError(t, err, "render compute-plane --no-apply failed")
		require.NoError(t, os.WriteFile(compFile, outBytes, 0o600))
	}

	// First apply may report 'configured' due to field-manager drift between
	// Helm (the original installer) and kubectl (the no-apply round-trip). The
	// real idempotency check is whether a SECOND apply produces all 'unchanged'.
	firstOut, _ := exec.Command("kubectl", "apply", "-f", cpFile, "-f", compFile).CombinedOutput()
	t.Logf("first apply absorbed field-manager drift:\n%s", firstOut)

	secondOut, err := exec.Command("kubectl", "apply", "-f", cpFile, "-f", compFile).CombinedOutput()
	require.NoError(t, err, "second kubectl apply failed: %s", secondOut)

	t.Logf("second apply output:\n%s", secondOut)

	// Assert: every line of the second apply is 'unchanged'.
	for _, line := range strings.Split(strings.TrimSpace(string(secondOut)), "\n") {
		if line == "" {
			continue
		}
		assert.Contains(t, line, "unchanged",
			"second-apply line did not say 'unchanged': %q", line)
	}
}
