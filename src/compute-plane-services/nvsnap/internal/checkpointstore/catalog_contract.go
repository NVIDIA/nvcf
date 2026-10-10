// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package checkpointstore

// The catalog contract shared by the agent and the server.
const (
	// CatalogCheckpointIDHeader carries the id the catalog holds a
	// registered checkpoint under: another id when a row of the same
	// content hash existed.
	CatalogCheckpointIDHeader = "X-Nvsnap-Checkpoint-Id"
	// InstanceCaptureWorkloadType marks the catalog rows of a workload
	// instance's capture, one per rank. Such a rank restores only with its
	// instance, through the agent's group record, never from a lookup.
	InstanceCaptureWorkloadType = "criu-instance"
	// InstanceCaptureMetaKey marks such a rank's capture manifest
	// (SourcePodMeta), so a catalog row rebuilt from it is marked too.
	InstanceCaptureMetaKey = "instance_capture"
)
