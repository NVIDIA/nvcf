// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package checkpointstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const xnsHash = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"

// promotedSharedFixture is the cluster after a shared-volume promote in
// "capture-ns": primary PV (Retain), secondary reader PV, rox claim.
func promotedSharedFixture() *fake.Clientset {
	sc := "nvmesh-sc"
	primary := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-primary", Labels: map[string]string{"nvsnap.io/role": "writer"}},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("96Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "nvmesh-vol:proj:capture-ns"}},
		},
	}
	sec := primary.DeepCopy()
	sec.ObjectMeta = metav1.ObjectMeta{Name: secondaryPVName(xnsHash), Labels: map[string]string{"nvsnap.io/role": "reader-shared"}}
	sec.Spec.ClaimRef = &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Name: sharedROXName(xnsHash), Namespace: "capture-ns"}
	rox := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: sharedROXName(xnsHash), Namespace: "capture-ns", Labels: map[string]string{labelHashShort: ShortHash(xnsHash)}},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: sec.Name, StorageClassName: &sc},
	}
	return fake.NewSimpleClientset(primary, sec, rox)
}

func sharedPromoter(kc *fake.Clientset) *SharedVolumePromoter {
	tx, _ := LookupVolumeHandleTransform("nvmesh")
	return &SharedVolumePromoter{KubeClient: kc, StorageClass: "nvmesh-sc", Transform: tx, MountOptions: []string{"ro", "norecovery", "nouuid"}, Log: logrus.New()}
}

func TestSharedVolume_EnsureClaimMintsNamespaceLocalClaim(t *testing.T) {
	kc := promotedSharedFixture()
	p := sharedPromoter(kc)
	ctx := context.Background()
	if err := p.EnsureClaim(ctx, xnsHash, "fn-ns"); err != nil {
		t.Fatal(err)
	}
	pvc, err := kc.CoreV1().PersistentVolumeClaims("fn-ns").Get(ctx, sharedROXName(xnsHash), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("rox claim not minted in fn-ns: %v", err)
	}
	pvName := namespacedSecondaryPVName(xnsHash, "fn-ns")
	if pvc.Spec.VolumeName != pvName || pvc.Spec.AccessModes[0] != corev1.ReadOnlyMany {
		t.Errorf("claim must bind the per-namespace PV read-only: %+v", pvc.Spec)
	}
	pv, err := kc.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.CSI.VolumeHandle != "nvmesh-vol:proj:fn-ns" {
		t.Errorf("handle must be rewritten for the consumer namespace, got %q", pv.Spec.CSI.VolumeHandle)
	}
	if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != "fn-ns" || !pv.Spec.CSI.ReadOnly || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("per-namespace PV must be pre-bound to fn-ns, read-only, retained: %+v", pv.Spec)
	}
	if pv.Labels[labelHashShort] != ShortHash(xnsHash) || pv.Labels[labelNamespace] != "fn-ns" {
		t.Errorf("per-namespace PV must carry hash and namespace labels for Delete: %v", pv.Labels)
	}
	// Idempotent, and a second namespace gets its own PV.
	if err := p.EnsureClaim(ctx, xnsHash, "fn-ns"); err != nil {
		t.Errorf("second EnsureClaim must be a no-op, got %v", err)
	}
	if err := p.EnsureClaim(ctx, xnsHash, "other-ns"); err != nil {
		t.Fatal(err)
	}
	pvs, _ := kc.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{LabelSelector: labelHashShort + "=" + ShortHash(xnsHash)})
	if len(pvs.Items) != 2 {
		t.Errorf("want one per-namespace PV per consumer namespace, got %d", len(pvs.Items))
	}
	// The capture namespace already has the promote's own claim: no-op.
	if err := p.EnsureClaim(ctx, xnsHash, "capture-ns"); err != nil {
		t.Errorf("capture namespace must be a no-op, got %v", err)
	}
}

func TestSharedVolume_EnsureClaimBeforePromoteIsNotFound(t *testing.T) {
	p := sharedPromoter(fake.NewSimpleClientset())
	if err := p.EnsureClaim(context.Background(), xnsHash, "fn-ns"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nothing promoted: want ErrNotFound, got %v", err)
	}
}

func TestSharedVolume_DeleteReapsNamespacedClaims(t *testing.T) {
	kc := promotedSharedFixture()
	p := sharedPromoter(kc)
	ctx := context.Background()
	for _, ns := range []string{"fn-a", "fn-b"} {
		if err := p.EnsureClaim(ctx, xnsHash, ns); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Delete(ctx, xnsHash, "capture-ns"); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"capture-ns", "fn-a", "fn-b"} {
		if _, err := kc.CoreV1().PersistentVolumeClaims(ns).Get(ctx, sharedROXName(xnsHash), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("%s: rox claim must be gone, err=%v", ns, err)
		}
	}
	pvs, _ := kc.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if len(pvs.Items) != 0 {
		names := []string{}
		for _, pv := range pvs.Items {
			names = append(names, pv.Name)
		}
		t.Errorf("all PVs must be gone after Delete, left %v", names)
	}
}

// Mount in a namespace without a claim mints it from the promote, so a
// chart in its own namespace restores from a capture made elsewhere.
func TestPerCapturePVCBackend_MountMintsClaimInRestoreNamespace(t *testing.T) {
	kc := promotedSharedFixture()
	b := &PerCapturePVCBackend{KubeClient: kc, Namespace: "nvsnap-system", StorageClass: "nvmesh-sc", Promoter: sharedPromoter(kc), Log: logrus.New()}
	pm, err := b.Mount(context.Background(), xnsHash, VolumeMeta{Name: "nvsnap-cachedir", MountPath: "/opt/nvsnap", Namespace: "fn-ns"})
	if err != nil {
		t.Fatal(err)
	}
	if pm.Volume.PersistentVolumeClaim == nil || pm.Volume.PersistentVolumeClaim.ClaimName != sharedROXName(xnsHash) {
		t.Errorf("mount must name the namespace-local rox claim, got %+v", pm.Volume)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("fn-ns").Get(context.Background(), sharedROXName(xnsHash), metav1.GetOptions{}); err != nil {
		t.Errorf("claim must exist in fn-ns after Mount: %v", err)
	}
}

// snapshot-clone: a namespace-local clone needs a snapshot in that
// namespace, pre-provisioned from the promote's content handle.
func TestSnapshotClone_EnsureClaimPreProvisionsSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		volumeSnapshotGVR: "VolumeSnapshotList", volumeSnapshotContentGVR: "VolumeSnapshotContentList",
	})
	ctx := context.Background()
	snapName := "snap-" + ShortHash(xnsHash)
	promoted := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot",
		"metadata": map[string]any{"name": snapName, "namespace": "capture-ns", "labels": map[string]any{"nvsnap.io/per-capture": "true"}},
		"status":   map[string]any{"readyToUse": true, "boundVolumeSnapshotContentName": "snapcontent-1", "restoreSize": "20Gi"},
	}}
	content := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshotContent",
		"metadata": map[string]any{"name": "snapcontent-1"},
		"spec":     map[string]any{"driver": "pd.csi.storage.gke.io"},
		"status":   map[string]any{"snapshotHandle": "projects/p/global/snapshots/s1"},
	}}
	// The namespaced copy is pre-seeded ready, standing in for the
	// external snapshotter that would bind it in a real cluster.
	nsSnap := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot",
		"metadata": map[string]any{"name": snapName, "namespace": "fn-ns", "labels": map[string]any{"nvsnap.io/per-capture": "true", labelHashShort: ShortHash(xnsHash), labelNamespace: "fn-ns"}},
		"status":   map[string]any{"readyToUse": true},
	}}
	for _, o := range []*unstructured.Unstructured{promoted, content, nsSnap} {
		gvr := volumeSnapshotGVR
		if o.GetKind() == "VolumeSnapshotContent" {
			gvr = volumeSnapshotContentGVR
		}
		if _, err := dyn.Resource(gvr).Namespace(o.GetNamespace()).Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	kc := fake.NewSimpleClientset()
	p := &SnapshotClonePromoter{KubeClient: kc, DynClient: dyn, StorageClass: "hyperdisk-ml", SnapshotClass: "hdml", ReadOnlyMany: true, SnapshotTimeout: 5 * time.Second, Log: logrus.New()}
	if err := p.EnsureClaim(ctx, xnsHash, "fn-ns"); err != nil {
		t.Fatal(err)
	}
	pre, err := dyn.Resource(volumeSnapshotContentGVR).Get(ctx, "snapcontent-1-"+nsSuffix("fn-ns"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pre-provisioned content missing: %v", err)
	}
	handle, _, _ := unstructured.NestedString(pre.Object, "spec", "source", "snapshotHandle")
	refNS, _, _ := unstructured.NestedString(pre.Object, "spec", "volumeSnapshotRef", "namespace")
	policy, _, _ := unstructured.NestedString(pre.Object, "spec", "deletionPolicy")
	if handle != "projects/p/global/snapshots/s1" || refNS != "fn-ns" || policy != "Retain" {
		t.Errorf("content must point at the promote's handle, bind into fn-ns and retain: handle=%q ref=%q policy=%q", handle, refNS, policy)
	}
	pvc, err := kc.CoreV1().PersistentVolumeClaims("fn-ns").Get(ctx, "rox-"+ShortHash(xnsHash), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("rox clone missing in fn-ns: %v", err)
	}
	if pvc.Spec.DataSource == nil || pvc.Spec.DataSource.Name != snapName || pvc.Spec.Resources.Requests[corev1.ResourceStorage] != resource.MustParse("20Gi") {
		t.Errorf("clone must come from the namespaced snapshot at the promote's restore size: %+v", pvc.Spec)
	}
	// Per-pod-clone storage has no shared claim to reproduce.
	perPod := &SnapshotClonePromoter{KubeClient: kc, DynClient: dyn, StorageClass: "gp3", ReadOnlyMany: false}
	if err := perPod.EnsureClaim(ctx, xnsHash, "fn-ns"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("per-pod clone: want ErrUnsupported, got %v", err)
	}
}
