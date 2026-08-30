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

package mscontroller

import (
	"context"
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func TestSelectHelmCacheBackend(t *testing.T) {
	const instanceNamespace = "instance-ns"

	t.Run("persisted selection wins over a conflicting existing StorageRequest", func(t *testing.T) {
		existing := &nvcav2beta1.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nvcav2beta1.ModelCacheRequest.Name(),
				Namespace: instanceNamespace,
			},
			Spec: nvcav2beta1.StorageRequestSpec{
				ModelCache: &nvcav2beta1.ModelCacheSpec{Backend: string(nvcastorage.HelmCacheBackendSamba)},
			},
		}
		r := newModelCacheSelectionReconciler(t, existing)
		request := &nvcav2beta1.ICMSRequest{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				nvcastorage.ModelCacheStorageSelectionAnnotationKey: "not-json",
			}},
		}

		_, err := r.selectHelmCacheBackend(t.Context(), request, instanceNamespace)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse model cache storage selection")
		assert.True(t, errors.Is(err, reconcile.TerminalError(nil)))
	})

	t.Run("matching persisted StorageRequest is adopted", func(t *testing.T) {
		request := requestWithModelCacheSelection(
			t, nvcastorage.ModelCacheWorkflowHelm, nvcastorage.ModelCacheSelectionDurable)
		raw := request.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey]
		existing := &nvcav2beta1.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nvcav2beta1.ModelCacheRequest.Name(),
				Namespace: instanceNamespace,
				Annotations: map[string]string{
					nvcastorage.ModelCacheStorageSelectionAnnotationKey: raw,
					nvcastorage.ICMSRequestUIDAnnotationKey:             string(request.UID),
				},
			},
			Spec: nvcav2beta1.StorageRequestSpec{
				Type:             nvcav2beta1.ModelCacheRequest,
				RequestName:      request.Name,
				RequestNamespace: request.Namespace,
				ModelCache: &nvcav2beta1.ModelCacheSpec{
					Backend:     string(nvcastorage.HelmCacheBackendNVMesh),
					CacheHandle: helmModelCacheHandle(request),
				},
			},
		}
		r := newModelCacheSelectionReconciler(t, existing)

		got, err := r.selectHelmCacheBackend(t.Context(), request, instanceNamespace)

		require.NoError(t, err)
		assert.Equal(t, nvcastorage.HelmCacheBackendNVMesh, got)
	})

	t.Run("ephemeral selection rejects a stale durable StorageRequest", func(t *testing.T) {
		existing := &nvcav2beta1.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nvcav2beta1.ModelCacheRequest.Name(),
				Namespace: instanceNamespace,
			},
			Spec: nvcav2beta1.StorageRequestSpec{
				Type:       nvcav2beta1.ModelCacheRequest,
				ModelCache: &nvcav2beta1.ModelCacheSpec{Backend: string(nvcastorage.HelmCacheBackendNVMesh)},
			},
		}
		r := newModelCacheSelectionReconciler(t, existing)
		request := requestWithModelCacheSelection(
			t, nvcastorage.ModelCacheWorkflowHelm, nvcastorage.ModelCacheSelectionEphemeral)

		_, err := r.selectHelmCacheBackend(t.Context(), request, instanceNamespace)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not create a StorageRequest")
		assert.True(t, errors.Is(err, reconcile.TerminalError(nil)))
	})

	t.Run("existing StorageRequest without model cache spec fails", func(t *testing.T) {
		existing := &nvcav2beta1.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nvcav2beta1.ModelCacheRequest.Name(),
				Namespace: instanceNamespace,
			},
		}
		r := newModelCacheSelectionReconciler(t, existing)

		_, err := r.selectHelmCacheBackend(t.Context(), &nvcav2beta1.ICMSRequest{}, instanceNamespace)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "has no modelCache spec")
	})

	for _, tt := range []struct {
		name string
		mode nvcastorage.ModelCacheSelectionMode
		want nvcastorage.HelmCacheBackend
	}{
		{
			name: "persisted durable selection uses NVMesh",
			mode: nvcastorage.ModelCacheSelectionDurable,
			want: nvcastorage.HelmCacheBackendNVMesh,
		},
		{
			name: "persisted ephemeral selection uses ephemeral cache",
			mode: nvcastorage.ModelCacheSelectionEphemeral,
			want: nvcastorage.HelmCacheBackendEphemeral,
		},
		{
			name: "persisted none selection disables cache",
			mode: nvcastorage.ModelCacheSelectionNone,
			want: nvcastorage.HelmCacheBackendNone,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newModelCacheSelectionReconciler(t)
			request := requestWithModelCacheSelection(t, nvcastorage.ModelCacheWorkflowHelm, tt.mode)

			got, err := r.selectHelmCacheBackend(t.Context(), request, instanceNamespace)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("malformed persisted selection fails without legacy fallback", func(t *testing.T) {
		r := newModelCacheSelectionReconciler(t)
		request := &nvcav2beta1.ICMSRequest{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				nvcastorage.ModelCacheStorageSelectionAnnotationKey: "{",
			}},
		}

		_, err := r.selectHelmCacheBackend(t.Context(), request, instanceNamespace)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse model cache storage selection")
		assert.True(t, errors.Is(err, reconcile.TerminalError(nil)))
	})

	t.Run("persisted regular-workflow selection fails", func(t *testing.T) {
		r := newModelCacheSelectionReconciler(t)
		request := requestWithModelCacheSelection(
			t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionNone)

		_, err := r.selectHelmCacheBackend(t.Context(), request, instanceNamespace)

		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow "regularModelCache" is not Helm`)
		assert.True(t, errors.Is(err, reconcile.TerminalError(nil)))
	})

	t.Run("request without selection uses legacy fallback", func(t *testing.T) {
		nvmeshClass := &storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: nvcastorage.NVMeshStorageClassName},
			Provisioner: "nvmesh-csi-driver",
		}
		r := newModelCacheSelectionReconciler(t, nvmeshClass)
		r.FeatureFlagFetcher = &featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.CachingSupport,
			featureflag.HelmModelCaching,
		}}

		got, err := r.selectHelmCacheBackend(
			t.Context(), &nvcav2beta1.ICMSRequest{}, instanceNamespace)

		require.NoError(t, err)
		assert.Equal(t, nvcastorage.HelmCacheBackendNVMesh, got)
	})

	t.Run("StorageRequest get error is returned", func(t *testing.T) {
		wantErr := errors.New("storage API unavailable")
		r := newModelCacheSelectionReconciler(t)
		r.Client = &getErrorClient{Client: r.Client, err: wantErr}

		_, err := r.selectHelmCacheBackend(t.Context(), &nvcav2beta1.ICMSRequest{}, instanceNamespace)

		require.Error(t, err)
		assert.ErrorIs(t, err, wantErr)
		assert.Contains(t, err.Error(), "get existing model cache StorageRequest")
	})
}

func TestPersistedDurableHelmCacheSelection(t *testing.T) {
	for _, tt := range []struct {
		name    string
		req     func(*testing.T) *nvcav2beta1.ICMSRequest
		want    bool
		wantErr string
	}{
		{
			name: "legacy request",
			req:  func(*testing.T) *nvcav2beta1.ICMSRequest { return &nvcav2beta1.ICMSRequest{} },
		},
		{
			name: "durable Helm decision",
			req: func(t *testing.T) *nvcav2beta1.ICMSRequest {
				return requestWithModelCacheSelection(
					t, nvcastorage.ModelCacheWorkflowHelm, nvcastorage.ModelCacheSelectionDurable)
			},
			want: true,
		},
		{
			name: "ephemeral Helm decision",
			req: func(t *testing.T) *nvcav2beta1.ICMSRequest {
				return requestWithModelCacheSelection(
					t, nvcastorage.ModelCacheWorkflowHelm, nvcastorage.ModelCacheSelectionEphemeral)
			},
		},
		{
			name: "wrong workflow",
			req: func(t *testing.T) *nvcav2beta1.ICMSRequest {
				return requestWithModelCacheSelection(
					t, nvcastorage.ModelCacheWorkflowRegular, nvcastorage.ModelCacheSelectionNone)
			},
			wantErr: "is not Helm",
		},
		{
			name: "malformed decision",
			req: func(*testing.T) *nvcav2beta1.ICMSRequest {
				return &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					nvcastorage.ModelCacheStorageSelectionAnnotationKey: "{",
				}}}
			},
			wantErr: "parse persisted model cache storage selection",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := persistedDurableHelmCacheSelection(tt.req(t))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func newModelCacheSelectionReconciler(t *testing.T, objects ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, nvcav2beta1.AddToScheme(scheme))
	require.NoError(t, storagev1.AddToScheme(scheme))

	return &Reconciler{
		ControllerOptions: ControllerOptions{FeatureFlagFetcher: &featureflagmock.Fetcher{}},
		Client: clientfake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(objects...).
			Build(),
	}
}

func requestWithModelCacheSelection(
	t *testing.T,
	workflow nvcastorage.ModelCacheWorkflow,
	mode nvcastorage.ModelCacheSelectionMode,
) *nvcav2beta1.ICMSRequest {
	t.Helper()
	var resolved *nvcastorage.ModelCacheStorageSelection
	if mode == nvcastorage.ModelCacheSelectionDurable {
		resolved = &nvcastorage.ModelCacheStorageSelection{
			StorageClassName:   nvcastorage.DefaultModelCacheStorageClassName,
			StorageClassUID:    types.UID("storage-class-uid"),
			StorageClassDigest: "storage-class-digest",
			CatalogDigest:      "catalog-digest",
			Provider:           "nvmesh",
			Provisioner:        nvcastorage.NVMeshStorageClassProvisioner,
			Transition:         nvcastorage.ModelCacheTransitionNVMesh,
			RequiredAccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
				corev1.ReadOnlyMany,
			},
		}
	}
	selection, err := nvcastorage.NewPersistedModelCacheStorageSelection(workflow, mode, resolved)
	require.NoError(t, err)
	if mode == nvcastorage.ModelCacheSelectionDurable {
		selection.BindingName = "model-cache-binding"
		selection.BindingUID = types.UID("binding-uid")
	}
	raw, err := selection.Marshal()
	require.NoError(t, err)

	return &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "request",
			Namespace: "requests",
			UID:       types.UID("request-uid"),
			Annotations: map[string]string{
				nvcastorage.ModelCacheStorageSelectionAnnotationKey: raw,
			},
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: &function.LaunchSpecification{
					CacheLaunchSpecification: &common.CacheLaunchSpecification{
						CacheHandle: "cache-handle",
						CacheSize:   1,
					},
					HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
				},
			},
		},
	}
}

type getErrorClient struct {
	client.Client
	err error
}

func (c *getErrorClient) Get(
	_ context.Context,
	_ client.ObjectKey,
	_ client.Object,
	_ ...client.GetOption,
) error {
	return c.err
}
