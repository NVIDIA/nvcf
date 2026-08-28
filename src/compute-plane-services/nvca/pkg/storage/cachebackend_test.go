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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func storageClass(name string) *storagev1.StorageClass {
	return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func cacheBackendClient(t *testing.T, scs ...*storagev1.StorageClass) *fake.ClientBuilder {
	t.Helper()
	sch := runtime.NewScheme()
	require.NoError(t, storagev1.AddToScheme(sch))
	b := fake.NewClientBuilder().WithScheme(sch)
	for _, sc := range scs {
		b = b.WithObjects(sc)
	}
	return b
}

func TestSelectHelmCacheBackend(t *testing.T) {
	cachingOnly := []*featureflag.FeatureFlag{featureflag.CachingSupport}
	cachingAndSamba := []*featureflag.FeatureFlag{
		featureflag.CachingSupport,
		&featureflag.HelmSharedStorage.FeatureFlag,
	}

	tests := []struct {
		name           string
		flags          []*featureflag.FeatureFlag
		storageClasses []*storagev1.StorageClass
		// modelCacheClass overrides the class the Samba backing PVC needs.
		modelCacheClass string
		want            HelmCacheBackend
	}{
		{
			name:  "caching disabled -> none",
			flags: nil,
			// nvcf-sc-30 present but caching off: still none.
			storageClasses: []*storagev1.StorageClass{storageClass(NVMeshStorageClassName)},
			want:           HelmCacheBackendNone,
		},
		{
			name:           "nvcf-sc-30 present -> nvmesh",
			flags:          cachingOnly,
			storageClasses: []*storagev1.StorageClass{storageClass(NVMeshStorageClassName)},
			want:           HelmCacheBackendNVMesh,
		},
		{
			name:           "nvcf-miniservice-sc present -> sharedfs",
			flags:          cachingOnly,
			storageClasses: []*storagev1.StorageClass{storageClass(HelmCacheSharedStorageClassName)},
			want:           HelmCacheBackendSharedFS,
		},
		{
			name:  "both classes present -> nvmesh wins",
			flags: cachingOnly,
			storageClasses: []*storagev1.StorageClass{
				storageClass(NVMeshStorageClassName),
				storageClass(HelmCacheSharedStorageClassName),
			},
			want: HelmCacheBackendNVMesh,
		},
		{
			name:           "no shared class, HelmSharedStorage on, model cache class present -> samba",
			flags:          cachingAndSamba,
			storageClasses: []*storagev1.StorageClass{storageClass(DefaultModelCacheStorageClassName)},
			want:           HelmCacheBackendSamba,
		},
		{
			// Samba's backing PVC would never bind, and that path has no failure
			// threshold, so the install would hang instead of degrading.
			name:           "no shared class, HelmSharedStorage on, no model cache class -> ephemeral",
			flags:          cachingAndSamba,
			storageClasses: nil,
			want:           HelmCacheBackendEphemeral,
		},
		{
			name:            "samba honors the model cache class override",
			flags:           cachingAndSamba,
			storageClasses:  []*storagev1.StorageClass{storageClass("custom-block-sc")},
			modelCacheClass: "custom-block-sc",
			want:            HelmCacheBackendSamba,
		},
		{
			// The override moves the check: the default class no longer counts.
			name:            "override set but only the default class exists -> ephemeral",
			flags:           cachingAndSamba,
			storageClasses:  []*storagev1.StorageClass{storageClass(DefaultModelCacheStorageClassName)},
			modelCacheClass: "custom-block-sc",
			want:            HelmCacheBackendEphemeral,
		},
		{
			name:           "no shared class, HelmSharedStorage off -> ephemeral",
			flags:          cachingOnly,
			storageClasses: nil,
			want:           HelmCacheBackendEphemeral,
		},
		{
			name:  "nvcf-miniservice-sc takes precedence over samba",
			flags: cachingAndSamba,
			storageClasses: []*storagev1.StorageClass{
				storageClass(HelmCacheSharedStorageClassName),
				storageClass(DefaultModelCacheStorageClassName),
			},
			want: HelmCacheBackendSharedFS,
		},
		{
			name:  "nvcf-sc-30 takes precedence over samba",
			flags: cachingAndSamba,
			storageClasses: []*storagev1.StorageClass{
				storageClass(NVMeshStorageClassName),
				storageClass(DefaultModelCacheStorageClassName),
			},
			want: HelmCacheBackendNVMesh,
		},
		{
			// Caching off short-circuits before any class lookup.
			name:           "caching disabled with samba flag on -> none",
			flags:          []*featureflag.FeatureFlag{&featureflag.HelmSharedStorage.FeatureFlag},
			storageClasses: []*storagev1.StorageClass{storageClass(DefaultModelCacheStorageClassName)},
			want:           HelmCacheBackendNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cacheBackendClient(t, tt.storageClasses...).Build()
			ff := &featureflagmock.Fetcher{EnabledFFs: tt.flags}

			got, err := SelectHelmCacheBackend(t.Context(), c, ff, tt.modelCacheClass)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestModelCacheStorageClassNameResolution(t *testing.T) {
	assert.Equal(t, DefaultModelCacheStorageClassName, ModelCacheStorageClassName(""))
	assert.Equal(t, "custom-block-sc", ModelCacheStorageClassName("custom-block-sc"))
}

// TestNvmeshModelCacheStorageClassNameResolution pins the NVMesh path's
// unconfigured default to NVMeshStorageClassName ("nvcf-sc-30"), not
// DefaultModelCacheStorageClassName ("nvcf-sc"). Regression test for the bug
// where doModelCacheNVMesh provisioned volumes on nvcf-sc even when
// SelectHelmCacheBackend had already confirmed nvcf-sc-30 (NVMesh) was
// available and selected the NVMesh backend.
func TestNvmeshModelCacheStorageClassNameResolution(t *testing.T) {
	assert.Equal(t, NVMeshStorageClassName, nvmeshModelCacheStorageClassName(""))
	assert.Equal(t, "custom-nvmesh-sc", nvmeshModelCacheStorageClassName("custom-nvmesh-sc"))
	assert.NotEqual(t, DefaultModelCacheStorageClassName, nvmeshModelCacheStorageClassName(""))
}

// TestSelectHelmCacheBackend_SambaClassLookupError proves a failed lookup of the
// Samba backing class surfaces as an error rather than silently degrading to the
// ephemeral cache: a transient API error must be retried, not treated as an
// absent StorageClass.
func TestSelectHelmCacheBackend_SambaClassLookupError(t *testing.T) {
	sch := runtime.NewScheme()
	require.NoError(t, storagev1.AddToScheme(sch))
	c := fake.NewClientBuilder().WithScheme(sch).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if key.Name == DefaultModelCacheStorageClassName {
					return apierrors.NewServiceUnavailable("storageclass lookup failed")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	ff := &featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{
		featureflag.CachingSupport,
		&featureflag.HelmSharedStorage.FeatureFlag,
	}}

	_, err := SelectHelmCacheBackend(t.Context(), c, ff, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), DefaultModelCacheStorageClassName)
}
