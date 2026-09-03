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

package nvca

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

const (
	selectionCatalogNamespace = "nvca-system"
	selectionCatalogNVMesh    = `apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  nvmesh-csi.excelero.com:
    provider: nvmesh
    accessModes: [ReadWriteOnce, ReadOnlyMany]
    readerMountOptions: [ro, norecovery, nouuid]
`
	selectionCatalogDisabled = `apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  nvmesh-csi.excelero.com:
    provider: nvmesh
    accessModes: []
    readerMountOptions: []
`
	selectionCatalogRWXReadOnly = `apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  csi.weka.io:
    provider: weka
    accessModes: [ReadWriteMany, ReadOnlyMany]
    readerMountOptions: []
`
)

func selectionStorageClass() *storagev1.StorageClass {
	retain := corev1.PersistentVolumeReclaimRetain
	wait := storagev1.VolumeBindingWaitForFirstConsumer
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nvcastorage.DefaultModelCacheStorageClassName,
			UID:  types.UID("nvcf-sc-uid"),
		},
		Provisioner:       nvcastorage.NVMeshStorageClassProvisioner,
		Parameters:        map[string]string{"pool": "model-cache"},
		ReclaimPolicy:     &retain,
		VolumeBindingMode: &wait,
		MountOptions:      []string{"nouuid", "noatime"},
	}
}

func selectionStorageClassForProvisioner(provisioner string) *storagev1.StorageClass {
	storageClass := selectionStorageClass()
	storageClass.Provisioner = provisioner
	return storageClass
}

func selectionCatalogConfigMap(raw string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcastorage.StorageCapabilityConfigMapName,
			Namespace: selectionCatalogNamespace,
		},
		Data: map[string]string{nvcastorage.StorageCapabilityConfigMapKey: raw},
	}
}

func selectionRequest(helm bool) *nvcav2beta1.ICMSRequest {
	launchSpec := &function.LaunchSpecification{
		CacheLaunchSpecification: &common.CacheLaunchSpecification{
			CacheArtifacts: true,
			CacheHandle:    "model-cache-handle",
			CacheSize:      1 << 30,
		},
	}
	if helm {
		launchSpec.HelmChartLaunchSpecification = &common.HelmChartLaunchSpecification{
			HelmChartURL: "https://example.invalid/chart.tgz",
		}
	}
	return &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "request", Namespace: RequestsNamespace},
		Spec: nvcav2beta1.ICMSRequestSpec{
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: launchSpec,
			},
		},
	}
}

func selectionBackendCache(
	objects []runtime.Object,
	flags ...*featureflag.FeatureFlag,
) (*BackendK8sCache, *fakek8sclient.Clientset) {
	k8sClient := fakek8sclient.NewSimpleClientset(objects...)
	clients := &kubeclients.KubeClients{K8s: k8sClient}
	return &BackendK8sCache{
		clients:            clients,
		systemNamespace:    selectionCatalogNamespace,
		requestsNamespace:  RequestsNamespace,
		featureFlagFetcher: &featureflagmock.Fetcher{EnabledFFs: flags},
	}, k8sClient
}

func parseRequestStorageSelection(
	t *testing.T,
	req *nvcav2beta1.ICMSRequest,
) *nvcastorage.PersistedModelCacheStorageSelection {
	t.Helper()
	raw := req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey]
	require.NotEmpty(t, raw)
	selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(raw)
	require.NoError(t, err)
	return selection
}

func TestPersistModelCacheStorageSelection(t *testing.T) {
	tests := []struct {
		name              string
		helm              bool
		objects           func() []runtime.Object
		flags             []*featureflag.FeatureFlag
		wantWorkflow      nvcastorage.ModelCacheWorkflow
		wantMode          nvcastorage.ModelCacheSelectionMode
		wantTransition    string
		wantResolvedState bool
		wantProvider      string
		wantProvisioner   string
		wantEncryption    bool
	}{
		{
			name: "regular durable NVMesh",
			objects: func() []runtime.Object {
				return []runtime.Object{selectionStorageClass(), selectionCatalogConfigMap(selectionCatalogNVMesh)}
			},
			flags:             []*featureflag.FeatureFlag{featureflag.CachingSupport},
			wantWorkflow:      nvcastorage.ModelCacheWorkflowRegular,
			wantMode:          nvcastorage.ModelCacheSelectionDurable,
			wantTransition:    nvcastorage.ModelCacheTransitionROXReadOnly,
			wantResolvedState: true,
			wantProvider:      nvcastorage.ModelCacheProviderNVMesh,
			wantProvisioner:   nvcastorage.NVMeshStorageClassProvisioner,
		},
		{
			name: "Helm durable NVMesh",
			helm: true,
			objects: func() []runtime.Object {
				return []runtime.Object{selectionStorageClass(), selectionCatalogConfigMap(selectionCatalogNVMesh)}
			},
			flags: []*featureflag.FeatureFlag{
				featureflag.CachingSupport,
				featureflag.HelmModelCaching,
			},
			wantWorkflow:      nvcastorage.ModelCacheWorkflowHelm,
			wantMode:          nvcastorage.ModelCacheSelectionDurable,
			wantTransition:    nvcastorage.ModelCacheTransitionROXReadOnly,
			wantResolvedState: true,
			wantProvider:      nvcastorage.ModelCacheProviderNVMesh,
			wantProvisioner:   nvcastorage.NVMeshStorageClassProvisioner,
		},
		{
			name: "regular durable provider-neutral RWX",
			objects: func() []runtime.Object {
				return []runtime.Object{
					selectionStorageClassForProvisioner("csi.weka.io"),
					selectionCatalogConfigMap(selectionCatalogRWXReadOnly),
				}
			},
			flags: []*featureflag.FeatureFlag{
				featureflag.CachingSupport,
				featureflag.NVMeshEncryption,
			},
			wantWorkflow:      nvcastorage.ModelCacheWorkflowRegular,
			wantMode:          nvcastorage.ModelCacheSelectionDurable,
			wantTransition:    nvcastorage.ModelCacheTransitionRWXReadOnly,
			wantResolvedState: true,
			wantProvider:      "weka",
			wantProvisioner:   "csi.weka.io",
		},
		{
			name: "disabled regular cache persists none",
			objects: func() []runtime.Object {
				return []runtime.Object{selectionStorageClass(), selectionCatalogConfigMap(selectionCatalogDisabled)}
			},
			flags:             []*featureflag.FeatureFlag{featureflag.CachingSupport},
			wantWorkflow:      nvcastorage.ModelCacheWorkflowRegular,
			wantMode:          nvcastorage.ModelCacheSelectionNone,
			wantTransition:    nvcastorage.ModelCacheTransitionDisabled,
			wantResolvedState: true,
			wantProvider:      nvcastorage.ModelCacheProviderNVMesh,
			wantProvisioner:   nvcastorage.NVMeshStorageClassProvisioner,
		},
		{
			name: "disabled Helm cache persists ephemeral",
			helm: true,
			objects: func() []runtime.Object {
				return []runtime.Object{selectionStorageClass(), selectionCatalogConfigMap(selectionCatalogDisabled)}
			},
			flags: []*featureflag.FeatureFlag{
				featureflag.CachingSupport,
				featureflag.HelmModelCaching,
			},
			wantWorkflow:      nvcastorage.ModelCacheWorkflowHelm,
			wantMode:          nvcastorage.ModelCacheSelectionEphemeral,
			wantTransition:    nvcastorage.ModelCacheTransitionDisabled,
			wantResolvedState: true,
			wantProvider:      nvcastorage.ModelCacheProviderNVMesh,
			wantProvisioner:   nvcastorage.NVMeshStorageClassProvisioner,
		},
		{
			name: "missing StorageClass disables regular cache",
			objects: func() []runtime.Object {
				return []runtime.Object{selectionCatalogConfigMap(selectionCatalogNVMesh)}
			},
			flags:        []*featureflag.FeatureFlag{featureflag.CachingSupport},
			wantWorkflow: nvcastorage.ModelCacheWorkflowRegular,
			wantMode:     nvcastorage.ModelCacheSelectionNone,
		},
		{
			name: "missing StorageClass falls Helm back to ephemeral",
			helm: true,
			objects: func() []runtime.Object {
				return []runtime.Object{selectionCatalogConfigMap(selectionCatalogNVMesh)}
			},
			flags: []*featureflag.FeatureFlag{
				featureflag.CachingSupport,
				featureflag.HelmModelCaching,
			},
			wantWorkflow: nvcastorage.ModelCacheWorkflowHelm,
			wantMode:     nvcastorage.ModelCacheSelectionEphemeral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache, _ := selectionBackendCache(tt.objects(), tt.flags...)
			req := selectionRequest(tt.helm)

			require.NoError(t, cache.persistModelCacheStorageSelection(t.Context(), req))
			selection := parseRequestStorageSelection(t, req)
			assert.Equal(t, tt.wantWorkflow, selection.Workflow)
			assert.Equal(t, tt.wantMode, selection.Mode)
			assert.Equal(t, tt.wantTransition, selection.Transition)
			if !tt.wantResolvedState {
				assert.Empty(t, selection.StorageClassName)
				assert.Empty(t, selection.StorageClassUID)
				assert.Empty(t, selection.StorageClassDigest)
				assert.Empty(t, selection.ProfileDigest)
				assert.Empty(t, selection.Provider)
				assert.Empty(t, selection.Provisioner)
				return
			}
			assert.Equal(t, nvcastorage.DefaultModelCacheStorageClassName, selection.StorageClassName)
			assert.Equal(t, types.UID("nvcf-sc-uid"), selection.StorageClassUID)
			assert.NotEmpty(t, selection.StorageClassDigest)
			assert.NotEmpty(t, selection.ProfileDigest)
			assert.Equal(t, tt.wantProvider, selection.Provider)
			assert.Equal(t, tt.wantProvisioner, selection.Provisioner)
			assert.Equal(t, tt.wantEncryption, selection.EncryptionRequired)
		})
	}
}

func TestCreateICMSCreationMessageRequestInvalidCatalogFailsBeforeCreate(t *testing.T) {
	objects := []runtime.Object{
		selectionStorageClass(),
		selectionCatalogConfigMap("drivers: ["),
	}
	cache, _ := selectionBackendCache(objects, featureflag.CachingSupport)
	cache.clients = mockKubeClients(objects...)
	cache.requestsNamespace = RequestsNamespace

	msg := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID: "invalid-catalog-request",
			NCAID:     "test-nca",
			Action:    common.FunctionCreationAction,
		},
		Details: function.Details{
			FunctionID:        "function-id",
			FunctionVersionID: "function-version-id",
		},
		LaunchSpecification: selectionRequest(false).Spec.CreationMsgInfo.FunctionLaunchSpecification,
	}

	created, err := cache.CreateICMSCreationMessageRequest(
		newTestContext(), msg, "receipt", "message-id", "queue")
	require.ErrorContains(t, err, "resolve model cache storage: parse storage capability catalog")
	assert.Nil(t, created)

	requests, listErr := cache.clients.BART.NvcaV2beta1().ICMSRequests(RequestsNamespace).
		List(t.Context(), metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.Empty(t, requests.Items, "an invalid catalog must fail before the ICMSRequest Create call")
}
