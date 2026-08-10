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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
		want           HelmCacheBackend
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
			name:           "no shared class, HelmSharedStorage on -> samba",
			flags:          cachingAndSamba,
			storageClasses: nil,
			want:           HelmCacheBackendSamba,
		},
		{
			name:           "no shared class, HelmSharedStorage off -> ephemeral",
			flags:          cachingOnly,
			storageClasses: nil,
			want:           HelmCacheBackendEphemeral,
		},
		{
			name:           "nvcf-miniservice-sc takes precedence over samba",
			flags:          cachingAndSamba,
			storageClasses: []*storagev1.StorageClass{storageClass(HelmCacheSharedStorageClassName)},
			want:           HelmCacheBackendSharedFS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cacheBackendClient(t, tt.storageClasses...).Build()
			ff := &featureflagmock.Fetcher{EnabledFFs: tt.flags}

			got, err := SelectHelmCacheBackend(t.Context(), c, ff)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
