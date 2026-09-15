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

package types //nolint:revive

// HelmCacheBackend identifies the storage backend selected for the Helm model
// cache. The backend is resolved from the CachingSupport gate plus the storage
// classes available in the cluster (see pkg/storage SelectHelmCacheBackend).
// It lives here so both pkg/storage and internal/metrics can share one set of
// backend values without duplication.
type HelmCacheBackend string

const (
	// HelmCacheBackendNone means caching is disabled (CachingSupport off).
	HelmCacheBackendNone HelmCacheBackend = "none"
	// HelmCacheBackendNVMesh uses NVMesh 3.x cross-namespace PV sharing.
	HelmCacheBackendNVMesh HelmCacheBackend = "nvmesh"
	// HelmCacheBackendSharedFS uses an operator-provided shared (RWX/ROX)
	// storage class (nvcf-miniservice-sc): WEKA, EFS, CephFS, external NFS, etc.
	HelmCacheBackendSharedFS HelmCacheBackend = "sharedfs"
	// HelmCacheBackendSamba deploys a Samba server (on block storage) and
	// caches via the shared-FS path.
	HelmCacheBackendSamba HelmCacheBackend = "samba"
	// HelmCacheBackendEphemeral is the per-pod emptyDir fallback when no
	// shared cache backend is available.
	HelmCacheBackendEphemeral HelmCacheBackend = "ephemeral"
)

// AllSelectableHelmCacheBackends is the set of backends that provision a
// cache (HelmCacheBackendNone excluded), used to pre-initialize per-backend
// metrics to zero.
var AllSelectableHelmCacheBackends = []HelmCacheBackend{
	HelmCacheBackendNVMesh,
	HelmCacheBackendSharedFS,
	HelmCacheBackendSamba,
	HelmCacheBackendEphemeral,
}
