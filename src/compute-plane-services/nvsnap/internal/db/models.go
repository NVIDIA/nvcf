/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package db

import "time"

// Checkpoint represents a checkpoint record in the catalog.
type Checkpoint struct {
	ID             string `json:"id"`
	CheckpointID   string `json:"checkpointId"`
	Namespace      string `json:"namespace"`
	PodName        string `json:"podName"`
	ContainerName  string `json:"containerName,omitempty"`
	ContainerImage string `json:"containerImage,omitempty"`
	NodeName       string `json:"nodeName"`
	CheckpointPath string `json:"checkpointPath,omitempty"`
	CheckpointSize int64  `json:"checkpointSize"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
	HasGPU         bool   `json:"hasGpu"`
	ModelName      string `json:"modelName,omitempty"`
	WorkloadType   string `json:"workloadType,omitempty"`

	// CaptureMethod ("rootfs" | "criu") is NOT a stored column — the
	// server fills it at read time from the capture manifest so the UI
	// can show how a capture was taken. Empty when no manifest is found.
	CaptureMethod string `json:"captureMethod,omitempty"`

	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	DurationSecs float64    `json:"durationSecs"`

	// CatalogInfo (nvsnap#59). Populated by the agent's register
	// call from its on-disk metadata.json. Indexed columns (Hash,
	// ImageRef, FunctionVersionID) back the content-addressed
	// lookup endpoint. Two captures of the same canonical content
	// (image + model + flags + driver major) hash equal — that's
	// the key NVCA's Hook A queries to dedup across fvIDs.
	Hash              string   `json:"hash,omitempty"`
	ImageRef          string   `json:"imageRef,omitempty"`
	ImageDigest       string   `json:"imageDigest,omitempty"`
	ModelID           string   `json:"modelId,omitempty"`
	EngineFlags       []string `json:"engineFlags,omitempty"`
	GPUType           string   `json:"gpuType,omitempty"`
	GPUCount          int      `json:"gpuCount,omitempty"`
	DriverVersion     string   `json:"driverVersion,omitempty"`
	DriverMajor       int      `json:"driverMajor,omitempty"`
	CUDAVersion       string   `json:"cudaVersion,omitempty"`
	CPUArchitecture   string   `json:"cpuArchitecture,omitempty"`
	FunctionName      string   `json:"functionName,omitempty"`
	FunctionVersionID string   `json:"functionVersionId,omitempty"`

	// L2 per-capture PVC (nvsnap#63). Driven by the
	// PerCapturePVCBackend's Put state machine; the agent's register
	// call never sets these. See migration 5 for the column semantics
	// and docs/L2-PVC-CRIU-DESIGN.md for the lifecycle.
	PVCName         string `json:"pvcName,omitempty"`
	PVCPromoteState string `json:"pvcPromoteState,omitempty"`
}

// PVC promote-state values. Restore-side polls these via
// /api/v1/checkpoints/lookup; the nvsnap-init container blocks until it
// sees "ready" or "failed" before exec'ing restore-entrypoint. Exact
// strings are part of the wire contract — don't rename without bumping
// the migration.
const (
	PVCPromoteStatePending      = "pending"
	PVCPromoteStateWriting      = "writing"
	PVCPromoteStateSnapshotting = "snapshotting"
	PVCPromoteStateReady        = "ready"
	PVCPromoteStateFailed       = "failed"
)

// LookupCriteria matches catalog rows by content identity. ImageRef
// is required (the primary indexed lookup key); the other fields
// narrow the match. Empty fields (other than ImageRef) match
// anything. EngineFlags is canonicalized server-side (sorted, with
// --model* stripped) before comparison.
//
// See nvsnap#59 for the design rationale; NVCA's Hook A uses this
// to dedup checkpoints across function-version-IDs.
type LookupCriteria struct {
	ImageRef    string   // required
	ModelID     string   // optional
	EngineFlags []string // optional, canonicalized server-side
	DriverMajor int      // optional (0 = match anything)
	Limit       int      // 0 = default 10, max 100
}

// Restore represents a restore record in the catalog.
type Restore struct {
	ID            string     `json:"id"`
	CheckpointID  string     `json:"checkpointId"`
	CheckpointRef string     `json:"checkpointRef,omitempty"`
	Namespace     string     `json:"namespace"`
	NodeName      string     `json:"nodeName"`
	NewPodName    string     `json:"newPodName,omitempty"`
	Status        string     `json:"status"`
	Message       string     `json:"message,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
	DurationSecs  float64    `json:"durationSecs"`
}

// DemoState represents the persisted demo session state.
type DemoState struct {
	Phase           string  `json:"phase"`
	WorkloadType    string  `json:"workloadType"`
	PodName         string  `json:"podName"`
	NodeName        string  `json:"nodeName"`
	Message         string  `json:"message"`
	Error           string  `json:"error"`
	DeployDuration  float64 `json:"deployDuration"`
	CkptDuration    float64 `json:"ckptDuration"`
	RestoreDuration float64 `json:"restoreDuration"`
	StartedAt       *string `json:"startedAt,omitempty"`
	CheckpointsJSON string  `json:"checkpointsJson"`
}

// CheckpointFilter holds optional filters for listing checkpoints.
type CheckpointFilter struct {
	Namespace    string
	PodName      string
	NodeName     string
	Status       string
	WorkloadType string
	HasGPU       *bool  // nil = no filter
	Cursor       string // created_at cursor for pagination (RFC3339)
	Limit        int    // max results (0 = unlimited)
	SortOrder    string // "asc" or "desc" (default "desc")
}

// PagedResult wraps a list result with pagination metadata.
type PagedResult[T any] struct {
	Items      []T    `json:"items"`
	Total      int    `json:"total"`
	NextCursor string `json:"nextCursor,omitempty"`
	HasMore    bool   `json:"hasMore"`
}

// RestoreFilter holds optional filters for listing restores.
type RestoreFilter struct {
	Namespace    string
	CheckpointID string
	NodeName     string
	Status       string
	Cursor       string
	Limit        int
}

// RetentionPolicy defines automatic checkpoint cleanup rules.
type RetentionPolicy struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Namespace     string    `json:"namespace"`     // "*" = all namespaces
	WorkloadType  string    `json:"workloadType"`  // "*" = all workload types
	MaxCount      int       `json:"maxCount"`      // 0 = unlimited
	MaxAgeHours   int       `json:"maxAgeHours"`   // 0 = unlimited
	MaxTotalBytes int64     `json:"maxTotalBytes"` // 0 = unlimited
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// AuditEntry records a single operation in the audit trail.
type AuditEntry struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Action     string    `json:"action"`   // "checkpoint.create", "checkpoint.delete", "restore.create", etc.
	Resource   string    `json:"resource"` // "checkpoint", "restore", "policy"
	ResourceID string    `json:"resourceId"`
	Actor      string    `json:"actor"`   // "api", "system", "policy:<name>"
	Details    string    `json:"details"` // JSON blob with extra context
	Status     string    `json:"status"`  // "success", "failed"
	Message    string    `json:"message"`
}

// AuditFilter holds optional filters for listing audit entries.
type AuditFilter struct {
	Action     string
	Resource   string
	ResourceID string
	Actor      string
	Since      string // RFC3339
	Limit      int
}
