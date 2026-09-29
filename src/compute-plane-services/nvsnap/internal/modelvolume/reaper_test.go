// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package modelvolume

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type (
	runtimeObject = runtime.Object
	updateAction  = k8stesting.UpdateAction
)

func pvFixture(name string, labels map[string]string, claimNS, claimName string, phase corev1.PersistentVolumePhase, readOnly bool, age time.Duration) *corev1.PersistentVolume {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, CreationTimestamp: metav1.NewTime(time.Now().Add(-age))},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "h", ReadOnly: readOnly}},
		},
		Status: corev1.PersistentVolumeStatus{Phase: phase},
	}
	if claimName != "" {
		pv.Spec.ClaimRef = &corev1.ObjectReference{Namespace: claimNS, Name: claimName}
	}
	return pv
}

func TestReaper_Sweep(t *testing.T) {
	ctx := context.Background()
	key := Key(uri)
	ro := map[string]string{IdentityLabel: key, "nvsnap.io/role": "reader-shared"}
	complete := map[string]string{IdentityLabel: key, CompleteLabel: "true"}
	incomplete := map[string]string{IdentityLabel: key}
	live := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sr-live"}}
	kc := fake.NewSimpleClientset(live,
		// Read-only PVs: Released, Bound in a dead namespace, Bound in a live one.
		pvFixture("ro-released", ro, "sr-gone", ClaimName(uri)+"-ro", corev1.VolumeReleased, true, time.Hour),
		pvFixture("ro-dead-ns", ro, "sr-dead", ClaimName(uri)+"-ro", corev1.VolumeBound, true, time.Hour),
		pvFixture("ro-live", ro, "sr-live", ClaimName(uri)+"-ro", corev1.VolumeBound, true, time.Hour),
		// Primaries: complete and Released (the normal artifact), incomplete
		// Released old (abandoned copy), incomplete Released young (in flight).
		pvFixture("primary-complete", complete, "nvsnap-system", ClaimName(uri), corev1.VolumeReleased, false, time.Hour),
		pvFixture("primary-abandoned", incomplete, "nvsnap-system", ClaimName(uri), corev1.VolumeReleased, false, time.Hour),
		pvFixture("primary-young", incomplete, "nvsnap-system", ClaimName(uri), corev1.VolumeReleased, false, time.Minute),
		// Unlabelled: a provisioner PV whose claim died before completion,
		// and an unrelated Released PV.
		pvFixture("pvc-unlabelled-abandoned", nil, "sr-gone", ClaimName(uri), corev1.VolumeReleased, false, time.Hour),
		pvFixture("pvc-unrelated", nil, "other", "data", corev1.VolumeReleased, false, time.Hour),
		// Checkpoint ROX secondaries carry the role label but no identity;
		// they are not this reaper's business.
		pvFixture("nvsnap-ro-pv-x", map[string]string{"nvsnap.io/role": "reader-shared"}, "nvsnap-system", "rox-x", corev1.VolumeReleased, true, time.Hour),
		// Released but still attached to a node: deleting it now would wedge
		// behind the attacher finalizer; wait for the detach.
		pvFixture("ro-released-attached", ro, "sr-gone", ClaimName(uri)+"-ro", corev1.VolumeReleased, true, time.Hour),
		&storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "va-1"}, Spec: storagev1.VolumeAttachmentSpec{
			Attacher: "nvmesh-csi.excelero.com", NodeName: "node-a", Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: ptr("ro-released-attached")}}},
	)
	r := &Reaper{Kube: kc}
	res, err := r.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.ReadOnlyPVs != 2 || res.AbandonedPrimaries != 2 {
		t.Errorf("result %+v", res)
	}
	left := map[string]bool{}
	pvs, _ := kc.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	for _, pv := range pvs.Items {
		left[pv.Name] = true
	}
	for _, want := range []string{"ro-live", "primary-complete", "primary-young", "pvc-unrelated", "nvsnap-ro-pv-x", "ro-released-attached"} {
		if !left[want] {
			t.Errorf("%s must survive", want)
		}
	}
	for _, gone := range []string{"ro-released", "ro-dead-ns", "primary-abandoned", "pvc-unlabelled-abandoned"} {
		if left[gone] {
			t.Errorf("%s must be removed", gone)
		}
	}
	// Abandoned primaries go with their storage: the reclaim policy was
	// flipped before the delete. The fake keeps no history, so check the
	// update path directly on a fresh object.
	pv := pvFixture("primary-abandoned-2", incomplete, "nvsnap-system", ClaimName(uri), corev1.VolumeReleased, false, time.Hour)
	kc2 := fake.NewSimpleClientset(pv)
	var policies []corev1.PersistentVolumeReclaimPolicy
	kc2.PrependReactor("update", "persistentvolumes", func(action k8stesting.Action) (bool, runtimeObject, error) {
		obj := action.(updateAction).GetObject().(*corev1.PersistentVolume)
		policies = append(policies, obj.Spec.PersistentVolumeReclaimPolicy)
		return false, nil, nil
	})
	if _, err := (&Reaper{Kube: kc2}).Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0] != corev1.PersistentVolumeReclaimDelete {
		t.Errorf("abandoned primary must be switched to reclaim Delete before removal, saw %v", policies)
	}
	if _, err := (&Reaper{Kube: kc}).Sweep(ctx); err != nil {
		t.Errorf("second sweep is a no-op: %v", err)
	}
}

func ptr(s string) *string { return &s }
