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
	"encoding/base64"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcaenvtest "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/envtest"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage/cacheprobe"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestReconcile_ModelCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg, _, cleanup, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanup)

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Scheme:                  mgrScheme,
		GracefulShutdownTimeout: new(time.Duration),
		BaseContext:             func() context.Context { return ctx },
		WebhookServer:           nvcaenvtest.NewFakeWebhookServer(),
		Metrics:                 nvcaenvtest.NewFakeMetricsOptions(),
		// Two model-cache envtests run in one process; the controller name
		// "modelcache" is otherwise globally unique per controller-runtime.
		Controller: ctrlconfig.Controller{SkipNameValidation: newBool(true)},
	})
	require.NoError(t, err)

	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()
	nvcaCfg := nvcaconfig.Config{}
	err = BuildController(nvcaCfg, nvcav1new.ModelCacheRequest, mgr, "my-cluster", "us-west-1", defaultTimeConfig, ControllerOptions{})
	require.NoError(t, err)

	mgrErrCh, err := nvcaenvtest.StartManager(ctx, mgr)
	require.NoError(t, err)

	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	mgr.GetCache().WaitForCacheSync(cctx)
	ccancel()

	c := mgr.GetClient()

	srNamespace := &corev1.Namespace{}
	srNamespace.Name = types.DefaultICMSRequestNamespace
	err = c.Create(ctx, srNamespace)
	require.NoError(t, err)
	err = c.Create(ctx, NewModelCacheInitNamespace())
	require.NoError(t, err)

	cacheHandle := "abc123handle"
	srSpec := newModelCacheICMSSpec(cacheHandle)

	sts := []*nvcav1new.StorageRequest{}
	for i := range 3 {
		namespace := &corev1.Namespace{}
		namespace.Name = fmt.Sprintf("sr-%d", i)
		err = c.Create(ctx, namespace)
		require.NoError(t, err)
		sr := &nvcav2beta1.ICMSRequest{}
		sr.Name, sr.Namespace = namespace.Name, srNamespace.Name
		sr.Spec = srSpec
		err = c.Create(ctx, sr)
		require.NoError(t, err)
		st := &nvcav1new.StorageRequest{}
		st.Name, st.Namespace = nvcav1new.ModelCacheRequest.Name(), namespace.Name
		st.Spec.Type = nvcav1new.ModelCacheRequest
		st.Spec.ICMSRequestName = sr.Name
		st.Spec.ICMSRequestNamespace = srNamespace.Name
		st.Spec.ModelCache = &nvcav1new.ModelCacheSpec{
			CacheHandle: cacheHandle,
			Encryption:  &nvcav1new.ModelCacheEncryption{Required: true},
		}
		err = c.Create(ctx, st)
		require.NoError(t, err)
		sts = append(sts, st)
	}
	primaryST := sts[0]

	// Test fan-out on all conditions.
	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StoragePending, st.Status.Phase)
			}
		}, 2*time.Second, 50*time.Millisecond)
	}

	// Ensure init artifacts exist.
	gotSecret := &corev1.Secret{}
	err = c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: "scsec-d5f545ee492260223239e813ad6a5795"}, gotSecret)
	require.NoError(t, err)
	gotSecret.TypeMeta = metav1.TypeMeta{}
	assert.Contains(t, gotSecret.Data, "dmcryptKey")

	gotSCName := "sc-d5f545ee492260223239e813ad6a5795"
	gotSC := &storagev1.StorageClass{}
	err = c.Get(ctx, client.ObjectKey{Name: "sc-d5f545ee492260223239e813ad6a5795"}, gotSC)
	require.NoError(t, err)
	gotSC.ObjectMeta = metav1.ObjectMeta{}
	gotSC.TypeMeta = metav1.TypeMeta{}
	vbm := storagev1.VolumeBindingImmediate
	reclaimPolicy := corev1.PersistentVolumeReclaimRetain
	assert.Equal(t, &storagev1.StorageClass{
		Provisioner:          NVMeshStorageClassProvisioner,
		AllowVolumeExpansion: newBool(true),
		VolumeBindingMode:    &vbm,
		ReclaimPolicy:        &reclaimPolicy,
		Parameters: map[string]string{
			NVMeshStorageClassVPG:       NVMeshStorageClassVPGType,
			NVMeshStorageClassCSIFS:     NVMeshStorageClassFS,
			NVMeshStorageClassCSISecret: "scsec-d5f545ee492260223239e813ad6a5795",
			NVMeshStorageClassCSINS:     ModelCacheInitNamespace,
		},
	}, gotSC)

	rwPVC := &corev1.PersistentVolumeClaim{}
	err = c.Get(ctx, client.ObjectKey{Name: "rw-pvc-" + cacheHandle, Namespace: ModelCacheInitNamespace}, rwPVC)
	require.NoError(t, err)
	if assert.NotNil(t, rwPVC.Spec.StorageClassName) {
		assert.Equal(t, gotSCName, *rwPVC.Spec.StorageClassName)
	}

	initJob := &batchv1.Job{}
	err = c.Get(ctx, client.ObjectKey{Name: "writer-job-" + cacheHandle, Namespace: ModelCacheInitNamespace}, initJob)
	require.NoError(t, err)

	// Create the job pod and set to running, ensure job is marked started.
	initJobPod := &corev1.Pod{}
	initJobPod.Name, initJobPod.Namespace = initJob.Spec.Template.Name+"-foobar", initJob.Namespace
	initJobPod.Labels = make(map[string]string, len(initJob.Spec.Template.Labels))
	maps.Copy(initJobPod.Labels, initJob.Spec.Template.Labels)
	maps.Copy(initJobPod.Labels, initJob.Spec.Selector.MatchLabels)
	initJobPod.Annotations = initJob.Spec.Template.Annotations
	initJobPod.Spec = initJob.Spec.Template.Spec
	err = c.Create(ctx, initJobPod)
	require.NoError(t, err)
	initJobPod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	err = c.Status().Update(ctx, initJobPod)
	require.NoError(t, err)

	initJob.Status.StartTime = &metav1.Time{Time: time.Now().Add(-1 * 1 * time.Minute)}
	err = c.Status().Update(ctx, initJob)
	require.NoError(t, err)

	// Ensure primart request is marked init running.
	// Dependents eventually will be on object updates.
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err = c.Get(ctx, client.ObjectKeyFromObject(primaryST), primaryST)
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav1new.StorageInitRunning, primaryST.Status.Phase,
				fmt.Sprintf("storage request %s/%s", primaryST.Name, primaryST.Namespace))
		}
	}, 5*time.Second, 50*time.Millisecond)

	primaryPV := &corev1.PersistentVolume{}
	primaryPV.Name = "primary-randomsuffix"
	primaryPV.Spec.ClaimRef = &corev1.ObjectReference{
		APIVersion: "v1",
		Kind:       "PersistentVolumeClaim",
		Namespace:  rwPVC.Namespace,
		Name:       rwPVC.Name,
		UID:        rwPVC.UID,
	}
	volumeHandlePrefix := "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:"
	primaryPV.Spec.CSI = &corev1.CSIPersistentVolumeSource{
		Driver:       "nvmesh",
		VolumeHandle: volumeHandlePrefix + ModelCacheInitNamespace,
	}
	primaryPV.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	primaryPV.Spec.Capacity = corev1.ResourceList{"storage": resource.MustParse("1Gi")}
	err = c.Create(ctx, primaryPV)
	require.NoError(t, err)

	rwPVC.Spec.VolumeName = primaryPV.Name
	err = c.Update(ctx, rwPVC)
	require.NoError(t, err)
	rwPVC.Status.Phase = corev1.ClaimBound
	err = c.Status().Update(ctx, rwPVC)
	require.NoError(t, err)

	// Ensure all are marked init running after PV/C bind events.
	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StorageInitRunning, st.Status.Phase,
					fmt.Sprintf("storage request %s/%s", st.Name, st.Namespace))
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	// Drive the writer Job to completion. The reconciler keys off
	// CompletionTime + Succeeded (see modelcache.go), not the conditions, but
	// the apiserver validates the condition shape and its rules differ by
	// version: k8s >= 1.34 requires SuccessCriteriaMet before Complete, while
	// k8s 1.30-1.33 reject SuccessCriteriaMet on this NonIndexed Job (it has no
	// SuccessPolicy). The Job's spec is reconciler-owned and immutable here, so
	// apply the modern shape and fall back to the pre-1.34 shape only when the
	// running apiserver returns an Invalid validation error.
	completeJob(ctx, t, c, initJob)

	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StorageCreating, st.Status.Phase,
					fmt.Sprintf("storage request %s/%s", st.Name, st.Namespace))
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	err = c.Get(ctx, client.ObjectKeyFromObject(primaryPV), primaryPV)
	require.NoError(t, err)
	primaryPV.Status.Phase = corev1.VolumeBound
	err = c.Status().Update(ctx, primaryPV)
	require.NoError(t, err)

	roPVCs := make([]*corev1.PersistentVolumeClaim, len(sts))
	for i, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			roPVC := &corev1.PersistentVolumeClaim{}
			err = c.Get(ctx, client.ObjectKey{Name: "ro-pvc-" + cacheHandle, Namespace: st.Namespace}, roPVC)
			if assert.NoError(ct, err) {
				roPVCs[i] = roPVC
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	for _, pvc := range roPVCs {
		if pvc == nil {
			continue
		}
		if assert.NotNil(t, pvc.Spec.StorageClassName) {
			assert.Equal(t, gotSCName, *pvc.Spec.StorageClassName)
		}
		pvc.Status.Phase = corev1.ClaimBound
		err = c.Status().Update(ctx, pvc)
		require.NoError(t, err)
	}

	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StorageReady, st.Status.Phase,
					fmt.Sprintf("storage request %s/%s", st.Name, st.Namespace))
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	for _, st := range sts {
		// Ensure secondary PV has updated volume handle.
		secondaryPV := &corev1.PersistentVolume{}
		err := c.Get(ctx, client.ObjectKey{Name: "secondary-pv-" + st.Namespace}, secondaryPV)
		require.NoError(t, err)
		assert.Equal(t, volumeHandlePrefix+st.Namespace, secondaryPV.Spec.CSI.VolumeHandle)

		err = c.Delete(ctx, st)
		require.NoError(t, err)
	}

	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			assert.True(ct, apierrors.IsNotFound(err))
		}, 10*time.Second, 50*time.Millisecond)
	}

	cancel()
	<-mgrErrCh
}

func TestGetPVCState(t *testing.T) {
	tests := []struct {
		name     string
		pvc      *corev1.PersistentVolumeClaim
		expected pvcState
	}{
		{
			name: "bound pvc",
			pvc: &corev1.PersistentVolumeClaim{
				Status: corev1.PersistentVolumeClaimStatus{
					Phase: corev1.ClaimBound,
				},
			},
			expected: 1, // pvcBound
		},
		{
			name: "unbound pvc",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Phase: corev1.ClaimPending,
				},
			},
			expected: 2, // pvcUnbound
		},
		{
			name: "bind failed pvc",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: metav1.Now().Add(-time.Hour)},
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Phase: corev1.ClaimPending,
				},
			},
			expected: 3, // pvcBindFailed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				k8sTimeConfig: (&k8sutil.TimeConfig{
					ModelCacheROPVCBindTimeGracePeriod: 2 * time.Minute,
				}).Complete(),
				metrics: newTestMetrics(),
			}
			state := r.getPVCState(tt.pvc)
			assert.Equal(t, tt.expected, state)
		})
	}
}

func TestGetInitCacheJobState(t *testing.T) {
	backoffLimit := int32(3)
	tests := []struct {
		name     string
		job      *batchv1.Job
		expected initCacheJobState
	}{
		{
			name: "job in progress",
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{BackoffLimit: &backoffLimit},
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: initCacheJobInProgress,
		},
		{
			name: "job completed",
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{BackoffLimit: &backoffLimit},
				Status: batchv1.JobStatus{
					CompletionTime: &metav1.Time{},
					Succeeded:      1,
				},
			},
			expected: initCacheJobCompleted,
		},
		{
			name: "job failed",
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{BackoffLimit: &backoffLimit},
				Status: batchv1.JobStatus{
					Failed: 4,
				},
			},
			expected: initCacheJobFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				fff:     &featureflagmock.Fetcher{},
				metrics: newTestMetrics(),
			}
			state := r.getInitCacheJobState(context.Background(), tt.job)
			assert.Equal(t, tt.expected, state)
		})
	}
}

func TestNewInitLease(t *testing.T) {
	stCopy := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-storage",
			Namespace: "test-ns",
		},
		Spec: nvcav1new.StorageRequestSpec{
			ICMSRequestName: "test-storage",
			ModelCache: &nvcav1new.ModelCacheSpec{
				CacheHandle: "foo",
			},
		},
	}

	lease := newInitLease(stCopy)

	assert.Equal(t, ModelCacheInitNamespace, lease.Namespace)
	assert.Equal(t, lease.Name, "modelcache-init-foo")
	assert.NotNil(t, lease.Spec)
	assert.NotNil(t, lease.Spec.HolderIdentity)
	assert.Equal(t, stCopy.Spec.ICMSRequestName, *lease.Spec.HolderIdentity)
}

func TestGetPrimaryPV(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		wantErr bool
	}{
		{
			name: "pv found",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "exp",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "pv not found no label",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv",
						Labels: map[string]string{
							primaryPVLabelKey: "true",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "pv not found other label",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "other",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "pv too many",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv-1",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "exp",
						},
					},
				},
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv-2",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "exp",
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(mgrScheme).
				WithObjects(tt.objects...).
				Build()

			r := &Reconciler{Client: client, metrics: newTestMetrics()}
			st := &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						types.NCAIDKey:             "test-nca",
						types.FunctionIDKey:        "test-function",
						types.FunctionVersionIDKey: "test-version",
					},
				},
				Spec: nvcav1new.StorageRequestSpec{
					ModelCache: &nvcav1new.ModelCacheSpec{
						CacheHandle: "exp",
					},
				},
			}
			_, err := r.getPrimaryPV(context.Background(), st)
			if (err != nil) != tt.wantErr {
				t.Errorf("getPrimaryPV() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_updateSecondaryPVVolumeHandle(t *testing.T) {
	namespace := "sr-fd7d88ab-6e18-4442-8a94-344da5f7341e"
	tests := []struct {
		name            string
		volumeHandle    string
		expVolumeHandle string
		expError        string
	}{
		{
			name:         "empty",
			volumeHandle: "",
			expError:     `volume handle "" has no colons`,
		},
		{
			name:         "no colons",
			volumeHandle: "foobar",
			expError:     `volume handle "foobar" has no colons`,
		},
		{
			name:            "colon only",
			volumeHandle:    ":",
			expVolumeHandle: ":" + namespace,
		},
		{
			name:            "empty namespace",
			volumeHandle:    "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:",
			expVolumeHandle: "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:" + namespace,
		},
		{
			name:            "mcinit namespace",
			volumeHandle:    "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:" + ModelCacheInitNamespace,
			expVolumeHandle: "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:" + namespace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updateSecondaryPVVolumeHandle(tt.volumeHandle, namespace)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expVolumeHandle, got)
			}
		})
	}
}

func getPrimaryPVObj() *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "primary-pv",
			Labels: map[string]string{
				primaryPVLabelKey:          "true",
				types.FunctionIDKey:        "test-function",
				types.FunctionVersionIDKey: "test-version",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			StorageClassName: "test-sc",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "test-driver",
					VolumeHandle: "test-handle",
				},
			},
		},
	}
}

func TestMapPodIssuesToFailureReason(t *testing.T) {
	tests := []struct {
		name           string
		podIssues      []string
		expectedReason string
	}{
		{
			name:           "empty issues returns init_job_failed",
			podIssues:      []string{},
			expectedReason: modelcachetypes.ReasonInitJobFailed,
		},
		{
			name:           "image pull issues",
			podIssues:      []string{"image pull issues"},
			expectedReason: modelcachetypes.ReasonImagePull,
		},
		{
			name:           "init stuck initializing",
			podIssues:      []string{"init stuck initializing"},
			expectedReason: modelcachetypes.ReasonInitStuck,
		},
		{
			name:           "scheduling timeout",
			podIssues:      []string{"timed out waiting to be scheduled"},
			expectedReason: modelcachetypes.ReasonSchedulingTimeout,
		},
		{
			name:           "admission rejected",
			podIssues:      []string{"admission rejected"},
			expectedReason: modelcachetypes.ReasonAdmissionRejected,
		},
		{
			name:           "unknown issue returns init_job_failed",
			podIssues:      []string{"some unknown issue"},
			expectedReason: modelcachetypes.ReasonInitJobFailed,
		},
		{
			name:           "image pull takes priority over other issues",
			podIssues:      []string{"init stuck initializing", "image pull issues", "admission rejected"},
			expectedReason: modelcachetypes.ReasonImagePull,
		},
		{
			name:           "init stuck takes priority over scheduling timeout",
			podIssues:      []string{"timed out waiting to be scheduled", "init stuck initializing"},
			expectedReason: modelcachetypes.ReasonInitStuck,
		},
		{
			name:           "scheduling timeout takes priority over admission rejected",
			podIssues:      []string{"admission rejected", "timed out waiting to be scheduled"},
			expectedReason: modelcachetypes.ReasonSchedulingTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := sets.New[string](tt.podIssues...)
			result := mapPodIssuesToFailureReason(issues)
			assert.Equal(t, tt.expectedReason, result)
		})
	}
}

// completeJob marks a writer Job as succeeded in a way the running apiserver
// accepts. The reconciler detects completion via CompletionTime + Succeeded
// (see modelcache.go), not the Job conditions, but the apiserver still
// validates the condition shape and its rules changed across versions:
//   - k8s >= 1.34 requires a SuccessCriteriaMet condition before Complete, and
//     rejects CompletionTime without Complete.
//   - k8s 1.30-1.33 reject SuccessCriteriaMet on a NonIndexed Job that has no
//     SuccessPolicy, which is exactly this writer Job's shape.
//
// The Job's spec is reconciler-owned and immutable on update, so the test
// cannot reshape it to satisfy both. Apply the modern (>= 1.34) condition shape
// and fall back to the pre-1.34 shape only when the apiserver returns an
// Invalid validation error (apierrors.IsInvalid).
func completeJob(ctx context.Context, t *testing.T, c client.Client, job *batchv1.Job) {
	t.Helper()

	job.Status.Succeeded = 1
	job.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	job.Status.Conditions = append(job.Status.Conditions,
		batchv1.JobCondition{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
		batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	)
	err := c.Status().Update(ctx, job)
	if err == nil {
		return
	}
	// Only a validation rejection means the modern condition shape is wrong for
	// this apiserver version. Any other error (conflict, network, RBAC) is a
	// real failure and must surface, not be masked by the fallback write.
	require.True(t, apierrors.IsInvalid(err), "unexpected error updating Job status: %v", err)

	// Pre-1.34 apiserver rejected SuccessCriteriaMet. Re-fetch to reset the
	// stale in-memory status and apply only Complete, which those versions
	// accept alongside CompletionTime.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(job), job))
	job.Status.Succeeded = 1
	job.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	job.Status.Conditions = append(job.Status.Conditions,
		batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	)
	require.NoError(t, c.Status().Update(ctx, job))
}

// newModelCacheLaunchEnvB64 builds the encoded launch env consumed by the
// model cache envtests at runtime. The registry credentials and assertion
// token are synthetic and assembled here instead of being committed as an
// encoded blob, which secret scanners flag even for fake values.
func newModelCacheLaunchEnvB64() string {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	registryCreds := b64(`{"k8sSecrets":[{"auths":{"nvcr.io":{"auth":"` + b64("stg-user:fake-registry-password") + `"}}}]}`)
	helmCreds := b64(`{"k8sSecrets":[{"auths":{"helm.ngc.nvidia.com":{"auth":"` + b64("stg-user:fake-registry-password") + `"}}}]}`)
	sidecarCred := b64(`{"auths":{"nvcr.io":{"auth":"` + b64("$oauthtoken:fake-sidecar-token") + `"}}}`)

	env := strings.Join([]string{
		`ATTACHED_GPU_COUNT="1"`,
		"BYOO_OTEL_COLLECTOR_CONTAINER=nvcr.io/qtfpt1h0bieu/nvcf-core/byoo-otel-collector:1.2.3",
		"CLOUD_PROVIDER=ON-PREM",
		"ESS_AGENT_CONTAINER=nvcr.io/nv-cf/nvcf-core/ess-agent:1.0.0",
		"FUNCTION_ID=5a3d4a7e-9ee3-4762-8d37-d3b40a6f84c6",
		"FUNCTION_NAME=my-func",
		"FUNCTION_VERSION_ID=2c948d9b-db5d-4f93-8c29-f5d8a5d89cb9",
		"GPU_NAME=L40",
		"HELM_CHART_INFERENCE_SERVICE_NAME=myservice",
		"CONTAINER_REGISTRIES_CREDENTIALS=" + registryCreds,
		"INFERENCE_CONTAINER_ENV=" + b64(`[{"key":"INFERENCE_ENV_KEY","value":"inference_value"}]`),
		"INFERENCE_HEALTH_ENDPOINT=/v2/health/ready",
		`INFERENCE_HEALTH_EXPECTED_RESPONSE_CODE="200"`,
		`INFERENCE_HEALTH_PORT="50051"`,
		`INFERENCE_PORT="50051"`,
		"INFERENCE_PROTOCOL=GRPC",
		"INFERENCE_URL=/grpc",
		"INIT_CONTAINER=nvcr.io/qtfpt1h0bieu/nvcf-core/nvcf_worker_init:0.24.10",
		`MAX_REQUEST_CONCURRENCY="1"`,
		"NCA_ID=_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
		"NVCF_FQDN=https://us-west-2.api.nvcf.nvidia.com",
		"NVCF_FQDN_GRPC=https://grpc.api.nvcf.nvidia.com",
		"NVCF_FQDN_NATS=tls://us-west-2.aws.cloud.nats.nvcf.nvidia.com:4222",
		"NVCF_WORKER_TOKEN=tok",
		"OTEL_CONTAINER=nvcr.io/qtfpt1h0bieu/nvcf-core/opentelemetry-collector:0.74.0",
		"OTEL_EXPORTER_OTLP_ENDPOINT=https://prod.otel.kaizen.nvidia.com:8282",
		"SECRETS_ASSERTION_TOKEN=fake-assertion-token",
		"SIDECAR_REGISTRY_CREDENTIAL=" + sidecarCred,
		"TRACING_ACCESS_TOKEN=trace-tok-1",
		"UTILS_CONTAINER=nvcr.io/qtfpt1h0bieu/nvcf-core/nvcf_worker_utils:2.21.4",
		"HELM_REGISTRIES_CREDENTIALS=" + helmCreds,
	}, "\n")
	return b64(env)
}

// newModelCacheICMSSpec returns the ICMS request spec used by the model cache
// envtests (NVMesh and shared-FS). The EnvironmentB64 carries the encoded
// launch env from which the writer job and cache PVC are decoded.
func newModelCacheICMSSpec(cacheHandle string) nvcav2beta1.ICMSRequestSpec {
	return nvcav2beta1.ICMSRequestSpec{
		FunctionDetails: function.Details{
			FunctionID:        "funcid-1",
			FunctionVersionID: "funcverid-1",
			FunctionType:      "DEFAULT",
		},
		Action:         common.FunctionCreationAction,
		NCAId:          "ncaid-1",
		RequestID:      "reqid1",
		MessageBatchID: "mbatchid1",
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				Action:            common.FunctionCreationAction,
				RequestID:         "reqid1",
				MessageBatchID:    "mbatchid1",
				InstanceType:      "ON-PREM.GPU.L40",
				InstanceTypeName:  "ON-PREM.GPU.L40_1x",
				InstanceTypeValue: "ON-PREM.GPU.L40",
				GPUType:           "L40",
				RequestedGPUCount: 1,
				InstanceCount:     1,
				NCAID:             "ncaid-1",
			},
			FunctionLaunchSpecification: &function.LaunchSpecification{
				CloudProvider:   "DGXCLOUD",
				ICMSEnvironment: "prod",
				GPUName:         "L40",
				EnvironmentB64:  newModelCacheLaunchEnvB64(),
				HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/image-segmentation-1.0.3.tgz",
					Values:       []byte(`{"foo":{"bar":"baz"}}`),
				},
				CacheLaunchSpecification: &common.CacheLaunchSpecification{
					CacheArtifacts: true,
					CacheHandle:    cacheHandle,
					CacheSize:      262144000,
				},
			},
		},
	}
}

// TestReconcile_ModelCacheSharedFS drives the shared-FS (nvcf-miniservice-sc) populate
// path: the cache is populated once via the single-writer init job (no NVMesh
// primary/secondary PV), then a per-namespace read-only PVC on the shared class
// is created and the request becomes Ready. The CSI probe is pre-seeded as ROX
// so the path does not attempt a live probe under envtest.
func TestReconcile_ModelCacheSharedFS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg, _, cleanup, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanup)

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Scheme:                  mgrScheme,
		GracefulShutdownTimeout: new(time.Duration),
		BaseContext:             func() context.Context { return ctx },
		WebhookServer:           nvcaenvtest.NewFakeWebhookServer(),
		Metrics:                 nvcaenvtest.NewFakeMetricsOptions(),
		// Two model-cache envtests run in one process; the controller name
		// "modelcache" is otherwise globally unique per controller-runtime.
		Controller: ctrlconfig.Controller{SkipNameValidation: newBool(true)},
	})
	require.NoError(t, err)

	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()
	err = BuildController(nvcaconfig.Config{}, nvcav1new.ModelCacheRequest, mgr, "my-cluster", "us-west-1", defaultTimeConfig, ControllerOptions{})
	require.NoError(t, err)

	mgrErrCh, err := nvcaenvtest.StartManager(ctx, mgr)
	require.NoError(t, err)

	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	mgr.GetCache().WaitForCacheSync(cctx)
	ccancel()

	c := mgr.GetClient()

	srNamespace := &corev1.Namespace{}
	srNamespace.Name = types.DefaultICMSRequestNamespace
	require.NoError(t, c.Create(ctx, srNamespace))
	require.NoError(t, c.Create(ctx, NewModelCacheInitNamespace()))

	// The shared class exists (operator- or Samba-provided) and its reader
	// access mode is pre-resolved to ROX so resolveSharedFSStrategy does not
	// run a live probe.
	require.NoError(t, c.Create(ctx, &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: HelmCacheSharedStorageClassName},
		Provisioner: SMBCSIDriverName,
	}))
	store := cacheprobe.NewStateStore(c, ModelCacheInitNamespace)
	require.NoError(t, store.Save(ctx, map[string]cacheprobe.Result{
		cacheprobe.ResultKey(HelmCacheSharedStorageClassName, cacheprobe.StrategyROX): {
			State: cacheprobe.StateSupported,
		},
	}))

	cacheHandle := "sharedfshandle"
	workloadNS := &corev1.Namespace{}
	workloadNS.Name = "sr-sharedfs"
	require.NoError(t, c.Create(ctx, workloadNS))

	sr := &nvcav2beta1.ICMSRequest{}
	sr.Name, sr.Namespace = workloadNS.Name, srNamespace.Name
	sr.Spec = newModelCacheICMSSpec(cacheHandle)
	require.NoError(t, c.Create(ctx, sr))

	st := &nvcav1new.StorageRequest{}
	st.Name, st.Namespace = nvcav1new.ModelCacheRequest.Name(), workloadNS.Name
	st.Spec.Type = nvcav1new.ModelCacheRequest
	st.Spec.ICMSRequestName = sr.Name
	st.Spec.ICMSRequestNamespace = srNamespace.Name
	st.Spec.ModelCache = &nvcav1new.ModelCacheSpec{
		CacheHandle: cacheHandle,
		Backend:     string(HelmCacheBackendSharedFS),
	}
	require.NoError(t, c.Create(ctx, st))

	// The writer job is created on the shared backend.
	initJob := &batchv1.Job{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKey{Name: "writer-job-" + cacheHandle, Namespace: ModelCacheInitNamespace}, initJob)
		assert.NoError(ct, err)
	}, 5*time.Second, 50*time.Millisecond)

	// The writer RW PVC is on the shared class, not an NVMesh class.
	rwPVC := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "rw-pvc-" + cacheHandle, Namespace: ModelCacheInitNamespace}, rwPVC))
	if assert.NotNil(t, rwPVC.Spec.StorageClassName) {
		assert.Equal(t, HelmCacheSharedStorageClassName, *rwPVC.Spec.StorageClassName)
	}

	// Drive the writer job to "started" so the request moves to InitRunning.
	initJobPod := &corev1.Pod{}
	initJobPod.Name, initJobPod.Namespace = initJob.Spec.Template.Name+"-foobar", initJob.Namespace
	initJobPod.Labels = make(map[string]string, len(initJob.Spec.Template.Labels))
	maps.Copy(initJobPod.Labels, initJob.Spec.Template.Labels)
	maps.Copy(initJobPod.Labels, initJob.Spec.Selector.MatchLabels)
	initJobPod.Annotations = initJob.Spec.Template.Annotations
	initJobPod.Spec = initJob.Spec.Template.Spec
	require.NoError(t, c.Create(ctx, initJobPod))
	initJobPod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	require.NoError(t, c.Status().Update(ctx, initJobPod))

	initJob.Status.StartTime = &metav1.Time{Time: time.Now().Add(-1 * time.Minute)}
	require.NoError(t, c.Status().Update(ctx, initJob))

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKeyFromObject(st), st)
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav1new.StorageInitRunning, st.Status.Phase)
		}
	}, 5*time.Second, 50*time.Millisecond)

	// Bind the writer RW PVC and complete the job: shared-FS keeps the writer
	// claim (no primary PV finalize) and moves to Creating.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rwPVC), rwPVC))
	rwPVC.Status.Phase = corev1.ClaimBound
	require.NoError(t, c.Status().Update(ctx, rwPVC))

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(initJob), initJob))
	completeJob(ctx, t, c, initJob)

	// A read-only reader PVC is created in the workload namespace on the shared
	// class with the probed ROX access mode.
	roPVC := &corev1.PersistentVolumeClaim{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKey{Name: "ro-pvc-" + cacheHandle, Namespace: workloadNS.Name}, roPVC)
		assert.NoError(ct, err)
	}, 5*time.Second, 50*time.Millisecond)
	if assert.NotNil(t, roPVC.Spec.StorageClassName) {
		assert.Equal(t, HelmCacheSharedStorageClassName, *roPVC.Spec.StorageClassName)
	}
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}, roPVC.Spec.AccessModes)

	// Shared-FS does not create cross-namespace primary/secondary PVs.
	secondaryPV := &corev1.PersistentVolume{}
	err = c.Get(ctx, client.ObjectKey{Name: "secondary-pv-" + st.Name}, secondaryPV)
	assert.True(t, apierrors.IsNotFound(err), "shared-FS must not create a secondary PV")

	// Bind the reader PVC: the request becomes Ready and exposes the RO PVC.
	roPVC.Status.Phase = corev1.ClaimBound
	require.NoError(t, c.Status().Update(ctx, roPVC))

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKeyFromObject(st), st)
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav1new.StorageReady, st.Status.Phase)
			if assert.NotNil(ct, st.Status.ModelCache) {
				assert.Equal(ct, "ro-pvc-"+cacheHandle, st.Status.ModelCache.ROPVCName)
			}
		}
	}, 5*time.Second, 50*time.Millisecond)

	// After population the init job and lease are torn down (partial cleanup)
	// while the writer PVC is retained as the durable populated marker.
	lease := &coordv1.Lease{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		leaseErr := c.Get(ctx, client.ObjectKey{Name: buildInitLeaseName(cacheHandle), Namespace: ModelCacheInitNamespace}, lease)
		assert.True(ct, apierrors.IsNotFound(leaseErr), "init lease must be deleted after shared-FS population")
	}, 5*time.Second, 50*time.Millisecond)

	// envtest runs no GC controller, so a foreground-deleted Job lingers with a
	// deletion timestamp; deletion having been initiated is the signal.
	gotJob := &batchv1.Job{}
	switch jobErr := c.Get(ctx, client.ObjectKeyFromObject(initJob), gotJob); {
	case apierrors.IsNotFound(jobErr):
	default:
		require.NoError(t, jobErr)
		assert.NotNil(t, gotJob.DeletionTimestamp, "init job deletion must be initiated after shared-FS population")
	}

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rwPVC), rwPVC))
	assert.Nil(t, rwPVC.DeletionTimestamp, "writer PVC must be retained as the populated marker")

	require.NoError(t, c.Delete(ctx, st))
	cancel()
	<-mgrErrCh
}

// TestReconcile_ModelCacheSamba drives the Samba (backend 3) path and asserts it
// follows the NVMesh lifecycle, differing only in the backing store. The first
// namespace populates the cache via the single-writer init job; on success the
// static SMB RW PV is retained as the durable primary-PV marker and the writer
// job/PVC/lease are torn down. A second namespace with the same handle then
// binds its own RO reader purely from the marker, without re-running the writer
// or touching the init lease, which is the cross-namespace / restart-safe
// behavior the marker provides.
func TestReconcile_ModelCacheSamba(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg, _, cleanup, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanup)

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Scheme:                  mgrScheme,
		GracefulShutdownTimeout: new(time.Duration),
		BaseContext:             func() context.Context { return ctx },
		WebhookServer:           nvcaenvtest.NewFakeWebhookServer(),
		Metrics:                 nvcaenvtest.NewFakeMetricsOptions(),
		Controller:              ctrlconfig.Controller{SkipNameValidation: newBool(true)},
	})
	require.NoError(t, err)

	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()
	// The Samba path requires the server image to be configured; an empty image
	// makes EnsureSambaModelCacheInfra return a terminal error.
	nvcaCfg := nvcaconfig.Config{}
	nvcaCfg.Agent.SharedStorage.Server.Image = "samba:test"
	err = BuildController(nvcaCfg, nvcav1new.ModelCacheRequest, mgr, "my-cluster", "us-west-1", defaultTimeConfig, ControllerOptions{})
	require.NoError(t, err)

	mgrErrCh, err := nvcaenvtest.StartManager(ctx, mgr)
	require.NoError(t, err)

	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	mgr.GetCache().WaitForCacheSync(cctx)
	ccancel()

	c := mgr.GetClient()

	srNamespace := &corev1.Namespace{}
	srNamespace.Name = types.DefaultICMSRequestNamespace
	require.NoError(t, c.Create(ctx, srNamespace))
	require.NoError(t, c.Create(ctx, NewModelCacheInitNamespace()))

	cacheHandle := "sambahandle"

	// newSambaSR creates the ICMSRequest + StorageRequest pair for a workload
	// namespace, all wired to the shared cache handle on the Samba backend.
	newSambaSR := func(nsName string) *nvcav1new.StorageRequest {
		ns := &corev1.Namespace{}
		ns.Name = nsName
		require.NoError(t, c.Create(ctx, ns))
		icms := &nvcav2beta1.ICMSRequest{}
		icms.Name, icms.Namespace = nsName, srNamespace.Name
		icms.Spec = newModelCacheICMSSpec(cacheHandle)
		require.NoError(t, c.Create(ctx, icms))
		st := &nvcav1new.StorageRequest{}
		st.Name, st.Namespace = nvcav1new.ModelCacheRequest.Name(), nsName
		st.Spec.Type = nvcav1new.ModelCacheRequest
		st.Spec.ICMSRequestName = icms.Name
		st.Spec.ICMSRequestNamespace = srNamespace.Name
		st.Spec.ModelCache = &nvcav1new.ModelCacheSpec{
			CacheHandle: cacheHandle,
			Backend:     string(HelmCacheBackendSamba),
		}
		require.NoError(t, c.Create(ctx, st))
		return st
	}

	st1 := newSambaSR("sr-samba-1")

	// The per-handle Samba server Deployment (samba-<handle>) is bootstrapped
	// idempotently. envtest has no deployment controller, so mark it Available to
	// unblock the writer init.
	dep := &appsv1.Deployment{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKey{Name: sambaModelCacheResourceName(cacheHandle), Namespace: ModelCacheInitNamespace}, dep)
		assert.NoError(ct, err)
	}, 5*time.Second, 50*time.Millisecond)
	dep.Status.Replicas = 1
	dep.Status.ReadyReplicas = 1
	dep.Status.AvailableReplicas = 1
	require.NoError(t, c.Status().Update(ctx, dep))

	// The writer job and the STATIC SMB RW PV/PVC are created (no StorageClass).
	initJob := &batchv1.Job{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKey{Name: "writer-job-" + cacheHandle, Namespace: ModelCacheInitNamespace}, initJob)
		assert.NoError(ct, err)
	}, 5*time.Second, 50*time.Millisecond)

	rwPVC := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "rw-pvc-" + cacheHandle, Namespace: ModelCacheInitNamespace}, rwPVC))
	assert.Equal(t, "samba-rw-pv-"+cacheHandle, rwPVC.Spec.VolumeName)
	if assert.NotNil(t, rwPVC.Spec.StorageClassName) {
		assert.Equal(t, "", *rwPVC.Spec.StorageClassName, "Samba RW PVC must bind a static PV, not a StorageClass")
	}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "samba-rw-pv-" + cacheHandle}, &corev1.PersistentVolume{}))

	// Drive the writer pod to running so the request moves to InitRunning.
	initJobPod := &corev1.Pod{}
	initJobPod.Name, initJobPod.Namespace = initJob.Spec.Template.Name+"-foobar", initJob.Namespace
	initJobPod.Labels = make(map[string]string, len(initJob.Spec.Template.Labels))
	maps.Copy(initJobPod.Labels, initJob.Spec.Template.Labels)
	maps.Copy(initJobPod.Labels, initJob.Spec.Selector.MatchLabels)
	initJobPod.Annotations = initJob.Spec.Template.Annotations
	initJobPod.Spec = initJob.Spec.Template.Spec
	require.NoError(t, c.Create(ctx, initJobPod))
	initJobPod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	require.NoError(t, c.Status().Update(ctx, initJobPod))

	initJob.Status.StartTime = &metav1.Time{Time: time.Now().Add(-1 * time.Minute)}
	require.NoError(t, c.Status().Update(ctx, initJob))

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKeyFromObject(st1), st1)
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav1new.StorageInitRunning, st1.Status.Phase)
		}
	}, 5*time.Second, 50*time.Millisecond)

	// Bind the RW PVC and complete the writer job.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rwPVC), rwPVC))
	rwPVC.Status.Phase = corev1.ClaimBound
	require.NoError(t, c.Status().Update(ctx, rwPVC))

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(initJob), initJob))
	completeJob(ctx, t, c, initJob)

	// On success the per-handle backing PVC (samba-<handle>) is stamped with the
	// durable populated marker. That label, not an NVMesh primary PV, is the
	// cross-namespace / restart-safe reuse signal for the Samba backend.
	backingPVC := &corev1.PersistentVolumeClaim{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKey{Name: SambaModelCacheBackingPVCName(cacheHandle), Namespace: ModelCacheInitNamespace}, backingPVC)
		if assert.NoError(ct, err) {
			assert.Equal(ct, cachePopulatedLabelValue, backingPVC.Labels[cachePopulatedLabelKey])
		}
	}, 5*time.Second, 50*time.Millisecond)

	// The writer's static plumbing PV is deleted with the writer teardown
	// (static PVs are never reclaimed by the PV controller, so it must be
	// removed explicitly). envtest has no PV-protection controller, so accept a
	// pending deletion (deletionTimestamp set) as deleted.
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		writerPV := &corev1.PersistentVolume{}
		err := c.Get(ctx, client.ObjectKey{Name: sambaModelCacheWriterPVName(cacheHandle)}, writerPV)
		assert.True(ct, apierrors.IsNotFound(err) || writerPV.DeletionTimestamp != nil,
			"writer plumbing PV must be deleted with the writer teardown")
	}, 5*time.Second, 50*time.Millisecond)

	// First namespace: an RO reader PV/PVC is created against the per-handle share.
	ro1 := &corev1.PersistentVolumeClaim{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKey{Name: "ro-pvc-" + cacheHandle, Namespace: st1.Namespace}, ro1)
		assert.NoError(ct, err)
	}, 5*time.Second, 50*time.Millisecond)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "samba-ro-pv-" + st1.Namespace + "-" + cacheHandle}, &corev1.PersistentVolume{}))
	ro1.Status.Phase = corev1.ClaimBound
	require.NoError(t, c.Status().Update(ctx, ro1))

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKeyFromObject(st1), st1)
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav1new.StorageReady, st1.Status.Phase)
		}
	}, 5*time.Second, 50*time.Millisecond)

	// Second namespace, same handle: it must reach Ready purely from the durable
	// backing-PVC populated marker. The init lease was deleted during the first
	// namespace's writer teardown; if the second namespace re-ran init it would
	// recreate the lease. Asserting the lease stays absent proves the marker path
	// was taken.
	st2 := newSambaSR("sr-samba-2")
	ro2 := &corev1.PersistentVolumeClaim{}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKey{Name: "ro-pvc-" + cacheHandle, Namespace: st2.Namespace}, ro2)
		assert.NoError(ct, err)
	}, 5*time.Second, 50*time.Millisecond)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "samba-ro-pv-" + st2.Namespace + "-" + cacheHandle}, &corev1.PersistentVolume{}))
	ro2.Status.Phase = corev1.ClaimBound
	require.NoError(t, c.Status().Update(ctx, ro2))

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKeyFromObject(st2), st2)
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav1new.StorageReady, st2.Status.Phase)
		}
	}, 5*time.Second, 50*time.Millisecond)

	lease := &coordv1.Lease{}
	leaseErr := c.Get(ctx, client.ObjectKey{Name: buildInitLeaseName(cacheHandle), Namespace: ModelCacheInitNamespace}, lease)
	assert.True(t, apierrors.IsNotFound(leaseErr),
		"second namespace must consume the cache via the backing-PVC marker, not re-run the init lease")

	// Deleting a consumer SR must run cleanup to completion and drop the
	// storage-request finalizer (the workload namespaces are not terminating
	// here, so this exercises the normal cleanup path, not the escape hatch).
	// Deleting one consumer must NOT delete the shared per-handle backing PVC;
	// it is reclaimed only when the cache goes idle (cleanupIdleModelCaches).
	require.NoError(t, c.Delete(ctx, st1))
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKeyFromObject(st1), st1)
		assert.True(ct, apierrors.IsNotFound(err), "st1 must be fully deleted (finalizer removed by cleanup)")
	}, 10*time.Second, 100*time.Millisecond)

	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: SambaModelCacheBackingPVCName(cacheHandle), Namespace: ModelCacheInitNamespace}, backingPVC),
		"shared backing PVC must survive a single consumer's deletion")

	require.NoError(t, c.Delete(ctx, st2))
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err := c.Get(ctx, client.ObjectKeyFromObject(st2), st2)
		assert.True(ct, apierrors.IsNotFound(err), "st2 must be fully deleted (finalizer removed by cleanup)")
	}, 10*time.Second, 100*time.Millisecond)

	cancel()
	<-mgrErrCh
}

// TestDoModelCacheSharedFS_Validation covers the early terminal-validation
// branches of the shared-FS path that run before any probe or client call.
func TestDoModelCacheSharedFS_Validation(t *testing.T) {
	tests := []struct {
		name       string
		modelCache *nvcav1new.ModelCacheSpec
	}{
		{name: "nil modelCache", modelCache: nil},
		{name: "empty cacheHandle", modelCache: &nvcav1new.ModelCacheSpec{Backend: string(HelmCacheBackendSharedFS)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				Client:  fake.NewClientBuilder().WithScheme(mgrScheme).Build(),
				metrics: newTestMetrics(),
				fff:     &featureflagmock.Fetcher{},
			}
			st := nvcav1new.StorageRequest{}
			stCopy := &nvcav1new.StorageRequest{Spec: nvcav1new.StorageRequestSpec{ModelCache: tt.modelCache}}
			_, err := r.doModelCacheSharedFS(context.Background(), st, stCopy, &nvcav2beta1.ICMSRequest{})
			require.Error(t, err)
			assert.True(t, isTerminal(err), "validation failure must be terminal")
		})
	}
}

// TestDoCleanupModelCacheNVMesh_RequeuesWhileWriterVolumeAttached proves the
// cleanup never blocks the single reconcile worker polling for volume detach:
// while the writer volume is still attached read-write it requeues (does not
// delete the writer PVC or mark cleanup successful), and once detached it
// completes.
func TestDoCleanupModelCacheNVMesh_RequeuesWhileWriterVolumeAttached(t *testing.T) {
	ctx := context.Background()
	handle := "attachhandle"
	pvName := "pv-" + handle

	rwPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rw-pvc-" + handle,
			Namespace: ModelCacheInitNamespace,
			Labels:    map[string]string{modelCacheHandleLabelKey: handle},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: pvName},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: pvName},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		},
	}
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: "va-1"},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: "smb.csi.k8s.io",
			NodeName: "node1",
			Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvName},
		},
	}
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(rwPVC, pv, va).Build()
	r := &Reconciler{
		Client:        c,
		metrics:       newTestMetrics(),
		fff:           &featureflagmock.Fetcher{},
		nowFunc:       time.Now,
		k8sTimeConfig: (&k8sutil.TimeConfig{}).Complete(),
	}
	st := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{Name: nvcav1new.ModelCacheRequest.Name(), Namespace: "ns1"},
		Spec: nvcav1new.StorageRequestSpec{
			Type:       nvcav1new.ModelCacheRequest,
			ModelCache: &nvcav1new.ModelCacheSpec{CacheHandle: handle},
		},
	}

	// Still attached read-write: requeue, do not delete the writer PVC, do not
	// mark cleanup successful.
	res, err := r.doCleanupModelCacheNVMesh(ctx, st)
	require.NoError(t, err)
	assert.Equal(t, volumeDetachRequeueInterval, res.RequeueAfter, "must requeue (not block) while attached")
	if cond := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeCleanupSuccessful); assert.NotNil(t, cond) {
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
	}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rwPVC), &corev1.PersistentVolumeClaim{}),
		"writer PVC must not be deleted while its volume is still attached")

	// Detach: cleanup completes, writer PVC deleted, condition True.
	require.NoError(t, c.Delete(ctx, va))
	res, err = r.doCleanupModelCacheNVMesh(ctx, st)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res, "must not requeue once detached")
	if cond := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeCleanupSuccessful); assert.NotNil(t, cond) {
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
	}
	getErr := c.Get(ctx, client.ObjectKeyFromObject(rwPVC), &corev1.PersistentVolumeClaim{})
	assert.True(t, apierrors.IsNotFound(getErr), "writer PVC must be deleted once detached")
}

// TestReclaimIdleSharedFSModelCaches proves the retained shared-FS writer PVC
// (the durable backing claim) is reclaimed once no StorageRequest references
// its handle and the idle period has passed, while active handles, recently
// referenced handles, and Samba backing PVCs (reclaimed with their server by
// the Samba pass) are left alone.
func TestReclaimIdleSharedFSModelCaches(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	idle := (&k8sutil.TimeConfig{}).Complete().ModelCacheIdlePeriod

	mkPVC := func(name, handle string, lastRef time.Time, sambaComponent bool) *corev1.PersistentVolumeClaim {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ModelCacheInitNamespace,
				Labels:    map[string]string{cachePopulatedLabelKey: cachePopulatedLabelValue},
				Annotations: map[string]string{
					primaryPVLastReferencedAnnotationKey: lastRef.Format(primaryPVLastReferencedTimeFormat),
				},
			},
		}
		if handle != "" {
			pvc.Labels[modelCacheHandleLabelKey] = handle
		}
		if sambaComponent {
			pvc.Labels[sambaModelCacheComponentLabelKey] = sambaModelCacheComponentLabelValue
		}
		return pvc
	}

	idlePVC := mkPVC("rw-pvc-idle", "idle-handle", now.Add(-2*idle), false)
	activePVC := mkPVC("rw-pvc-active", "active-handle", now.Add(-2*idle), false)
	recentPVC := mkPVC("rw-pvc-recent", "recent-handle", now.Add(-idle/2), false)
	sambaPVC := mkPVC("samba-idle2", "", now.Add(-2*idle), true)

	c := fake.NewClientBuilder().WithScheme(mgrScheme).
		WithObjects(idlePVC, activePVC, recentPVC, sambaPVC).Build()
	r := &Reconciler{
		Client:        c,
		metrics:       newTestMetrics(),
		fff:           &featureflagmock.Fetcher{},
		nowFunc:       func() time.Time { return now },
		k8sTimeConfig: (&k8sutil.TimeConfig{}).Complete(),
	}

	stList := &nvcav1new.StorageRequestList{Items: []nvcav1new.StorageRequest{{
		Spec: nvcav1new.StorageRequestSpec{
			Type:       nvcav1new.ModelCacheRequest,
			ModelCache: &nvcav1new.ModelCacheSpec{CacheHandle: "active-handle"},
		},
	}}}

	require.NoError(t, r.reclaimIdleSharedFSModelCaches(ctx, stList))

	err := c.Get(ctx, client.ObjectKeyFromObject(idlePVC), &corev1.PersistentVolumeClaim{})
	assert.True(t, apierrors.IsNotFound(err), "idle unreferenced writer PVC must be reclaimed")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(activePVC), &corev1.PersistentVolumeClaim{}),
		"actively referenced handle must be kept")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(recentPVC), &corev1.PersistentVolumeClaim{}),
		"recently referenced handle must be kept")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(sambaPVC), &corev1.PersistentVolumeClaim{}),
		"samba backing PVCs are reclaimed by the samba pass, not here")
}
