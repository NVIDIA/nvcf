// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package modelvolume

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const uri = "hf://Qwen/Qwen2.5-32B-Instruct"

func TestProvisioner_WriterClaimModes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		mode Mode
		want corev1.PersistentVolumeAccessMode
	}{{ModeRWX, corev1.ReadWriteMany}, {ModeBlock, corev1.ReadWriteOnce}} {
		kc := fake.NewSimpleClientset()
		p := &Provisioner{Kube: kc, Cfg: Config{Mode: tc.mode, StorageClass: "sc", Size: resource.MustParse("512Gi")}}
		name, err := p.EnsureWriterClaim(ctx, uri, "fn")
		if err != nil || name != ClaimName(uri) {
			t.Fatalf("%s: %v %q", tc.mode, err, name)
		}
		pvc, err := kc.CoreV1().PersistentVolumeClaims("fn").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if pvc.Spec.AccessModes[0] != tc.want || *pvc.Spec.StorageClassName != "sc" || pvc.Labels[IdentityLabel] != Key(uri) || pvc.Annotations[IdentityAnnotation] != uri {
			t.Errorf("%s: claim %+v", tc.mode, pvc)
		}
		if _, err := p.EnsureWriterClaim(ctx, uri, "fn"); err != nil {
			t.Errorf("%s: second call must be a no-op: %v", tc.mode, err)
		}
	}
}

func TestProvisioner_LookupAndComplete_Block(t *testing.T) {
	ctx := context.Background()
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-model"}, Spec: corev1.PersistentVolumeSpec{
		PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "c:v:fn-a"}}}}
	kc := fake.NewSimpleClientset(pv)
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeBlock, StorageClass: "sc", Size: resource.MustParse("1Gi")}}
	if st, err := p.Lookup(ctx, uri); err != nil || st.Exists || st.Complete {
		t.Errorf("nothing yet: %+v %v", st, err)
	}
	if _, err := p.EnsureWriterClaim(ctx, uri, "fn-a"); err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims("fn-a").Get(ctx, ClaimName(uri), metav1.GetOptions{})
	pvc.Spec.VolumeName = "pv-model"
	if _, err := kc.CoreV1().PersistentVolumeClaims("fn-a").Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, err := p.Lookup(ctx, uri); err != nil || !st.Exists || st.Complete || st.ClaimNamespace != "fn-a" {
		t.Errorf("in flight: %+v %v", st, err)
	}
	if err := p.MarkComplete(ctx, uri, "fn-a"); err != nil {
		t.Fatal(err)
	}
	// Block mode: the claim is released so the volume detaches; the retained
	// PV carries the identity and completion.
	if _, err := kc.CoreV1().PersistentVolumeClaims("fn-a").Get(ctx, ClaimName(uri), metav1.GetOptions{}); err == nil {
		t.Error("writer claim must be released after completion on block storage")
	}
	got, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pv-model", metav1.GetOptions{})
	if got.Labels[CompleteLabel] != "true" || got.Labels[IdentityLabel] != Key(uri) || got.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain || got.Annotations[IdentityAnnotation] != uri {
		t.Errorf("primary PV must be labelled complete and retained: %+v", got.ObjectMeta)
	}
	st, _ := p.Lookup(ctx, uri)
	if !st.Complete || st.PrimaryPV != "pv-model" {
		t.Errorf("after MarkComplete: %+v", st)
	}
	if err := p.MarkComplete(ctx, uri, "fn-a"); err != nil {
		t.Errorf("second MarkComplete (claim gone, PV complete) must be a no-op: %v", err)
	}
	if st, _ := p.Lookup(ctx, "hf://other/model"); st.Exists {
		t.Error("lookup must be per identity")
	}
	if len(Key(uri)) != 16 || ClaimName(uri) != "nvsnap-model-"+Key(uri) || ReadOnlyClaimName(uri) != ClaimName(uri)+"-ro" {
		t.Errorf("names: %s %s", ClaimName(uri), ReadOnlyClaimName(uri))
	}
	if d, _ := p.Detached(ctx, "pv-model"); !d {
		t.Error("no VolumeAttachment means detached")
	}
}

func TestProvisioner_LookupAndComplete_RWX(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeRWX, StorageClass: "sc", Size: resource.MustParse("1Gi")}}
	if _, err := p.EnsureWriterClaim(ctx, uri, "fn-a"); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkComplete(ctx, uri, "fn-a"); err != nil {
		t.Fatal(err)
	}
	pvc, err := kc.CoreV1().PersistentVolumeClaims("fn-a").Get(ctx, ClaimName(uri), metav1.GetOptions{})
	if err != nil || pvc.Labels[CompleteLabel] != "true" {
		t.Errorf("RWX keeps the shared claim and labels it: %v %v", err, pvc.Labels)
	}
	if st, _ := p.Lookup(ctx, uri); !st.Complete {
		t.Errorf("RWX complete: %+v", st)
	}
}

func TestProvisioner_DownloadJobIdempotent(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeBlock, StorageClass: "sc", Size: resource.MustParse("1Gi")}}
	step := DownloadStep{
		Container:        corev1.Container{Image: "vllm/vllm-openai", Command: []string{"/bin/sh", "-c"}, Args: []string{"hf download x && touch /m/.nvsnap-complete"}, VolumeMounts: []corev1.VolumeMount{{Name: "models", MountPath: "/m"}}},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pull"}},
		Tolerations:      []corev1.Toleration{{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists}},
		VolumeName:       "models",
	}
	name, err := p.EnsureDownloadJob(ctx, uri, "fn", ClaimName(uri), step)
	if err != nil || name != JobName(uri) {
		t.Fatalf("%v %q", err, name)
	}
	job, err := kc.BatchV1().Jobs("fn").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished > 60 {
		t.Error("the Job must remove its pod soon after success; a lingering Succeeded pod keeps the volume attached")
	}
	ps := job.Spec.Template.Spec
	if ps.RestartPolicy != corev1.RestartPolicyOnFailure || ps.ImagePullSecrets[0].Name != "pull" || len(ps.Tolerations) != 1 || ps.Volumes[0].PersistentVolumeClaim.ClaimName != ClaimName(uri) || ps.Containers[0].VolumeMounts[0].MountPath != "/m" || job.Annotations[IdentityAnnotation] != uri {
		t.Errorf("job spec: %+v", ps)
	}
	if _, err := p.EnsureDownloadJob(ctx, uri, "fn", ClaimName(uri), step); err != nil {
		t.Errorf("second EnsureDownloadJob must be a no-op: %v", err)
	}
	if ok, _ := p.JobSucceeded(ctx, uri, "fn"); ok {
		t.Error("job has not succeeded yet")
	}
	job.Status.Succeeded = 1
	if _, err := kc.BatchV1().Jobs("fn").UpdateStatus(ctx, job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.JobSucceeded(ctx, uri, "fn"); !ok {
		t.Error("succeeded job must report true")
	}
}
