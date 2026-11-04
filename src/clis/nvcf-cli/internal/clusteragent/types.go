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

// Package clusteragent inspects the NVCF cluster agent (NVCA) running on a
// compute-plane cluster. It reads the NVCFBackend and ICMSRequest custom
// resources and exposes a small, CLI-owned domain model so callers never touch
// the raw CRD schemas.
//
// All reads go through the AgentInspector interface. Today the only
// implementation is the Kubernetes dynamic-client adapter (k8s_inspector.go),
// but the interface is the seam where a future NVCA HTTP introspection endpoint
// or a control-plane data source can be swapped in without touching the cobra
// handlers, the output formatters, or the phase-mapping logic.
package clusteragent

import "context"

// Phase is the collapsed, user-facing lifecycle phase for a scheduled function.
// The NVCA tracks eight granular ICMSRequest statuses; DerivePhase folds them
// into these.
type Phase string

const (
	PhaseDeploying Phase = "DEPLOYING"
	PhaseActive    Phase = "ACTIVE"
	PhaseDraining  Phase = "DRAINING"
	// PhaseFailed covers RequestFailed/RequestFailureAcknowledged.
	PhaseFailed Phase = "FAILED"
)

// GPUUsage is one GPU type's capacity on the cluster, from NVCFBackend
// status.gpuUsage.
type GPUUsage struct {
	Name      string `json:"name"`
	Capacity  int64  `json:"capacity"`
	Available int64  `json:"available"`
	Allocated int64  `json:"allocated"`
}

// ICMSInfo holds the control-plane (ICMS) view of the cluster. It enriches the
// CR-derived status. Available is false when enrichment was skipped or failed;
// in that case Note explains why and the rest of the command still succeeds.
type ICMSInfo struct {
	Available         bool   `json:"available"`
	ClusterName       string `json:"clusterName,omitempty"`
	NVCAVersion       string `json:"nvcaVersion,omitempty"`
	ClusterStatus     string `json:"clusterStatus,omitempty"`
	NVCALastConnected string `json:"nvcaLastConnected,omitempty"`
	Note              string `json:"note,omitempty"`
}

// AgentStatus is the NVCA status for a single compute-plane cluster, merged
// from the NVCFBackend CR and (optionally) SIS.
type AgentStatus struct {
	ComputePlaneContext string     `json:"computePlaneContext,omitempty"`
	Namespace           string     `json:"namespace"`
	ClusterID           string     `json:"clusterId,omitempty"`
	ClusterName         string     `json:"clusterName,omitempty"`
	NVCAVersion         string     `json:"nvcaVersion"`
	AgentHealth         string     `json:"agentStatus"`
	KubernetesVersion   string     `json:"kubernetesVersion,omitempty"`
	LastUpdated         string     `json:"lastUpdated,omitempty"`
	GPU                 []GPUUsage `json:"gpu,omitempty"`
	ControlPlane        *ICMSInfo   `json:"controlPlane,omitempty"`
}

// Instance is one running instance backing a scheduled function, from
// ICMSRequest status.instances.
type Instance struct {
	ID                    string            `json:"id"`
	Type                  string            `json:"type,omitempty"`
	Status                string            `json:"status,omitempty"`
	LastReportedStatus    string            `json:"lastReportedStatus,omitempty"`
	LastReportedTimestamp string            `json:"lastReportedTimestamp,omitempty"`
	Attributes            map[string]string `json:"attributes,omitempty"`
}

// ScheduledFunction is one function version scheduled on the cluster, derived
// from an ICMSRequest CR.
type ScheduledFunction struct {
	FunctionID        string `json:"functionId"`
	FunctionVersionID string `json:"functionVersionId"`
	Namespace         string `json:"namespace"`
	Action            string `json:"action,omitempty"`
	RequestStatus     string `json:"requestStatus"`
	Phase             Phase  `json:"phase"`
	InstanceCount     int    `json:"instanceCount"`
}

// FunctionDetail is the detailed view of one scheduled function version.
type FunctionDetail struct {
	ScheduledFunction
	Instances          []Instance `json:"instances,omitempty"`
	LastStatusUpdated  string     `json:"lastStatusUpdated,omitempty"`
	LastACKTimestamp   string     `json:"lastAckTimestamp,omitempty"`
	LastReconcileError string     `json:"lastReconcileError,omitempty"`
	ReconcileErrors    uint64     `json:"reconcileErrors,omitempty"`
}

// ListOptions controls ListFunctions.
type ListOptions struct {
	// Namespace scopes the ICMSRequest listing. Empty means all namespaces.
	Namespace string
	// PhaseFilter, when set, restricts results to a single phase.
	PhaseFilter Phase
}

// AgentInspector reads NVCA state from a compute-plane cluster. Implementations
// return domain types only, never raw Kubernetes objects.
type AgentInspector interface {
	// Status returns the NVCA agent status from the NVCFBackend CR in namespace.
	Status(ctx context.Context, namespace string) (*AgentStatus, error)
	// ListFunctions returns the function versions scheduled on the cluster.
	ListFunctions(ctx context.Context, opts ListOptions) ([]ScheduledFunction, error)
	// GetFunction returns the detailed state for one function version. An empty
	// versionID matches the first request for the function regardless of version.
	GetFunction(ctx context.Context, functionID, versionID string) (*FunctionDetail, error)
}
