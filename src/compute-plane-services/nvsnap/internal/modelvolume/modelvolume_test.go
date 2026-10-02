// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package modelvolume

import (
	"context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// On a shared filesystem the lifecycle is the block one: the primary claim
// lives in the nvsnap namespace, Lookup reports its bound volume while the
// download runs, completion labels the retained PV and releases the claim.
func TestProvisioner_LookupAndComplete_RWX(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeRWX, StorageClass: "sc", Size: resource.MustParse("1Gi")}}
	sysNS := p.Cfg.SystemNamespace()
	if _, err := p.EnsureWriterClaim(ctx, uri, sysNS); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Lookup(ctx, uri); !st.Exists || st.Complete || st.InFlightPV != "" || st.ClaimNamespace != sysNS {
		t.Errorf("unbound in-flight claim: %+v", st)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-fs"}, Spec: corev1.PersistentVolumeSpec{
		PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "fss.csi.oraclecloud.com", VolumeHandle: "fs:ip:/export"}}}}
	if _, err := kc.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, ClaimName(uri), metav1.GetOptions{})
	pvc.Spec.VolumeName, pvc.Status.Phase = "pv-fs", corev1.ClaimBound
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Lookup(ctx, uri); st.Complete || st.InFlightPV != "pv-fs" {
		t.Errorf("a bound in-flight claim exposes its volume for early views: %+v", st)
	}
	if err := p.MarkComplete(ctx, uri, sysNS); err != nil {
		t.Fatal(err)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, ClaimName(uri), metav1.GetOptions{}); err == nil {
		t.Error("the writer claim is released on completion")
	}
	got, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pv-fs", metav1.GetOptions{})
	if got.Labels[CompleteLabel] != "true" || got.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("the primary PV is the artifact: %+v", got.ObjectMeta)
	}
	if st, _ := p.Lookup(ctx, uri); !st.Complete || st.PrimaryPV != "pv-fs" {
		t.Errorf("complete: %+v", st)
	}
	if err := p.MarkComplete(ctx, uri, sysNS); err != nil {
		t.Errorf("idempotent after release: %v", err)
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

// A block-mode claim is sized from the bytes that landed: measured plus
// ten percent headroom, whole GiB, never below the floor. A 1 GB model
// no longer reserves the 512Gi ceiling.
func TestConfig_VolumeSize(t *testing.T) {
	gib := int64(1) << 30
	cfg := Config{}
	for _, tc := range []struct {
		bytes int64
		want  string
	}{
		{999_604_126, "2Gi"}, // 0.93 GiB + 10% = 1.02 GiB -> 2Gi
		{100 * gib, "110Gi"}, // exact headroom
		{1, "1Gi"},           // floor without MinSize
		{290 * gib, "319Gi"}, // 70B class
	} {
		q := cfg.VolumeSize(tc.bytes)
		if got := q.String(); got != tc.want {
			t.Errorf("VolumeSize(%d) = %s, want %s", tc.bytes, got, tc.want)
		}
	}
	cfg.MinSize = resource.MustParse("8Gi")
	if q := cfg.VolumeSize(999_604_126); q.String() != "8Gi" {
		t.Errorf("MinSize floor: got %s", q.String())
	}
	if q := cfg.VolumeSize(100 * gib); q.String() != "110Gi" {
		t.Errorf("MinSize must not cap: got %s", q.String())
	}
	if (Config{}).SystemNamespace() != "nvsnap-system" || (Config{Namespace: "x"}).SystemNamespace() != "x" {
		t.Error("system namespace default")
	}
}

// Block mode stages into an emptyDir: no claim exists yet because its size
// is unknown until the download has run, and not every storage class can
// grow a volume. The Job outlives its success so the agent can copy, it
// names the staged volume, and it carries no service-account token.
func TestProvisioner_StagingJob(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeBlock, StorageClass: "sc"}}
	step := DownloadStep{
		Container: corev1.Container{Image: "img", Command: []string{"/bin/sh", "-c"}, Args: []string{"dl"},
			VolumeMounts: []corev1.VolumeMount{{Name: "models", MountPath: "/m"}, {Name: "kube-api-access-abc", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount"}, {Name: "secrets", MountPath: "/var/secrets"}}},
		VolumeName: "models",
		Volumes: []corev1.Volume{
			{Name: "kube-api-access-abc", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{}}},
			{Name: "secrets", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "fn"}}},
		},
	}
	if _, err := p.EnsureDownloadJob(ctx, uri, "fn", "", step); err != nil {
		t.Fatal(err)
	}
	job, err := kc.BatchV1().Jobs("fn").Get(ctx, JobName(uri), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ps := job.Spec.Template.Spec
	if ps.Volumes[0].Name != "models" || ps.Volumes[0].EmptyDir == nil || job.Annotations[StagingAnnotation] != "models" || job.Spec.Template.Annotations[StagingAnnotation] != "models" {
		t.Errorf("landing must be an emptyDir named by the staging annotation on Job and pod: %+v %v", ps.Volumes[0], job.Annotations)
	}
	// kubelet removes the emptyDir when the pod terminates, so the download
	// is an init container and a hold container keeps the pod Running until
	// the agent has copied and deletes the Job; a deadline bounds the wait.
	if len(ps.InitContainers) != 1 || ps.InitContainers[0].Name != DownloadContainer || len(ps.Containers) != 1 || ps.Containers[0].Name != HoldContainer || ps.Containers[0].Image != "img" {
		t.Fatalf("staging pod = download init + hold container: inits=%d containers=%v", len(ps.InitContainers), ps.Containers)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds < 3600 {
		t.Error("staging Job needs an active deadline as the safety net")
	}
	hold := ps.Containers[0]
	if hold.SecurityContext == nil || hold.SecurityContext.Capabilities == nil || hold.Resources.Limits.Cpu().IsZero() {
		t.Errorf("hold container must satisfy the baselines: %+v", hold)
	}
	if !StagingReady(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: []corev1.ContainerStatus{{Name: DownloadContainer, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}) ||
		StagingReady(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: []corev1.ContainerStatus{{Name: DownloadContainer, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}) ||
		StagingReady(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}) {
		t.Error("StagingReady: Running pod with download init exited 0, nothing else")
	}
	names := []string{}
	for _, v := range ps.Volumes {
		names = append(names, v.Name)
	}
	mounts := []string{}
	for _, m := range ps.InitContainers[0].VolumeMounts {
		mounts = append(mounts, m.Name)
	}
	if len(names) != 2 || names[1] != "secrets" || len(mounts) != 2 || mounts[1] != "secrets" {
		t.Errorf("projected SA token dropped, credentials kept: volumes=%v mounts=%v", names, mounts)
	}
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Error("staging creates no claim")
	}
	if err := p.DeleteJob(ctx, uri, "fn"); err != nil {
		t.Fatal(err)
	}
	if _, err := kc.BatchV1().Jobs("fn").Get(ctx, JobName(uri), metav1.GetOptions{}); err == nil {
		t.Error("job must be gone")
	}
	if err := p.DeleteJob(ctx, uri, "fn"); err != nil {
		t.Errorf("second delete is a no-op: %v", err)
	}
}

func TestProvisioner_SizedClaimAndWaitBound(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeBlock, StorageClass: "sc", Size: resource.MustParse("512Gi")}}
	name, err := p.EnsureSizedClaim(ctx, uri, "nvsnap-system", resource.MustParse("3Gi"))
	if err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, name, metav1.GetOptions{})
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "3Gi" {
		t.Errorf("sized claim requests %s, want 3Gi", got.String())
	}
	if _, err := p.EnsureSizedClaim(ctx, uri, "nvsnap-system", resource.MustParse("9Gi")); err != nil {
		t.Fatal(err)
	}
	again, _ := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, name, metav1.GetOptions{})
	if got := again.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "3Gi" {
		t.Errorf("an existing claim is kept as is, got %s", got.String())
	}
	if _, err := p.WaitBound(ctx, "nvsnap-system", name, 10*time.Millisecond); err == nil {
		t.Error("unbound claim must time out")
	}
	again.Spec.VolumeName = "pv-1"
	again.Status.Phase = corev1.ClaimBound
	if _, err := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Update(ctx, again, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if pv, err := p.WaitBound(ctx, "nvsnap-system", name, time.Second); err != nil || pv != "pv-1" {
		t.Errorf("bound: %q %v", pv, err)
	}
}

func TestProvisioner_FailureRecord(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeBlock, StorageClass: "sc"}}
	if st, _ := p.Lookup(ctx, uri); st.Failed {
		t.Error("no record yet")
	}
	if err := p.RecordFailure(ctx, uri, "copy failed"); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordFailure(ctx, uri, "again"); err != nil {
		t.Errorf("record is idempotent: %v", err)
	}
	st, err := p.Lookup(ctx, uri)
	if err != nil || !st.Failed {
		t.Errorf("fresh record marks the identity failed: %+v %v", st, err)
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, FailureRecordName(uri), metav1.GetOptions{})
	cm.Data["failedAt"] = time.Now().Add(-2 * FailureTTL).UTC().Format(time.RFC3339)
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Lookup(ctx, uri); st.Failed {
		t.Error("an expired record no longer holds pods back")
	}
	if err := p.ClearFailure(ctx, uri); err != nil {
		t.Fatal(err)
	}
	if err := p.ClearFailure(ctx, uri); err != nil {
		t.Errorf("clear is idempotent: %v", err)
	}
}

// The election is the Create itself: many callers for one identity see
// exactly one created, with no Get-then-Create gap.
func TestClaimSizedClaim_ExactlyOneCreator(t *testing.T) {
	kc := fake.NewSimpleClientset()
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeBlock, StorageClass: "sc", Namespace: "nvsnap-system", Kind: KindCache}}
	ctx := context.Background()
	var wg sync.WaitGroup
	var created int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, err := p.ClaimSizedClaim(ctx, "cache://abc", "nvsnap-system", resource.MustParse("8Gi"))
			if err != nil {
				t.Error(err)
			}
			if c {
				atomic.AddInt32(&created, 1)
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("created=%d, want exactly one winner", created)
	}
	if _, c, _ := p.ClaimSizedClaim(ctx, "cache://abc", "nvsnap-system", resource.MustParse("8Gi")); c {
		t.Error("an existing claim is never re-created")
	}
}

// Lookup serves the newest complete generation of a set and reports it;
// an unannotated primary is generation 1.
func TestLookup_ServesNewestGeneration(t *testing.T) {
	ctx := context.Background()
	uri := "cache://abc123"
	mk := func(name, gen string, age time.Duration) *corev1.PersistentVolume {
		pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			Labels: map[string]string{CacheLabel: Key(uri), CompleteLabel: "true"}, Annotations: map[string]string{}},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "d", VolumeHandle: name}}}}
		if gen != "" {
			pv.Annotations[GenerationAnnotation] = gen
			pv.Annotations[DeltaFingerprintAnnotation] = "fp-" + gen
		}
		return pv
	}
	kc := fake.NewSimpleClientset(mk("g1", "", 3*time.Hour), mk("g3", "3", time.Hour), mk("g2", "2", 2*time.Hour))
	p := &Provisioner{Kube: kc, Cfg: Config{Mode: ModeBlock, Kind: KindCache, Namespace: "nvsnap-system"}}
	st, err := p.Lookup(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Complete || st.PrimaryPV != "g3" || st.Generation != 3 || st.DeltaFingerprint != "fp-3" || st.RefreshStable {
		t.Errorf("newest generation served: %+v", st)
	}
	if err := p.MarkRefreshStable(ctx, "g3"); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Lookup(ctx, uri); !st.RefreshStable {
		t.Errorf("stable flag read back: %+v", st)
	}
	cc := Config{Kind: KindCache}
	if cc.GenerationClaimName(uri, 1) != cc.ClaimName(uri) || cc.GenerationClaimName(uri, 2) != cc.ClaimName(uri)+"-g2" {
		t.Error("generation 1 keeps the plain claim name; later ones carry a suffix")
	}
}

// A claim whose Create succeeds only because the previous winner just
// released it, after labelling the volume complete, must not start a
// second collection of the same set. The Create is re-checked against a
// complete volume at or past the claim's generation.
func TestClaimSizedClaim_VoidAfterCompletion(t *testing.T) {
	ctx := context.Background()
	uri := "cache://abc"
	cfg := Config{Mode: ModeBlock, StorageClass: "sc", Namespace: "nvsnap-system", Kind: KindCache}
	complete := func(name string, gen string) *corev1.PersistentVolume {
		pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name,
			Labels: map[string]string{cfg.Label(): Key(uri), CompleteLabel: "true"}, Annotations: map[string]string{}},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "c:v:" + name}}},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
		if gen != "" {
			pv.Annotations[GenerationAnnotation] = gen
		}
		return pv
	}
	kc := fake.NewSimpleClientset(complete("pv-gen1", ""))
	p := &Provisioner{Kube: kc, Cfg: cfg}
	if _, created, err := p.ClaimSizedClaim(ctx, uri, "nvsnap-system", resource.MustParse("2Gi")); err != nil || created {
		t.Fatalf("a complete set voids the election: created=%v err=%v", created, err)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, cfg.ClaimName(uri), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the void claim must be released, got %v", err)
	}
	// A later generation is new work: generation 1 being complete does not
	// void a claim for generation 2.
	if _, created, err := p.ClaimSizedClaimNamed(ctx, cfg.GenerationClaimName(uri, 2), uri, "nvsnap-system", resource.MustParse("2Gi")); err != nil || !created {
		t.Fatalf("generation 2 is not done yet: created=%v err=%v", created, err)
	}
	if cfg.claimGeneration(uri, cfg.GenerationClaimName(uri, 3)) != 3 || cfg.claimGeneration(uri, cfg.ClaimName(uri)) != 1 {
		t.Fatal("claimGeneration must invert GenerationClaimName")
	}
}

// The byte count a writer leaves in the marker is recorded on the primary
// and served by Lookup in place of the nominal capacity.
func TestProvisioner_RecordBytes(t *testing.T) {
	ctx := context.Background()
	uri := "cache://sized"
	cfg := Config{Mode: ModeRWX, StorageClass: "sc", Namespace: "nvsnap-system", Kind: KindCache}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-sized",
		Labels: map[string]string{cfg.Label(): Key(uri), CompleteLabel: "true"}},
		Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Ti")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "fss", VolumeHandle: "h"}}}}
	kc := fake.NewSimpleClientset(pv)
	p := &Provisioner{Kube: kc, Cfg: cfg}
	if st, _ := p.Lookup(ctx, uri); st.PrimaryBytes != 4<<40 {
		t.Fatalf("without a record the capacity is the size: %+v", st)
	}
	if err := p.RecordBytes(ctx, "pv-sized", 65549091410); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Lookup(ctx, uri); st.PrimaryBytes != 65549091410 {
		t.Errorf("the recorded bytes win over the capacity: %+v", st)
	}
	if MarkerBytes([]byte("65549091410\n")) != 65549091410 || MarkerBytes(nil) != 0 || MarkerBytes([]byte("x")) != 0 || MarkerBytes([]byte("-5")) != 0 {
		t.Error("MarkerBytes parses a count and nothing else")
	}
	if err := p.RecordBytes(ctx, "pv-sized", 0); err != nil {
		t.Error("a zero count is a no-op, not an error")
	}
}
