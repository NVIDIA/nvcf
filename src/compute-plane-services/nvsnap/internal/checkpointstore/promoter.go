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

// Storage-agnostic L2 promotion (nvsnap#171).
//
// PerCapturePVCBackend owns the storage-AGNOSTIC orchestration of the L2
// tier: hash-keyed lease serialization, the single-copy write phase
// (RWO writer PVC + mount-holder copy), the detach durability barrier,
// the pvc_promote_state machine, and namespace scoping. The one part
// that is storage-SPECIFIC — turning the populated writer volume into a
// durable artifact N restore pods can read, and rendering the pod-spec
// to consume it — is delegated to a Promoter.
//
// Strategies (see docs/design/STORAGE-AGNOSTIC-L2-PROMOTION.md):
//   - SnapshotClonePromoter: CSI VolumeSnapshot → clone. Shared ROX PVC
//     where the SC supports ReadOnlyMany (Hyperdisk-ML); per-pod RWO
//     clone where it doesn't (EBS).
//   - SharedVolumePromoter: zero-copy — the same backing volume is
//     RO-attached to N pods via static PVs (NVMesh, EFS, Filestore).
//
// The Promoter is selected per L2 StorageClass at agent startup from a
// provisioner[/parameters.type] profile table (built-in ⊕ ConfigMap).

package checkpointstore

import "context"

// PromoteInput is what PerCapturePVCBackend hands a Promoter once the
// writer PVC (rwx-<hash>) is populated and detached. The Promoter turns
// it into the durable restore artifact for Hash.
type PromoteInput struct {
	// Hash is the content-addressed capture identity.
	Hash string
	// Namespace is where the writer PVC lives and where the durable
	// artifact must be created (PVCs are namespace-scoped; restore pods
	// can only mount same-namespace claims — nvsnap#82).
	Namespace string
	// WriterPVCName is the populated, detached RWO writer PVC
	// (rwx-<hash>). The Promoter snapshots it (snapshot-clone) or
	// retains+reuses its backing volume (shared-volume).
	WriterPVCName string
	// SizeBytes is the writer PVC's requested size; the Promoter sizes
	// the durable artifact at least this large.
	SizeBytes int64
}

// PromoteResult tells the orchestrator how restore consumes the result
// so it can record the correct pvc_promote_state + catalog rox_name.
type PromoteResult struct {
	// SharedClaimName is the single PVC every restore pod mounts
	// read-only (snapshot-clone with ROX, or shared-volume). Empty when
	// PerPod is true.
	SharedClaimName string
	// PerPod is true when each restore pod gets its own artifact created
	// lazily in MountSpec (RWO-only storage, e.g. EBS). The catalog
	// records the underlying snapshot/dataSource instead of a shared PVC.
	PerPod bool
	// ReusedWriterVolume is true when no copy happened (shared-volume) —
	// informational for metrics/logs.
	ReusedWriterVolume bool
}

// StorageCaps advertises what a Promoter's backing storage supports, for
// startup validation + logging.
type StorageCaps struct {
	// Snapshots: a CSI VolumeSnapshotClass is available for the driver.
	Snapshots bool
	// ReadOnlyMany: a single volume can bind ReadOnlyMany across nodes
	// (one shared rox PVC for N readers). False ⇒ per-pod clone.
	ReadOnlyMany bool
	// SharedVolume: the same backing volume can be RO-attached N times
	// (zero-copy fan-out via static PVs).
	SharedVolume bool
	// Strategy names the promoter: "snapshot-clone" | "shared-volume" |
	// "copy-per-pod". Logged at startup.
	Strategy string
}

// Promoter is the storage-specific half of the L2 tier (nvsnap#171). One
// implementation per storage strategy; selected per L2 StorageClass.
type Promoter interface {
	// Promote turns the populated, detached writer PVC into the durable
	// restore artifact for in.Hash. Called once per hash by the lease
	// winner, after the write phase + detach barrier. Idempotent: a
	// retry after a partial promote must converge, not duplicate.
	Promote(ctx context.Context, in PromoteInput) (PromoteResult, error)

	// MountSpec returns the Volume + VolumeMount a restore pod uses to
	// read the promoted artifact for hash. Per-pod strategies create the
	// per-pod artifact here (keyed by vol so each pod gets its own);
	// shared strategies return the one shared claim. Returns ErrNotFound
	// when no promoted artifact exists for hash.
	MountSpec(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error)

	// Delete reclaims every artifact this strategy created for hash in
	// the given namespace. Idempotent (NotFound == success).
	Delete(ctx context.Context, hash, namespace string) error

	// Caps advertises capabilities for startup validation + logging.
	Caps() StorageCaps
}
