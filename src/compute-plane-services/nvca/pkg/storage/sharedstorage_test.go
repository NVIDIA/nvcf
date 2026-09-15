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
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	translatecommon "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/bombsimon/logrusr/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func newTestMetrics() *metrics.Metrics {
	return metrics.NewDefaultMetrics(
		"test-nca-id", "test-cluster", "test-group", "test-version",
		metrics.WithRegisterer(prometheus.NewRegistry()),
	)
}

func newTestContext(level ...logrus.Level) context.Context {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)
	log := core.GetLogger(ctx)
	if len(level) != 0 {
		log.Logger.SetLevel(level[0])
	}
	k8sLogger := logrusr.New(log, logrusr.WithReportCaller())
	ctx = logf.IntoContext(ctx, k8sLogger)
	return ctx
}

func TestStorageHelperAPIs(t *testing.T) {
	testPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "smb-container",
					Image: "random-image",
				},
			},
		},
	}

	stReq := nvcav1new.StorageRequest{
		Spec: nvcav1new.StorageRequestSpec{
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
			},
		},
	}

	configureSMBPod(&testPod, &stReq)
	assert.NotEmpty(t, testPod.Spec.Containers[0].Env)
}

func TestSMBConfigureHelpers(t *testing.T) {
	ctx := newTestContext()

	// create scheme
	sch := newTestScheme()

	namespace := &corev1.Namespace{}
	namespace.Name = "sr-123"

	// create fake kubernetes client
	k8sClient := newFakeClient(sch, namespace)
	assert.NotNil(t, k8sClient)

	stReq := nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: namespace.Name,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type:          nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{},
		},
		Status: nvcav1new.StorageRequestStatus{
			SharedStorage: &nvcav1new.SharedStorageStatus{
				KNS: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "kns-rwpvc",
					ReadOnlyPVCName:  "kns-ropvc",
				},
				Secrets: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "secret-rwpvc",
					ReadOnlyPVCName:  "secret-ropvc",
				},
			},
		},
	}

	stCopy := stReq

	// create the object
	err := k8sClient.Create(ctx, &stReq)
	assert.NoError(t, err)

	// call the reconciler manually as the setup is a mock
	r := &Reconciler{Client: k8sClient, metrics: newTestMetrics(), fff: &featureflagmock.Fetcher{}}
	_, err = r.doSharedStorageSMB(ctx, &stReq, &stCopy)
	assert.EqualError(t, err, "terminal error: smb container image is empty")

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: namespace.Name,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
			},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StoragePending,
			SharedStorage: &nvcav1new.SharedStorageStatus{
				KNS: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "kns-rwpvc",
					ReadOnlyPVCName:  "kns-ropvc",
				},
				Secrets: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "secret-rwpvc",
					ReadOnlyPVCName:  "secret-ropvc",
				},
			},
		},
	}

	_, err = r.doSharedStorageSMB(ctx, &stReq, &stCopy)
	assert.NoError(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: namespace.Name,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
			},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StorageInitRunning,
			SharedStorage: &nvcav1new.SharedStorageStatus{
				KNS: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "kns-rwpvc",
					ReadOnlyPVCName:  "kns-ropvc",
				},
				Secrets: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "secret-rwpvc",
					ReadOnlyPVCName:  "secret-ropvc",
				},
			},
		},
	}

	_, err = r.doSharedStorageSMB(ctx, &stReq, &stCopy)
	assert.NoError(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: namespace.Name,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
			},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StorageCreating,
			SharedStorage: &nvcav1new.SharedStorageStatus{
				KNS: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "kns-rwpvc",
					ReadOnlyPVCName:  "kns-ropvc",
				},
				Secrets: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "secret-rwpvc",
					ReadOnlyPVCName:  "secret-ropvc",
				},
			},
		},
	}

	_, err = r.doSharedStorageSMB(ctx, &stReq, &stCopy)
	assert.NoError(t, err)

	doCleanupNamespaced(ctx, r.Client, &stReq)
}

func TestDoSharedStorageSMB_MergesConfiguredTolerations(t *testing.T) {
	ctx := newTestContext()
	sch := newTestScheme()

	namespace := &corev1.Namespace{}
	namespace.Name = "sr-tolerations"

	k8sClient := newFakeClient(sch, namespace)
	r := &Reconciler{Client: k8sClient, metrics: newTestMetrics(), fff: &featureflagmock.Fetcher{}}

	stReq := nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: namespace.Name,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
				Server: &nvcav1new.SharedStorageServerSpec{
					SMBServerPodTolerations: []corev1.Toleration{{
						Key:      "dedicated",
						Operator: corev1.TolerationOpEqual,
						Value:    "nvca",
						Effect:   corev1.TaintEffectNoSchedule,
					}},
				},
			},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StoragePending,
			SharedStorage: &nvcav1new.SharedStorageStatus{
				KNS: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "kns-rwpvc",
					ReadOnlyPVCName:  "kns-ropvc",
				},
				Secrets: nvcav1new.SharedStorageTypeStatus{
					ReadWritePVCName: "secret-rwpvc",
					ReadOnlyPVCName:  "secret-ropvc",
				},
			},
		},
	}
	stCopy := stReq

	_, err := r.doSharedStorageSMB(ctx, &stReq, &stCopy)
	require.NoError(t, err)

	smbPod := &corev1.Pod{}
	require.NoError(t, r.Client.Get(ctx, client.ObjectKey{
		Namespace: namespace.Name,
		Name:      SMBServerPodName,
	}, smbPod))

	assert.Contains(t, smbPod.Spec.Tolerations, corev1.Toleration{
		Key:      translatecommon.NVIDIAGPUTolerationKey,
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	})
	assert.Contains(t, smbPod.Spec.Tolerations, corev1.Toleration{
		Key:      "dedicated",
		Operator: corev1.TolerationOpEqual,
		Value:    "nvca",
		Effect:   corev1.TaintEffectNoSchedule,
	})
}

func TestInitSharedStoragePVs(t *testing.T) {
	tests := []struct {
		name             string
		rwPVCName        string
		roPVCName        string
		sambaVolumeShare string
		storageClassName string
		namespace        string
		podIP            string
		capacity         resource.Quantity
		expectedPVs      int
		expectedRW       string
		expectedRO       string
	}{
		{
			name:             "test-new-pvs-and-pvcs",
			rwPVCName:        "rw-pvc",
			roPVCName:        "ro-pvc",
			sambaVolumeShare: "samba-share",
			storageClassName: "storage-class",
			namespace:        "namespace",
			podIP:            "10.0.0.1",
			capacity:         resource.MustParse("1Gi"),
			expectedPVs:      2,
			expectedRW:       "rw-pvc",
			expectedRO:       "ro-pvc",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, objects := initSharedStoragePVs(
				test.rwPVCName,
				test.roPVCName,
				test.sambaVolumeShare,
				test.storageClassName,
				test.namespace,
				test.podIP,
				test.capacity,
			)

			assert.Equal(t, test.expectedRW, status.ReadWritePVCName)
			assert.Equal(t, test.expectedRO, status.ReadOnlyPVCName)

			for _, obj := range objects {
				switch typedObj := obj.(type) {
				case *corev1.PersistentVolume:
					pv := typedObj
					assert.NotNil(t, pv.Spec.CSI)
					assert.Equal(t, test.storageClassName, pv.Spec.StorageClassName)
				case *corev1.PersistentVolumeClaim:
					pvc := typedObj
					assert.NotNil(t, pvc.Spec.StorageClassName)
					assert.Equal(t, test.capacity, pvc.Spec.Resources.Requests[corev1.ResourceStorage])
				}
			}
		})
	}
}

func TestNewPV(t *testing.T) {
	tests := []struct {
		name                 string
		pvName               string
		storageClassName     string
		sambaVolumeShare     string
		podIP                string
		credsSecretName      string
		namespace            string
		uid                  uint16
		gid                  uint16
		dirMode              string
		fileMode             string
		accessMode           corev1.PersistentVolumeAccessMode
		capacity             resource.Quantity
		expectedVolumeHandle string
	}{
		{
			name:                 "test-new-pv",
			pvName:               "pv-name",
			storageClassName:     "storage-class",
			sambaVolumeShare:     "samba-share",
			podIP:                "10.0.0.1",
			credsSecretName:      "creds-secret",
			namespace:            "namespace",
			uid:                  1000,
			gid:                  1000,
			dirMode:              "0700",
			fileMode:             "0600",
			accessMode:           corev1.ReadWriteMany,
			capacity:             resource.MustParse("1Gi"),
			expectedVolumeHandle: "10.0.0.1/samba-share#1000",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pv := newPV(
				test.pvName,
				test.storageClassName,
				test.sambaVolumeShare,
				test.podIP,
				test.credsSecretName,
				test.namespace,
				test.uid,
				test.gid,
				test.dirMode,
				test.fileMode,
				test.accessMode,
				test.capacity,
			)

			assert.NotNil(t, pv.Spec.CSI)
			assert.Equal(t, test.expectedVolumeHandle, pv.Spec.CSI.VolumeHandle)
			assert.Equal(t, test.storageClassName, pv.Spec.StorageClassName)
			assert.Equal(t, test.capacity, pv.Spec.Capacity[corev1.ResourceStorage])
		})
	}
}

func TestNewPVC(t *testing.T) {
	tests := []struct {
		name                 string
		pvcName              string
		pvName               string
		storageClass         string
		accessMode           corev1.PersistentVolumeAccessMode
		capacity             resource.Quantity
		expectedPVCName      string
		expectedPVName       string
		expectedStorageClass string
	}{
		{
			name:                 "test-new-pvc",
			pvcName:              "pvc-name",
			pvName:               "pv-name",
			storageClass:         "storage-class",
			accessMode:           corev1.ReadWriteMany,
			capacity:             resource.MustParse("1Gi"),
			expectedPVCName:      "pvc-name",
			expectedPVName:       "pv-name",
			expectedStorageClass: "storage-class",
		},
		{
			name:                 "test-new-pvc-with-empty-storage-class",
			pvcName:              "pvc-name",
			pvName:               "pv-name",
			storageClass:         "",
			accessMode:           corev1.ReadWriteMany,
			capacity:             resource.MustParse("1Gi"),
			expectedPVCName:      "pvc-name",
			expectedPVName:       "pv-name",
			expectedStorageClass: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pvc := newPVC(
				test.pvcName,
				test.pvName,
				test.storageClass,
				test.accessMode,
				test.capacity,
			)

			assert.NotNil(t, pvc)
			assert.Equal(t, test.expectedPVCName, pvc.Name)
			assert.Equal(t, test.expectedPVName, pvc.Spec.VolumeName)
			assert.Equal(t, test.expectedStorageClass, *pvc.Spec.StorageClassName)
			assert.Equal(t, test.capacity, pvc.Spec.Resources.Requests[corev1.ResourceStorage])
		})
	}
}

func TestNewSMBSecrets(t *testing.T) {
	tests := []struct {
		name       string
		expectedRW *corev1.Secret
		expectedRO *corev1.Secret
	}{
		{
			name: "rw and ro secrets",
			expectedRW: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: readWriteSecretName,
				},
				Data: map[string][]byte{
					"username": []byte(readWriteUsername),
					"uid":      []byte(strconv.Itoa(int(readWriteUID))),
					"gid":      []byte(strconv.Itoa(int(smbGID))),
				},
			},
			expectedRO: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: readOnlySecretName,
				},
				Data: map[string][]byte{
					"username": []byte(readOnlyUsername),
					"uid":      []byte(strconv.Itoa(int(readOnlyUID))),
					"gid":      []byte(strconv.Itoa(int(smbGID))),
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rwSecret, roSecret := newSMBSecrets()

			assert.Equal(t, test.expectedRW.Name, rwSecret.Name)
			assert.Subset(t, rwSecret.Data, test.expectedRW.Data)
			assert.NotEmpty(t, rwSecret.Data["password"])

			assert.Equal(t, test.expectedRO.Name, roSecret.Name)
			assert.Subset(t, roSecret.Data, test.expectedRO.Data)
			assert.NotEmpty(t, roSecret.Data["password"])
		})
	}
}

func TestNewStorageClass(t *testing.T) {
	bindingMode := strings.Clone(string(storagev1.VolumeBindingImmediate))
	tests := []struct {
		name     string
		nameArg  string
		expected *storagev1.StorageClass
	}{
		{
			name:    "storage class with name",
			nameArg: "my-storage-class",
			expected: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "my-storage-class",
					Finalizers: []string{StorageRequestFinalizer},
				},
				Provisioner:       "smb.csi.k8s.io",
				VolumeBindingMode: (*storagev1.VolumeBindingMode)(&bindingMode),
			},
		},
		{
			name:    "storage class with empty name",
			nameArg: "",
			expected: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Finalizers: []string{StorageRequestFinalizer},
					Name:       "",
				},
				Provisioner:       "smb.csi.k8s.io",
				VolumeBindingMode: (*storagev1.VolumeBindingMode)(&bindingMode),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := newStorageClass(test.nameArg)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestGetAllStorageCreatingPhaseOps(t *testing.T) {
	tests := []struct {
		name     string
		st       *nvcav1new.StorageRequest
		expected []storageCreatingPhaseOp
	}{
		{
			name: "both read-only and read-write PVCs",
			st: &nvcav1new.StorageRequest{
				Status: nvcav1new.StorageRequestStatus{
					SharedStorage: &nvcav1new.SharedStorageStatus{
						Secrets: nvcav1new.SharedStorageTypeStatus{
							ReadOnlyPVCName:      "ro-pvc-secrets",
							ReadOnlyPVName:       "ro-pv-secrets",
							ReadOnlyAccessMode:   corev1.ReadOnlyMany,
							ReadWritePVCName:     "rw-pvc-secrets",
							ReadWritePVName:      "rw-pv-secrets",
							ReadWriteAccessMode:  corev1.ReadWriteMany,
							StorageClassName:     "storage-class-secrets",
							StorageCapacity:      resource.MustParse("1Gi"),
							CreatePVCIfNotExists: true,
						},
						KNS: nvcav1new.SharedStorageTypeStatus{
							ReadOnlyPVCName:      "ro-pvc-kns",
							ReadOnlyPVName:       "ro-pv-kns",
							ReadOnlyAccessMode:   corev1.ReadOnlyMany,
							ReadWritePVCName:     "rw-pvc-kns",
							ReadWritePVName:      "rw-pv-kns",
							ReadWriteAccessMode:  corev1.ReadWriteMany,
							StorageClassName:     "storage-class-kns",
							StorageCapacity:      resource.MustParse("2Gi"),
							CreatePVCIfNotExists: true,
						},
					},
				},
			},
			expected: []storageCreatingPhaseOp{
				{
					CreatePVCIFNotExists: true,
					PVCName:              "ro-pvc-secrets",
					PVName:               "ro-pv-secrets",
					StorageClassName:     "storage-class-secrets",
					AccessMode:           corev1.ReadOnlyMany,
					Capacity:             resource.MustParse("1Gi"),
				},
				{
					CreatePVCIFNotExists: true,
					PVCName:              "rw-pvc-secrets",
					PVName:               "rw-pv-secrets",
					StorageClassName:     "storage-class-secrets",
					AccessMode:           corev1.ReadWriteMany,
					Capacity:             resource.MustParse("1Gi"),
				},
				{
					CreatePVCIFNotExists: true,
					PVCName:              "ro-pvc-kns",
					PVName:               "ro-pv-kns",
					StorageClassName:     "storage-class-kns",
					AccessMode:           corev1.ReadOnlyMany,
					Capacity:             resource.MustParse("2Gi"),
				},
				{
					CreatePVCIFNotExists: true,
					PVCName:              "rw-pvc-kns",
					PVName:               "rw-pv-kns",
					StorageClassName:     "storage-class-kns",
					AccessMode:           corev1.ReadWriteMany,
					Capacity:             resource.MustParse("2Gi"),
				},
			},
		},
		{
			name: "only read-write PVCs",
			st: &nvcav1new.StorageRequest{
				Status: nvcav1new.StorageRequestStatus{
					SharedStorage: &nvcav1new.SharedStorageStatus{
						Secrets: nvcav1new.SharedStorageTypeStatus{
							ReadWritePVCName:     "rw-pvc-secrets",
							ReadWritePVName:      "rw-pv-secrets",
							ReadWriteAccessMode:  corev1.ReadWriteMany,
							StorageClassName:     "storage-class-secrets",
							StorageCapacity:      resource.MustParse("1Gi"),
							CreatePVCIfNotExists: true,
						},
						KNS: nvcav1new.SharedStorageTypeStatus{
							ReadWritePVCName:     "rw-pvc-kns",
							ReadWritePVName:      "rw-pv-kns",
							ReadWriteAccessMode:  corev1.ReadWriteMany,
							StorageClassName:     "storage-class-kns",
							StorageCapacity:      resource.MustParse("2Gi"),
							CreatePVCIfNotExists: true,
						},
					},
				},
			},
			expected: []storageCreatingPhaseOp{
				{
					CreatePVCIFNotExists: true,
					PVCName:              "rw-pvc-secrets",
					PVName:               "rw-pv-secrets",
					StorageClassName:     "storage-class-secrets",
					AccessMode:           corev1.ReadWriteMany,
					Capacity:             resource.MustParse("1Gi"),
				},
				{
					CreatePVCIFNotExists: true,
					PVCName:              "rw-pvc-kns",
					PVName:               "rw-pv-kns",
					StorageClassName:     "storage-class-kns",
					AccessMode:           corev1.ReadWriteMany,
					Capacity:             resource.MustParse("2Gi"),
				},
			},
		},
		{
			name: "only read-only PVCs",
			st: &nvcav1new.StorageRequest{
				Status: nvcav1new.StorageRequestStatus{
					SharedStorage: &nvcav1new.SharedStorageStatus{
						Secrets: nvcav1new.SharedStorageTypeStatus{
							ReadOnlyPVCName:      "ro-pvc-secrets",
							ReadOnlyPVName:       "ro-pv-secrets",
							ReadOnlyAccessMode:   corev1.ReadOnlyMany,
							StorageClassName:     "storage-class-secrets",
							StorageCapacity:      resource.MustParse("1Gi"),
							CreatePVCIfNotExists: true,
						},
						KNS: nvcav1new.SharedStorageTypeStatus{
							ReadOnlyPVCName:      "ro-pvc-kns",
							ReadOnlyPVName:       "ro-pv-kns",
							ReadOnlyAccessMode:   corev1.ReadOnlyMany,
							StorageClassName:     "storage-class-kns",
							StorageCapacity:      resource.MustParse("2Gi"),
							CreatePVCIfNotExists: true,
						},
					},
				},
			},
			expected: []storageCreatingPhaseOp{
				{
					CreatePVCIFNotExists: true,
					PVCName:              "ro-pvc-secrets",
					PVName:               "ro-pv-secrets",
					StorageClassName:     "storage-class-secrets",
					AccessMode:           corev1.ReadOnlyMany,
					Capacity:             resource.MustParse("1Gi"),
				},
				{
					CreatePVCIFNotExists: true,
					PVCName:              "ro-pvc-kns",
					PVName:               "ro-pv-kns",
					StorageClassName:     "storage-class-kns",
					AccessMode:           corev1.ReadOnlyMany,
					Capacity:             resource.MustParse("2Gi"),
				},
			},
		},
		{
			name: "no PVCs",
			st: &nvcav1new.StorageRequest{
				Status: nvcav1new.StorageRequestStatus{
					SharedStorage: &nvcav1new.SharedStorageStatus{},
				},
			},
			expected: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := getAllStorageCreatingPhaseOps(test.st)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestDoSharedStorageSMB(t *testing.T) {
	ctx := newTestContext()

	sch := newTestScheme()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-ns"}}

	// Create CSI driver
	csiDriver := &storagev1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: SMBCSIDriverName,
		},
	}

	k8sClient := newFakeClient(sch, namespace, csiDriver)

	tests := []struct {
		name          string
		storageReq    *nvcav1new.StorageRequest
		expectedPhase nvcav1new.StoragePhase
		expectError   bool
		setupFunc     func(*nvcav1new.StorageRequest)
	}{
		{
			name: "empty SMB container image",
			storageReq: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-storage",
					Namespace: namespace.Name,
					Labels: map[string]string{
						"function-version-id": "test-func",
					},
				},
				Spec: nvcav1new.StorageRequestSpec{
					Type: nvcav1new.SharedStorageRequest,
					SharedStorage: &nvcav1new.SharedStorageSpec{
						Size: resource.MustParse("1Gi"),
					},
				},
				Status: nvcav1new.StorageRequestStatus{
					Phase: nvcav1new.StorageUnknown,
				},
			},
			expectedPhase: nvcav1new.StorageFailed,
			expectError:   true,
		},
		{
			name: "missing pull secret",
			storageReq: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-storage",
					Namespace: namespace.Name,
					Labels: map[string]string{
						"function-version-id": "test-func",
					},
				},
				Spec: nvcav1new.StorageRequestSpec{
					Type: nvcav1new.SharedStorageRequest,
					SharedStorage: &nvcav1new.SharedStorageSpec{
						SMBContainerImage: "smb:latest",
						Size:              resource.MustParse("1Gi"),
					},
				},
				Status: nvcav1new.StorageRequestStatus{
					Phase: nvcav1new.StorageUnknown,
				},
			},
			expectedPhase: nvcav1new.StorageFailed,
			expectError:   true,
		},
		{
			name: "valid storage request - unknown phase",
			storageReq: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-storage",
					Namespace: namespace.Name,
					Labels: map[string]string{
						"function-version-id": "test-func",
					},
				},
				Spec: nvcav1new.StorageRequestSpec{
					Type: nvcav1new.SharedStorageRequest,
					SharedStorage: &nvcav1new.SharedStorageSpec{
						SMBContainerImage:    "smb:latest",
						WorkerPullSecretName: "test-secret",
						Size:                 resource.MustParse("1Gi"),
					},
				},
				Status: nvcav1new.StorageRequestStatus{
					Phase: nvcav1new.StorageUnknown,
				},
			},
			expectedPhase: nvcav1new.StoragePending,
			expectError:   false,
			setupFunc: func(st *nvcav1new.StorageRequest) {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-secret",
						Namespace: st.Namespace,
					},
				}
				_ = k8sClient.Create(ctx, secret)
			},
		},
		{
			name: "valid storage request - pending phase",
			storageReq: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-storage",
					Namespace: namespace.Name,
					Labels: map[string]string{
						"function-version-id": "test-func",
					},
				},
				Spec: nvcav1new.StorageRequestSpec{
					Type: nvcav1new.SharedStorageRequest,
					SharedStorage: &nvcav1new.SharedStorageSpec{
						SMBContainerImage:    "smb:latest",
						WorkerPullSecretName: "test-secret",
						Size:                 resource.MustParse("1Gi"),
					},
				},
				Status: nvcav1new.StorageRequestStatus{
					Phase: nvcav1new.StoragePending,
				},
			},
			expectedPhase: nvcav1new.StorageInitRunning,
			expectError:   false,
			setupFunc: func(st *nvcav1new.StorageRequest) {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-secret",
						Namespace: st.Namespace,
					},
				}
				_ = k8sClient.Create(ctx, secret)
			},
		},
		{
			name: "invalid phase",
			storageReq: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-storage",
					Namespace: namespace.Name,
				},
				Status: nvcav1new.StorageRequestStatus{
					Phase: "InvalidPhase",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc(tt.storageReq)
			}

			stCopy := tt.storageReq.DeepCopy()
			r := &Reconciler{Client: k8sClient, fff: &featureflagmock.Fetcher{}}
			_, err := r.doSharedStorageSMB(ctx, tt.storageReq, stCopy)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectedPhase != "" {
				assert.Equal(t, tt.expectedPhase, stCopy.Status.Phase)
			}
		})
	}
}

// TestDoSharedStorageSMB_GetError_Requeues verifies that transient Get errors
// when checking SMB pod return requeue without error (to avoid counting as reconcile failure).
func TestDoSharedStorageSMB_GetError_Requeues(t *testing.T) {
	ctx := newTestContext()
	sch := newTestScheme()

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns-get-error",
		},
	}

	// Create a StorageRequest in StorageInitRunning phase
	stReq := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-storage-get-error",
			Namespace: namespace.Name,
			Labels: map[string]string{
				"function-version-id": "test-fv-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage: "smb:latest",
				Size:              resource.MustParse("1Gi"),
			},
		},
		Status: nvcav1new.StorageRequestStatus{
			Phase: nvcav1new.StorageInitRunning,
		},
	}

	k8sClient := newFakeClient(sch, namespace, stReq)

	r := &Reconciler{
		Client:  k8sClient,
		fff:     &featureflagmock.Fetcher{},
		metrics: newTestMetrics(),
	}

	stCopy := stReq.DeepCopy()
	res, err := r.doSharedStorageSMB(ctx, stReq, stCopy)

	// Should return nil error with RequeueAfter (pod not found triggers requeue)
	assert.NoError(t, err, "SMB pod Get error should not return an error")
	assert.NotZero(t, res.RequeueAfter, "Should requeue after transient Get error")
}
