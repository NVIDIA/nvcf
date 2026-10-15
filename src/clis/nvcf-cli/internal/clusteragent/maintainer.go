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

package clusteragent

import (
	"context"
	"time"
)

// AgentMaintainer performs maintenance mutations against a compute-plane
// cluster's NVCA. It is the write-side counterpart to AgentInspector: drain and
// undrain toggle CordonAndDrain maintenance on the NVCA agent-config ConfigMap
// (which the operator picks up on a rollout restart), and the kill operations
// delete ICMSRequest CRs so the NVCA reconciler evicts the workloads.
//
// The Kubernetes implementation (k8s_maintainer.go) mirrors the proven operator
// logic in nvca/pkg/operator/cleanup/cleanup.go. The interface is the seam where
// a future NVCA HTTP maintenance endpoint can be swapped in without touching the
// cobra handlers.
type AgentMaintainer interface {
	// ResolveCluster reads the NVCFBackend CR in backendNS and returns the
	// cluster identity and the system/requests namespaces, applying defaults for
	// any field the CR leaves empty. It is the common preamble for every
	// maintenance operation: the identity feeds the optional cluster guard and
	// the kill-all confirmation, and the namespaces target the writes.
	ResolveCluster(ctx context.Context, backendNS string) (*ClusterTarget, error)
	// Drain puts the cluster into CordonAndDrain maintenance.
	Drain(ctx context.Context, opts DrainOptions) (*DrainResult, error)
	// Undrain reverses Drain, returning the cluster to normal operation.
	Undrain(ctx context.Context, opts DrainOptions) (*DrainResult, error)
	// KillFunction force-terminates every ICMSRequest matching functionID (and
	// versionID, when non-empty). An empty versionID matches all versions.
	KillFunction(ctx context.Context, functionID, versionID string, opts KillOptions) (*KillResult, error)
	// KillAll force-terminates every ICMSRequest on the cluster.
	KillAll(ctx context.Context, opts KillOptions) (*KillResult, error)
}

// ClusterTarget is the NVCFBackend-derived identity and namespace layout of a
// compute-plane cluster.
type ClusterTarget struct {
	ClusterID         string `json:"clusterId,omitempty"`
	ClusterName       string `json:"clusterName,omitempty"`
	SystemNamespace   string `json:"systemNamespace"`
	RequestsNamespace string `json:"requestsNamespace"`
}

// DrainOptions controls Drain and Undrain.
type DrainOptions struct {
	// BackendNS is the namespace holding the NVCFBackend CR.
	BackendNS string
	// ExpectClusterID, when non-empty, must match the live cluster's ID or name
	// or the operation is refused. Empty means trust the selected context.
	ExpectClusterID string
	// DryRun reports the intended change without mutating the cluster.
	DryRun bool
	// Force skips waiting for the NVCA rollout to complete.
	Force bool
	// Timeout bounds the rollout wait. Zero (with Force false) skips the wait.
	Timeout time.Duration
}

// KillOptions controls KillFunction and KillAll.
type KillOptions struct {
	// BackendNS is the namespace holding the NVCFBackend CR.
	BackendNS string
	// ExpectClusterID, when non-empty, must match the live cluster's ID or name
	// or the operation is refused.
	ExpectClusterID string
	// Reason is an optional operator-supplied audit note recorded in logs.
	Reason string
	// DryRun reports what would be deleted without deleting anything.
	DryRun bool
	// Force strips finalizers before deleting, so a CR stuck Terminating is
	// removed even when NVCA is not running to process its finalizer.
	Force bool
}

// DrainResult is the outcome of a Drain or Undrain.
type DrainResult struct {
	ClusterID        string `json:"clusterId,omitempty"`
	ClusterName      string `json:"clusterName,omitempty"`
	SystemNamespace  string `json:"systemNamespace"`
	Mode             string `json:"mode"`
	ConfigChanged    bool   `json:"configChanged"`
	RolloutTriggered bool   `json:"rolloutTriggered"`
	RolloutComplete  bool   `json:"rolloutComplete"`
	DryRun           bool   `json:"dryRun"`
	Message          string `json:"message,omitempty"`
}

// KilledRequest is one ICMSRequest targeted by a kill operation. Error is set
// when that CR failed to delete; otherwise it was deleted (or would be, in a
// dry run).
type KilledRequest struct {
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
	FunctionID        string `json:"functionId,omitempty"`
	FunctionVersionID string `json:"functionVersionId,omitempty"`
	Error             string `json:"error,omitempty"`
}

// KillResult is the outcome of KillFunction or KillAll.
type KillResult struct {
	ClusterID         string          `json:"clusterId,omitempty"`
	ClusterName       string          `json:"clusterName,omitempty"`
	RequestsNamespace string          `json:"requestsNamespace"`
	Reason            string          `json:"reason,omitempty"`
	Affected          []KilledRequest `json:"affected"`
	FailedCount       int             `json:"failedCount"`
	DryRun            bool            `json:"dryRun"`
}
