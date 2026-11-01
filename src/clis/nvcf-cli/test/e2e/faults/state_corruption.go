//go:build e2e

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

// Package faults provides helpers for fault-injection E2E tests (T11-T17).
package faults

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// TruncateOCICache truncates the cached helmfile.d.tar.gz for the given OCI
// digest to 100 bytes, making it unreadable as a valid tar archive. The next
// `nvcf-cli self-hosted up` invocation should detect the corruption, emit
// phase_failed{errCategory:"cache_corruption", retryClass:"immediate"}, remove
// the entry, and re-fetch.
//
// Precondition: a successful `up` must have already populated the cache.
// homeDir is typically os.UserHomeDir(). digest is the OCI layer digest (e.g.
// "sha256:abc123...") used to locate the cache subdirectory.
func TruncateOCICache(homeDir, digest string) error {
	target := filepath.Join(homeDir, ".cache", "nvcf-cli", "stacks", digest, "helmfile.d.tar.gz")
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("OCI cache file not found at %s (run a successful `up` first): %w", target, err)
	}
	if info.Size() < 100 {
		// Already truncated or suspiciously small — treat as no-op so the test
		// doesn't pass trivially when the file never existed.
		return fmt.Errorf("cache file at %s is already <100 bytes (%d); is this a valid cache entry?", target, info.Size())
	}
	if err := os.Truncate(target, 100); err != nil {
		return fmt.Errorf("truncate %s: %w", target, err)
	}
	return nil
}

// ForceHelmPendingUpgrade starts `helm upgrade --atomic <release> <chart>` in
// the given namespace and kills it after a brief delay, leaving the release in
// the "pending-upgrade" state. The next helmfile apply by the orchestrator
// should detect this and emit phase_failed{errCategory:"helm_pending_upgrade",
// retryClass:"after_remediation"} with rollback instructions.
//
// Precondition: helm must be on PATH and the release must already exist (i.e.
// control plane was successfully installed at least once).
func ForceHelmPendingUpgrade(release, chart, namespace string) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("helm not on PATH: %w", err)
	}
	cmd := exec.Command("helm", "upgrade", "--atomic", release, chart, "-n", namespace)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start helm upgrade: %w", err)
	}
	// Give helm enough time to begin the release transaction (create the
	// pending-upgrade entry in the release history) before we kill it.
	time.Sleep(1500 * time.Millisecond)
	if err := cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill helm upgrade: %w", err)
	}
	_ = cmd.Wait() // wait returns non-nil because we killed it; expected
	return nil
}

// KillCassandraMigrationJob force-deletes all pods belonging to the named
// Kubernetes Job in the given namespace (grace period 0). This causes
// helmfile's waitForJobs to time out, simulating a hung Cassandra migration
// lock. The orchestrator should emit phase_failed{errCategory:
// "cassandra_migration_lock", retryClass:"after_remediation"}.
//
// Precondition: kubectl must be on PATH and the job must be running (i.e.
// control plane install is in progress at the point this is called).
func KillCassandraMigrationJob(namespace, jobName string) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not on PATH: %w", err)
	}
	cmd := exec.Command("kubectl", "delete", "pod",
		"-n", namespace,
		"-l", "job-name="+jobName,
		"--grace-period=0", "--force",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl delete pod -l job-name=%s: %w", jobName, err)
	}
	return nil
}

// InsertHalfRegisteredSISRow inserts a row into sis.clusters_by_account_id
// WITHOUT the corresponding cluster_oidc_by_cluster_id row, simulating a
// partial write that left the SIS state inconsistent. The orchestrator should
// detect this during the register phase, emit phase_progress{reason:
// "partial_sis_write detected; reconciling"}, and converge without any manual
// cleanup.
//
// This helper connects via cqlsh directly to the Cassandra instance (host:port
// must be reachable from the test host — on mcamp-dev-vm the Cassandra
// NodePort is typically exposed). It will return an error if cqlsh is not on
// PATH.
//
// WARNING: runs against a live Cassandra instance. Use only on dev-VM clusters.
func InsertHalfRegisteredSISRow(host, port, ncaID, clusterName string) error {
	if _, err := exec.LookPath("cqlsh"); err != nil {
		return fmt.Errorf("cqlsh not on PATH (install cassandra-tools or use kubectl exec): %w", err)
	}
	// Insert only the clusters_by_account_id row; deliberately skip
	// cluster_oidc_by_cluster_id to simulate the half-registered state.
	cql := fmt.Sprintf(
		`INSERT INTO sis.clusters_by_account_id (account_id, cluster_name, cluster_id) VALUES ('%s', '%s', uuid());`,
		ncaID, clusterName,
	)
	cmd := exec.Command("cqlsh", host, port, "-e", cql)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cqlsh INSERT half-registered row (ncaID=%s cluster=%s): %w", ncaID, clusterName, err)
	}
	return nil
}
