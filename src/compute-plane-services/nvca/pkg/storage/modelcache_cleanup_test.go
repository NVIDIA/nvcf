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
	stderrors "errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestPrimaryPVSelector(t *testing.T) {
	// primaryPVSel must carry the primary-PV exists requirement so cleanup
	// lists only model-cache primary PVs. An empty selector would match every
	// PV in the cluster.
	assert.False(t, primaryPVSel.Matches(labels.Set{}),
		"empty label set must not match; selector should require the primary-PV label")
	assert.True(t, primaryPVSel.Matches(labels.Set{primaryPVLabelKey: "true"}),
		"a PV carrying the primary-PV label must match")
}

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

func TestAnnotatedPerRequestCleanupRefusesMixedBindingOwnership(t *testing.T) {
	for _, tt := range []struct {
		name       string
		pvBinding  string
		pvcBinding string
		mutate     func(*corev1.PersistentVolume, *corev1.PersistentVolumeClaim)
	}{
		{
			name:       "missing PV binding label",
			pvcBinding: string(helmBindingTestBindingUID),
		},
		{
			name:       "foreign PVC binding label",
			pvBinding:  string(helmBindingTestBindingUID),
			pvcBinding: "other-binding",
		},
		{
			name:       "missing PV request UID label",
			pvBinding:  string(helmBindingTestBindingUID),
			pvcBinding: string(helmBindingTestBindingUID),
			mutate: func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
				delete(pv.Labels, ModelCacheRequestUIDLabelKey)
			},
		},
		{
			name:       "foreign PVC request UID label",
			pvBinding:  string(helmBindingTestBindingUID),
			pvcBinding: string(helmBindingTestBindingUID),
			mutate: func(_ *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) {
				pvc.Labels[ModelCacheRequestUIDLabelKey] = "replacement-request-uid"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, binding, st, _ := newHelmBindingTestFixture(t)
			pv, pvc := perRequestBindingOwnedVolumes(st, tt.pvBinding, tt.pvcBinding)
			if tt.mutate != nil {
				tt.mutate(pv, pvc)
			}
			job := bindingOwnedTestJob()
			lease := bindingOwnedTestLease(helmBindingTestRequestName)
			c := fake.NewClientBuilder().WithScheme(mgrScheme).
				WithObjects(binding, pv, pvc, job, lease).Build()
			r := &Reconciler{Client: c}

			res, err := r.doCleanupModelCacheNVMesh(t.Context(), st)
			assert.Equal(t, reconcile.Result{}, res)
			require.ErrorContains(t, err, "ownership mismatch")
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}))
			require.NoError(t, c.Get(
				t.Context(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{}))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(lease), &coordv1.Lease{}))
			condition := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeCleanupSuccessful)
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
		})
	}
}

func TestAnnotatedPerRequestCleanupDeletesExactBindingOwnership(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	pv, pvc := perRequestBindingOwnedVolumes(
		st, string(helmBindingTestBindingUID), string(helmBindingTestBindingUID))
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, pv, pvc).Build()
	r := &Reconciler{Client: c}

	res, err := r.doCleanupModelCacheNVMesh(t.Context(), st)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
	assert.True(t, errors.IsNotFound(c.Get(
		t.Context(), client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{})))
	assert.True(t, errors.IsNotFound(c.Get(
		t.Context(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{})))
	condition := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeCleanupSuccessful)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
}

func TestAnnotatedCleanupTombstoneDeletesReadersButPreservesSharedWriter(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	binding.Status.RequestReferences = nil
	pv, pvc := perRequestBindingOwnedVolumes(
		st, string(helmBindingTestBindingUID), string(helmBindingTestBindingUID))
	job := bindingOwnedTestJob()
	lease := bindingOwnedTestLease(helmBindingTestRequestName)
	c := fake.NewClientBuilder().WithScheme(mgrScheme).
		WithObjects(binding, pv, pvc, job, lease).Build()
	r := &Reconciler{Client: c}

	res, err := r.doCleanupModelCacheNVMesh(t.Context(), st)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
	assert.True(t, errors.IsNotFound(c.Get(
		t.Context(), client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{})))
	assert.True(t, errors.IsNotFound(c.Get(
		t.Context(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{})))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{}))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(lease), &coordv1.Lease{}))
	condition := meta.FindStatusCondition(st.Status.Conditions, ConditionTypeCleanupSuccessful)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
}

func TestAnnotatedReaderCleanupUsesExactDeletePreconditions(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	pv, pvc := perRequestBindingOwnedVolumes(
		st, string(helmBindingTestBindingUID), string(helmBindingTestBindingUID))
	validated := map[string]bool{}
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, pv, pvc).
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

	_, err := r.doCleanupModelCacheNVMesh(t.Context(), st)
	require.NoError(t, err)
	assert.True(t, validated[pv.Name])
	assert.True(t, validated[pvc.Name])
}

func TestAnnotatedReaderCleanupRefusesDeletePolicyPVBeforeAnyDelete(t *testing.T) {
	_, binding, st, _ := newHelmBindingTestFixture(t)
	pv, pvc := perRequestBindingOwnedVolumes(
		st, string(helmBindingTestBindingUID), string(helmBindingTestBindingUID))
	pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
	deletes := 0
	c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding, pv, pvc).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object,
				opts ...client.DeleteOption,
			) error {
				deletes++
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &Reconciler{Client: c}

	res, err := r.doCleanupModelCacheNVMesh(t.Context(), st)
	assert.Equal(t, reconcile.Result{}, res)
	require.ErrorContains(t, err, "reclaim policy")
	assert.Equal(t, 0, deletes)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}))
	require.NoError(t, c.Get(
		t.Context(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}))
}

func TestAnnotatedReaderCleanupClassifiesListErrorsWithoutDeleting(t *testing.T) {
	for _, tt := range []struct {
		name        string
		readErr     error
		wantRequeue bool
		wantError   bool
	}{
		{
			name: "transient API failure",
			readErr: errors.NewServiceUnavailable(
				"temporary reader inventory failure"),
			wantRequeue: true,
		},
		{
			name: "authorization failure",
			readErr: errors.NewForbidden(
				corev1.Resource("persistentvolumes"), "", stderrors.New("denied")),
			wantError: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, binding, st, _ := newHelmBindingTestFixture(t)
			deletes := 0
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(binding).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, list client.ObjectList,
						_ ...client.ListOption,
					) error {
						if _, ok := list.(*corev1.PersistentVolumeList); ok {
							return tt.readErr
						}
						return nil
					},
					Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object,
						opts ...client.DeleteOption,
					) error {
						deletes++
						return cl.Delete(ctx, obj, opts...)
					},
				}).Build()
			r := &Reconciler{Client: c}

			res, err := r.doCleanupModelCacheNVMesh(t.Context(), st)
			assert.Equal(t, tt.wantRequeue, res.Requeue)
			assert.Equal(t, 0, deletes)
			if tt.wantError {
				require.Error(t, err)
				assert.True(t, errors.IsForbidden(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCleanupIdleModelCachesSkipsBindingOwnedPrimaryPV(t *testing.T) {
	now := time.Now()
	timeConfig := (&k8sutil.TimeConfig{}).Complete()
	oldReference := now.Add(-timeConfig.ModelCacheIdlePeriod - time.Minute).
		Format(primaryPVLastReferencedTimeFormat)
	bindingHandle := "binding-cache"
	bindingPV := idlePrimaryPV("binding-primary", bindingHandle, oldReference)
	bindingPV.Labels[ModelCacheBindingUIDLabelKey] = string(helmBindingTestBindingUID)
	legacyPV := idlePrimaryPV("legacy-primary", "legacy-cache", oldReference)
	encryptedSC := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name: "binding-encrypted-sc",
		Annotations: map[string]string{
			encryptedModelCacheStorageClassAnnotation: encryptedModelCacheStorageClassAnnotationValue,
		},
	}}
	bindingPV.Spec.StorageClassName = encryptedSC.Name
	legacyPV.Spec.StorageClassName = encryptedSC.Name

	c := fake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjects(bindingPV, legacyPV, encryptedSC).
		WithIndex(&nvcav1new.StorageRequest{}, objectNameFieldPath, objectNameExtractValues).
		Build()
	statuses := newInitStatusCache(c)
	statuses.put(bindingHandle, nvcav1new.StorageRequestStatus{Phase: nvcav1new.StorageReady})
	r := &Reconciler{
		Client:        c,
		nowFunc:       func() time.Time { return now },
		k8sTimeConfig: timeConfig,
		initStatuses:  statuses,
		metrics:       newTestMetrics(),
	}

	require.NoError(t, r.cleanupIdleModelCaches(t.Context()))
	gotBindingPV := &corev1.PersistentVolume{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(bindingPV), gotBindingPV))
	assert.Equal(t, corev1.PersistentVolumeReclaimRetain, gotBindingPV.Spec.PersistentVolumeReclaimPolicy)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(encryptedSC), &storagev1.StorageClass{}))
	_, found := statuses.get(bindingHandle)
	assert.True(t, found, "binding-owned primary PV must preserve its init-status entry")
	assert.True(t, errors.IsNotFound(c.Get(
		t.Context(), client.ObjectKeyFromObject(legacyPV), &corev1.PersistentVolume{})),
		"legacy idle GC must continue reclaiming unlabeled primary PVs")
}

func perRequestBindingOwnedVolumes(
	st *nvcav1new.StorageRequest,
	pvBinding string,
	pvcBinding string,
) (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim) {
	labels := getClusterWideResourceLabels(st)
	pvLabels := map[string]string{
		StorageRequestOwnerKey:     labels[StorageRequestOwnerKey],
		StorageRequestNamespaceKey: labels[StorageRequestNamespaceKey],
	}
	if pvBinding != "" {
		pvLabels[ModelCacheBindingUIDLabelKey] = pvBinding
	}
	pvLabels[ModelCacheRequestUIDLabelKey] = string(helmBindingTestRequestUID)
	pvcLabels := map[string]string{
		StorageRequestOwnerKey:     labels[StorageRequestOwnerKey],
		StorageRequestNamespaceKey: labels[StorageRequestNamespaceKey],
	}
	if pvcBinding != "" {
		pvcLabels[ModelCacheBindingUIDLabelKey] = pvcBinding
	}
	pvcLabels[ModelCacheRequestUIDLabelKey] = string(helmBindingTestRequestUID)
	pvName := "secondary-pv-" + st.Spec.ICMSRequestName
	pvcName := "ro-pvc-" + st.Spec.ModelCache.CacheHandle
	pvcUID := k8stypes.UID("reader-pvc-uid")
	return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName, UID: k8stypes.UID("secondary-pv-uid"), ResourceVersion: "1", Labels: pvLabels,
			},
			Spec: corev1.PersistentVolumeSpec{
				AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
					Driver: NVMeshStorageClassProvisioner, VolumeHandle: "test-volume-handle",
				}},
				ClaimRef: &corev1.ObjectReference{
					APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: st.Namespace,
					Name: pvcName, UID: pvcUID,
				},
			},
		}, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvcName, Namespace: st.Namespace, UID: pvcUID,
				ResourceVersion: "1", Labels: pvcLabels,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
				VolumeName:  pvName,
			},
		}
}

func idlePrimaryPV(name, cacheHandle, lastReference string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				primaryPVLabelKey:        primaryPVLabelValue,
				modelCacheHandleLabelKey: cacheHandle,
			},
			Annotations: map[string]string{
				primaryPVLastReferencedAnnotationKey: lastReference,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              DefaultModelCacheStorageClassName,
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeAvailable},
	}
}

// bindingForRetirement builds an Active binding that owns one of each resource
// kind, so a retirement test proves the finalizer is only dropped after every
// declared object is gone.
func bindingForRetirement(name, handle string, created time.Time) *nvcav2beta1.ModelCacheBinding {
	return &nvcav2beta1.ModelCacheBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ModelCacheInitNamespace,
			CreationTimestamp: metav1.NewTime(created),
			Finalizers:        []string{nvcav2beta1.ModelCacheBindingFinalizer},
			Labels:            map[string]string{modelCacheHandleLabelKey: handle},
		},
		Spec: nvcav2beta1.ModelCacheBindingSpec{
			Resources: nvcav2beta1.ModelCacheBindingResourceIntent{
				WriterNamespace:            ModelCacheInitNamespace,
				PersistentVolumeClaimNames: []string{"rw-pvc-" + handle},
				PersistentVolumeNames:      []string{"secondary-pv-" + handle},
				JobNames:                   []string{"writer-job-" + handle},
				LeaseName:                  "lease-" + handle,
			},
		},
		Status: nvcav2beta1.ModelCacheBindingStatus{Phase: nvcav2beta1.ModelCacheBindingPhaseActive},
	}
}

func retirementReconciler(t *testing.T, now time.Time, objs ...client.Object) *Reconciler {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(mgrScheme).
		WithRESTMapper(newTestRESTMapper(mgrScheme)).
		WithStatusSubresource(&nvcav2beta1.ModelCacheBinding{})
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	return &Reconciler{
		Client:        builder.Build(),
		metrics:       newTestMetrics(),
		fff:           &featureflagmock.Fetcher{},
		nowFunc:       func() time.Time { return now },
		k8sTimeConfig: (&k8sutil.TimeConfig{}).Complete(),
	}
}

// TestRetireIdleModelCacheBindings covers the lifecycle that did not exist:
// bindings were created Active with a finalizer nothing removed, so they and
// the resources the finalizer protects accumulated for the life of the cluster.
func TestRetireIdleModelCacheBindings(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	idle := (&k8sutil.TimeConfig{}).Complete().ModelCacheIdlePeriod
	empty := &nvcav1new.StorageRequestList{}

	t.Run("an idle unreferenced binding starts retiring", func(t *testing.T) {
		b := bindingForRetirement("b-1", "h1", now.Add(-idle-time.Minute))
		r := retirementReconciler(t, now, b)
		require.NoError(t, r.retireIdleModelCacheBindings(t.Context(), empty))

		got := &nvcav2beta1.ModelCacheBinding{}
		require.NoError(t, r.Client.Get(t.Context(), client.ObjectKeyFromObject(b), got))
		assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseRetiring, got.Status.Phase)
		require.NotNil(t, got.Status.LastPhaseTransitionTime)
		assert.Contains(t, got.Finalizers, nvcav2beta1.ModelCacheBindingFinalizer,
			"the finalizer holds until the resources are released")
	})

	t.Run("a young binding is left alone", func(t *testing.T) {
		b := bindingForRetirement("b-2", "h2", now.Add(-time.Minute))
		r := retirementReconciler(t, now, b)
		require.NoError(t, r.retireIdleModelCacheBindings(t.Context(), empty))

		got := &nvcav2beta1.ModelCacheBinding{}
		require.NoError(t, r.Client.Get(t.Context(), client.ObjectKeyFromObject(b), got))
		assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, got.Status.Phase,
			"a warm cache must survive a function scaling to zero")
	})

	t.Run("a referenced binding is never retired", func(t *testing.T) {
		b := bindingForRetirement("b-3", "h3", now.Add(-idle-time.Hour))
		b.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{
			{Namespace: "tenant", Name: "req", UID: "uid-1"},
		}
		r := retirementReconciler(t, now, b)
		require.NoError(t, r.retireIdleModelCacheBindings(t.Context(), empty))

		got := &nvcav2beta1.ModelCacheBinding{}
		require.NoError(t, r.Client.Get(t.Context(), client.ObjectKeyFromObject(b), got))
		assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, got.Status.Phase)
	})

	t.Run("a live request on the handle holds the binding", func(t *testing.T) {
		b := bindingForRetirement("b-4", "h4", now.Add(-idle-time.Hour))
		r := retirementReconciler(t, now, b)
		live := &nvcav1new.StorageRequestList{Items: []nvcav1new.StorageRequest{{
			ObjectMeta: metav1.ObjectMeta{Name: "st", Namespace: "tenant"},
			Spec:       nvcav1new.StorageRequestSpec{ModelCache: &nvcav1new.ModelCacheSpec{CacheHandle: "h4"}},
		}}}
		require.NoError(t, r.retireIdleModelCacheBindings(t.Context(), live))

		got := &nvcav2beta1.ModelCacheBinding{}
		require.NoError(t, r.Client.Get(t.Context(), client.ObjectKeyFromObject(b), got))
		assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, got.Status.Phase,
			"a request still naming the handle keeps the cache")
	})

	t.Run("retiring releases the declared resources and the finalizer", func(t *testing.T) {
		b := bindingForRetirement("b-5", "h5", now.Add(-idle-time.Hour))
		b.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
		owned := []client.Object{
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
				Name: "rw-pvc-h5", Namespace: ModelCacheInitNamespace}},
			&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "secondary-pv-h5"}},
			&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: "writer-job-h5", Namespace: ModelCacheInitNamespace}},
			&coordv1.Lease{ObjectMeta: metav1.ObjectMeta{
				Name: "lease-h5", Namespace: ModelCacheInitNamespace}},
		}
		// An object of the same kind that the binding does not name: retirement
		// must not reach another cache's resources.
		bystander := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: "rw-pvc-other", Namespace: ModelCacheInitNamespace}}

		r := retirementReconciler(t, now, append(append([]client.Object{b}, owned...), bystander)...)
		require.NoError(t, r.retireIdleModelCacheBindings(t.Context(), empty))

		for _, o := range owned {
			err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(o), o.DeepCopyObject().(client.Object))
			assert.True(t, apierrors.IsNotFound(err), "%T %s should be released", o, o.GetName())
		}
		require.NoError(t, r.Client.Get(t.Context(),
			client.ObjectKeyFromObject(bystander), &corev1.PersistentVolumeClaim{}),
			"an object the binding does not name must survive")

		got := &nvcav2beta1.ModelCacheBinding{}
		err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(b), got)
		if err == nil {
			assert.NotContains(t, got.Finalizers, nvcav2beta1.ModelCacheBindingFinalizer,
				"the finalizer must be dropped so the object can be deleted")
		} else {
			assert.True(t, apierrors.IsNotFound(err))
		}
	})
}

// TestModelCacheInitWritersRemaining covers the invariant that keeps a cleanup
// race recoverable: the ownership Lease is not released while a writer object
// survives. The normal init path renews the Lease and then creates its
// objects, so a create can land after cleanup took its snapshot without
// touching the Lease, which is the only thing the per-delete guard watches.
// Releasing the Lease then would strand that object, because the next cleanup
// finds no Lease, refuses to authorize, and the finalizer never clears.
func TestModelCacheInitWritersRemaining(t *testing.T) {
	const handle = "raced-handle"
	labels := map[string]string{modelCacheHandleLabelKey: handle}
	listOpts := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(ModelCacheInitNamespace),
	}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ModelCacheInitNamespace, Labels: labels}
	}

	t.Run("nothing left releases the Lease", func(t *testing.T) {
		r := retirementReconciler(t, time.Now())
		got, err := r.modelCacheInitWritersRemaining(t.Context(), listOpts, false)
		require.NoError(t, err)
		assert.Zero(t, got)
	})

	t.Run("a writer Job created after the snapshot holds the Lease", func(t *testing.T) {
		r := retirementReconciler(t, time.Now(),
			&batchv1.Job{ObjectMeta: meta("writer-job-" + handle)})
		got, err := r.modelCacheInitWritersRemaining(t.Context(), listOpts, false)
		require.NoError(t, err)
		assert.Equal(t, 1, got)
	})

	t.Run("a pull Secret counts too", func(t *testing.T) {
		r := retirementReconciler(t, time.Now(),
			&corev1.Secret{ObjectMeta: meta("writer-job-" + handle + "-0-pull-worker")})
		got, err := r.modelCacheInitWritersRemaining(t.Context(), listOpts, false)
		require.NoError(t, err)
		assert.Equal(t, 1, got)
	})

	t.Run("a retained writer PVC is the backing store, not a leftover", func(t *testing.T) {
		r := retirementReconciler(t, time.Now(),
			&corev1.PersistentVolumeClaim{ObjectMeta: meta("rw-pvc-" + handle)})

		got, err := r.modelCacheInitWritersRemaining(t.Context(), listOpts, true)
		require.NoError(t, err)
		assert.Zero(t, got, "the shared filesystem backend keeps this claim on purpose")

		got, err = r.modelCacheInitWritersRemaining(t.Context(), listOpts, false)
		require.NoError(t, err)
		assert.Equal(t, 1, got, "a backend that does not retain it must still be waited for")
	})

	t.Run("objects already terminating do not stall the release", func(t *testing.T) {
		terminating := &batchv1.Job{ObjectMeta: meta("writer-job-" + handle)}
		now := metav1.NewTime(time.Now())
		terminating.DeletionTimestamp = &now
		terminating.Finalizers = []string{"test/hold"}
		r := retirementReconciler(t, time.Now(), terminating)
		got, err := r.modelCacheInitWritersRemaining(t.Context(), listOpts, false)
		require.NoError(t, err)
		assert.Zero(t, got)
	})
}

// TestRequireUnchangedModelCacheInitLease closes the create side of the
// cleanup race. Holding the Lease at the top of a reconcile is not authority
// to create: cleanup takes the same Lease with a compare-and-swap and then
// deletes the writer objects, so anything created after its snapshot lands
// behind it. The create path proves the Lease is still the object it was
// admitted on, immediately before creating.
func TestRequireUnchangedModelCacheInitLease(t *testing.T) {
	newLease := func() *coordv1.Lease {
		return &coordv1.Lease{ObjectMeta: metav1.ObjectMeta{
			Name: "init-lease", Namespace: ModelCacheInitNamespace,
			// The fake client does not assign one; a real API server always does.
			UID: "init-lease-uid",
		}}
	}

	t.Run("an unchanged Lease admits the create", func(t *testing.T) {
		lease := newLease()
		r := retirementReconciler(t, time.Now(), lease)
		observed := &coordv1.Lease{}
		require.NoError(t, r.Client.Get(t.Context(), client.ObjectKeyFromObject(lease), observed))

		require.NoError(t, r.requireUnchangedModelCacheInitLease(t.Context(), observed))
	})

	t.Run("a Lease modified by cleanup refuses the create", func(t *testing.T) {
		lease := newLease()
		r := retirementReconciler(t, time.Now(), lease)
		observed := &coordv1.Lease{}
		require.NoError(t, r.Client.Get(t.Context(), client.ObjectKeyFromObject(lease), observed))

		// What cleanup's compare-and-swap does: bump the Lease.
		cleanupTouched := observed.DeepCopy()
		now := metav1.NowMicro()
		cleanupTouched.Spec.RenewTime = &now
		require.NoError(t, r.Client.Update(t.Context(), cleanupTouched))

		err := r.requireUnchangedModelCacheInitLease(t.Context(), observed)
		require.Error(t, err)
		assert.ErrorIs(t, err, errModelCacheInitLeaseChanged)
		assert.Contains(t, err.Error(), "modified after this reconcile was admitted")
	})

	t.Run("a released Lease refuses the create", func(t *testing.T) {
		lease := newLease()
		r := retirementReconciler(t, time.Now(), lease)
		observed := &coordv1.Lease{}
		require.NoError(t, r.Client.Get(t.Context(), client.ObjectKeyFromObject(lease), observed))
		require.NoError(t, r.Client.Delete(t.Context(), lease))

		err := r.requireUnchangedModelCacheInitLease(t.Context(), observed)
		require.Error(t, err)
		assert.ErrorIs(t, err, errModelCacheInitLeaseChanged)
		assert.Contains(t, err.Error(), "was released")
	})

	t.Run("no observed identity is refused rather than assumed", func(t *testing.T) {
		r := retirementReconciler(t, time.Now())
		err := r.requireUnchangedModelCacheInitLease(t.Context(), nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, errModelCacheInitLeaseChanged,
			"a missing identity is a programming error, not a lost race")
	})
}
