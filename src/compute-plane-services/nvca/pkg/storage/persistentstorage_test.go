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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

func TestPersistentStorageConfigureHelpers(t *testing.T) {
	// create scheme
	sch := runtime.NewScheme()
	nvcav1new.AddToScheme(sch)
	assert.NotNil(t, sch)

	// create fake kubernetes client
	k8sClient := fake.NewClientBuilder().WithScheme(sch).Build()
	assert.NotNil(t, k8sClient)

	stReq := nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stor-req",
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			InternalPersistentStorage: &nvcav1new.InternalPersistentStorageSpec{},
		},
	}

	stCopy := stReq

	// create the object
	ctx := context.Background()
	err := k8sClient.Create(ctx, &stReq)
	assert.Nil(t, err)

	// call the reconciler manually as the setup is a mock
	r := &Reconciler{Client: k8sClient, metrics: newTestMetrics()}
	_, err = r.doInternalPersistentStorage(ctx, &stReq, &stCopy)
	assert.Nil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stor-req",
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			InternalPersistentStorage: &nvcav1new.InternalPersistentStorageSpec{},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StoragePending,
		},
	}

	_, err = r.doInternalPersistentStorage(ctx, &stReq, &stCopy)
	assert.Nil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stor-req",
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			InternalPersistentStorage: &nvcav1new.InternalPersistentStorageSpec{},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StorageInitRunning,
		},
	}

	_, err = r.doInternalPersistentStorage(ctx, &stReq, &stCopy)
	assert.NotNil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stor-req",
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			InternalPersistentStorage: &nvcav1new.InternalPersistentStorageSpec{},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StorageCreating,
		},
	}

	_, err = r.doInternalPersistentStorage(ctx, &stReq, &stCopy)
	assert.NotNil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stor-req",
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			InternalPersistentStorage: &nvcav1new.InternalPersistentStorageSpec{},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StoragePending,
		},
	}

	stCopy.Status.Phase = nvcav1new.StorageFailed
	_, err = r.doInternalPersistentStorage(ctx, &stReq, &stCopy)
	assert.Nil(t, err)
}
