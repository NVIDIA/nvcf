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
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	reprovisionCacheHandle = "reprovision-cache"
	reprovisionNCAID       = "reprovision-nca"
	reprovisionRequestNS   = "requests"
	reprovisionRequestName = "reprovision-request"
	reprovisionWorkloadNS  = "workload"
	reprovisionRequestUID  = types.UID("reprovision-request-uid")
	reprovisionBindingUID  = types.UID("reprovision-binding-uid")
)

type helmReprovisionFixture struct {
	binding *nvcav2beta1.ModelCacheBinding
	request *nvcav1new.StorageRequest
	icms    *nvcav2beta1.ICMSRequest
	class   *storagev1.StorageClass
}

func newHelmReprovisionFixture(t *testing.T, encrypted bool) *helmReprovisionFixture {
	t.Helper()
	reclaimPolicy := corev1.PersistentVolumeReclaimRetain
	bindingMode := storagev1.VolumeBindingImmediate
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: DefaultModelCacheStorageClassName,
			UID:  types.UID("model-cache-storage-class-uid"),
		},
		Provisioner:       NVMeshStorageClassProvisioner,
		ReclaimPolicy:     &reclaimPolicy,
		VolumeBindingMode: &bindingMode,
		Parameters: map[string]string{
			NVMeshStorageClassVPG:   NVMeshStorageClassVPGType,
			NVMeshStorageClassCSIFS: NVMeshStorageClassFS,
		},
	}
	selection, err := NewPersistedModelCacheStorageSelection(
		ModelCacheWorkflowHelm,
		ModelCacheSelectionDurable,
		&ModelCacheStorageSelection{
			StorageClassName:     sc.Name,
			StorageClassUID:      sc.UID,
			StorageClassDigest:   digestStorageClass(sc),
			ProfileDigest:        "sha256:" + strings.Repeat("a", 64),
			Provider:             "nvmesh",
			Provisioner:          sc.Provisioner,
			Transition:           ModelCacheTransitionROXReadOnly,
			RequiredAccessModes:  requiredAccessModesForTransition(ModelCacheTransitionROXReadOnly),
			RequiredMountOptions: []string{"ro", "norecovery", "nouuid"},
		},
	)
	require.NoError(t, err)
	selection.EncryptionRequired = encrypted

	binding, err := NewModelCacheBinding(
		selection, reprovisionNCAID, reprovisionCacheHandle, ModelCacheInitNamespace)
	require.NoError(t, err)
	binding.UID = reprovisionBindingUID
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
	binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{{
		Namespace: reprovisionRequestNS,
		Name:      reprovisionRequestName,
		UID:       reprovisionRequestUID,
	}}
	selection.BindingName = binding.Name
	selection.BindingUID = binding.UID
	rawSelection, err := selection.Marshal()
	require.NoError(t, err)

	request := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcav1new.ModelCacheRequest.Name(),
			Namespace: reprovisionWorkloadNS,
			Labels: map[string]string{
				nvcatypes.NCAIDKey: nvcatypes.MakeNCAIDLabelValue(reprovisionNCAID),
			},
			Annotations: map[string]string{
				ModelCacheStorageSelectionAnnotationKey: rawSelection,
				ICMSRequestUIDAnnotationKey:             string(reprovisionRequestUID),
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type:                 nvcav1new.ModelCacheRequest,
			ICMSRequestName:      reprovisionRequestName,
			ICMSRequestNamespace: reprovisionRequestNS,
			ModelCache: &nvcav1new.ModelCacheSpec{
				CacheHandle: reprovisionCacheHandle,
				Backend:     string(HelmCacheBackendNVMesh),
				Encryption:  &nvcav1new.ModelCacheEncryption{Required: encrypted},
			},
		},
	}
	icms := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reprovisionRequestName,
			Namespace: reprovisionRequestNS,
			UID:       reprovisionRequestUID,
			Annotations: map[string]string{
				ModelCacheStorageSelectionAnnotationKey: rawSelection,
			},
		},
		Spec: newModelCacheICMSSpec(reprovisionCacheHandle),
	}
	icms.Spec.NCAId = reprovisionNCAID
	icms.Spec.CreationMsgInfo.NCAID = reprovisionNCAID

	return &helmReprovisionFixture{
		binding: binding,
		request: request,
		icms:    icms,
		class:   sc,
	}
}

type helmReprovisionCalls struct {
	storageClassGets int
	creates          []string
}

func newHelmReprovisionClient(
	t *testing.T,
	objects []client.Object,
	storageClassGetError error,
) (client.Client, *helmReprovisionCalls) {
	t.Helper()
	calls := &helmReprovisionCalls{}
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithRESTMapper(newTestRESTMapper(mgrScheme)).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if _, ok := obj.(*storagev1.StorageClass); ok && key.Name == DefaultModelCacheStorageClassName {
					calls.storageClassGets++
					if storageClassGetError != nil {
						return storageClassGetError
					}
				}
				return cl.Get(ctx, key, obj, opts...)
			},
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object,
				opts ...client.CreateOption,
			) error {
				calls.creates = append(calls.creates,
					fmt.Sprintf("%T %s/%s", obj, obj.GetNamespace(), obj.GetName()))
				if _, ok := obj.(*corev1.PersistentVolumeClaim); ok && obj.GetCreationTimestamp().Time.IsZero() {
					obj.SetCreationTimestamp(metav1.Now())
				}
				return cl.Create(ctx, obj, opts...)
			},
		}).Build()
	return c, calls
}

func newHelmReprovisionReconciler(c client.Client) *Reconciler {
	return &Reconciler{
		Client:                 c,
		modelCacheStorageClass: DefaultModelCacheStorageClassName,
		fff:                    &featureflagmock.Fetcher{},
		metrics:                newTestMetrics(),
		nowFunc:                time.Now,
		randReader:             bytes.NewReader(bytes.Repeat([]byte("x"), NVMeshKeyBytes)),
		initStatuses:           newInitStatusCache(c),
		k8sTimeConfig:          (&k8sutil.TimeConfig{}).Complete(),
	}
}

func TestDoModelCacheNVMeshClassifiesBindingRereadErrors(t *testing.T) {
	for _, tt := range []struct {
		name        string
		readErr     error
		wantError   bool
		wantRequeue bool
	}{
		{
			name:        "temporary API outage requeues",
			readErr:     apierrors.NewServiceUnavailable("temporary binding reread failure"),
			wantRequeue: true,
		},
		{
			name: "authorization failure surfaces",
			readErr: apierrors.NewForbidden(
				schema.GroupResource{Group: "nvca.nvcf.nvidia.io", Resource: "modelcachebindings"},
				"binding", fmt.Errorf("denied")),
			wantError: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHelmReprovisionFixture(t, false)
			fixture.request.Status.Phase = nvcav1new.StoragePending
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithRESTMapper(newTestRESTMapper(mgrScheme)).
				WithObjects(fixture.binding).
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
			r := newHelmReprovisionReconciler(c)
			stCopy := fixture.request.DeepCopy()

			res, err := r.doModelCacheNVMesh(
				t.Context(), *fixture.request, stCopy, fixture.icms)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantRequeue, res.Requeue)
			assert.False(t, isTerminal(err))
			assert.Equal(t, nvcav1new.StoragePending, stCopy.Status.Phase)
		})
	}
}

func TestHelmWriterReprovisionRejectsMissingOrDriftedStorageClassBeforeCreate(t *testing.T) {
	deletePolicy := corev1.PersistentVolumeReclaimDelete
	for _, tt := range []struct {
		name        string
		omitClass   bool
		mutate      func(*storagev1.StorageClass)
		want        string
		wantDrift   bool
		wantMissing bool
	}{
		{name: "missing", omitClass: true, want: "not found", wantMissing: true},
		{name: "UID drift", mutate: func(sc *storagev1.StorageClass) {
			sc.UID = types.UID("replacement-storage-class-uid")
		}, want: "UID changed", wantDrift: true},
		{name: "provisioner drift", mutate: func(sc *storagev1.StorageClass) {
			sc.Provisioner = "other.csi.example.com"
		}, want: "provisioner changed", wantDrift: true},
		{name: "reclaim policy drift", mutate: func(sc *storagev1.StorageClass) {
			sc.ReclaimPolicy = &deletePolicy
		}, want: "reclaimPolicy Retain", wantDrift: true},
		{name: "configuration digest drift", mutate: func(sc *storagev1.StorageClass) {
			sc.Parameters["changed"] = "true"
		}, want: "configuration digest changed", wantDrift: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHelmReprovisionFixture(t, false)
			objects := []client.Object{fixture.binding}
			if !tt.omitClass {
				liveClass := fixture.class.DeepCopy()
				if tt.mutate != nil {
					tt.mutate(liveClass)
				}
				objects = append(objects, liveClass)
			}
			c, calls := newHelmReprovisionClient(t, objects, nil)
			r := newHelmReprovisionReconciler(c)
			stCopy := fixture.request.DeepCopy()

			_, err := r.doModelCacheRouted(t.Context(), *fixture.request, stCopy, fixture.icms)
			require.ErrorContains(t, err, tt.want)
			assert.True(t, isTerminal(err))
			assert.Equal(t, 1, calls.storageClassGets)
			assert.Empty(t, calls.creates, "no writer object may be created before the precondition passes")
			if tt.wantDrift {
				assert.ErrorIs(t, err, ErrModelCacheStorageSelectionDrift)
			}
			if tt.wantMissing {
				assert.True(t, apierrors.IsNotFound(err))
			}
		})
	}
}

func TestHelmWriterReprovisionRetriesTransientStorageClassRead(t *testing.T) {
	fixture := newHelmReprovisionFixture(t, false)
	c, calls := newHelmReprovisionClient(t, []client.Object{fixture.binding},
		apierrors.NewServiceUnavailable("temporary StorageClass read failure"))
	r := newHelmReprovisionReconciler(c)

	res, err := r.doModelCacheRouted(
		t.Context(), *fixture.request, fixture.request.DeepCopy(), fixture.icms)
	require.NoError(t, err)
	assert.True(t, res.Requeue, "transient API failures must remain retryable")
	assert.Equal(t, 1, calls.storageClassGets)
	assert.Empty(t, calls.creates)
}

func TestHelmWriterReprovisionWithExactStorageClassProceeds(t *testing.T) {
	fixture := newHelmReprovisionFixture(t, false)
	c, calls := newHelmReprovisionClient(
		t, []client.Object{fixture.binding, fixture.class}, nil)
	r := newHelmReprovisionReconciler(c)
	stCopy := fixture.request.DeepCopy()

	res, err := r.doModelCacheRouted(t.Context(), *fixture.request, stCopy, fixture.icms)
	require.NoError(t, err)
	assert.True(t, res.Requeue)
	assert.Equal(t, nvcav1new.StoragePending, stCopy.Status.Phase)
	assert.Equal(t, 1, calls.storageClassGets)
	assert.NotEmpty(t, calls.creates)

	writer := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{
		Namespace: ModelCacheInitNamespace,
		Name:      "rw-pvc-" + reprovisionCacheHandle,
	}, writer))
	require.NotNil(t, writer.Spec.StorageClassName)
	assert.Equal(t, DefaultModelCacheStorageClassName, *writer.Spec.StorageClassName)
	assert.Equal(t, string(reprovisionBindingUID), writer.Labels[ModelCacheBindingUIDLabelKey])
}

func TestHelmExistingBindingOwnedWriterDoesNotRereadStorageClass(t *testing.T) {
	fixture := newHelmReprovisionFixture(t, false)
	c, calls := newHelmReprovisionClient(
		t, []client.Object{fixture.binding, fixture.class}, nil)
	r := newHelmReprovisionReconciler(c)
	stCopy := fixture.request.DeepCopy()

	res, err := r.doModelCacheRouted(t.Context(), *fixture.request, stCopy, fixture.icms)
	require.NoError(t, err)
	assert.True(t, res.Requeue)
	assert.Equal(t, nvcav1new.StoragePending, stCopy.Status.Phase)
	existingWriter := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{
		Namespace: ModelCacheInitNamespace,
		Name:      "rw-pvc-" + reprovisionCacheHandle,
	}, existingWriter))
	require.NoError(t, c.Delete(t.Context(), fixture.class))
	calls.storageClassGets = 0
	calls.creates = nil

	next := stCopy.DeepCopy()
	res, err = r.doModelCacheRouted(t.Context(), *stCopy, next, fixture.icms)
	require.NoError(t, err)
	assert.True(t, res.Requeue)
	assert.Equal(t, 0, calls.storageClassGets)
	assert.Empty(t, calls.creates)

	unknown := stCopy.DeepCopy()
	unknown.Status.Phase = nvcav1new.StorageUnknown
	unknownOut := unknown.DeepCopy()
	calls.creates = nil
	res, err = r.doModelCacheRouted(t.Context(), *unknown, unknownOut, fixture.icms)
	require.NoError(t, err)
	assert.True(t, res.Requeue)
	assert.Equal(t, nvcav1new.StoragePending, unknownOut.Status.Phase)
	assert.Equal(t, 0, calls.storageClassGets)
	assert.NotContains(t, calls.creates,
		"*v1.PersistentVolumeClaim "+ModelCacheInitNamespace+"/rw-pvc-"+reprovisionCacheHandle)

	got := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(existingWriter), got))
	assert.Equal(t, existingWriter.UID, got.UID)
	assert.Equal(t, string(reprovisionBindingUID), got.Labels[ModelCacheBindingUIDLabelKey])
}

func TestHelmWriterPreflightRejectsSameBindingDriftBeforeAnyCreate(t *testing.T) {
	for _, tt := range []struct {
		name          string
		missingWriter bool
		mutate        func(*testing.T, context.Context, client.Client)
	}{
		{
			name: "writer PVC immutable spec drift",
			mutate: func(t *testing.T, ctx context.Context, c client.Client) {
				t.Helper()
				pvc := &corev1.PersistentVolumeClaim{}
				require.NoError(t, c.Get(ctx, client.ObjectKey{
					Namespace: ModelCacheInitNamespace, Name: "rw-pvc-" + reprovisionCacheHandle,
				}, pvc))
				pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
				require.NoError(t, c.Update(ctx, pvc))
			},
		},
		{
			name: "writer Job immutable spec drift", missingWriter: true,
			mutate: func(t *testing.T, ctx context.Context, c client.Client) {
				t.Helper()
				job := &batchv1.Job{}
				require.NoError(t, c.Get(ctx, client.ObjectKey{
					Namespace: ModelCacheInitNamespace, Name: "writer-job-" + reprovisionCacheHandle,
				}, job))
				require.NotEmpty(t, job.Spec.Template.Spec.Containers)
				job.Spec.Template.Spec.Containers[0].Image = "tampered.invalid/writer:latest"
				require.NoError(t, c.Update(ctx, job))
			},
		},
		{
			name: "writer Lease immutable duration drift",
			mutate: func(t *testing.T, ctx context.Context, c client.Client) {
				t.Helper()
				lease := &coordv1.Lease{}
				require.NoError(t, c.Get(ctx, client.ObjectKey{
					Namespace: ModelCacheInitNamespace, Name: buildInitLeaseName(reprovisionCacheHandle),
				}, lease))
				duration := int32(1)
				lease.Spec.LeaseDurationSeconds = &duration
				require.NoError(t, c.Update(ctx, lease))
			},
		},
		{
			name: "writer pull Secret immutable data drift", missingWriter: true,
			mutate: func(t *testing.T, ctx context.Context, c client.Client) {
				t.Helper()
				secret := &corev1.Secret{}
				require.NoError(t, c.Get(ctx, client.ObjectKey{
					Namespace: ModelCacheInitNamespace,
					Name:      "writer-job-" + reprovisionCacheHandle + "-0-pull-worker",
				}, secret))
				if secret.Data == nil {
					secret.Data = map[string][]byte{}
				}
				secret.Data["tampered"] = []byte("true")
				require.NoError(t, c.Update(ctx, secret))
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHelmReprovisionFixture(t, false)
			c, calls := newHelmReprovisionClient(
				t, []client.Object{fixture.binding, fixture.class}, nil)
			r := newHelmReprovisionReconciler(c)
			first := fixture.request.DeepCopy()
			_, err := r.doModelCacheRouted(
				t.Context(), *fixture.request, first, fixture.icms)
			require.NoError(t, err)
			tt.mutate(t, t.Context(), c)
			if tt.missingWriter {
				writer := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
					Namespace: ModelCacheInitNamespace, Name: "rw-pvc-" + reprovisionCacheHandle,
				}}
				require.NoError(t, c.Delete(t.Context(), writer))
			}
			calls.creates = nil

			_, err = r.doModelCacheRouted(
				t.Context(), *fixture.request, fixture.request.DeepCopy(), fixture.icms)
			require.Error(t, err)
			assert.True(t, isTerminal(err))
			require.ErrorIs(t, err, errModelCacheBindingOwnership)
			assert.Empty(t, calls.creates,
				"the complete writer-object preflight must finish before the first create")
		})
	}
}

func TestHelmSuccessfulInitRetriesCleanupFailureWithoutPhaseAdvance(t *testing.T) {
	fixture := newHelmReprovisionFixture(t, false)
	seedClient, _ := newHelmReprovisionClient(
		t, []client.Object{fixture.binding, fixture.class}, nil)
	seedReconciler := newHelmReprovisionReconciler(seedClient)
	request := fixture.request.DeepCopy()
	_, err := seedReconciler.doModelCacheRouted(
		t.Context(), *fixture.request, request, fixture.icms)
	require.NoError(t, err)

	writer := &corev1.PersistentVolumeClaim{}
	require.NoError(t, seedClient.Get(t.Context(), client.ObjectKey{
		Namespace: ModelCacheInitNamespace, Name: "rw-pvc-" + reprovisionCacheHandle,
	}, writer))
	writer.UID = "writer-pvc-uid"
	writer.Spec.VolumeName = "primary-pv-" + reprovisionCacheHandle
	writer.Status.Phase = corev1.ClaimBound
	job := &batchv1.Job{}
	require.NoError(t, seedClient.Get(t.Context(), client.ObjectKey{
		Namespace: ModelCacheInitNamespace, Name: "writer-job-" + reprovisionCacheHandle,
	}, job))
	completion := metav1.Now()
	job.Status.CompletionTime = &completion
	job.Status.Succeeded = 1
	lease := &coordv1.Lease{}
	require.NoError(t, seedClient.Get(t.Context(), client.ObjectKey{
		Namespace: ModelCacheInitNamespace, Name: buildInitLeaseName(reprovisionCacheHandle),
	}, lease))
	secret := &corev1.Secret{}
	require.NoError(t, seedClient.Get(t.Context(), client.ObjectKey{
		Namespace: ModelCacheInitNamespace,
		Name:      "writer-job-" + reprovisionCacheHandle + "-0-pull-worker",
	}, secret))

	primary := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: writer.Spec.VolumeName},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   append([]corev1.PersistentVolumeAccessMode(nil), writer.Spec.AccessModes...),
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              *writer.Spec.StorageClassName,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1", Kind: "PersistentVolumeClaim",
				Namespace: writer.Namespace, Name: writer.Name, UID: writer.UID,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       fixture.class.Provisioner,
					VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
				},
			},
		},
	}
	cleanupLists := 0
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithRESTMapper(newTestRESTMapper(mgrScheme)).
		WithObjects(fixture.binding.DeepCopy(), writer, job, lease, secret, primary).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList,
				opts ...client.ListOption,
			) error {
				if _, ok := list.(*batchv1.JobList); ok {
					cleanupLists++
					return apierrors.NewServiceUnavailable("temporary successful-init cleanup read")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	r := newHelmReprovisionReconciler(c)
	request.Status.Phase = nvcav1new.StorageInitRunning
	out := request.DeepCopy()

	res, err := r.reconcileInitModelCacheNVMesh(
		t.Context(), *request, out, writer.DeepCopy(), job.DeepCopy(),
		[]*corev1.Secret{secret.DeepCopy()}, HelmCacheBackendNVMesh, true, nil)
	require.NoError(t, err)
	assert.True(t, res.Requeue)
	assert.Equal(t, nvcav1new.StorageInitRunning, out.Status.Phase)
	assert.Equal(t, 1, cleanupLists)
	gotPrimary := &corev1.PersistentVolume{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: primary.Name}, gotPrimary))
	assert.Equal(t, string(reprovisionBindingUID),
		gotPrimary.Labels[ModelCacheBindingUIDLabelKey])
}

func TestHelmEncryptedWriterDoesNotRereadBaseStorageClass(t *testing.T) {
	fixture := newHelmReprovisionFixture(t, true)
	c, calls := newHelmReprovisionClient(t, []client.Object{fixture.binding}, nil)
	r := newHelmReprovisionReconciler(c)
	stCopy := fixture.request.DeepCopy()

	res, err := r.doModelCacheRouted(t.Context(), *fixture.request, stCopy, fixture.icms)
	require.NoError(t, err)
	assert.True(t, res.Requeue)
	assert.Equal(t, 0, calls.storageClassGets)

	derivedClassName := buildStorageClassName(reprovisionNCAID)
	derivedClass := &storagev1.StorageClass{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: derivedClassName}, derivedClass))
	writer := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{
		Namespace: ModelCacheInitNamespace,
		Name:      "rw-pvc-" + reprovisionCacheHandle,
	}, writer))
	require.NotNil(t, writer.Spec.StorageClassName)
	assert.Equal(t, derivedClassName, *writer.Spec.StorageClassName)
}

func TestHelmStaticReaderCreationDoesNotRereadStorageClass(t *testing.T) {
	fixture := newHelmReprovisionFixture(t, false)
	primaryPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "primary-reprovision-cache",
			Labels: map[string]string{
				primaryPVLabelKey:            primaryPVLabelValue,
				modelCacheHandleLabelKey:     reprovisionCacheHandle,
				ModelCacheBindingUIDLabelKey: string(reprovisionBindingUID),
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Namespace:  ModelCacheInitNamespace,
				Name:       "rw-pvc-" + reprovisionCacheHandle,
				UID:        types.UID("reprovision-writer-pvc-uid"),
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       NVMeshStorageClassProvisioner,
					VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
				},
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              DefaultModelCacheStorageClassName,
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeAvailable},
	}
	c, calls := newHelmReprovisionClient(
		t, []client.Object{fixture.binding, primaryPV}, nil)
	r := newHelmReprovisionReconciler(c)
	stCopy := fixture.request.DeepCopy()

	_, err := r.doModelCacheRouted(t.Context(), *fixture.request, stCopy, fixture.icms)
	require.NoError(t, err)
	assert.Equal(t, 0, calls.storageClassGets)
	assert.Equal(t, nvcav1new.StorageCreating, stCopy.Status.Phase)

	secondaryPV := &corev1.PersistentVolume{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{
		Name: "secondary-pv-" + reprovisionRequestName,
	}, secondaryPV))
	assert.Equal(t, string(reprovisionBindingUID), secondaryPV.Labels[ModelCacheBindingUIDLabelKey])
	assert.Equal(t, string(reprovisionRequestUID), secondaryPV.Labels[ModelCacheRequestUIDLabelKey])
	roPVC := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{
		Namespace: reprovisionWorkloadNS,
		Name:      "ro-pvc-" + reprovisionCacheHandle,
	}, roPVC))
	assert.Equal(t, string(reprovisionBindingUID), roPVC.Labels[ModelCacheBindingUIDLabelKey])
	assert.Equal(t, string(reprovisionRequestUID), roPVC.Labels[ModelCacheRequestUIDLabelKey])

	_, err = r.doModelCacheRouted(
		t.Context(), *fixture.request, fixture.request.DeepCopy(), fixture.icms)
	require.NoError(t, err)
	assert.Equal(t, 0, calls.storageClassGets,
		"reconciling existing static reader objects must also use the persisted provisioner")
}

func TestHelmReaderCreateRaceRejectsStaleRequestGeneration(t *testing.T) {
	for _, target := range []string{"secondary PV", "reader PVC"} {
		t.Run(target, func(t *testing.T) {
			fixture := newHelmReprovisionFixture(t, false)
			primary := newHelmReaderPrimaryPV()
			injected := false
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithRESTMapper(newTestRESTMapper(mgrScheme)).
				WithObjects(fixture.binding, primary).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, cl client.WithWatch, obj client.Object,
						opts ...client.CreateOption,
					) error {
						resource := ""
						switch typed := obj.(type) {
						case *corev1.PersistentVolume:
							if target == "secondary PV" && typed.Name == "secondary-pv-"+reprovisionRequestName {
								resource = "persistentvolumes"
							}
						case *corev1.PersistentVolumeClaim:
							if target == "reader PVC" && typed.Namespace == reprovisionWorkloadNS &&
								typed.Name == "ro-pvc-"+reprovisionCacheHandle {
								resource = "persistentvolumeclaims"
							}
						}
						if resource == "" || injected {
							return cl.Create(ctx, obj, opts...)
						}
						stale, ok := obj.DeepCopyObject().(client.Object)
						if !ok {
							return fmt.Errorf("race object %T is not a client object", obj)
						}
						stale.GetLabels()[ModelCacheRequestUIDLabelKey] = "stale-request-uid"
						if err := cl.Create(ctx, stale, opts...); err != nil {
							return err
						}
						injected = true
						return apierrors.NewAlreadyExists(
							schema.GroupResource{Resource: resource}, obj.GetName())
					},
				}).Build()
			r := newHelmReprovisionReconciler(c)
			stCopy := fixture.request.DeepCopy()
			stCopy.Labels[ModelCacheBindingUIDLabelKey] = string(reprovisionBindingUID)

			_, err := r.doModelCacheNVMesh(t.Context(), *fixture.request, stCopy, fixture.icms)
			require.ErrorContains(t, err, "request UID")
			assert.True(t, isTerminal(err))
			assert.True(t, injected)

			var stale client.Object
			if target == "secondary PV" {
				stale = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{
					Name: "secondary-pv-" + reprovisionRequestName,
				}}
			} else {
				stale = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
					Name: "ro-pvc-" + reprovisionCacheHandle, Namespace: reprovisionWorkloadNS,
				}}
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(stale), stale))
			assert.Equal(t, "stale-request-uid", stale.GetLabels()[ModelCacheRequestUIDLabelKey])
		})
	}
}

func TestHelmReaderDoesNotRecreatePVCForStaleClaimUID(t *testing.T) {
	fixture := newHelmReprovisionFixture(t, false)
	primary := newHelmReaderPrimaryPV()
	secondary := primary.DeepCopy()
	secondary.ObjectMeta = metav1.ObjectMeta{
		Name: "secondary-pv-" + reprovisionRequestName,
		Labels: map[string]string{
			ModelCacheBindingUIDLabelKey: string(reprovisionBindingUID),
			ModelCacheRequestUIDLabelKey: string(reprovisionRequestUID),
		},
	}
	secondary.Spec.AccessModes = accessModesRO
	secondary.Spec.ClaimRef = &corev1.ObjectReference{
		APIVersion: "v1", Kind: "PersistentVolumeClaim",
		Namespace: reprovisionWorkloadNS, Name: "ro-pvc-" + reprovisionCacheHandle,
		UID: types.UID("deleted-reader-pvc-uid"),
	}
	secondary.Spec.CSI.VolumeHandle = "cluster:csi:volume:" + reprovisionWorkloadNS
	secondary.Status = corev1.PersistentVolumeStatus{}
	c, calls := newHelmReprovisionClient(
		t, []client.Object{fixture.binding, primary, secondary}, nil)
	r := newHelmReprovisionReconciler(c)
	stCopy := fixture.request.DeepCopy()
	stCopy.Labels[ModelCacheBindingUIDLabelKey] = string(reprovisionBindingUID)

	_, err := r.doModelCacheNVMesh(t.Context(), *fixture.request, stCopy, fixture.icms)
	require.ErrorContains(t, err, "reader PVC generation that no longer exists")
	assert.True(t, isTerminal(err))
	assert.Empty(t, calls.creates, "a stale claimRef UID must be rejected before reader PVC creation")
	reader := &corev1.PersistentVolumeClaim{}
	assert.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKey{
		Namespace: reprovisionWorkloadNS, Name: "ro-pvc-" + reprovisionCacheHandle,
	}, reader)))
}

func newHelmReaderPrimaryPV() *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "primary-" + reprovisionCacheHandle,
			Labels: map[string]string{
				primaryPVLabelKey:            primaryPVLabelValue,
				modelCacheHandleLabelKey:     reprovisionCacheHandle,
				ModelCacheBindingUIDLabelKey: string(reprovisionBindingUID),
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1", Kind: "PersistentVolumeClaim",
				Namespace: ModelCacheInitNamespace, Name: "rw-pvc-" + reprovisionCacheHandle,
				UID: types.UID("reprovision-writer-pvc-uid"),
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver: NVMeshStorageClassProvisioner, VolumeHandle: "cluster:csi:volume:" + ModelCacheInitNamespace,
			}},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              DefaultModelCacheStorageClassName,
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeAvailable},
	}
}
