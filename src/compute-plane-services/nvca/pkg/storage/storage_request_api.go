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

// Package storage provides a dual-version API for StorageRequest: get/update
// both v1 and v2beta1; create only v2beta1.
package storage

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	nvcav1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
)

// StorageRequestRef holds a StorageRequest in either v1 or v2beta1 form (exactly one non-nil).
// Used so get/update can work with both versions; V1() is used for reconcile logic.
type StorageRequestRef struct { //nolint:revive // name intentional for storage package
	v1Obj      *nvcav1.StorageRequest
	v2Beta1Obj *nvcav2beta1.StorageRequest
}

// V1 returns the StorageRequest as v1 for use in reconciler (converts from v2beta1 if needed).
func (r *StorageRequestRef) V1() *nvcav1.StorageRequest {
	if r.v1Obj != nil {
		return r.v1Obj
	}
	return nvcav2beta1.StorageRequestToV1(r.v2Beta1Obj)
}

// NewStorageRequestRefFromV2Beta1 returns a ref holding the given v2beta1 object (for use from callers that create v2beta1).
func NewStorageRequestRefFromV2Beta1(st *nvcav2beta1.StorageRequest) *StorageRequestRef {
	if st == nil {
		return nil
	}
	return &StorageRequestRef{v2Beta1Obj: st}
}

// StorageRequestAPI provides get/update for both v1 and v2beta1, and create for v2beta1 only.
type StorageRequestAPI interface { //nolint:revive // name intentional for storage package
	Get(ctx context.Context, namespace, name string) (*StorageRequestRef, error)
	Update(ctx context.Context, ref *StorageRequestRef, updated *nvcav1.StorageRequest) error
	Create(ctx context.Context, namespace string, st *nvcav2beta1.StorageRequest) (*nvcav2beta1.StorageRequest, error)
}

type storageRequestAPI struct {
	clientset versioned.Interface
}

// NewStorageRequestAPI returns a StorageRequestAPI that uses the given clientset.
func NewStorageRequestAPI(clientset versioned.Interface) StorageRequestAPI {
	return &storageRequestAPI{clientset: clientset}
}

// Get tries v2beta1 first, then v1; returns a ref with the object (or NotFound).
func (a *storageRequestAPI) Get(ctx context.Context, namespace, name string) (*StorageRequestRef, error) {
	st2, err := a.clientset.NvcaV2beta1().StorageRequests(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return &StorageRequestRef{v2Beta1Obj: st2}, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	st1, err := a.clientset.NvcaV1().StorageRequests(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return &StorageRequestRef{v1Obj: st1}, nil
}

// Update applies the v1 updated object to the ref's resource and performs status then spec update.
// On 409 Conflict (object modified), retries with the latest version.
func (a *storageRequestAPI) Update(ctx context.Context, ref *StorageRequestRef, updated *nvcav1.StorageRequest) error {
	namespace, name := ref.v2Beta1Obj.Namespace, ref.v2Beta1Obj.Name
	if ref.v2Beta1Obj == nil {
		namespace, name = ref.v1Obj.Namespace, ref.v1Obj.Name
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		freshRef, err := a.Get(ctx, namespace, name)
		if err != nil {
			return fmt.Errorf("get storage request %s: %w", name, err)
		}
		if freshRef.v2Beta1Obj != nil {
			nvcav2beta1.ApplyV1SpecAndStatus(freshRef.v2Beta1Obj, &updated.Spec, &updated.Status)
			if _, err := a.clientset.NvcaV2beta1().StorageRequests(freshRef.v2Beta1Obj.Namespace).
				UpdateStatus(ctx, freshRef.v2Beta1Obj, metav1.UpdateOptions{}); err != nil {
				return err
			}
			if _, err := a.clientset.NvcaV2beta1().StorageRequests(freshRef.v2Beta1Obj.Namespace).
				Update(ctx, freshRef.v2Beta1Obj, metav1.UpdateOptions{}); err != nil {
				return err
			}
			ref.v2Beta1Obj = freshRef.v2Beta1Obj
			ref.v1Obj = nil
			return nil
		}
		freshRef.v1Obj.Spec = updated.Spec
		freshRef.v1Obj.Status = updated.Status
		if _, err := a.clientset.NvcaV1().StorageRequests(freshRef.v1Obj.Namespace).
			UpdateStatus(ctx, freshRef.v1Obj, metav1.UpdateOptions{}); err != nil {
			return err
		}
		if _, err := a.clientset.NvcaV1().StorageRequests(freshRef.v1Obj.Namespace).
			Update(ctx, freshRef.v1Obj, metav1.UpdateOptions{}); err != nil {
			return err
		}
		ref.v1Obj = freshRef.v1Obj
		ref.v2Beta1Obj = nil
		return nil
	})
}

// Create creates a v2beta1 StorageRequest only.
func (a *storageRequestAPI) Create(ctx context.Context, namespace string, st *nvcav2beta1.StorageRequest) (*nvcav2beta1.StorageRequest, error) {
	return a.clientset.NvcaV2beta1().StorageRequests(namespace).Create(ctx, st, metav1.CreateOptions{})
}
