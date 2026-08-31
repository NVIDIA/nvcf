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
	"fmt"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
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

func persistedSelectionAnnotation(
	t *testing.T,
	workflow nvcastorage.ModelCacheWorkflow,
	mode nvcastorage.ModelCacheSelectionMode,
	resolved *nvcastorage.ModelCacheStorageSelection,
) string {
	t.Helper()
	selection, err := nvcastorage.NewPersistedModelCacheStorageSelection(workflow, mode, resolved)
	require.NoError(t, err)
	raw, err := selection.Marshal()
	require.NoError(t, err)
	return raw
}

func resolvedSelectionForSetup(t *testing.T) *nvcastorage.ModelCacheStorageSelection {
	t.Helper()
	k8sClient := fakek8sclient.NewSimpleClientset(
		selectionStorageClass(),
		selectionCatalogConfigMap(selectionCatalogNVMesh),
	)
	selection, err := nvcastorage.ResolveModelCacheStorageWithClientset(
		t.Context(), k8sClient, selectionCatalogNamespace, nvcastorage.ModelCacheWorkflowRegular)
	require.NoError(t, err)
	return selection
}

func testContainerModelCacheBackend(
	k8sClient *fakek8sclient.Clientset,
) K8sComputeBackend {
	clients := &kubeclients.KubeClients{K8s: k8sClient}
	bk8s := &BackendK8sCache{
		clients:              clients,
		podInstanceNamespace: RequestsNamespace,
		featureFlagFetcher:   &featureflagmock.Fetcher{},
	}
	return K8sComputeBackend{clients: clients, bk8s: bk8s}
}

func assertNoKubernetesWrites(t *testing.T, actions []k8stesting.Action) {
	t.Helper()
	for _, action := range actions {
		switch action.GetVerb() {
		case "create", "update", "patch", "delete", "delete-collection":
			t.Errorf("unexpected Kubernetes write before model-cache selection validation: %s %s",
				action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestSetupContainerModelCachingSelectionContract(t *testing.T) {
	resolved := resolvedSelectionForSetup(t)
	durableRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionDurable, resolved)
	noneRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionNone, nil)
	helmRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowHelm, nvcastorage.ModelCacheSelectionNone, nil)
	ephemeralRaw := `{"version":"v1alpha1","workflow":"regularModelCache","mode":"ephemeral"}`

	defaultClass := nvcastorage.DefaultModelCacheStorageClassName
	customClass := "custom-model-cache-class"

	tests := []struct {
		name          string
		raw           string
		storageClass  *string
		objects       []runtime.Object
		wantErr       string
		wantTerminal  bool
		wantNoOp      bool
		wantReadCount int
	}{
		{
			name:         "none is a no-op",
			raw:          noneRaw,
			storageClass: &defaultClass,
			wantNoOp:     true,
		},
		{
			name:         "malformed selection",
			raw:          "{",
			storageClass: &defaultClass,
			wantErr:      "parse persisted model cache storage selection",
			wantTerminal: true,
		},
		{
			name:         "wrong workflow",
			raw:          helmRaw,
			storageClass: &defaultClass,
			wantErr:      "is not regularModelCache",
			wantTerminal: true,
		},
		{
			name:         "ephemeral mode is unsupported for regular cache",
			raw:          ephemeralRaw,
			storageClass: &defaultClass,
			wantErr:      "ephemeral model cache selection requires Helm workflow",
			wantTerminal: true,
		},
		{
			name:         "custom StorageClass is rejected",
			raw:          durableRaw,
			storageClass: &customClass,
			wantErr:      "StorageClass override",
			wantTerminal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fakek8sclient.NewSimpleClientset(tt.objects...)
			backend := testContainerModelCacheBackend(k8sClient)
			req := &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "request",
					Namespace: RequestsNamespace,
					Annotations: map[string]string{
						nvcastorage.ModelCacheStorageSelectionAnnotationKey: tt.raw,
					},
				},
			}
			if tt.raw == durableRaw {
				installActiveRegularModelCacheBinding(t, &backend, req, tt.raw)
			}
			rwPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "rw-pvc-cache", Namespace: RequestsNamespace},
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: tt.storageClass,
				},
			}
			initJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: "writer-job-cache", Namespace: RequestsNamespace},
			}

			mf, roPVCName, err := backend.setupContainerModelCaching(
				newTestContext(), req, rwPVC, initJob, nil)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, tt.wantTerminal, nvcaerrors.IsTerminal(err))
				assert.Nil(t, mf)
				assert.Empty(t, roPVCName)
			} else {
				require.NoError(t, err)
				require.NotNil(t, mf)
				assert.Empty(t, roPVCName)
				pod := &corev1.Pod{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						VolumeMounts: []corev1.VolumeMount{{Name: "unrelated", ReadOnly: false}},
					}},
				}}
				before := pod.DeepCopy()
				mf(pod)
				assert.Equal(t, before, pod, "none selection must return a true no-op mutator")
			}

			assertNoKubernetesWrites(t, k8sClient.Actions())
			assert.Len(t, k8sClient.Actions(), tt.wantReadCount)
		})
	}
}

func TestSetModelCacheVolumeMountsReadOnly(t *testing.T) {
	containers := []corev1.Container{
		{
			Name: "inference",
			VolumeMounts: []corev1.VolumeMount{
				{Name: ModelVolumeName, MountPath: "/model", ReadOnly: false},
				{Name: "config", MountPath: "/config", ReadOnly: false},
			},
		},
		{
			Name: "sidecar",
			VolumeMounts: []corev1.VolumeMount{
				{Name: ModelVolumeName, MountPath: "/models", ReadOnly: true},
			},
		},
		{Name: "no-mounts"},
	}

	setModelCacheVolumeMountsReadOnly(containers)

	assert.True(t, containers[0].VolumeMounts[0].ReadOnly)
	assert.False(t, containers[0].VolumeMounts[1].ReadOnly, "unrelated mounts must not be changed")
	assert.True(t, containers[1].VolumeMounts[0].ReadOnly)
	assert.NotPanics(t, func() { setModelCacheVolumeMountsReadOnly(nil) })
}

func TestSetupContainerFunctionModelCachingArtifactDecodeFailure(t *testing.T) {
	resolved := resolvedSelectionForSetup(t)
	durableRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionDurable, resolved)

	tests := []struct {
		name           string
		annotation     string
		wantErr        string
		wantTerminal   bool
		wantLegacyNoOp bool
	}{
		{
			name:         "persisted durable selection fails closed",
			annotation:   durableRaw,
			wantErr:      "decode artifacts for persisted durable regular model cache",
			wantTerminal: true,
		},
		{
			name:           "legacy request falls back to uncached",
			wantLegacyNoOp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fakek8sclient.NewSimpleClientset()
			backend := testContainerModelCacheBackend(k8sClient)
			req := &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "request", Namespace: RequestsNamespace},
			}
			if tt.annotation != "" {
				req.Annotations = map[string]string{
					nvcastorage.ModelCacheStorageSelectionAnnotationKey: tt.annotation,
				}
			}
			invalidArtifact := function.LaunchArtifact{Specification: "%%%"}

			mf, roPVCName, err := backend.setupContainerFunctionModelCaching(
				newTestContext(), req, invalidArtifact, invalidArtifact, func(_ client.Object) {})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, tt.wantTerminal, nvcaerrors.IsTerminal(err))
				assert.Nil(t, mf)
				assert.Empty(t, roPVCName)
			} else {
				require.NoError(t, err)
				require.True(t, tt.wantLegacyNoOp)
				require.NotNil(t, mf)
				assert.Empty(t, roPVCName)
				pod := &corev1.Pod{}
				before := pod.DeepCopy()
				mf(pod)
				assert.Equal(t, before, pod)
			}
			assert.Empty(t, k8sClient.Actions(), "artifact decode failure must happen before Kubernetes access")
		})
	}
}

func TestValidatePersistedRegularModelCacheArtifacts(t *testing.T) {
	resolved := resolvedSelectionForSetup(t)
	durableRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionDurable, resolved)
	durable, err := nvcastorage.ParsePersistedModelCacheStorageSelection(durableRaw)
	require.NoError(t, err)
	noneRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionNone, nil)
	none, err := nvcastorage.ParsePersistedModelCacheStorageSelection(noneRaw)
	require.NoError(t, err)
	present := function.LaunchArtifact{Specification: "present"}
	missing := function.LaunchArtifact{}

	tests := []struct {
		name      string
		selection *nvcastorage.PersistedModelCacheStorageSelection
		rwPVC     function.LaunchArtifact
		initJob   function.LaunchArtifact
		wantErr   bool
	}{
		{name: "durable selection with both artifacts", selection: durable, rwPVC: present, initJob: present},
		{name: "durable selection missing PVC", selection: durable, rwPVC: missing, initJob: present, wantErr: true},
		{name: "durable selection missing init Job", selection: durable, rwPVC: present, initJob: missing, wantErr: true},
		{name: "durable selection missing both", selection: durable, rwPVC: missing, initJob: missing, wantErr: true},
		{name: "legacy request preserves missing-artifact fallback", rwPVC: missing, initJob: missing},
		{name: "none selection does not require artifacts", selection: none, rwPVC: missing, initJob: missing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePersistedRegularModelCacheArtifacts(tt.selection, tt.rwPVC, tt.initJob)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err,
				"persisted durable regular model cache requires both PVC and init Job artifacts")
			assert.True(t, nvcaerrors.IsTerminal(err))
		})
	}
}

func TestSetupContainerModelCachingFailedOutcome(t *testing.T) {
	resolved := resolvedSelectionForSetup(t)
	durableRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionDurable, resolved)
	defaultClass := nvcastorage.DefaultModelCacheStorageClassName

	tests := []struct {
		name           string
		annotation     string
		wantErr        string
		wantTerminal   bool
		wantLegacyNoOp bool
	}{
		{
			name:         "persisted durable execution error fails closed",
			annotation:   durableRaw,
			wantErr:      "forced PVC create failure",
			wantTerminal: true,
		},
		{
			name:           "legacy request falls back to uncached",
			wantLegacyNoOp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fakek8sclient.NewSimpleClientset(selectionStorageClass())
			k8sClient.PrependReactor("create", "persistentvolumeclaims",
				func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("forced PVC create failure")
				})
			backend := testContainerModelCacheBackend(k8sClient)
			req := &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "request", Namespace: RequestsNamespace},
				Spec: nvcav2beta1.ICMSRequestSpec{
					FunctionDetails: function.Details{FunctionVersionID: "function-version-id"},
				},
			}
			var binding *nvcav2beta1.ModelCacheBinding
			if tt.annotation != "" {
				req.Annotations = map[string]string{
					nvcastorage.ModelCacheStorageSelectionAnnotationKey: tt.annotation,
				}
				binding = installActiveRegularModelCacheBinding(t, &backend, req, tt.annotation)
			}
			rwPVCName := "rw-pvc-cache"
			initJobName := "writer-job-cache"
			if binding != nil {
				rwPVCName = binding.Spec.Resources.PersistentVolumeClaimNames[0]
				initJobName = binding.Spec.Resources.JobNames[0]
			}
			rwPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: rwPVCName, Namespace: RequestsNamespace},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &defaultClass,
				},
			}
			initJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: initJobName, Namespace: RequestsNamespace},
			}

			mf, roPVCName, err := backend.setupContainerModelCaching(
				newTestContext(), req, rwPVC, initJob, func(_ client.Object) {})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, tt.wantTerminal, nvcaerrors.IsTerminal(err))
				assert.Nil(t, mf)
				assert.Empty(t, roPVCName)
			} else {
				require.NoError(t, err)
				require.True(t, tt.wantLegacyNoOp)
				require.NotNil(t, mf)
				assert.Empty(t, roPVCName)
			}

			createAttempts := 0
			for _, action := range k8sClient.Actions() {
				if action.GetVerb() == "create" && action.GetResource().Resource == "persistentvolumeclaims" {
					createAttempts++
				}
			}
			assert.Equal(t, 1, createAttempts, "the forced failure must produce ModelCachingFailed")
		})
	}
}

func TestRegularModelCacheRuntimeDecisionHonorsPersistedSelection(t *testing.T) {
	resolved := resolvedSelectionForSetup(t)
	durableRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionDurable, resolved)
	noneRaw := persistedSelectionAnnotation(
		t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionNone, nil)

	tests := []struct {
		name          string
		raw           string
		legacyEnabled bool
		wantEnabled   bool
		wantPersisted bool
		wantErr       bool
	}{
		{name: "legacy gate enabled", legacyEnabled: true, wantEnabled: true},
		{name: "legacy gate disabled"},
		{name: "persisted durable survives gate disable", raw: durableRaw, wantEnabled: true, wantPersisted: true},
		{name: "persisted none survives gate enable", raw: noneRaw, legacyEnabled: true, wantPersisted: true},
		{name: "malformed selection fails closed", raw: "{", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &nvcav2beta1.ICMSRequest{}
			if tt.raw != "" {
				req.Annotations = map[string]string{nvcastorage.ModelCacheStorageSelectionAnnotationKey: tt.raw}
			}
			enabled, persisted, err := regularModelCacheRuntimeDecision(req, tt.legacyEnabled)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnabled, enabled)
			assert.Equal(t, tt.wantPersisted, persisted)
		})
	}
}
