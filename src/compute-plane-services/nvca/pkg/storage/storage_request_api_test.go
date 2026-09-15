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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"

	nvcav1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	fakeclientset "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
)

// TestStorageRequestAPI_Update_PropagatesFinalizers verifies that Update persists finalizer
// changes from the updated v1 object — specifically that removing a finalizer via
// controllerutil.RemoveFinalizer is not silently dropped, which was the root cause of
// StorageRequest namespaces being stuck Terminating indefinitely.
func TestStorageRequestAPI_Update_PropagatesFinalizers(t *testing.T) {
	const (
		namespace = "sr-test-ns"
		name      = "internal-persistent-storage"
		finalizer = "nvca.nvcf.nvidia.io/storage-request-finalizer"
	)

	tests := []struct {
		name              string
		existingObj       runtime.Object
		ref               func(runtime.Object) *StorageRequestRef
		updatedFinalizers []string
		wantFinalizers    []string
	}{
		{
			name: "removes finalizer from v2beta1 object",
			existingObj: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:            name,
					Namespace:       namespace,
					Finalizers:      []string{finalizer},
					ResourceVersion: "1",
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type: nvcav2beta1.StorageRequestType(nvcav1.InternalPersistentStorageRequest),
				},
			},
			ref: func(obj runtime.Object) *StorageRequestRef {
				return &StorageRequestRef{v2Beta1Obj: obj.(*nvcav2beta1.StorageRequest)}
			},
			updatedFinalizers: []string{},
			wantFinalizers:    []string{},
		},
		{
			name: "removes finalizer from v1 object",
			existingObj: &nvcav1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:            name,
					Namespace:       namespace,
					Finalizers:      []string{finalizer},
					ResourceVersion: "1",
				},
				Spec: nvcav1.StorageRequestSpec{
					Type: nvcav1.InternalPersistentStorageRequest,
				},
			},
			ref: func(obj runtime.Object) *StorageRequestRef {
				return &StorageRequestRef{v1Obj: obj.(*nvcav1.StorageRequest)}
			},
			updatedFinalizers: []string{},
			wantFinalizers:    []string{},
		},
		{
			name: "preserves other finalizers when removing one from v2beta1",
			existingObj: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:            name,
					Namespace:       namespace,
					Finalizers:      []string{"other-finalizer", finalizer},
					ResourceVersion: "1",
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type: nvcav2beta1.StorageRequestType(nvcav1.InternalPersistentStorageRequest),
				},
			},
			ref: func(obj runtime.Object) *StorageRequestRef {
				return &StorageRequestRef{v2Beta1Obj: obj.(*nvcav2beta1.StorageRequest)}
			},
			updatedFinalizers: []string{"other-finalizer"},
			wantFinalizers:    []string{"other-finalizer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientset := fakeclientset.NewSimpleClientset(tt.existingObj)
			api := NewStorageRequestAPI(clientset)
			ref := tt.ref(tt.existingObj)

			updated := &nvcav1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:       name,
					Namespace:  namespace,
					Finalizers: tt.updatedFinalizers,
				},
				Spec: nvcav1.StorageRequestSpec{
					Type: nvcav1.InternalPersistentStorageRequest,
				},
			}

			err := api.Update(context.Background(), ref, updated)
			require.NoError(t, err)

			// Verify the finalizer was actually persisted in k8s.
			got2, err2 := clientset.NvcaV2beta1().StorageRequests(namespace).Get(context.Background(), name, metav1.GetOptions{})
			if err2 == nil {
				assert.ElementsMatch(t, tt.wantFinalizers, got2.GetFinalizers(),
					"v2beta1: finalizer change must be persisted to k8s")
				return
			}
			got1, err1 := clientset.NvcaV1().StorageRequests(namespace).Get(context.Background(), name, metav1.GetOptions{})
			require.NoError(t, err1, "object should exist in either v2beta1 or v1")
			assert.ElementsMatch(t, tt.wantFinalizers, got1.GetFinalizers(),
				"v1: finalizer change must be persisted to k8s")
		})
	}
}

// TestStorageRequestAPI_Update_V1RefNilSafe verifies that Update does not panic when
// the ref holds only a v1 object (v2Beta1Obj is nil) — a pre-upgrade object scenario.
func TestStorageRequestAPI_Update_V1RefNilSafe(t *testing.T) {
	const (
		namespace = "sr-test-ns"
		name      = "shared-storage"
	)

	existing := &nvcav1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			Finalizers:      []string{StorageRequestFinalizer},
			ResourceVersion: "1",
		},
		Spec: nvcav1.StorageRequestSpec{
			Type: nvcav1.SharedStorageRequest,
		},
	}
	clientset := fakeclientset.NewSimpleClientset(existing)
	api := NewStorageRequestAPI(clientset)

	// ref with only v1Obj set — simulates a pre-upgrade object
	ref := &StorageRequestRef{v1Obj: existing}

	updated := &nvcav1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: []string{},
		},
		Spec: nvcav1.StorageRequestSpec{
			Type: nvcav1.SharedStorageRequest,
		},
	}

	// Must not panic.
	require.NotPanics(t, func() {
		_ = api.Update(context.Background(), ref, updated)
	})
}

func TestStorageRequestAPI_Update_UsesResourceVersionFromStatusUpdate(t *testing.T) {
	const (
		namespace = "sr-test-ns"
		name      = "shared-storage"
	)
	statusUpdatedResourceVersion := "2"
	storageRequestResource := schema.GroupResource{
		Group:    "nvca.nvcf.nvidia.io",
		Resource: "storagerequests",
	}

	tests := []struct {
		name        string
		existingObj runtime.Object
		ref         func(runtime.Object) *StorageRequestRef
	}{
		{
			name: "v2beta1",
			existingObj: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:            name,
					Namespace:       namespace,
					Finalizers:      []string{StorageRequestFinalizer},
					ResourceVersion: "1",
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type: nvcav2beta1.StorageRequestType(nvcav1.SharedStorageRequest),
				},
			},
			ref: func(obj runtime.Object) *StorageRequestRef {
				return &StorageRequestRef{v2Beta1Obj: obj.(*nvcav2beta1.StorageRequest)}
			},
		},
		{
			name: "v1",
			existingObj: &nvcav1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:            name,
					Namespace:       namespace,
					Finalizers:      []string{StorageRequestFinalizer},
					ResourceVersion: "1",
				},
				Spec: nvcav1.StorageRequestSpec{
					Type: nvcav1.SharedStorageRequest,
				},
			},
			ref: func(obj runtime.Object) *StorageRequestRef {
				return &StorageRequestRef{v1Obj: obj.(*nvcav1.StorageRequest)}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientset := fakeclientset.NewSimpleClientset(tt.existingObj)
			statusUpdates := 0
			specUpdates := 0
			clientset.Fake.PrependReactor("update", "storagerequests", func(action ktesting.Action) (bool, runtime.Object, error) {
				updateAction := action.(ktesting.UpdateAction)
				obj := updateAction.GetObject().DeepCopyObject()
				if action.GetSubresource() == "status" {
					statusUpdates++
					setStorageRequestResourceVersion(t, obj, statusUpdatedResourceVersion)
					return true, obj, nil
				}
				if action.GetSubresource() != "" {
					return false, nil, nil
				}
				specUpdates++
				if rv := storageRequestResourceVersion(t, obj); rv != statusUpdatedResourceVersion {
					return true, nil, apierrors.NewConflict(storageRequestResource, name, errors.New("stale resource version"))
				}
				return true, obj, nil
			})

			api := NewStorageRequestAPI(clientset)
			ref := tt.ref(tt.existingObj)
			updated := &nvcav1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:       name,
					Namespace:  namespace,
					Finalizers: []string{},
				},
				Spec: nvcav1.StorageRequestSpec{
					Type: nvcav1.SharedStorageRequest,
				},
			}

			err := api.Update(context.Background(), ref, updated)
			require.NoError(t, err)
			assert.Equal(t, 1, statusUpdates)
			assert.Equal(t, 1, specUpdates)
		})
	}
}

func setStorageRequestResourceVersion(t *testing.T, obj runtime.Object, resourceVersion string) {
	t.Helper()
	switch st := obj.(type) {
	case *nvcav2beta1.StorageRequest:
		st.ResourceVersion = resourceVersion
	case *nvcav1.StorageRequest:
		st.ResourceVersion = resourceVersion
	default:
		t.Fatalf("unexpected storage request object type %T", obj)
	}
}

func storageRequestResourceVersion(t *testing.T, obj runtime.Object) string {
	t.Helper()
	switch st := obj.(type) {
	case *nvcav2beta1.StorageRequest:
		return st.ResourceVersion
	case *nvcav1.StorageRequest:
		return st.ResourceVersion
	default:
		t.Fatalf("unexpected storage request object type %T", obj)
		return ""
	}
}
