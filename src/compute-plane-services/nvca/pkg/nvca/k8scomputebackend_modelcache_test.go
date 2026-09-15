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
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	TestPVCName = "ropvc-test-name"
)

func Test_pvcReclaimDeleteCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	objs := []runtime.Object{getPVCObject(), getPVObject()}
	clients := mockKubeClients(objs...)
	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(1).
		Start(ctx)
	require.NoError(t, err)

	st, err := bc.k8sArtifactHelper.(K8sComputeBackend).CheckPVCState(ctx, TestPVCName)
	assert.Equal(t, PVCNotFound, st)
	assert.Nil(t, err)

	_, err = bc.clients.K8s.CoreV1().PersistentVolumeClaims(SystemNamespace).Get(ctx, TestPVCName, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err))
}

func Test_pvcRebindRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	objs := []runtime.Object{getPVCObject()}
	clients := mockKubeClients(objs...)
	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(1).
		Start(ctx)
	require.NoError(t, err)

	st, err := bc.k8sArtifactHelper.(K8sComputeBackend).CheckPVCState(ctx, TestPVCName)
	assert.Equal(t, PVCFoundBindFailed, st)
	assert.NotNil(t, err)

	bc, _, err = NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(1).
		WithPVCRebind(true).
		Start(ctx)
	require.NoError(t, err)

	st, err = bc.k8sArtifactHelper.(K8sComputeBackend).CheckPVCState(ctx, TestPVCName)
	assert.Equal(t, PVCFoundUnBound, st)
	assert.Nil(t, err)

	pvcObj, _ := bc.clients.K8s.CoreV1().PersistentVolumeClaims("nvcf-backend").Get(ctx,
		TestPVCName, metav1.GetOptions{})
	assert.NotEmpty(t, pvcObj.Annotations)
	assert.Contains(t, pvcObj.Annotations, types.NVCARebindAttemptedAnnotationKey)
}

func TestSetupPVCForReaders(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(1).
		Start(ctx)
	require.NoError(t, err)

	rwPvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rw-pvc-functionversionid",
			Namespace: "nvcf-backend",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: "test-pv",
		},
	}

	sReq := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sr-random-request",
			Namespace: "nvcf-backend",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{
				FunctionVersionID: "functionversionid",
			},
		},
	}

	mf := func(obj client.Object) {
		obj.SetNamespace("nvcf-backend")
	}

	phase, err := bc.k8sArtifactHelper.(K8sComputeBackend).SetupPVCForReaders(ctx, rwPvc, "test-init-job", sReq, mf)
	assert.NotNil(t, err)
	assert.Equal(t, phase, ROPVCSetupQueryFailed)
}

func Test_SetupPVCompletionFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	// turnoff volume detach check
	skipVolumeDetachCheck = true

	rwPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rw-pvc-functionversionid",
			Namespace: "nvcf-backend",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: "test-pv",
		},
	}

	testPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
		},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "nvcf-sc",
			ClaimRef: &corev1.ObjectReference{
				Name:      "rw-pvc-functionversionid",
				Namespace: "nvcf-backend",
			},
		},
	}

	sReq := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sr-random-request",
			Namespace: "nvcf-backend",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{
				FunctionVersionID: "functionversionid",
			},
		},
	}

	objs := []runtime.Object{rwPVC, testPV}
	clients := mockKubeClients(objs...)
	mf := func(obj client.Object) {
		obj.SetNamespace("nvcf-backend")
	}

	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(1).
		Start(ctx)
	require.NoError(t, err)

	phase, err := bc.k8sArtifactHelper.(K8sComputeBackend).SetupPVCForReaders(ctx, rwPVC, "test-init-job", sReq, mf)
	assert.Nil(t, err)
	assert.Equal(t, phase, ROPVCSetupCompleted)

	pvc, err := bc.clients.K8s.CoreV1().PersistentVolumeClaims("nvcf-backend").Get(ctx, "ro-pvc-functionversionid", metav1.GetOptions{})
	assert.Nil(t, err)
	assert.NotNil(t, pvc)
}

func getPVCObject() *corev1.PersistentVolumeClaim {
	pvcObj := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TestPVCName,
			Namespace: RequestsNamespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
			VolumeName:  TestPVCName,
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimLost,
		},
	}
	return &pvcObj
}

func getPVObject() *corev1.PersistentVolume {
	pvcObj := corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: TestPVCName,
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   ROAccessMode,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		},
	}
	return &pvcObj
}

func TestGetInitCacheJobFailureReason(t *testing.T) {
	tests := []struct {
		name           string
		job            *batchv1.Job
		expectedReason string
	}{
		{
			name: "job exceeded default backoff limit (6)",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: RequestsNamespace, // Must match podInstanceNamespace
				},
				Spec: batchv1.JobSpec{
					// BackoffLimit not set, defaults to 6
				},
				Status: batchv1.JobStatus{
					Failed: 7, // Exceeded default limit of 6
				},
			},
			expectedReason: "job_backoff_exceeded",
		},
		{
			name: "job exceeded custom backoff limit (3)",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: RequestsNamespace,
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(3)),
				},
				Status: batchv1.JobStatus{
					Failed: 4, // Exceeded custom limit of 3
				},
			},
			expectedReason: "job_backoff_exceeded",
		},
		{
			name: "job at custom backoff limit (3) - not exceeded",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: RequestsNamespace,
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(3)),
				},
				Status: batchv1.JobStatus{
					Failed: 3, // At limit but not exceeded
				},
			},
			expectedReason: "job_timeout",
		},
		{
			name: "job exceeded high custom backoff limit (10)",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: RequestsNamespace,
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(10)),
				},
				Status: batchv1.JobStatus{
					Failed: 11, // Exceeded custom limit of 10
				},
			},
			expectedReason: "job_backoff_exceeded",
		},
		{
			name: "job with zero backoff limit",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: RequestsNamespace,
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(0)),
				},
				Status: batchv1.JobStatus{
					Failed: 1, // Any failure exceeds limit of 0
				},
			},
			expectedReason: "job_backoff_exceeded",
		},
		{
			name: "job not exceeded - returns timeout",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: RequestsNamespace,
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(6)),
				},
				Status: batchv1.JobStatus{
					Failed: 2, // Well under limit
				},
			},
			expectedReason: "job_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(newTestContext())
			t.Cleanup(cancel)

			objs := []runtime.Object{tt.job}
			clients := mockKubeClients(objs...)
			bc, _, err := NewBackendk8sCacheBuilder().
				WithClients(clients).
				WithNamespaceLabels(labels.Set{"foo": "bar"}).
				WithStaticGPUCapacity(1).
				Start(ctx)
			require.NoError(t, err)

			reason := bc.k8sArtifactHelper.(K8sComputeBackend).getInitCacheJobFailureReason(ctx, tt.job)
			assert.Equal(t, tt.expectedReason, reason)
		})
	}
}

func TestGetInitCacheJobFailureReason_JobNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	// No job in the cluster
	clients := mockKubeClients()
	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(1).
		Start(ctx)
	require.NoError(t, err)

	nonExistentJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "non-existent-job",
			Namespace: RequestsNamespace, // Must match podInstanceNamespace
		},
	}

	reason := bc.k8sArtifactHelper.(K8sComputeBackend).getInitCacheJobFailureReason(ctx, nonExistentJob)
	assert.Equal(t, "job_not_found", reason)
}
