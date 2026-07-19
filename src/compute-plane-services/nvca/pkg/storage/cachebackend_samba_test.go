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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func sambaInfraClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	sch := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(sch))
	require.NoError(t, appsv1.AddToScheme(sch))
	require.NoError(t, storagev1.AddToScheme(sch))
	require.NoError(t, netv1.AddToScheme(sch))
	return fake.NewClientBuilder().WithScheme(sch).
		WithStatusSubresource(&appsv1.Deployment{}).
		WithObjects(objs...).Build()
}

func TestEnsureSambaModelCacheInfra_RequiresImage(t *testing.T) {
	c := sambaInfraClient(t)
	_, err := EnsureSambaModelCacheInfra(t.Context(), c, "h1", "", corev1.ResourceRequirements{}, resource.MustParse("1Gi"))
	require.Error(t, err)
}

func TestEnsureSambaModelCacheInfra_RequiresCacheHandle(t *testing.T) {
	c := sambaInfraClient(t)
	_, err := EnsureSambaModelCacheInfra(t.Context(), c, "", "samba:latest", corev1.ResourceRequirements{}, resource.MustParse("1Gi"))
	require.Error(t, err)
}

func TestEnsureSambaModelCacheInfra_PerHandle_SizedFromCacheSize_NeverCreatesStorageClass(t *testing.T) {
	ctx := t.Context()
	c := sambaInfraClient(t)
	handle := "abc123handle"
	size := resource.MustParse("7Gi")
	name := sambaModelCacheResourceName(handle)

	// First pass: server not yet available -> not ready, but resources created.
	ready, err := EnsureSambaModelCacheInfra(ctx, c, handle, "samba:latest", corev1.ResourceRequirements{}, size)
	require.NoError(t, err)
	assert.False(t, ready, "not ready until the per-handle server is available")

	// Shared RW + RO creds secrets.
	for _, sname := range []string{SambaModelCacheReadWriteSecretName, SambaModelCacheReadOnlySecretName} {
		s := &corev1.Secret{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: sname}, s), sname)
		assert.NotEmpty(t, s.Data["password"])
	}

	// Per-handle backing PVC on nvcf-sc, sized to cacheSize, named samba-<handle>,
	// carrying the GC component label + handle annotation but NOT the cache-handle
	// label (that would collide with init-writer-PVC cleanup).
	pvc := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: SambaModelCacheBackingPVCName(handle)}, pvc))
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, SambaModelCacheDataStorageClassName, *pvc.Spec.StorageClassName)
	assert.True(t, size.Equal(pvc.Spec.Resources.Requests[corev1.ResourceStorage]), "backing PVC sized to cacheSize")
	assert.Equal(t, sambaModelCacheComponentLabelValue, pvc.Labels[sambaModelCacheComponentLabelKey])
	assert.Equal(t, handle, pvc.Annotations[sambaModelCacheHandleAnnotationKey])
	assert.Empty(t, pvc.Labels[modelCacheHandleLabelKey], "backing PVC must not carry the cache-handle label")

	// Per-handle Service + NetworkPolicy + Deployment, all named samba-<handle>.
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: name}, &corev1.Service{}))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: name}, &netv1.NetworkPolicy{}))
	dep := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: name}, dep))
	assert.Equal(t, "samba:latest", dep.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, SambaModelCacheBackingPVCName(handle),
		dep.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName, "server mounts its own backing PVC")

	// CRITICAL regression guard: NVCA must NEVER create nvcf-miniservice-sc (operator's
	// branch-2 class) or any StorageClass.
	scl := &storagev1.StorageClassList{}
	require.NoError(t, c.List(ctx, scl))
	assert.Empty(t, scl.Items, "Samba backend must not create any StorageClass")

	// Mark available; second pass -> ready, still no StorageClass.
	dep.Status.AvailableReplicas = 1
	require.NoError(t, c.Status().Update(ctx, dep))
	ready, err = EnsureSambaModelCacheInfra(ctx, c, handle, "samba:latest", corev1.ResourceRequirements{}, size)
	require.NoError(t, err)
	assert.True(t, ready)
	require.NoError(t, c.List(ctx, scl))
	assert.Empty(t, scl.Items, "still no StorageClass after the server is ready")
	err = c.Get(ctx, client.ObjectKey{Name: HelmCacheSharedStorageClassName}, &storagev1.StorageClass{})
	assert.True(t, apierrors.IsNotFound(err), "nvcf-miniservice-sc must not exist")
}

func TestEnsureSambaModelCacheInfra_Idempotent(t *testing.T) {
	ctx := t.Context()
	c := sambaInfraClient(t)
	handle := "idempotent-handle"
	size := resource.MustParse("1Gi")
	_, err := EnsureSambaModelCacheInfra(ctx, c, handle, "samba:latest", corev1.ResourceRequirements{}, size)
	require.NoError(t, err)
	_, err = EnsureSambaModelCacheInfra(ctx, c, handle, "samba:latest", corev1.ResourceRequirements{}, size)
	require.NoError(t, err)
	deps := &appsv1.DeploymentList{}
	require.NoError(t, c.List(ctx, deps, client.InNamespace(ModelCacheInitNamespace)))
	count := 0
	for _, d := range deps.Items {
		if d.Name == sambaModelCacheResourceName(handle) {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

// TestEnsureSambaModelCacheInfra_DistinctHandlesDistinctServers proves two
// cacheHandles get independent servers + backing PVCs (no shared global PVC).
func TestEnsureSambaModelCacheInfra_DistinctHandlesDistinctServers(t *testing.T) {
	ctx := t.Context()
	c := sambaInfraClient(t)
	_, err := EnsureSambaModelCacheInfra(ctx, c, "handle-a", "samba:latest", corev1.ResourceRequirements{}, resource.MustParse("1Gi"))
	require.NoError(t, err)
	_, err = EnsureSambaModelCacheInfra(ctx, c, "handle-b", "samba:latest", corev1.ResourceRequirements{}, resource.MustParse("2Gi"))
	require.NoError(t, err)

	for _, h := range []string{"handle-a", "handle-b"} {
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: sambaModelCacheResourceName(h)}, &appsv1.Deployment{}), h)
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: SambaModelCacheBackingPVCName(h)}, &corev1.PersistentVolumeClaim{}), h)
	}
	assert.NotEqual(t, sambaModelCacheResourceName("handle-a"), sambaModelCacheResourceName("handle-b"))
}

func TestDeleteSambaModelCacheInfra(t *testing.T) {
	ctx := t.Context()
	handle := "delete-me"
	// Static SMB PVs for the handle (writer plumbing + an orphaned reader whose
	// namespace vanished) must be swept; a same-handle PV from another driver
	// (an NVMesh primary) must be untouched.
	cap1 := resource.MustParse("1Gi")
	rwPV := newSambaModelCachePV(sambaModelCacheWriterPVName(handle), "rw-pvc-"+handle, ModelCacheInitNamespace, handle, false, cap1)
	roPV := newSambaModelCachePV("samba-ro-pv-gone-ns-"+handle, "ro-pvc-"+handle, "gone-ns", handle, true, cap1)
	otherDriverPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "primary-" + handle,
			Labels: map[string]string{modelCacheHandleLabelKey: handle},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:    corev1.ResourceList{corev1.ResourceStorage: cap1},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "vh"},
			},
		},
	}
	c := sambaInfraClient(t, rwPV, roPV, otherDriverPV)
	_, err := EnsureSambaModelCacheInfra(ctx, c, handle, "samba:latest", corev1.ResourceRequirements{}, resource.MustParse("1Gi"))
	require.NoError(t, err)

	require.NoError(t, DeleteSambaModelCacheInfra(ctx, c, handle))

	name := sambaModelCacheResourceName(handle)
	for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &netv1.NetworkPolicy{}, &corev1.PersistentVolumeClaim{}} {
		err := c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: name}, obj)
		assert.True(t, apierrors.IsNotFound(err), "%T should be deleted", obj)
	}
	// The handle's static SMB PVs are swept (static PVs are never reclaimed by
	// the PV controller, so leaving them leaks).
	for _, pvName := range []string{rwPV.Name, roPV.Name} {
		err := c.Get(ctx, client.ObjectKey{Name: pvName}, &corev1.PersistentVolume{})
		assert.True(t, apierrors.IsNotFound(err), "static SMB PV %s should be swept", pvName)
	}
	// Same-handle PVs from other drivers are untouched.
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: otherDriverPV.Name}, &corev1.PersistentVolume{}))
	// Shared creds secrets are retained for other handles.
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: SambaModelCacheReadWriteSecretName}, &corev1.Secret{}))

	// Idempotent: deleting again is not an error.
	require.NoError(t, DeleteSambaModelCacheInfra(ctx, c, handle))
}

func TestEnsureSambaModelCacheInfra_CreateErrorWrapped(t *testing.T) {
	sch := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(sch))
	require.NoError(t, appsv1.AddToScheme(sch))
	require.NoError(t, netv1.AddToScheme(sch))
	c := fake.NewClientBuilder().WithScheme(sch).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					return errors.New("boom")
				}
				return nil
			},
		}).Build()
	_, err := EnsureSambaModelCacheInfra(t.Context(), c, "h1", "samba:latest", corev1.ResourceRequirements{}, resource.MustParse("1Gi"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure samba model cache")
	assert.Contains(t, err.Error(), "boom")
}

func TestEnsureSambaModelCacheInfra_GetDeploymentErrorWrapped(t *testing.T) {
	sch := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(sch))
	require.NoError(t, appsv1.AddToScheme(sch))
	require.NoError(t, netv1.AddToScheme(sch))
	c := fake.NewClientBuilder().WithScheme(sch).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				if _, ok := obj.(*appsv1.Deployment); ok {
					return errors.New("get-boom")
				}
				return apierrors.NewNotFound(schema.GroupResource{}, key.Name)
			},
		}).Build()
	_, err := EnsureSambaModelCacheInfra(t.Context(), c, "h1", "samba:latest", corev1.ResourceRequirements{}, resource.MustParse("1Gi"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get samba deployment")
}

func TestNewSambaModelCachePV_RWandRO(t *testing.T) {
	cap1 := resource.MustParse("1Gi")
	handle := "abc123"
	// Each handle has its own per-handle server, so the share root IS the cache
	// root (no per-handle subdir). Both writer and readers mount the share root;
	// access is differentiated by RW vs RO credentials.
	root := "//" + sambaModelCacheResourceName(handle) + "." + ModelCacheInitNamespace + ".svc/" + SambaModelCacheShareName

	rw := newSambaModelCachePV("rw-pv", "rw-pvc-"+handle, ModelCacheInitNamespace, handle, false, cap1)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, rw.Spec.AccessModes)
	assert.Equal(t, "", rw.Spec.StorageClassName, "static PV must not reference a StorageClass")
	assert.Equal(t, SMBCSIDriverName, rw.Spec.CSI.Driver)
	assert.Equal(t, root, rw.Spec.CSI.VolumeAttributes["source"], "writer mounts the per-handle share root")
	assert.Equal(t, SambaModelCacheReadWriteSecretName, rw.Spec.CSI.NodeStageSecretRef.Name)
	assert.Equal(t, ModelCacheInitNamespace, rw.Spec.CSI.NodeStageSecretRef.Namespace)
	require.NotNil(t, rw.Spec.ClaimRef)
	assert.Equal(t, "rw-pvc-"+handle, rw.Spec.ClaimRef.Name)

	// Reader mounts the same per-handle share root, read-only, as a distinct user.
	ro := newSambaModelCachePV("ro-pv", "ro-pvc-"+handle, "workload-ns", handle, true, cap1)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}, ro.Spec.AccessModes)
	assert.Equal(t, root, ro.Spec.CSI.VolumeAttributes["source"], "reader mounts the per-handle share root")
	assert.Equal(t, SambaModelCacheReadOnlySecretName, ro.Spec.CSI.NodeStageSecretRef.Name)
	assert.Equal(t, "workload-ns", ro.Spec.ClaimRef.Namespace)
	// VolumeHandle must be unique per PV (the static SMB CSI driver dedups by it).
	assert.NotEqual(t, rw.Spec.CSI.VolumeHandle, ro.Spec.CSI.VolumeHandle)

	pvc := newSambaModelCachePVC("ro-pvc-"+handle, "workload-ns", "ro-pv", true, cap1)
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, "", *pvc.Spec.StorageClassName, "static PVC must not trigger dynamic provisioning")
	assert.Equal(t, "ro-pv", pvc.Spec.VolumeName)
}

func TestSambaModelCacheResourceName_LongHandleBounded(t *testing.T) {
	long := ""
	for range 80 {
		long += "a"
	}
	name := sambaModelCacheResourceName(long)
	assert.LessOrEqual(t, len(name), 63, "name must respect the 63-char limit")
	// Stable: same input yields same name.
	assert.Equal(t, name, sambaModelCacheResourceName(long))
}
