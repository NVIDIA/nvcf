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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// HelmCacheBackend identifies the storage backend selected for the Helm model
// cache. The backend is resolved from the CachingSupport and HelmModelCaching
// gates plus the storage classes available in the cluster.
// The type and values live in pkg/types so metrics can share them; they are
// aliased here for the storage-facing API.
type HelmCacheBackend = nvcatypes.HelmCacheBackend

const (
	// HelmCacheBackendNone means caching is disabled (CachingSupport or
	// HelmModelCaching off).
	HelmCacheBackendNone = nvcatypes.HelmCacheBackendNone
	// HelmCacheBackendNVMesh uses NVMesh 3.x cross-namespace PV sharing
	// (the existing doModelCacheNVMesh path).
	HelmCacheBackendNVMesh = nvcatypes.HelmCacheBackendNVMesh
	// HelmCacheBackendSharedFS uses an operator-provided shared (RWX/ROX)
	// storage class (nvcf-miniservice-sc): WEKA, EFS, CephFS, external NFS, etc.
	HelmCacheBackendSharedFS = nvcatypes.HelmCacheBackendSharedFS
	// HelmCacheBackendSamba deploys a per-handle Samba server backed by a block
	// storage PVC and serves the cache over static SMB PVs. It creates no
	// StorageClass of its own (see cachebackend_samba.go).
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

// ModelCacheStorageClassName resolves the storage class NVCA uses for model
// cache volumes: the override when set, otherwise
// DefaultModelCacheStorageClassName. Backend selection and the Samba backing
// PVC both resolve the name here so the class that is checked for existence is
// always the class the volume is created on.
func ModelCacheStorageClassName(override string) string {
	if override != "" {
		return override
	}

	return DefaultModelCacheStorageClassName
}

// SelectHelmCacheBackend resolves the Helm model-cache storage backend. All
// caching is gated on CachingSupport plus the HelmModelCaching sub-gate; the
// mechanism is then chosen by which storage class the cluster provides,
// falling back to Samba (when HelmSharedStorage is enabled and the block class
// Samba needs exists) and finally to a per-pod ephemeral cache:
//
//  1. nvcf-sc-30 present    -> NVMesh 3.x installed      -> NVMesh
//  2. nvcf-miniservice-sc present  -> operator shared storage   -> SharedFS
//  3. HelmSharedStorage on, model cache class present -> NVCA deploys Samba -> Samba
//  4. otherwise             -> per-pod emptyDir fallback  -> Ephemeral
//
// modelCacheStorageClass is Agent.ModelCache.StorageClassName, the same config
// value the storage controller provisions model cache volumes with; empty
// resolves to the default.
func SelectHelmCacheBackend(
	ctx context.Context,
	c client.Client,
	ff featureflag.Fetcher,
	modelCacheStorageClass string,
) (HelmCacheBackend, error) {
	if !ff.IsFeatureFlagEnabled(featureflag.CachingSupport) ||
		!ff.IsFeatureFlagEnabled(featureflag.HelmModelCaching) {
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
		// Samba is reached only when neither shared class exists, which usually
		// means NVMesh is not installed and the block class its backing PVC
		// needs is absent too. An unbindable backing PVC leaves the Samba
		// Deployment unavailable forever, and that path has no failure
		// threshold: the ModelCacheRequest would requeue indefinitely and block
		// the install instead of degrading. Verify the class first and take the
		// ephemeral cache when it is missing.
		dataClass := ModelCacheStorageClassName(modelCacheStorageClass)
		dataClassPresent, err := storageClassExists(ctx, c, dataClass)
		if err != nil {
			return "", err
		}
		if dataClassPresent {
			return HelmCacheBackendSamba, nil
		}
		logf.FromContext(ctx).Info(
			"Samba model cache backing storage class is missing, using the per-pod ephemeral cache",
			"storageClass", dataClass)
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
