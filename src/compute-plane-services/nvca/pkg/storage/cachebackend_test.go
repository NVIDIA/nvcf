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
	corev1 "k8s.io/api/core/v1"
	"strings"
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

func storageClassWithProvisioner(name, provisioner string) *storagev1.StorageClass {
	sc := storageClass(name)
	sc.Provisioner = provisioner
	return sc
}

// nvmeshClass is the model cache class as an NVMesh deployment renders it.
func nvmeshClass() *storagev1.StorageClass {
	return storageClassWithProvisioner(DefaultModelCacheStorageClassName, NVMeshStorageClassProvisioner)
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
	cachingOnly := []*featureflag.FeatureFlag{
		featureflag.CachingSupport,
		featureflag.HelmModelCaching,
	}
	cachingAndSamba := []*featureflag.FeatureFlag{
		featureflag.CachingSupport,
		featureflag.HelmModelCaching,
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
			// NVMesh class present but caching off: still none.
			storageClasses: []*storagev1.StorageClass{nvmeshClass()},
			want:           HelmCacheBackendNone,
		},
		{
			name:  "HelmModelCaching off -> none",
			flags: []*featureflag.FeatureFlag{featureflag.CachingSupport},
			// CachingSupport on and NVMesh class present, but the Helm sub-gate
			// is off: no backend is selected.
			storageClasses: []*storagev1.StorageClass{nvmeshClass()},
			want:           HelmCacheBackendNone,
		},
		{
			name:           "CachingSupport off, HelmModelCaching on -> none",
			flags:          []*featureflag.FeatureFlag{featureflag.HelmModelCaching},
			storageClasses: []*storagev1.StorageClass{nvmeshClass()},
			want:           HelmCacheBackendNone,
		},
		{
			name:           "model cache class provisioned by nvmesh -> nvmesh",
			flags:          cachingOnly,
			storageClasses: []*storagev1.StorageClass{nvmeshClass()},
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
				nvmeshClass(),
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
			name:           "nvmesh class takes precedence over samba",
			flags:          cachingAndSamba,
			storageClasses: []*storagev1.StorageClass{nvmeshClass()},
			want:           HelmCacheBackendNVMesh,
		},
		{
			// A model cache class exists but is not NVMesh: presence alone
			// selects nothing.
			name:  "model cache class with another provisioner -> not nvmesh",
			flags: cachingOnly,
			storageClasses: []*storagev1.StorageClass{
				storageClassWithProvisioner(DefaultModelCacheStorageClassName, "csi.weka.io"),
			},
			want: HelmCacheBackendEphemeral,
		},
		{
			name:  "nvmesh detection honors the model cache class override",
			flags: cachingOnly,
			storageClasses: []*storagev1.StorageClass{
				storageClassWithProvisioner("custom-block-sc", NVMeshStorageClassProvisioner),
			},
			modelCacheClass: "custom-block-sc",
			want:            HelmCacheBackendNVMesh,
		},
		{
			// The override moves the check: an NVMesh default class no longer
			// counts when the agent is configured to use another class.
			name:            "override set but only the nvmesh default class exists -> not nvmesh",
			flags:           cachingOnly,
			storageClasses:  []*storagev1.StorageClass{nvmeshClass()},
			modelCacheClass: "custom-block-sc",
			want:            HelmCacheBackendEphemeral,
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

// TestSelectHelmCacheBackend_SambaClassLookupError proves a failed lookup of the
// Samba backing class surfaces as an error rather than silently degrading to the
// ephemeral cache: a transient API error must be retried, not treated as an
// absent StorageClass.
// TestSelectHelmCacheBackend_ModelCacheClassLookupError pins that a failed
// lookup of the model cache class surfaces as an error whether or not Samba is
// enabled. NVMesh is identified by that class's provisioner, so the class is
// read on every selection; a transient API error must requeue rather than
// silently pick a fallback.
func TestSelectHelmCacheBackend_ModelCacheClassLookupError(t *testing.T) {
	base := []*featureflag.FeatureFlag{featureflag.CachingSupport, featureflag.HelmModelCaching}
	tests := []struct {
		name  string
		flags []*featureflag.FeatureFlag
	}{
		{name: "samba off", flags: base},
		{name: "samba on", flags: append(append([]*featureflag.FeatureFlag{}, base...), &featureflag.HelmSharedStorage.FeatureFlag)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			ff := &featureflagmock.Fetcher{EnabledFFs: tt.flags}

			_, err := SelectHelmCacheBackend(t.Context(), c, ff, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), DefaultModelCacheStorageClassName)
		})
	}
}

func TestPersistedHelmCacheBackend(t *testing.T) {
	for _, tt := range []struct {
		name    string
		raw     string
		want    HelmCacheBackend
		wantErr bool
	}{
		{name: "empty legacy value", want: HelmCacheBackendNVMesh},
		{name: "NVMesh", raw: string(HelmCacheBackendNVMesh), want: HelmCacheBackendNVMesh},
		{name: "shared filesystem", raw: string(HelmCacheBackendSharedFS), want: HelmCacheBackendSharedFS},
		{name: "Samba", raw: string(HelmCacheBackendSamba), want: HelmCacheBackendSamba},
		{name: "none is not durable", raw: string(HelmCacheBackendNone), wantErr: true},
		{name: "ephemeral is not durable", raw: string(HelmCacheBackendEphemeral), wantErr: true},
		{name: "unknown", raw: "invented", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PersistedHelmCacheBackend(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHelmCacheBackendFromSelectionRoutesBothShapes(t *testing.T) {
	selection := func(transition string, modes []corev1.PersistentVolumeAccessMode,
		options []string) *PersistedModelCacheStorageSelection {
		return &PersistedModelCacheStorageSelection{
			Version:              ModelCacheStorageSelectionVersion,
			Workflow:             ModelCacheWorkflowHelm,
			Mode:                 ModelCacheSelectionDurable,
			StorageClassName:     DefaultModelCacheStorageClassName,
			StorageClassUID:      "uid-1",
			StorageClassDigest:   "v1:sha256:" + strings.Repeat("a", 64),
			ProfileDigest:        "sha256:" + strings.Repeat("b", 64),
			Provider:             "weka",
			Provisioner:          "csi.weka.io",
			Transition:           transition,
			RequiredAccessModes:  modes,
			RequiredMountOptions: options,
		}
	}

	rwx := selection(ModelCacheTransitionRWXReadOnly,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, nil)
	backend, err := HelmCacheBackendFromSelection(rwx)
	require.NoError(t, err, "a ReadWriteMany backend must have a durable Helm path")
	assert.Equal(t, HelmCacheBackendSharedFS, backend)

	rox := selection(ModelCacheTransitionROXReadOnly,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadOnlyMany},
		[]string{"ro", "norecovery", "nouuid"})
	rox.Provider = ModelCacheProviderNVMesh
	rox.Provisioner = NVMeshStorageClassProvisioner
	backend, err = HelmCacheBackendFromSelection(rox)
	require.NoError(t, err)
	assert.Equal(t, HelmCacheBackendNVMesh, backend)
}
