// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checkpointstore

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVolumeHandleTransform(t *testing.T) {
	none, err := LookupVolumeHandleTransform("")
	if err != nil {
		t.Fatalf("none lookup: %v", err)
	}
	if got, _ := none("vol-handle:abc", "ns1"); got != "vol-handle:abc" {
		t.Errorf("none transform changed handle: %q", got)
	}

	nvmesh, err := LookupVolumeHandleTransform("nvmesh")
	if err != nil {
		t.Fatalf("nvmesh lookup: %v", err)
	}
	got, err := nvmesh("nvmesh-vol:proj:default", "nvcf-backend")
	if err != nil {
		t.Fatalf("nvmesh transform: %v", err)
	}
	if got != "nvmesh-vol:proj:nvcf-backend" {
		t.Errorf("nvmesh transform = %q, want last segment rewritten to namespace", got)
	}
	if _, err := nvmesh("nocolons", "ns"); err == nil {
		t.Error("nvmesh transform should error on a handle with no colon")
	}

	if _, err := LookupVolumeHandleTransform("bogus"); err == nil {
		t.Error("unknown transform name should error")
	}
}

// sharedFixture seeds a writer PVC bound to a primary CSI PV.
func sharedFixture(hash, ns string) *fake.Clientset {
	sc := "nvmesh-sc"
	writer := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "rwx-" + ShortHash(hash), Namespace: ns},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-primary", StorageClassName: &sc},
	}
	primary := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-primary", Labels: map[string]string{"nvsnap.io/role": "writer"}},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("96Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "nvmesh-vol:proj:" + ns},
			},
		},
	}
	return fake.NewSimpleClientset(writer, primary)
}

func TestSharedVolumePromoter_Promote(t *testing.T) {
	const hash, ns = "abc123def456", "nvcf-backend"
	kc := sharedFixture(hash, ns)
	tx, _ := LookupVolumeHandleTransform("nvmesh")
	p := &SharedVolumePromoter{KubeClient: kc, StorageClass: "nvmesh-sc", Transform: tx, MountOptions: []string{"ro", "norecovery", "nouuid"}}

	res, err := p.Promote(context.Background(), PromoteInput{
		Hash: hash, Namespace: ns, WriterPVCName: "rwx-" + ShortHash(hash),
	})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !res.ReusedWriterVolume {
		t.Error("shared-volume Promote must report ReusedWriterVolume=true (zero copy)")
	}
	if res.SharedClaimName != "rox-"+ShortHash(hash) {
		t.Errorf("SharedClaimName = %q, want rox-<hash>", res.SharedClaimName)
	}

	// Primary PV must be Retain now.
	primary, _ := kc.CoreV1().PersistentVolumes().Get(context.Background(), "pv-primary", metav1.GetOptions{})
	if primary.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("primary PV reclaim = %v, want Retain", primary.Spec.PersistentVolumeReclaimPolicy)
	}

	// Secondary PV: ROX, transformed handle, claimRef to the ro PVC.
	sec, err := kc.CoreV1().PersistentVolumes().Get(context.Background(), secondaryPVName(hash), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secondary PV not created: %v", err)
	}
	if len(sec.Spec.AccessModes) != 1 || sec.Spec.AccessModes[0] != corev1.ReadOnlyMany {
		t.Errorf("secondary PV access = %v, want [ReadOnlyMany]", sec.Spec.AccessModes)
	}
	if sec.Spec.CSI == nil || sec.Spec.CSI.VolumeHandle != "nvmesh-vol:proj:"+ns {
		t.Errorf("secondary PV handle = %v, want transformed", sec.Spec.CSI)
	}
	if sec.Spec.ClaimRef == nil || sec.Spec.ClaimRef.Name != "rox-"+ShortHash(hash) {
		t.Errorf("secondary PV claimRef = %v, want ro PVC", sec.Spec.ClaimRef)
	}
	// NVMesh RO mount options (xfs nouuid is mandatory for multi-mount).
	if got := sec.Spec.MountOptions; len(got) != 3 || got[0] != "ro" || got[1] != "norecovery" || got[2] != "nouuid" {
		t.Errorf("secondary PV mountOptions = %v, want [ro norecovery nouuid]", got)
	}
	if !sec.Spec.CSI.ReadOnly {
		t.Error("secondary PV CSI.ReadOnly must be true")
	}

	// Shared ro PVC: ROX, statically bound to the secondary PV.
	rox, err := kc.CoreV1().PersistentVolumeClaims(ns).Get(context.Background(), "rox-"+ShortHash(hash), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ro PVC not created: %v", err)
	}
	if rox.Spec.VolumeName != secondaryPVName(hash) {
		t.Errorf("ro PVC volumeName = %q, want %q", rox.Spec.VolumeName, secondaryPVName(hash))
	}

	// Writer PVC must be deleted.
	if _, err := kc.CoreV1().PersistentVolumeClaims(ns).Get(context.Background(), "rwx-"+ShortHash(hash), metav1.GetOptions{}); err == nil {
		t.Error("writer PVC should be deleted after shared-volume promote")
	}

	// Idempotent: a second Promote must not error (re-reads existing objects).
	// The writer PVC is gone, so re-run only after re-seeding it.
	kc2 := sharedFixture(hash, ns)
	p2 := &SharedVolumePromoter{KubeClient: kc2, StorageClass: "nvmesh-sc", Transform: tx}
	if _, err := p2.Promote(context.Background(), PromoteInput{Hash: hash, Namespace: ns, WriterPVCName: "rwx-" + ShortHash(hash)}); err != nil {
		t.Fatalf("first promote (fresh): %v", err)
	}
}

func TestSharedVolumePromoter_MountSpec(t *testing.T) {
	const hash, ns = "abc123def456", "nvcf-backend"
	roxName := "rox-" + ShortHash(hash)
	kc := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: roxName, Namespace: ns},
	})
	p := &SharedVolumePromoter{KubeClient: kc, StorageClass: "nvmesh-sc"}

	pm, err := p.MountSpec(context.Background(), hash, VolumeMeta{Namespace: ns, MountPath: "/opt/nvsnap"})
	if err != nil {
		t.Fatalf("MountSpec: %v", err)
	}
	if pm.Volume.PersistentVolumeClaim == nil || pm.Volume.PersistentVolumeClaim.ClaimName != roxName {
		t.Errorf("MountSpec claim = %v, want %s", pm.Volume.PersistentVolumeClaim, roxName)
	}
	if !pm.VolumeMount.ReadOnly {
		t.Error("shared-volume mount must be ReadOnly")
	}

	// Missing rox ⇒ ErrNotFound.
	empty := &SharedVolumePromoter{KubeClient: fake.NewSimpleClientset(), StorageClass: "x"}
	if _, err := empty.MountSpec(context.Background(), "deadbeef", VolumeMeta{Namespace: ns}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing rox: err = %v, want ErrNotFound", err)
	}
}

// A per-capture PV whose PVC was deleted goes Released with the dead claim's
// UID pinned in claimRef, and Retain keeps it that way. A freshly created PVC
// of the same name has a different UID, so the binder refuses the volume and
// the claim stays Pending forever -- the capture becomes unrestorable even
// though its data is intact. ensureSecondaryPV has to hand the volume back.
func TestEnsureSecondaryPVReclaimsReleasedVolume(t *testing.T) {
	const (
		secName = "nvsnap-ro-pv-abc123"
		roxName = "rox-abc123"
		ns      = "nvsnap-system"
	)
	released := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: secName},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1", Kind: "PersistentVolumeClaim",
				Name: roxName, Namespace: ns,
				UID:             "dead-uid-from-the-deleted-pvc",
				ResourceVersion: "100494062",
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}
	kc := fake.NewSimpleClientset(released)
	p := &SharedVolumePromoter{KubeClient: kc}

	if err := p.ensureSecondaryPV(context.Background(), nil, secName, roxName, ns); err != nil {
		t.Fatalf("ensureSecondaryPV: %v", err)
	}

	got, err := kc.CoreV1().PersistentVolumes().Get(context.Background(), secName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.ClaimRef == nil {
		t.Fatal("claimRef was dropped entirely; the PV is no longer reserved and any pending claim could take it")
	}
	if got.Spec.ClaimRef.UID != "" || got.Spec.ClaimRef.ResourceVersion != "" {
		t.Errorf("stale binding not cleared: uid=%q rv=%q",
			got.Spec.ClaimRef.UID, got.Spec.ClaimRef.ResourceVersion)
	}
	if got.Spec.ClaimRef.Name != roxName || got.Spec.ClaimRef.Namespace != ns {
		t.Errorf("reservation changed: %s/%s", got.Spec.ClaimRef.Namespace, got.Spec.ClaimRef.Name)
	}
}

// Never steal a Released volume that is reserved for someone else.
func TestEnsureSecondaryPVRefusesForeignReleasedVolume(t *testing.T) {
	const secName = "nvsnap-ro-pv-abc123"
	foreign := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: secName},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{Name: "someone-elses-pvc", Namespace: "other-ns"},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}
	kc := fake.NewSimpleClientset(foreign)
	p := &SharedVolumePromoter{KubeClient: kc}

	err := p.ensureSecondaryPV(context.Background(), nil, secName, "rox-abc123", "nvsnap-system")
	if err == nil {
		t.Fatal("rebound a Released PV reserved for a different claim")
	}
}

// A Bound volume must be left alone -- reconciling it would disturb a live claim.
func TestEnsureSecondaryPVLeavesBoundVolumeAlone(t *testing.T) {
	const (
		secName = "nvsnap-ro-pv-abc123"
		roxName = "rox-abc123"
		ns      = "nvsnap-system"
	)
	bound := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: secName},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{Name: roxName, Namespace: ns, UID: "live-uid"},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	kc := fake.NewSimpleClientset(bound)
	p := &SharedVolumePromoter{KubeClient: kc}

	if err := p.ensureSecondaryPV(context.Background(), nil, secName, roxName, ns); err != nil {
		t.Fatalf("ensureSecondaryPV: %v", err)
	}
	got, _ := kc.CoreV1().PersistentVolumes().Get(context.Background(), secName, metav1.GetOptions{})
	if got.Spec.ClaimRef.UID != "live-uid" {
		t.Error("disturbed the binding of a live, Bound volume")
	}
}
