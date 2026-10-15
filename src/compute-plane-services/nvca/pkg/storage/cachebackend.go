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

package storage

import (
	"context"
	"fmt"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// HelmCacheBackend identifies the storage backend selected for the Helm model
// cache. The backend is resolved from the CachingSupport gate plus the storage
// classes available in the cluster, replacing the old HelmCachingSupport flag.
// The type and values live in pkg/types so metrics can share them; they are
// aliased here for the storage-facing API.
type HelmCacheBackend = nvcatypes.HelmCacheBackend

const (
	// HelmCacheBackendNone means caching is disabled (CachingSupport off).
	HelmCacheBackendNone = nvcatypes.HelmCacheBackendNone
	// HelmCacheBackendNVMesh uses NVMesh 3.x cross-namespace PV sharing
	// (the existing doModelCacheNVMesh path).
	HelmCacheBackendNVMesh = nvcatypes.HelmCacheBackendNVMesh
	// HelmCacheBackendSharedFS uses an operator-provided shared (RWX/ROX)
	// storage class (nvcf-miniservice-sc): WEKA, EFS, CephFS, external NFS, etc.
	HelmCacheBackendSharedFS = nvcatypes.HelmCacheBackendSharedFS
	// HelmCacheBackendSamba deploys a Samba server (on block storage) and
	// creates nvcf-miniservice-sc pointing at it, then caches via the shared-FS path.
	HelmCacheBackendSamba = nvcatypes.HelmCacheBackendSamba
	// HelmCacheBackendEphemeral is the per-pod emptyDir fallback when no
	// shared cache backend is available.
	HelmCacheBackendEphemeral = nvcatypes.HelmCacheBackendEphemeral
)

const (
	// NVMeshStorageClassName, when present in the cluster, signals that
	// NVMesh 3.x (with cross-namespace PV sharing) is installed.
	NVMeshStorageClassName = "nvcf-sc-30"
	// HelmCacheSharedStorageClassName is the shared storage class used for
	// non-NVMesh cross-namespace model caching. It is either pre-provisioned
	// by the operator or created by NVCA pointing at a Samba server.
	HelmCacheSharedStorageClassName = "nvcf-miniservice-sc"
)

// SelectHelmCacheBackend resolves the Helm model-cache storage backend. All
// caching is gated on CachingSupport; the mechanism is then chosen by which
// storage class the cluster provides, falling back to Samba (when
// HelmSharedStorage is enabled) and finally to a per-pod ephemeral cache:
//
//  1. nvcf-sc-30 present    -> NVMesh 3.x installed      -> NVMesh
//  2. nvcf-miniservice-sc present  -> operator shared storage   -> SharedFS
//  3. HelmSharedStorage on  -> NVCA deploys Samba         -> Samba
//  4. otherwise             -> per-pod emptyDir fallback  -> Ephemeral
func SelectHelmCacheBackend(
	ctx context.Context,
	c client.Client,
	ff featureflag.Fetcher,
) (HelmCacheBackend, error) {
	if !ff.IsFeatureFlagEnabled(featureflag.CachingSupport) {
		return HelmCacheBackendNone, nil
	}

	nvmeshPresent, err := storageClassExists(ctx, c, NVMeshStorageClassName)
	if err != nil {
		return "", err
	}
	if nvmeshPresent {
		return HelmCacheBackendNVMesh, nil
	}

	sharedPresent, err := storageClassExists(ctx, c, HelmCacheSharedStorageClassName)
	if err != nil {
		return "", err
	}
	if sharedPresent {
		return HelmCacheBackendSharedFS, nil
	}

	if ff.IsFeatureFlagEnabled(&featureflag.HelmSharedStorage.FeatureFlag) {
		return HelmCacheBackendSamba, nil
	}

	return HelmCacheBackendEphemeral, nil
}

// storageClassExists reports whether a cluster-scoped StorageClass exists,
// treating NotFound as a clean negative.
func storageClassExists(ctx context.Context, c client.Client, name string) (bool, error) {
	sc := &storagev1.StorageClass{}
	switch err := c.Get(ctx, client.ObjectKey{Name: name}, sc); {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("get storageclass %q: %w", name, err)
	default:
		return true, nil
	}
}
