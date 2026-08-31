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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	helmBindingTestCacheHandle = "cache-handle"
	helmBindingTestNCAID       = "nca-a"
	helmBindingTestRequestNS   = "requests"
	helmBindingTestRequestName = "request-a"
	helmBindingTestRequestUID  = types.UID("request-uid")
	helmBindingTestBindingUID  = types.UID("binding-uid")
)

func newHelmBindingTestFixture(t *testing.T) (
	*PersistedModelCacheStorageSelection,
	*nvcav2beta1.ModelCacheBinding,
	*nvcav1new.StorageRequest,
	*nvcav2beta1.ICMSRequest,
) {
	t.Helper()
	selection := durableBindingSelection(t)
	binding, err := NewModelCacheBinding(
		selection, helmBindingTestNCAID, helmBindingTestCacheHandle, ModelCacheInitNamespace)
	require.NoError(t, err)
	binding.UID = helmBindingTestBindingUID
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
	binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{{
		Namespace: helmBindingTestRequestNS,
		Name:      helmBindingTestRequestName,
		UID:       helmBindingTestRequestUID,
	}}
	selection.BindingName = binding.Name
	selection.BindingUID = binding.UID
	raw, err := selection.Marshal()
	require.NoError(t, err)

	st := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcav1new.ModelCacheRequest.Name(),
			Namespace: "workload-a",
			Labels: map[string]string{
				nvcatypes.NCAIDKey: nvcatypes.MakeNCAIDLabelValue(helmBindingTestNCAID),
			},
			Annotations: map[string]string{
				ModelCacheStorageSelectionAnnotationKey: raw,
				ICMSRequestUIDAnnotationKey:             string(helmBindingTestRequestUID),
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type:                 nvcav1new.ModelCacheRequest,
			ICMSRequestName:      helmBindingTestRequestName,
			ICMSRequestNamespace: helmBindingTestRequestNS,
			ModelCache: &nvcav1new.ModelCacheSpec{
				CacheHandle: helmBindingTestCacheHandle,
				Backend:     string(HelmCacheBackendNVMesh),
			},
		},
	}
	icmsReq := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmBindingTestRequestName,
			Namespace: helmBindingTestRequestNS,
			UID:       helmBindingTestRequestUID,
			Annotations: map[string]string{
				ModelCacheStorageSelectionAnnotationKey: raw,
			},
		},
		Spec: newModelCacheICMSSpec(helmBindingTestCacheHandle),
	}
	icmsReq.Spec.NCAId = helmBindingTestNCAID
	icmsReq.Spec.CreationMsgInfo.NCAID = helmBindingTestNCAID
	return selection, binding, st, icmsReq
}

func TestValidatePersistedHelmCacheSelectionRequiresExactBinding(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mutate    func(*nvcav2beta1.ModelCacheBinding)
		omit      bool
		wantErr   string
		wantValid bool
	}{
		{name: "valid", wantValid: true},
		{name: "missing", omit: true, wantErr: "get model cache binding"},
		{name: "wrong UID", mutate: func(binding *nvcav2beta1.ModelCacheBinding) {
			binding.UID = types.UID("other-binding")
		}, wantErr: "does not match persisted UID"},
		{name: "retiring", mutate: func(binding *nvcav2beta1.ModelCacheBinding) {
			binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
		}, wantErr: "is not Active"},
		{name: "missing request reference", mutate: func(binding *nvcav2beta1.ModelCacheBinding) {
			binding.Status.RequestReferences = nil
		}, wantErr: "has no reference to ICMSRequest"},
		{name: "spec drift", mutate: func(binding *nvcav2beta1.ModelCacheBinding) {
			binding.Spec.Decision.Provider = "other"
		}, wantErr: "immutable spec does not match"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, binding, st, icmsReq := newHelmBindingTestFixture(t)
			if tt.mutate != nil {
				tt.mutate(binding)
			}
			builder := fake.NewClientBuilder().WithScheme(mgrScheme)
			if !tt.omit {
				builder = builder.WithObjects(binding)
			}
			r := &Reconciler{Client: builder.Build(), metrics: newTestMetrics()}
			stCopy := st.DeepCopy()
			err := r.validatePersistedHelmCacheSelection(t.Context(), stCopy, icmsReq)
			if tt.wantValid {
				require.NoError(t, err)
				assert.Equal(t, string(helmBindingTestBindingUID),
					stCopy.Labels[ModelCacheBindingUIDLabelKey])
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
			assert.ErrorIs(t, err, reconcile.TerminalError(nil))
			assert.Empty(t, stCopy.Labels[ModelCacheBindingUIDLabelKey])
		})
	}
}

func TestValidatePersistedHelmCacheSelectionRequiresMatchingICMSSelection(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*testing.T, *PersistedModelCacheStorageSelection, *nvcav2beta1.ICMSRequest)
	}{
		{
			name: "missing ICMS selection",
			mutate: func(_ *testing.T, _ *PersistedModelCacheStorageSelection, request *nvcav2beta1.ICMSRequest) {
				delete(request.Annotations, ModelCacheStorageSelectionAnnotationKey)
			},
		},
		{
			name: "different valid ICMS selection",
			mutate: func(t *testing.T, selection *PersistedModelCacheStorageSelection, request *nvcav2beta1.ICMSRequest) {
				t.Helper()
				selection.ProfileDigest = "sha256:different"
				raw, err := selection.Marshal()
				require.NoError(t, err)
				request.Annotations[ModelCacheStorageSelectionAnnotationKey] = raw
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selection, binding, st, request := newHelmBindingTestFixture(t)
			tt.mutate(t, selection, request)
			r := &Reconciler{
				Client:  fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding).Build(),
				metrics: newTestMetrics(),
			}

			err := r.validatePersistedHelmCacheSelection(t.Context(), st.DeepCopy(), request)
			require.ErrorContains(t, err, "does not match the live ICMSRequest selection")
			assert.True(t, isTerminal(err))
		})
	}
}

func TestDoModelCacheRoutedClassifiesBindingReadErrors(t *testing.T) {
	for _, tt := range []struct {
		name           string
		readErr        error
		wantError      bool
		wantRequeue    bool
		wantTerminal   bool
		wantFinalPhase nvcav1new.StoragePhase
	}{
		{
			name:           "temporary API outage requeues",
			readErr:        apierrors.NewServiceUnavailable("temporary binding read failure"),
			wantRequeue:    true,
			wantFinalPhase: nvcav1new.StoragePending,
		},
		{
			name: "authorization failure surfaces without changing state",
			readErr: apierrors.NewForbidden(
				corev1.Resource("modelcachebindings"), helmBindingTestRequestName, errors.New("denied")),
			wantError:      true,
			wantFinalPhase: nvcav1new.StoragePending,
		},
		{
			name: "missing persisted binding is terminal",
			readErr: apierrors.NewNotFound(
				corev1.Resource("modelcachebindings"), helmBindingTestRequestName),
			wantError:      true,
			wantTerminal:   true,
			wantFinalPhase: nvcav1new.StorageFailed,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, binding, st, icmsReq := newHelmBindingTestFixture(t)
			st.Status.Phase = nvcav1new.StoragePending
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
						obj client.Object, opts ...client.GetOption,
					) error {
						if _, ok := obj.(*nvcav2beta1.ModelCacheBinding); ok {
							return tt.readErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).Build()
			r := &Reconciler{Client: c, metrics: newTestMetrics()}
			stCopy := st.DeepCopy()

			res, err := r.doModelCacheRouted(t.Context(), *st, stCopy, icmsReq)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantRequeue, res.Requeue)
			assert.Equal(t, tt.wantTerminal, isTerminal(err))
			assert.Equal(t, tt.wantFinalPhase, stCopy.Status.Phase)
		})
	}
}

func TestModelCacheBindingUIDLabelHelpers(t *testing.T) {
	obj := &corev1.PersistentVolumeClaim{}
	require.ErrorContains(t,
		ValidateModelCacheBindingUIDLabel(obj, helmBindingTestBindingUID), "ownership mismatch")
	require.NoError(t, SetModelCacheBindingUIDLabel(obj, helmBindingTestBindingUID))
	require.NoError(t, ValidateModelCacheBindingUIDLabel(obj, helmBindingTestBindingUID))
	require.ErrorContains(t,
		SetModelCacheBindingUIDLabel(obj, types.UID("other-binding")), "ownership mismatch")
	assert.Equal(t, string(helmBindingTestBindingUID), obj.Labels[ModelCacheBindingUIDLabelKey])

	require.ErrorContains(t,
		ValidateModelCacheRequestUIDLabel(obj, helmBindingTestRequestUID), "ownership mismatch")
	require.NoError(t, SetModelCacheRequestUIDLabel(obj, helmBindingTestRequestUID))
	require.NoError(t, ValidateModelCacheReaderOwnership(
		obj, helmBindingTestBindingUID, helmBindingTestRequestUID))
	require.ErrorContains(t,
		SetModelCacheRequestUIDLabel(obj, types.UID("replacement-request")), "ownership mismatch")
	assert.Equal(t, string(helmBindingTestRequestUID), obj.Labels[ModelCacheRequestUIDLabelKey])
}

func TestCreateOrValidateModelCacheBindingOwnedObjectRejectsForeignObject(t *testing.T) {
	key := client.ObjectKey{Name: "rw-pvc-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace}
	foreign := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: key.Name, Namespace: key.Namespace,
		Labels: map[string]string{ModelCacheBindingUIDLabelKey: "other-binding"},
	}}
	wanted := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: key.Name, Namespace: key.Namespace,
		Labels: map[string]string{ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID)},
	}}
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(foreign).Build()
	r := &Reconciler{Client: c}

	alreadyExists, err := r.createOrValidateModelCacheBindingOwnedObject(t.Context(), wanted)
	assert.True(t, alreadyExists)
	require.ErrorContains(t, err, "ownership mismatch")
	got := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(t.Context(), key, got))
	assert.Equal(t, "other-binding", got.Labels[ModelCacheBindingUIDLabelKey])
}

func TestCreateOrValidateModelCacheBindingOwnedSecretRequiresExactIntent(t *testing.T) {
	immutable := true
	wanted := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "writer-job-" + helmBindingTestCacheHandle + "-0-pull-worker",
			Namespace: ModelCacheInitNamespace,
			Labels: map[string]string{
				modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
				ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
				"stable.test/label":          "expected",
			},
			Annotations: map[string]string{"stable.test/annotation": "expected"},
		},
		Type:       corev1.SecretTypeDockerConfigJson,
		Immutable:  &immutable,
		StringData: map[string]string{corev1.DockerConfigJsonKey: `{"auths":{"registry.test":{}}}`},
	}
	for _, tt := range []struct {
		name    string
		mutate  func(*corev1.Secret)
		wantErr bool
	}{
		{name: "exact server representation"},
		{name: "type drift", wantErr: true, mutate: func(secret *corev1.Secret) {
			secret.Type = corev1.SecretTypeOpaque
		}},
		{name: "data drift", wantErr: true, mutate: func(secret *corev1.Secret) {
			secret.Data[corev1.DockerConfigJsonKey] = []byte(`{"auths":{"other.test":{}}}`)
		}},
		{name: "immutable drift", wantErr: true, mutate: func(secret *corev1.Secret) {
			value := false
			secret.Immutable = &value
		}},
		{name: "stable label drift", wantErr: true, mutate: func(secret *corev1.Secret) {
			delete(secret.Labels, "stable.test/label")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			existing := wanted.DeepCopy()
			existing.Data = modelCacheSecretData(wanted)
			existing.StringData = nil
			if tt.mutate != nil {
				tt.mutate(existing)
			}
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(existing).Build()
			r := &Reconciler{Client: c}

			alreadyExists, err := r.createOrValidateModelCacheBindingOwnedObject(
				t.Context(), wanted.DeepCopy())
			assert.True(t, alreadyExists)
			if tt.wantErr {
				require.ErrorIs(t, err, errModelCacheBindingOwnership)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestHelmInitResourcesCarryBindingUID(t *testing.T) {
	selection, _, st, _ := newHelmBindingTestFixture(t)
	st.Labels = map[string]string{ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID)}
	rwPVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "rw-pvc-" + helmBindingTestCacheHandle}}
	initJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "writer-job-" + helmBindingTestCacheHandle},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Name: "writer-job-" + helmBindingTestCacheHandle},
		}},
	}
	pullSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "worker-pull"}}
	c := fake.NewClientBuilder().WithScheme(mgrScheme).Build()
	r := &Reconciler{
		Client:       c,
		nowFunc:      time.Now,
		metrics:      newTestMetrics(),
		initStatuses: newInitStatusCache(c),
	}

	res, err := r.doInitModelCacheNVMesh(
		t.Context(), *st, st.DeepCopy(), rwPVC, initJob, []*corev1.Secret{pullSecret}, HelmCacheBackendNVMesh)
	require.NoError(t, err)
	assert.True(t, res.Requeue)

	wantUID := string(helmBindingTestBindingUID)
	for _, obj := range []client.Object{
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: "rw-pvc-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: "writer-job-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "writer-job-" + helmBindingTestCacheHandle + "-0-pull-worker", Namespace: ModelCacheInitNamespace}},
		&coordv1.Lease{ObjectMeta: metav1.ObjectMeta{
			Name: buildInitLeaseName(helmBindingTestCacheHandle), Namespace: ModelCacheInitNamespace}},
	} {
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(obj), obj))
		assert.Equal(t, wantUID, obj.GetLabels()[ModelCacheBindingUIDLabelKey], "%T", obj)
	}
	createdJob := &batchv1.Job{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{
		Name: "writer-job-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace}, createdJob))
	assert.Equal(t, wantUID, createdJob.Spec.Template.Labels[ModelCacheBindingUIDLabelKey])

	writerUID := types.UID("writer-pvc-uid")
	primaryPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "primary-pv"},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName:              selection.StorageClassName,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Namespace:  ModelCacheInitNamespace,
				Name:       "rw-pvc-" + helmBindingTestCacheHandle,
				UID:        writerUID,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       selection.Provisioner,
					VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
				},
			},
		},
	}
	require.NoError(t, c.Create(t.Context(), primaryPV))
	boundWriterPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rw-pvc-" + helmBindingTestCacheHandle,
			Namespace: ModelCacheInitNamespace,
			UID:       writerUID,
			Labels: map[string]string{
				ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &selection.StorageClassName,
			VolumeName:       primaryPV.Name,
		},
	}
	require.NoError(t, r.finalizePrimaryPVOnSuccessfulInit(t.Context(), st, boundWriterPVC))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(primaryPV), primaryPV))
	assert.Equal(t, wantUID, primaryPV.Labels[ModelCacheBindingUIDLabelKey])
}

func TestPrepareHelmModelCacheBindingResourcesRequiresExactIntent(t *testing.T) {
	_, binding, _, _ := newHelmBindingTestFixture(t)
	newResources := func(t *testing.T) (*corev1.PersistentVolumeClaim, *batchv1.Job, *coordv1.Lease) {
		t.Helper()
		rwPVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: binding.Spec.Resources.PersistentVolumeClaimNames[0], Namespace: ModelCacheInitNamespace,
		}}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: binding.Spec.Resources.JobNames[0], Namespace: ModelCacheInitNamespace,
			},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{}},
		}
		lease := &coordv1.Lease{ObjectMeta: metav1.ObjectMeta{
			Name: binding.Spec.Resources.LeaseName, Namespace: ModelCacheInitNamespace,
		}}
		for _, obj := range []metav1.Object{rwPVC, job, &job.Spec.Template.ObjectMeta, lease} {
			require.NoError(t, SetModelCacheBindingUIDLabel(obj, binding.UID))
		}
		return rwPVC, job, lease
	}

	for _, tt := range []struct {
		name   string
		mutate func(*corev1.PersistentVolumeClaim, *batchv1.Job, *coordv1.Lease)
	}{
		{name: "valid"},
		{name: "wrong writer PVC name", mutate: func(pvc *corev1.PersistentVolumeClaim, _ *batchv1.Job, _ *coordv1.Lease) {
			pvc.Name = "other-writer"
		}},
		{name: "wrong writer Job name", mutate: func(_ *corev1.PersistentVolumeClaim, job *batchv1.Job, _ *coordv1.Lease) {
			job.Name = "other-job"
		}},
		{name: "wrong Lease name", mutate: func(_ *corev1.PersistentVolumeClaim, _ *batchv1.Job, lease *coordv1.Lease) {
			lease.Name = "other-lease"
		}},
		{name: "wrong writer namespace", mutate: func(pvc *corev1.PersistentVolumeClaim, _ *batchv1.Job, _ *coordv1.Lease) {
			pvc.Namespace = "other-namespace"
		}},
		{name: "unowned Job Pod template", mutate: func(_ *corev1.PersistentVolumeClaim, job *batchv1.Job, _ *coordv1.Lease) {
			delete(job.Spec.Template.Labels, ModelCacheBindingUIDLabelKey)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rwPVC, job, lease := newResources(t)
			if tt.mutate != nil {
				tt.mutate(rwPVC, job, lease)
			}
			r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(mgrScheme).Build()}
			err := r.prepareHelmModelCacheBindingResources(t.Context(), binding, rwPVC, job, lease)
			if tt.mutate == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errModelCacheBindingOwnership)
		})
	}

	t.Run("existing foreign writer is not adopted", func(t *testing.T) {
		rwPVC, job, lease := newResources(t)
		foreign := rwPVC.DeepCopy()
		foreign.Labels[ModelCacheBindingUIDLabelKey] = "other-binding"
		r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(foreign).Build()}
		err := r.prepareHelmModelCacheBindingResources(t.Context(), binding, rwPVC, job, lease)
		require.ErrorIs(t, err, errModelCacheBindingOwnership)
	})
}

func TestFinalizePrimaryPVRequiresExactWriterClaim(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*corev1.PersistentVolumeClaim, *corev1.PersistentVolume)
	}{
		{name: "valid"},
		{name: "missing claimRef", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.ClaimRef = nil
		}},
		{name: "wrong claim namespace", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.ClaimRef.Namespace = "other-namespace"
		}},
		{name: "wrong claim name", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.ClaimRef.Name = "other-writer"
		}},
		{name: "wrong claim UID", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.ClaimRef.UID = "other-writer-uid"
		}},
		{name: "wrong claim kind", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.ClaimRef.Kind = "Secret"
		}},
		{name: "empty volume handle", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.CSI.VolumeHandle = ""
		}},
		{name: "wrong volume handle namespace", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.CSI.VolumeHandle = "cluster:csi:volume:other-namespace"
		}},
		{name: "wrong reclaim policy", mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
			pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selection, _, st, _ := newHelmBindingTestFixture(t)
			st.Labels[ModelCacheBindingUIDLabelKey] = string(helmBindingTestBindingUID)
			writerUID := types.UID("writer-pvc-uid")
			writer := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "rw-pvc-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace,
					UID:    writerUID,
					Labels: map[string]string{ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID)},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &selection.StorageClassName,
					VolumeName:       "primary-pv",
				},
			}
			primary := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "primary-pv"},
				Spec: corev1.PersistentVolumeSpec{
					AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName:              selection.StorageClassName,
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					ClaimRef: &corev1.ObjectReference{
						APIVersion: "v1", Kind: "PersistentVolumeClaim",
						Namespace: writer.Namespace, Name: writer.Name, UID: writer.UID,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       selection.Provisioner,
							VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
						},
					},
				},
			}
			if tt.mutate != nil {
				tt.mutate(writer, primary)
			}
			c := fake.NewClientBuilder().WithScheme(mgrScheme).Build()
			require.NoError(t, c.Create(t.Context(), primary))
			r := &Reconciler{Client: c, nowFunc: time.Now}
			err := r.finalizePrimaryPVOnSuccessfulInit(t.Context(), st, writer)
			got := &corev1.PersistentVolume{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: primary.Name}, got))
			if tt.mutate == nil {
				require.NoError(t, err)
				assert.Equal(t, string(helmBindingTestBindingUID), got.Labels[ModelCacheBindingUIDLabelKey])
				return
			}
			require.Error(t, err)
			assert.True(t, isTerminal(err))
			assert.Empty(t, got.Labels[ModelCacheBindingUIDLabelKey])
			assert.Empty(t, got.Annotations[primaryPVLastReferencedAnnotationKey])
		})
	}
}

func TestHelmReaderValidatorsRejectStaleRequestGeneration(t *testing.T) {
	selection, _, st, _ := newHelmBindingTestFixture(t)
	className := selection.StorageClassName
	primary := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
		}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              className,
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver: selection.Provisioner, VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
			}},
		},
	}
	rwPVC := &corev1.PersistentVolumeClaim{Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &className}}
	roPVCName := "ro-pvc-" + helmBindingTestCacheHandle
	secondary := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "secondary-pv-" + helmBindingTestRequestName,
			Labels: map[string]string{
				ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
				ModelCacheRequestUIDLabelKey: string(helmBindingTestRequestUID),
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   accessModesRO,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              className,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1", Kind: "PersistentVolumeClaim",
				Namespace: st.Namespace, Name: roPVCName,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver: selection.Provisioner, VolumeHandle: "cluster:csi:volume:" + st.Namespace,
			}},
		},
	}
	roPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: roPVCName, Namespace: st.Namespace, UID: "reader-pvc-uid",
			Labels: map[string]string{
				ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
				ModelCacheRequestUIDLabelKey: string(helmBindingTestRequestUID),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessModesRO, StorageClassName: &className, VolumeName: secondary.Name,
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}

	require.NoError(t, validateHelmModelCacheSecondaryPV(
		st, selection, primary, secondary, roPVCName, helmBindingTestBindingUID, helmBindingTestRequestUID))
	require.NoError(t, validateHelmModelCacheReaderPVC(
		st, rwPVC, secondary, roPVC, helmBindingTestBindingUID, helmBindingTestRequestUID))

	stalePV := secondary.DeepCopy()
	stalePV.Labels[ModelCacheRequestUIDLabelKey] = "stale-request-uid"
	require.ErrorContains(t, validateHelmModelCacheSecondaryPV(
		st, selection, primary, stalePV, roPVCName, helmBindingTestBindingUID, helmBindingTestRequestUID),
		"request UID")
	stalePVC := roPVC.DeepCopy()
	stalePVC.Labels[ModelCacheRequestUIDLabelKey] = "stale-request-uid"
	require.ErrorContains(t, validateHelmModelCacheReaderPVC(
		st, rwPVC, secondary, stalePVC, helmBindingTestBindingUID, helmBindingTestRequestUID),
		"request UID")

	secondaryWithStaleClaim := secondary.DeepCopy()
	secondaryWithStaleClaim.Spec.ClaimRef.UID = "deleted-reader-pvc-uid"
	require.ErrorContains(t, validateHelmModelCacheReaderPVC(
		st, rwPVC, secondaryWithStaleClaim, roPVC,
		helmBindingTestBindingUID, helmBindingTestRequestUID), "claimRef UID")

	deletePolicy := secondary.DeepCopy()
	deletePolicy.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
	require.ErrorContains(t, validateHelmModelCacheSecondaryPV(
		st, selection, primary, deletePolicy, roPVCName,
		helmBindingTestBindingUID, helmBindingTestRequestUID), "reclaim policy")
	csiDrift := secondary.DeepCopy()
	csiDrift.Spec.CSI.FSType = "other-fs"
	require.ErrorContains(t, validateHelmModelCacheSecondaryPV(
		st, selection, primary, csiDrift, roPVCName,
		helmBindingTestBindingUID, helmBindingTestRequestUID), "CSI source")

	boundReader := roPVC.DeepCopy()
	boundReader.Status.Phase = corev1.ClaimBound
	require.ErrorContains(t, validateHelmModelCacheReaderPVC(
		st, rwPVC, secondary, boundReader,
		helmBindingTestBindingUID, helmBindingTestRequestUID), "claimRef UID")
	boundSecondary := secondary.DeepCopy()
	boundSecondary.Spec.ClaimRef.UID = boundReader.UID
	require.NoError(t, validateHelmModelCacheReaderPVC(
		st, rwPVC, boundSecondary, boundReader,
		helmBindingTestBindingUID, helmBindingTestRequestUID))
	boundSecondary.Spec.ClaimRef.UID = "stale-reader-pvc-uid"
	require.ErrorContains(t, validateHelmModelCacheReaderPVC(
		st, rwPVC, boundSecondary, boundReader,
		helmBindingTestBindingUID, helmBindingTestRequestUID), "claimRef UID")
}

func TestHelmPrimaryPVReuseRequiresExactImmutableIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*corev1.PersistentVolume)
	}{
		{name: "valid"},
		{name: "wrong binding", mutate: func(pv *corev1.PersistentVolume) {
			pv.Labels[ModelCacheBindingUIDLabelKey] = "other-binding"
		}},
		{name: "wrong provisioner", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.CSI.Driver = "other.csi.example.com"
		}},
		{name: "wrong handle namespace", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.CSI.VolumeHandle = "cluster:csi:volume:other-namespace"
		}},
		{name: "wrong StorageClass", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.StorageClassName = "other-class"
		}},
		{name: "missing writer claim UID", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.ClaimRef.UID = ""
		}},
		{name: "wrong reclaim policy", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		}},
		{name: "wrong access modes", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.AccessModes = accessModesRO
		}},
		{name: "stale live writer UID", mutate: func(pv *corev1.PersistentVolume) {
			pv.Spec.ClaimRef.UID = "stale-writer-pvc-uid"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selection, _, st, _ := newHelmBindingTestFixture(t)
			rwPVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
				Name: "rw-pvc-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace,
				UID: "writer-pvc-uid",
			}}
			primary := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "primary-pv", Labels: map[string]string{
					primaryPVLabelKey: primaryPVLabelValue, modelCacheHandleLabelKey: helmBindingTestCacheHandle,
					ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
				}},
				Spec: corev1.PersistentVolumeSpec{
					AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					StorageClassName:              selection.StorageClassName,
					ClaimRef: &corev1.ObjectReference{
						APIVersion: "v1", Kind: "PersistentVolumeClaim",
						Namespace: ModelCacheInitNamespace, Name: rwPVC.Name, UID: "writer-pvc-uid",
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
						Driver: selection.Provisioner, VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
					}},
				},
			}
			if tt.mutate != nil {
				tt.mutate(primary)
			}
			err := validateHelmModelCachePrimaryPVForReuse(
				st, selection, rwPVC, primary, helmBindingTestBindingUID)
			if tt.mutate == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errModelCacheBindingOwnership)
		})
	}

	t.Run("valid after writer PVC deletion", func(t *testing.T) {
		selection, _, st, _ := newHelmBindingTestFixture(t)
		rwPVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: "rw-pvc-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace,
		}}
		primary := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "primary-pv", Labels: map[string]string{
				primaryPVLabelKey: primaryPVLabelValue, modelCacheHandleLabelKey: helmBindingTestCacheHandle,
				ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
			}},
			Spec: corev1.PersistentVolumeSpec{
				AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				StorageClassName:              selection.StorageClassName,
				ClaimRef: &corev1.ObjectReference{
					APIVersion: "v1", Kind: "PersistentVolumeClaim",
					Namespace: ModelCacheInitNamespace, Name: rwPVC.Name, UID: "historical-writer-pvc-uid",
				},
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
					Driver: selection.Provisioner, VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
				}},
			},
		}
		require.NoError(t, validateHelmModelCachePrimaryPVForReuse(
			st, selection, rwPVC, primary, helmBindingTestBindingUID))
	})
}

func TestAnnotatedCleanupRefusesOwnershipMismatch(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	wrongJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "writer-job-" + helmBindingTestCacheHandle,
		Namespace: ModelCacheInitNamespace,
		Labels: map[string]string{
			modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
			ModelCacheBindingUIDLabelKey: "other-binding",
		},
	}}
	lease := bindingOwnedTestLease(helmBindingTestRequestName)
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, wrongJob, lease).Build()
	r := &Reconciler{Client: c}

	errs := r.cleanupInitModelCache(t.Context(), st, false)
	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], "ownership mismatch")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(wrongJob), &batchv1.Job{}))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(lease), &coordv1.Lease{}))
}

func TestAnnotatedCleanupRefusesAmbiguousWriterResourcesBeforeDeletion(t *testing.T) {
	for _, tt := range []struct {
		name       string
		extra      client.Object
		wantErr    string
		includeJob bool
	}{
		{
			name: "multiple writer Jobs",
			extra: &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: "writer-job-duplicate", Namespace: ModelCacheInitNamespace,
				UID: types.UID("duplicate-job-uid"), ResourceVersion: "1",
				Labels: map[string]string{
					modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
					ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
				},
			}},
			wantErr:    "found 2 writer Jobs",
			includeJob: true,
		},
		{
			name: "non-canonical writer PVC",
			extra: &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
				Name: "rw-pvc-other", Namespace: ModelCacheInitNamespace,
				UID: types.UID("other-pvc-uid"), ResourceVersion: "1",
				Labels: map[string]string{
					modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
					ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
				},
			}},
			wantErr: "does not match cache handle",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, binding, st, _ := newHelmBindingTestFixture(t)
			job := bindingOwnedTestJob()
			lease := bindingOwnedTestLease(helmBindingTestRequestName)
			objects := []client.Object{binding, lease, tt.extra}
			if tt.includeJob {
				objects = append(objects, job)
			}
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(objects...).Build()
			r := &Reconciler{Client: c}

			errs := r.cleanupInitModelCache(t.Context(), st, false)
			require.Len(t, errs, 1)
			require.ErrorContains(t, errs[0], tt.wantErr)
			for _, obj := range objects {
				got, ok := obj.DeepCopyObject().(client.Object)
				require.True(t, ok)
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(obj), got))
			}
		})
	}
}

func TestAnnotatedCleanupDoesNotDeleteAnotherLeaseHoldersWriter(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	job := bindingOwnedTestJob()
	lease := bindingOwnedTestLease("request-b")
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, job, lease).Build()
	r := &Reconciler{Client: c}

	require.Empty(t, r.cleanupInitModelCache(t.Context(), st, false))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{}))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(lease), &coordv1.Lease{}))
}

func TestAnnotatedCleanupDoesNotAuthorizeRecreatedSameNameRequest(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	job := bindingOwnedTestJob()
	lease := bindingOwnedTestLease(
		helmBindingTestRequestName + "@replacement-request-uid")
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, job, lease).Build()
	r := &Reconciler{Client: c}

	require.Empty(t, r.cleanupInitModelCache(t.Context(), st, false))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{}))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(lease), &coordv1.Lease{}))
}

func TestAnnotatedCleanupStopsAfterInconclusiveTargetList(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	job := bindingOwnedTestJob()
	lease := bindingOwnedTestLease(helmBindingTestRequestName)
	jobLists := 0
	deletes := 0
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, job, lease).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList,
				opts ...client.ListOption,
			) error {
				if _, ok := list.(*batchv1.JobList); ok {
					jobLists++
					if jobLists == 2 {
						return apierrors.NewServiceUnavailable("inconclusive writer Job list")
					}
				}
				return cl.List(ctx, list, opts...)
			},
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object,
				opts ...client.DeleteOption,
			) error {
				deletes++
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &Reconciler{Client: c}

	errs := r.cleanupInitModelCache(t.Context(), st, false)
	require.Len(t, errs, 1)
	assert.True(t, apierrors.IsServiceUnavailable(errs[0]))
	assert.Equal(t, 0, deletes)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{}))
}

func TestAnnotatedCleanupRevalidatesLeaseBeforeEveryDelete(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	job := bindingOwnedTestJob()
	lease := bindingOwnedTestLease(helmBindingTestRequestName)
	deletes := 0
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, job, lease).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object,
				patch client.Patch, opts ...client.PatchOption,
			) error {
				if err := cl.Patch(ctx, obj, patch, opts...); err != nil {
					return err
				}
				if _, ok := obj.(*coordv1.Lease); !ok {
					return nil
				}
				current := &coordv1.Lease{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
					return err
				}
				replacement := "other-request@other-request-uid"
				current.Spec.HolderIdentity = &replacement
				return cl.Update(ctx, current)
			},
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object,
				opts ...client.DeleteOption,
			) error {
				deletes++
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &Reconciler{Client: c}

	errs := r.cleanupInitModelCache(t.Context(), st, false)
	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], errModelCacheBindingOwnership)
	assert.Equal(t, 0, deletes)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{}))
}

func TestHandleLeaseUsesAcquireTimeWhenRenewTimeIsNil(t *testing.T) {
	_, _, st, _ := newHelmBindingTestFixture(t)
	now := time.Now().UTC()
	holder := "other-request@other-request-uid"
	lease := bindingOwnedTestLease(holder)
	lease.Spec.RenewTime = nil
	lease.Spec.AcquireTime = &metav1.MicroTime{Time: now.Add(-time.Minute)}
	otherRequest := &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{
		Name: "other-request", Namespace: helmBindingTestRequestNS, UID: "other-request-uid",
	}}
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(lease, otherRequest).Build()
	r := &Reconciler{
		Client: c, ICMSRequestNamespace: helmBindingTestRequestNS,
		nowFunc: func() time.Time { return now },
	}
	wanted := newInitLease(st)
	require.NoError(t, propagateModelCacheBindingUIDLabel(st, wanted))

	res, holds, _, err := r.handleLease(t.Context(), wanted)
	require.NoError(t, err)
	assert.False(t, holds)
	assert.Greater(t, res.RequeueAfter, 58*time.Minute)
	assert.LessOrEqual(t, res.RequeueAfter, 59*time.Minute)
}

func TestHandleLeaseClassifiesHolderLookupErrors(t *testing.T) {
	for _, tt := range []struct {
		name        string
		readErr     error
		wantRequeue bool
		wantError   bool
	}{
		{
			name: "transient",
			readErr: apierrors.NewServiceUnavailable(
				"temporary Lease holder lookup failure"),
			wantRequeue: true,
		},
		{
			name: "forbidden",
			readErr: apierrors.NewForbidden(
				corev1.Resource("icmsrequests"), "other-request", errors.New("denied")),
			wantError: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, st, _ := newHelmBindingTestFixture(t)
			lease := bindingOwnedTestLease("other-request@other-request-uid")
			acquire := metav1.NowMicro()
			lease.Spec.AcquireTime = &acquire
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(lease).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
						obj client.Object, opts ...client.GetOption,
					) error {
						if _, ok := obj.(*nvcav2beta1.ICMSRequest); ok {
							return tt.readErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).Build()
			r := &Reconciler{
				Client: c, ICMSRequestNamespace: helmBindingTestRequestNS, nowFunc: time.Now,
			}
			wanted := newInitLease(st)
			require.NoError(t, propagateModelCacheBindingUIDLabel(st, wanted))

			res, holds, _, err := r.handleLease(t.Context(), wanted)
			assert.False(t, holds)
			assert.Equal(t, tt.wantRequeue, res.Requeue)
			if tt.wantError {
				require.Error(t, err)
				assert.True(t, apierrors.IsForbidden(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestHandleLeaseClassifiesLeaseAPIErrors(t *testing.T) {
	for _, tt := range []struct {
		name        string
		getErr      error
		createErr   error
		wantRequeue bool
		wantError   bool
	}{
		{
			name: "transient Lease read",
			getErr: apierrors.NewServiceUnavailable(
				"temporary Lease read failure"),
			wantRequeue: true,
		},
		{
			name: "forbidden Lease read",
			getErr: apierrors.NewForbidden(
				corev1.Resource("leases"), "lease", errors.New("denied")),
			wantError: true,
		},
		{
			name: "concurrent Lease create",
			createErr: apierrors.NewAlreadyExists(
				corev1.Resource("leases"), "lease"),
			wantRequeue: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, st, _ := newHelmBindingTestFixture(t)
			c := fake.NewClientBuilder().WithScheme(mgrScheme).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
						obj client.Object, opts ...client.GetOption,
					) error {
						if _, ok := obj.(*coordv1.Lease); ok && tt.getErr != nil {
							return tt.getErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
					Create: func(ctx context.Context, cl client.WithWatch, obj client.Object,
						opts ...client.CreateOption,
					) error {
						if _, ok := obj.(*coordv1.Lease); ok && tt.createErr != nil {
							return tt.createErr
						}
						return cl.Create(ctx, obj, opts...)
					},
				}).Build()
			r := &Reconciler{Client: c, nowFunc: time.Now}
			wanted := newInitLease(st)
			require.NoError(t, propagateModelCacheBindingUIDLabel(st, wanted))

			res, holds, _, err := r.handleLease(t.Context(), wanted)
			assert.False(t, holds)
			assert.Equal(t, tt.wantRequeue, res.Requeue)
			if tt.wantError {
				require.Error(t, err)
				assert.True(t, apierrors.IsForbidden(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestModelCacheSuccessfulInitCleanupResult(t *testing.T) {
	for _, tt := range []struct {
		name          string
		errs          []error
		want          reconcile.Result
		wantErr       bool
		wantForbidden bool
	}{
		{name: "success"},
		{
			name: "volume detach pending", errs: []error{errVolumeStillAttached},
			want: reconcile.Result{RequeueAfter: volumeDetachRequeueInterval},
		},
		{
			name: "transient API failure", errs: []error{
				fmt.Errorf("wrapped cleanup read: %w", apierrors.NewServiceUnavailable("temporary")),
			}, want: reconcile.Result{Requeue: true},
		},
		{
			name: "non-transient API failure", errs: []error{
				apierrors.NewForbidden(corev1.Resource("jobs"), "writer", errors.New("denied")),
			}, wantErr: true, wantForbidden: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := modelCacheSuccessfulInitCleanupResult(tt.errs)
			assert.Equal(t, tt.want, res)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantForbidden {
					assert.True(t, apierrors.IsForbidden(err))
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAnnotatedCleanupDeletesExactBindingOwnersWriter(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	job := bindingOwnedTestJob()
	lease := bindingOwnedTestLease(helmBindingTestRequestName)
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, job, lease).Build()
	r := &Reconciler{Client: c}

	require.Empty(t, r.cleanupInitModelCache(t.Context(), st, false))
	assert.True(t, apierrors.IsNotFound(c.Get(
		t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{})))
	assert.True(t, apierrors.IsNotFound(c.Get(
		t.Context(), client.ObjectKeyFromObject(lease), &coordv1.Lease{})))
}

func TestAnnotatedSharedWriterCleanupUsesExactDeletePreconditions(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	job := bindingOwnedTestJob()
	secretName := job.Name + "-0-pull-worker"
	job.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: secretName}}
	lease := bindingOwnedTestLease(helmBindingTestRequestName)
	writerPVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "rw-pvc-" + helmBindingTestCacheHandle, Namespace: ModelCacheInitNamespace,
		UID: types.UID("writer-pvc-uid"), ResourceVersion: "1",
		Labels: map[string]string{
			modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
			ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
		},
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: job.Name + "-pod", Namespace: ModelCacheInitNamespace,
		UID: types.UID("writer-pod-uid"), ResourceVersion: "1",
		Labels: map[string]string{
			modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
			ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
		},
	}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: secretName, Namespace: ModelCacheInitNamespace,
		UID: types.UID("pull-secret-uid"), ResourceVersion: "1",
		Labels: map[string]string{
			modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
			ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
		},
	}}
	objects := []client.Object{binding, job, lease, writerPVC, pod, secret}
	validated := map[string]bool{}
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object,
				opts ...client.DeleteOption,
			) error {
				deleteOptions := &client.DeleteOptions{}
				for _, opt := range opts {
					opt.ApplyToDelete(deleteOptions)
				}
				require.NotNil(t, deleteOptions.Preconditions, obj.GetName())
				require.NotNil(t, deleteOptions.Preconditions.UID, obj.GetName())
				require.NotNil(t, deleteOptions.Preconditions.ResourceVersion, obj.GetName())
				assert.Equal(t, obj.GetUID(), *deleteOptions.Preconditions.UID)
				assert.Equal(t, obj.GetResourceVersion(), *deleteOptions.Preconditions.ResourceVersion)
				validated[obj.GetName()] = true
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &Reconciler{Client: c}

	require.Empty(t, r.cleanupInitModelCache(t.Context(), st, false))
	for _, obj := range []client.Object{job, lease, writerPVC, pod, secret} {
		assert.True(t, validated[obj.GetName()], obj.GetName())
	}
}

func TestValidateModelCacheBindingForCleanupUsesDeletedRequestTombstone(t *testing.T) {
	for _, tt := range []struct {
		name              string
		request           *nvcav2beta1.ICMSRequest
		wantErr           string
		wantReference     bool
		transientGetError error
	}{
		{name: "request is gone"},
		{
			name: "request is deleting",
			request: &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{
				Name:              helmBindingTestRequestName,
				Namespace:         helmBindingTestRequestNS,
				UID:               helmBindingTestRequestUID,
				DeletionTimestamp: &metav1.Time{Time: time.Now()},
				Finalizers:        []string{"test.nvcf.nvidia.io/finalizer"},
			}},
		},
		{
			name: "live request without reference",
			request: &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{
				Name: helmBindingTestRequestName, Namespace: helmBindingTestRequestNS, UID: helmBindingTestRequestUID,
			}},
			wantErr: "live ICMSRequest",
		},
		{
			name: "request name was reused",
			request: &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{
				Name: helmBindingTestRequestName, Namespace: helmBindingTestRequestNS, UID: types.UID("replacement-uid"),
			}},
			wantErr: "does not match recorded UID",
		},
		{
			name:              "transient request read",
			transientGetError: apierrors.NewServiceUnavailable("temporary ICMSRequest read failure"),
			wantErr:           "temporary ICMSRequest read failure",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, binding, st, _ := newHelmBindingTestFixture(t)
			binding.Status.RequestReferences = nil
			objects := []client.Object{binding}
			if tt.request != nil {
				objects = append(objects, tt.request)
			}
			builder := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(objects...)
			if tt.transientGetError != nil {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
						obj client.Object, opts ...client.GetOption,
					) error {
						if _, ok := obj.(*nvcav2beta1.ICMSRequest); ok {
							return tt.transientGetError
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				})
			}
			r := &Reconciler{Client: builder.Build()}

			uid, annotated, referencePresent, err :=
				r.validateModelCacheBindingForCleanup(t.Context(), st)
			assert.True(t, annotated)
			assert.Equal(t, tt.wantReference, referencePresent)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Empty(t, uid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, helmBindingTestBindingUID, uid)
		})
	}
}

func bindingOwnedTestJob() *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:            "writer-job-" + helmBindingTestCacheHandle,
		Namespace:       ModelCacheInitNamespace,
		UID:             types.UID("writer-job-uid"),
		ResourceVersion: "1",
		Labels: map[string]string{
			modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
			ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
		},
	}}
}

func bindingOwnedTestLease(holder string) *coordv1.Lease {
	if holder == helmBindingTestRequestName {
		holder += "@" + string(helmBindingTestRequestUID)
	}
	duration := int32(3600)
	return &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:            buildInitLeaseName(helmBindingTestCacheHandle),
			Namespace:       ModelCacheInitNamespace,
			UID:             types.UID("writer-lease-uid"),
			ResourceVersion: "1",
			Labels: map[string]string{
				modelCacheHandleLabelKey:     helmBindingTestCacheHandle,
				ModelCacheBindingUIDLabelKey: string(helmBindingTestBindingUID),
			},
		},
		Spec: coordv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration},
	}
}
