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

func TestReaper_OrphanReadOnlyClaims(t *testing.T) {
	ctx := context.Background()
	key := Key(uri)
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	young := metav1.NewTime(time.Now().Add(-time.Minute))
	roLabels := func() map[string]string {
		return map[string]string{"nvsnap.io/role": "reader", IdentityLabel: key, "app.kubernetes.io/managed-by": "nvsnap"}
	}
	mk := func(ns, name string, ts metav1.Time) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: roLabels(), CreationTimestamp: ts}}
	}
	reader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "sr-live"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "m", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ro-used"}}}}}}
	holder := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nvsnap-model-holder-x", Namespace: "nvsnap-system", Labels: map[string]string{"app.kubernetes.io/component": "mount-holder"}},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "m", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ro-holder-only"}}}}}}
	kc := fake.NewSimpleClientset(reader, holder,
		mk("sr-live", "ro-used", old),              // referenced by a reader: keep
		mk("sr-live", "ro-idle", old),              // nobody references it: delete
		mk("sr-live", "ro-young", young),           // too young to judge: keep
		mk("nvsnap-system", "ro-holder-only", old), // only a mount-holder holds it: delete (holder is owned by it)
	)
	res, err := (&Reaper{Kube: kc}).Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.OrphanClaims != 2 {
		t.Errorf("orphan claims removed: %d", res.OrphanClaims)
	}
	left := map[string]bool{}
	claims, _ := kc.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	for _, c := range claims.Items {
		left[c.Name] = true
	}
	if !left["ro-used"] || !left["ro-young"] || left["ro-idle"] || left["ro-holder-only"] {
		t.Errorf("claims left: %v", left)
	}
	// The holder is deleted explicitly: pvc-protection keeps the claim
	// Terminating while a pod mounts it, and owner GC waits for the claim.
	if _, err := kc.CoreV1().Pods("nvsnap-system").Get(ctx, "nvsnap-model-holder-x", metav1.GetOptions{}); err == nil {
		t.Error("mount-holder must be deleted with its orphan claim")
	}
	if _, err := kc.CoreV1().Pods("sr-live").Get(ctx, "r", metav1.GetOptions{}); err != nil {
		t.Error("reader pods are never touched by the claim reaper")
	}
}

// Retention: a complete primary is retired with its storage once nothing
// has admitted against it for Retention and no reader is bound in a live
// namespace. Its read-only views go with it. Recently used, still read,
// or Retention zero: kept. Caches follow the same rule as models.
func TestReaper_RetiresCompletePrimariesPastRetention(t *testing.T) {
	ctx := context.Background()
	key := Key(uri)
	complete := map[string]string{IdentityLabel: key, CompleteLabel: "true"}
	ro := map[string]string{IdentityLabel: key, "nvsnap.io/role": "reader-shared"}
	cacheKey := "abc123def4567890"
	cacheComplete := map[string]string{CacheLabel: cacheKey, CompleteLabel: "true"}
	live := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sr-live"}}
	stamp := func(pv *corev1.PersistentVolume, ago time.Duration) *corev1.PersistentVolume {
		pv.Annotations = map[string]string{LastUsedAnnotation: time.Now().Add(-ago).UTC().Format(time.RFC3339)}
		return pv
	}
	oldUnused := stamp(pvFixture("primary-old-unused", complete, "nvsnap-system", ClaimName(uri), corev1.VolumeReleased, false, 30*24*time.Hour), 8*24*time.Hour)
	oldRO := pvFixture("primary-old-unused-ro-released", ro, "sr-gone", ClaimName(uri)+"-ro", corev1.VolumeReleased, true, 20*24*time.Hour)
	recent := stamp(pvFixture("primary-recent", map[string]string{IdentityLabel: Key("hf://o/other"), CompleteLabel: "true"}, "nvsnap-system", ClaimName("hf://o/other"), corev1.VolumeReleased, false, 30*24*time.Hour), 2*24*time.Hour)
	stillRead := stamp(pvFixture("primary-still-read", map[string]string{IdentityLabel: Key("hf://o/read"), CompleteLabel: "true"}, "nvsnap-system", ClaimName("hf://o/read"), corev1.VolumeReleased, false, 30*24*time.Hour), 9*24*time.Hour)
	stillReadRO := pvFixture("primary-still-read-ro", map[string]string{IdentityLabel: Key("hf://o/read"), "nvsnap.io/role": "reader-shared"}, "sr-live", ClaimName("hf://o/read")+"-ro", corev1.VolumeBound, true, time.Hour)
	noStamp := pvFixture("primary-no-stamp-old", map[string]string{IdentityLabel: Key("hf://o/legacy"), CompleteLabel: "true"}, "nvsnap-system", ClaimName("hf://o/legacy"), corev1.VolumeReleased, false, 10*24*time.Hour)
	cacheOld := stamp(pvFixture("cache-old-unused", cacheComplete, "nvsnap-system", "nvsnap-cache-"+cacheKey, corev1.VolumeReleased, false, 30*24*time.Hour), 8*24*time.Hour)
	kc := fake.NewSimpleClientset(live, oldUnused, oldRO, recent, stillRead, stillReadRO, noStamp, cacheOld)

	r := &Reaper{Kube: kc, Retention: 7 * 24 * time.Hour}
	res, err := r.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.RetiredPrimaries != 3 {
		t.Errorf("retired %d primaries, want 3 (old unused model, legacy without stamp, old unused cache): %+v", res.RetiredPrimaries, res)
	}
	left := map[string]bool{}
	pvs, _ := kc.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	for _, pv := range pvs.Items {
		left[pv.Name] = true
	}
	for _, gone := range []string{"primary-old-unused", "primary-old-unused-ro-released", "primary-no-stamp-old", "cache-old-unused"} {
		if left[gone] {
			t.Errorf("%s must be retired", gone)
		}
	}
	for _, want := range []string{"primary-recent", "primary-still-read", "primary-still-read-ro"} {
		if !left[want] {
			t.Errorf("%s must survive: recently used or still read", want)
		}
	}

	// Retention zero keeps everything, whatever its age.
	kc2 := fake.NewSimpleClientset(stamp(pvFixture("primary-forever", complete, "nvsnap-system", ClaimName(uri), corev1.VolumeReleased, false, 400*24*time.Hour), 300*24*time.Hour))
	if res, err := (&Reaper{Kube: kc2}).Sweep(ctx); err != nil || res.RetiredPrimaries != 0 {
		t.Errorf("Retention 0 must never retire: %+v %v", res, err)
	}
	if _, err := kc2.CoreV1().PersistentVolumes().Get(ctx, "primary-forever", metav1.GetOptions{}); err != nil {
		t.Error("primary-forever must survive with Retention 0")
	}
}

// Generations of one set are judged apart: a view that names its source
// keeps only that primary alive, so a superseded generation retires while
// the serving one, bound through a labelled view, survives.
func TestReaper_SupersededGenerationRetiresWhileServingOneIsRead(t *testing.T) {
	ctx := context.Background()
	key := "abc123def4567890"
	complete := map[string]string{CacheLabel: key, CompleteLabel: "true"}
	live := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sr-live"}}
	old := pvFixture("set-g1", complete, "nvsnap-system", "nvsnap-cache-"+key, corev1.VolumeReleased, false, 30*24*time.Hour)
	old.Annotations = map[string]string{LastUsedAnnotation: time.Now().Add(-9 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	newer := pvFixture("set-g2", complete, "nvsnap-system", "nvsnap-cache-"+key+"-g2", corev1.VolumeReleased, false, 20*24*time.Hour)
	newer.Annotations = map[string]string{LastUsedAnnotation: time.Now().Add(-9 * 24 * time.Hour).UTC().Format(time.RFC3339), GenerationAnnotation: "2"}
	view := pvFixture("set-g2-ro", map[string]string{CacheLabel: key, "nvsnap.io/role": "reader-shared", SourcePVLabel: "set-g2"}, "sr-live", "nvsnap-cache-"+key+"-ro", corev1.VolumeBound, true, time.Hour)
	kc := fake.NewSimpleClientset(live, old, newer, view)
	r := &Reaper{Kube: kc, Retention: 7 * 24 * time.Hour}
	res, err := r.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.RetiredPrimaries != 1 {
		t.Errorf("retired %d, want 1 (the superseded generation): %+v", res.RetiredPrimaries, res)
	}
	if _, err := kc.CoreV1().PersistentVolumes().Get(ctx, "set-g1", metav1.GetOptions{}); err == nil {
		t.Error("generation 1 has no bound view and is past retention: retired")
	}
	for _, keep := range []string{"set-g2", "set-g2-ro"} {
		if _, err := kc.CoreV1().PersistentVolumes().Get(ctx, keep, metav1.GetOptions{}); err != nil {
			t.Errorf("%s must survive: a live namespace reads generation 2", keep)
		}
	}
}

// A superseded generation retires an hour after its last use once no
// reader is bound, long before retention; the serving generation stays.
func TestReaper_SupersededGenerationRetiresAfterGrace(t *testing.T) {
	ctx := context.Background()
	key := "abc123def4567890"
	complete := map[string]string{CacheLabel: key, CompleteLabel: "true"}
	old := pvFixture("set-g1", complete, "nvsnap-system", "nvsnap-cache-"+key, corev1.VolumeReleased, false, 3*time.Hour)
	old.Annotations = map[string]string{LastUsedAnnotation: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)}
	newer := pvFixture("set-g2", complete, "nvsnap-system", "nvsnap-cache-"+key+"-g2", corev1.VolumeReleased, false, 90*time.Minute)
	newer.Annotations = map[string]string{LastUsedAnnotation: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339), GenerationAnnotation: "2"}
	kc := fake.NewSimpleClientset(old, newer)
	r := &Reaper{Kube: kc, Retention: 7 * 24 * time.Hour}
	res, err := r.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.RetiredPrimaries != 1 {
		t.Errorf("retired %d, want 1 (the superseded generation past its grace): %+v", res.RetiredPrimaries, res)
	}
	if _, err := kc.CoreV1().PersistentVolumes().Get(ctx, "set-g1", metav1.GetOptions{}); err == nil {
		t.Error("superseded generation 1 retires after the grace")
	}
	if _, err := kc.CoreV1().PersistentVolumes().Get(ctx, "set-g2", metav1.GetOptions{}); err != nil {
		t.Error("the serving generation stays under retention")
	}
	// Inside the grace it stays.
	fresh := pvFixture("set-g1", complete, "nvsnap-system", "nvsnap-cache-"+key, corev1.VolumeReleased, false, 3*time.Hour)
	fresh.Annotations = map[string]string{LastUsedAnnotation: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)}
	kc2 := fake.NewSimpleClientset(fresh, newer.DeepCopy())
	if res, _ := (&Reaper{Kube: kc2, Retention: 7 * 24 * time.Hour}).Sweep(ctx); res.RetiredPrimaries != 0 {
		t.Errorf("inside the grace nothing retires: %+v", res)
	}
}
