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
	// CRIUScopeMetaKey records the tenant scope of the namespace a CRIU
	// capture was taken in (SourcePodMeta). A CRIU checkpoint holds the
	// process's memory, so it is restored only into a pod of the same
	// scope.
	CRIUScopeMetaKey = "criu_scope"
)
