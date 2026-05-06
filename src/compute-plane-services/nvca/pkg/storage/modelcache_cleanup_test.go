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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestCleanupModelCaches(t *testing.T) {
	// create the object
	ctx := context.Background()

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Labels: map[string]string{
				types.NCAIDKey:             "random-ncaid",
				types.FunctionIDKey:        "random-fn-id",
				types.FunctionVersionIDKey: "random-fn-versionid",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "nvcf-sc",
		},
	}

	// create fake kubernetes client
	k8sClient := fake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjects(pv).
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

	// call the reconciler manually as the setup is a mock
	r := &Reconciler{
		Client:       k8sClient,
		nowFunc:      time.Now,
		initStatuses: newInitStatusCache(k8sClient),
		metrics:      newTestMetrics(),
	}

	err := r.cleanupIdleModelCaches(ctx)
	require.NoError(t, err)

	pvCopy := &corev1.PersistentVolume{}

	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(pv), pvCopy)
	require.NoError(t, err)
	assert.Equal(t, pvCopy.Name, "test-pv")

	lastRefTime := time.Now().Add(-2 * time.Hour).Format(primaryPVLastReferencedTimeFormat)
	pv = &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Labels: map[string]string{
				types.NCAIDKey:                       "random-ncaid",
				types.FunctionIDKey:                  "random-fn-id",
				types.FunctionVersionIDKey:           "random-fn-versionid",
				primaryPVLabelKey:                    primaryPVLabelValue,
				primaryPVLastReferencedAnnotationKey: lastRefTime,
			},
			Annotations: map[string]string{
				primaryPVLastReferencedAnnotationKey: lastRefTime,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "nvcf-sc",
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeFailed,
		},
	}

	// create fake kubernetes client
	k8sClient = fake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjects(pv).
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

	// call the reconciler manually as the setup is a mock
	r = &Reconciler{
		Client:       k8sClient,
		nowFunc:      time.Now,
		initStatuses: newInitStatusCache(k8sClient),
	}

	err = r.cleanupIdleModelCaches(ctx)
	require.NoError(t, err)

	pvCopy = &corev1.PersistentVolume{}

	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(pv), pvCopy)
	require.True(t, errors.IsNotFound(err))

	pv = &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Labels: map[string]string{
				types.NCAIDKey:             "random-ncaid",
				types.FunctionIDKey:        "random-fn-id",
				types.FunctionVersionIDKey: "random-fn-versionid",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "nvcf-sc",
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeAvailable,
		},
	}

	// create fake kubernetes client
	k8sClient = fake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjects(pv).
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

	// call the reconciler manually as the setup is a mock
	r = &Reconciler{
		Client:       k8sClient,
		nowFunc:      time.Now,
		initStatuses: newInitStatusCache(k8sClient),
	}

	err = r.cleanupIdleModelCaches(ctx)
	require.NoError(t, err)

	pvCopy = &corev1.PersistentVolume{}

	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(pv), pvCopy)
	require.NoError(t, err)
	assert.Equal(t, pvCopy.Name, "test-pv")
}
