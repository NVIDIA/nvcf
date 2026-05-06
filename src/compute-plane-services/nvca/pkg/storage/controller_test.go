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
	"testing"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

func TestBuildController(t *testing.T) {
	mgr, err := ctrl.NewManager(&rest.Config{}, manager.Options{
		Scheme: mgrScheme,
	})
	require.NoError(t, err)

	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()
	cfg := nvcaconfig.Config{}
	err = BuildController(cfg, nvcav1new.SharedStorageRequest, mgr, "my-cluster", "us-west-1", defaultTimeConfig, ControllerOptions{})
	assert.NoError(t, err)
}

func Test_filterOnStorageRequestType(t *testing.T) {
	tests := []struct {
		reqType  nvcav1new.StorageRequestType
		object   client.Object
		expected bool
	}{
		{
			reqType: nvcav1new.SharedStorageRequest,
			object: &nvcav1new.StorageRequest{
				Spec: nvcav1new.StorageRequestSpec{
					Type: nvcav1new.SharedStorageRequest,
				},
			},
			expected: true,
		},
		{
			reqType: nvcav1new.SharedStorageRequest,
			object: &nvcav1new.StorageRequest{
				Spec: nvcav1new.StorageRequestSpec{
					Type: nvcav1new.ModelCacheRequest,
				},
			},
			expected: false,
		},
		{
			reqType:  nvcav1new.SharedStorageRequest,
			object:   &storagev1.StorageClass{},
			expected: false,
		},
		{
			reqType: nvcav1new.SharedStorageRequest,
			object: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						StorageRequestOwnerKey: nvcav1new.SharedStorageRequest.Name(),
					},
				},
			},
			expected: true,
		},
		{
			reqType: nvcav1new.ModelCacheRequest,
			object: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						StorageRequestOwnerKey: nvcav1new.SharedStorageRequest.Name(),
					},
				},
			},
			expected: false,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			assert.Equal(t, test.expected, filterOnStorageRequestType(test.reqType)(test.object))
		})
	}
}

func Test_getClusterWideEventHandler(t *testing.T) {
	tests := []struct {
		reqType  nvcav1new.StorageRequestType
		object   client.Object
		expected []reconcile.Request
	}{
		{
			reqType: nvcav1new.SharedStorageRequest,
			object: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
						StorageRequestNamespaceKey: "foo",
					},
				},
			},
			expected: []reconcile.Request{
				{
					NamespacedName: client.ObjectKey{Namespace: "foo", Name: nvcav1new.SharedStorageRequest.Name()},
				},
			},
		},
		{
			reqType: nvcav1new.ModelCacheRequest,
			object: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						StorageRequestOwnerKey:     nvcav1new.ModelCacheRequest.Name(),
						StorageRequestNamespaceKey: "foo",
					},
				},
			},
			expected: []reconcile.Request{
				{
					NamespacedName: client.ObjectKey{Namespace: "foo", Name: nvcav1new.ModelCacheRequest.Name()},
				},
			},
		},
		{
			reqType: nvcav1new.ModelCacheRequest,
			object: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: nil,
		},
		{
			reqType: nvcav1new.SharedStorageRequest,
			object: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						StorageRequestNamespaceKey: "foo",
						StorageRequestOwnerKey:     "some-non-existent-owner",
					},
				},
			},
			expected: nil,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			mapFunc := getClusterWideEventHandlerMapFunc(test.reqType)
			assert.Equal(t, test.expected, mapFunc(context.Background(), test.object))
		})
	}
}

func TestHasStorageRequestOwnerReference(t *testing.T) {
	tests := []struct {
		name           string
		storageReqType nvcav1new.StorageRequestType
		object         client.Object
		expectedResult bool
	}{
		{
			name:           "owner reference matches",
			storageReqType: nvcav1new.SharedStorageRequest,
			object: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "nvca.nvcf.nvidia.io/v1",
							Kind:       "StorageRequest",
							Name:       nvcav1new.SharedStorageRequest.Name(),
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name:           "owner reference does not match",
			storageReqType: nvcav1new.SharedStorageRequest,
			object: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "nvca.nvcf.nvidia.io/v1",
							Kind:       "StorageRequest",
							Name:       nvcav1new.ModelCacheRequest.Name(),
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name:           "no owner references",
			storageReqType: nvcav1new.SharedStorageRequest,
			object: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "some-object",
				},
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualResult := hasStorageRequestOwnerReference(tt.storageReqType, tt.object)
			assert.Equal(t, tt.expectedResult, actualResult)
		})
	}
}

func Test_getModelCacheFanOutEventHandlerMapFunc(t *testing.T) {
	type spec struct {
		name    string
		obj     client.Object
		expReqs []reconcile.Request
	}

	sts := []client.Object{
		&nvcav1new.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name: nvcav1new.ModelCacheRequest.Name(), Namespace: "foo",
				Labels: map[string]string{modelCacheHandleLabelKey: "cachehandle1"},
			},
			Spec: nvcav1new.StorageRequestSpec{
				ModelCache: &nvcav1new.ModelCacheSpec{
					CacheHandle: "cachehandle1",
				},
			},
		},
		&nvcav1new.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name: nvcav1new.ModelCacheRequest.Name(), Namespace: "bar",
				Labels: map[string]string{modelCacheHandleLabelKey: "cachehandle2"},
			},
			Spec: nvcav1new.StorageRequestSpec{
				ModelCache: &nvcav1new.ModelCacheSpec{
					CacheHandle: "cachehandle2",
				},
			},
		},
	}
	cases := []spec{
		{
			name:    "no object match",
			obj:     &corev1.Pod{},
			expReqs: nil,
		},
		{
			name: "lease no annotations",
			obj: &coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{Name: "lease1", Namespace: ModelCacheInitNamespace},
			},
			expReqs: nil,
		},
		{
			name: "lease non matching annotation",
			obj: &coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name: "lease1", Namespace: ModelCacheInitNamespace,
					Labels: map[string]string{
						modelCacheHandleLabelKey: "cachehandle3",
					},
				},
			},
			expReqs: nil,
		},
		{
			name: "lease matching annotation",
			obj: &coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name: "lease1", Namespace: ModelCacheInitNamespace,
					Labels: map[string]string{
						modelCacheHandleLabelKey: "cachehandle1",
					},
				},
			},
			expReqs: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "foo", Name: nvcav1new.ModelCacheRequest.Name()}},
			},
		},
		{
			name: "pv matching annotation",
			obj: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pv1",
					Labels: map[string]string{
						modelCacheHandleLabelKey: "cachehandle1",
					},
				},
			},
			expReqs: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "foo", Name: nvcav1new.ModelCacheRequest.Name()}},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newTestScheme()
			c := clientfake.NewClientBuilder().
				WithScheme(scheme).
				WithRESTMapper(newTestRESTMapper(scheme)).
				WithObjects(sts...).
				WithStatusSubresource(&nvcav1new.StorageRequest{}).
				// WithIndex(
				// 	&nvcav1new.StorageRequest{},
				// 	modelCacheHandleFieldPath,
				// 	modelCacheHandleExtractValues,
				// ).
				WithIndex(
					&nvcav1new.StorageRequest{},
					objectNameFieldPath,
					objectNameExtractValues,
				).
				Build()
			gotReqs := getModelCacheFanOutEventHandlerMapFunc(c)(context.Background(), tt.obj)
			assert.Equal(t, tt.expReqs, gotReqs)
		})
	}
}
