// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tarstream"
	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

const mvURI = "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"

// writerFixture: the writer claim bound to an NVMesh PV, as after the
// webhook created it and the CSI provisioner bound it.
func writerFixture(t *testing.T) (*fake.Clientset, *modelvolume.Provisioner) {
	t.Helper()
	sc := "nvcf-sc"
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-abc"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("512Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "cluster:csi-abc:vol:sr-fn"}},
		},
	}
	kc := fake.NewSimpleClientset(pv)
	p := &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: modelvolume.ModeBlock, StorageClass: sc, Size: resource.MustParse("512Gi"), Reader: modelvolume.ReaderHostPath}}
	// The primary claim lives in the nvsnap namespace, bound to the volume
	// the Job wrote.
	sysNS := p.Cfg.SystemNamespace()
	if _, err := p.EnsureWriterClaim(context.Background(), mvURI, sysNS); err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(context.Background(), modelvolume.ClaimName(mvURI), metav1.GetOptions{})
	pvc.Spec.VolumeName = "pvc-abc"
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Update(context.Background(), pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	return kc, p
}

func mvController(t *testing.T, kc *fake.Clientset, p *modelvolume.Provisioner, node string) (c *ModelVolumeController, attached *[]string, bound *[][2]string) {
	t.Helper()
	tx, _ := checkpointstore.LookupVolumeHandleTransform("nvmesh")
	minter := &checkpointstore.SharedVolumePromoter{KubeClient: kc, StorageClass: "nvcf-sc", Transform: tx, MountOptions: []string{"ro", "norecovery", "nouuid"}, Log: logrus.New()}
	att := []string{}
	bnd := [][2]string{}
	mounts := map[string]string{} // dst -> device, the fake mount table
	c = &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: node, HostRoot: filepath.Join(t.TempDir(), "models"), Log: logrus.New()}
	c.attach = func(_ context.Context, ns, claim string) (string, error) {
		att = append(att, ns+"/"+claim)
		return "/host/var/lib/kubelet/pods/h/volumes/kubernetes.io~csi/pv/mount", nil
	}
	c.bind = func(src, dst string) error {
		bnd = append(bnd, [2]string{src, dst})
		mounts[dst] = "/dev/nvmesh/csi-abc"
		return nil
	}
	c.unbind = func(dst string) error { delete(mounts, dst); return nil }
	c.mountedDevice = func(dst string) string { return mounts[dst] }
	return c, &att, &bnd
}

func downloadJob(succeeded int32) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: modelvolume.JobName(mvURI), Namespace: "sr-fn",
			Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI)},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI}},
		Status: batchv1.JobStatus{Succeeded: succeeded},
	}
}

func readerPod(ns, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "r-1", Namespace: ns,
			Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "reader", modelvolume.PendingLabel: "true"},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI, modelvolume.LandingAnnotation: "/config/models"}},
		Spec: corev1.PodSpec{NodeName: node},
	}
}

func TestModelVolumeController_JobCompletionMintsReadOnly(t *testing.T) {
	kc, p := writerFixture(t)
	c, _, _ := mvController(t, kc, p, "node-a")
	ctx := context.Background()

	c.HandleJob(ctx, downloadJob(0)) // still running
	if st, _ := p.Lookup(ctx, mvURI); st.Complete {
		t.Fatal("no completion before the Job succeeds")
	}
	c.HandleJob(ctx, downloadJob(1))
	st, _ := p.Lookup(ctx, mvURI)
	if !st.Complete || st.PrimaryPV != "pvc-abc" {
		t.Fatalf("a succeeded Job must complete the identity on the retained PV: %+v", st)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims(p.Cfg.SystemNamespace()).Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Error("the download claim must be released so the volume detaches")
	}
	primary, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-abc", metav1.GetOptions{})
	if primary.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain || primary.Labels[modelvolume.CompleteLabel] != "true" {
		t.Error("the primary PV must be retained and labelled complete; it is the artifact")
	}
	c.HandleJob(ctx, downloadJob(1)) // idempotent after release
}

func TestModelVolumeController_PendingReaderBoundOnItsNode(t *testing.T) {
	kc, p := writerFixture(t)
	ctx := context.Background()
	c, attached, bound := mvController(t, kc, p, "node-b")
	reader := readerPod("other-ns", "node-b")
	if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	c.Handle(ctx, reader) // not complete yet
	if len(*attached) != 0 || len(*bound) != 0 {
		t.Fatal("nothing may be attached before the download completes")
	}
	cw, _, _ := mvController(t, kc, p, "node-a")
	cw.HandleJob(ctx, downloadJob(1))

	// Readers on other nodes are not this agent's business, even before
	// anything is bound here.
	c.Handle(ctx, readerPod("other-ns", "node-c"))
	if len(*attached) != 0 {
		t.Fatal("a reader on another node must be ignored")
	}

	c.Handle(ctx, reader)
	if len(*attached) != 1 || (*attached)[0] != "other-ns/"+modelvolume.ReadOnlyClaimName(mvURI) {
		t.Errorf("reader's read-only claim in its own namespace must be attached, got %v", *attached)
	}
	ro, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read-only claim must be minted in the reader namespace from the retained PV: %v", err)
	}
	roPV, _ := kc.CoreV1().PersistentVolumes().Get(ctx, ro.Spec.VolumeName, metav1.GetOptions{})
	if roPV.Spec.CSI.VolumeHandle != "cluster:csi-abc:vol:other-ns" || !roPV.Spec.CSI.ReadOnly {
		t.Errorf("read-only PV must carry the reader namespace's handle: %+v", roPV.Spec.CSI)
	}
	wantDst := filepath.Join(c.HostRoot, modelvolume.Key(mvURI))
	if len(*bound) != 1 || (*bound)[0][1] != wantDst || (*bound)[0][0] == "" {
		t.Errorf("bind must land on the reader's hostPath %s, got %v", wantDst, *bound)
	}
	got, _ := kc.CoreV1().Pods("other-ns").Get(ctx, "r-1", metav1.GetOptions{})
	if got.Labels[modelvolume.PendingLabel] != "false" {
		t.Errorf("reader must be un-pended, labels %v", got.Labels)
	}
	// A second reader of the same identity on this node reuses the bind.
	c.Handle(ctx, reader)
	if len(*bound) != 1 {
		t.Error("the bind is per identity per node, not per pod")
	}
}

// While the download's read-write attachment still exists, no read-only
// claim is minted and nothing is bound: NVMesh would refuse the attach.
func TestModelVolumeController_WaitsForPrimaryDetach(t *testing.T) {
	kc, p := writerFixture(t)
	ctx := context.Background()
	pvName := "pvc-abc"
	va := &storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "csi-1"}, Spec: storagev1.VolumeAttachmentSpec{
		Attacher: "nvmesh-csi.excelero.com", NodeName: "node-a", Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvName}}}
	if _, err := kc.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cw, _, _ := mvController(t, kc, p, "node-a")
	cw.HandleJob(ctx, downloadJob(1))
	c, attached, bound := mvController(t, kc, p, "node-b")
	reader := readerPod("other-ns", "node-b")
	if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, reader)
	if len(*attached) != 0 || len(*bound) != 0 {
		t.Fatal("nothing may be attached while the primary is still attached read-write")
	}
	if err := kc.StorageV1().VolumeAttachments().Delete(ctx, "csi-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, reader)
	if len(*attached) != 1 || len(*bound) != 1 {
		t.Errorf("after detach the reader must be served: attached=%v bound=%v", *attached, *bound)
	}
}

// Memory is not the truth: with nothing mounted at the target (agent
// restarted, operator unmounted) the reader is bound again, and a bind that
// belongs to a replaced volume of the same identity is redone.
func TestModelVolumeController_RebindsWhenMountIsMissingOrStale(t *testing.T) {
	kc, p := writerFixture(t)
	ctx := context.Background()
	cw, _, _ := mvController(t, kc, p, "node-a")
	cw.HandleJob(ctx, downloadJob(1))
	c, _, bound := mvController(t, kc, p, "node-b")
	reader := readerPod("third-ns", "node-b")
	if _, err := kc.CoreV1().Pods("third-ns").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, reader)
	if len(*bound) != 1 {
		t.Fatalf("first bind expected, got %v", *bound)
	}
	// Someone unmounted it: the next reader event binds again.
	_ = c.unbind((*bound)[0][1])
	c.Handle(ctx, reader)
	if len(*bound) != 2 {
		t.Errorf("a missing mount must be bound again, got %d binds", len(*bound))
	}
	// The identity was re-downloaded onto a new volume: the old bind is
	// stale and must be replaced.
	c.mountedDevice = func(string) string { return "/dev/nvmesh/csi-OLD" }
	unbound := 0
	c.unbind = func(string) error { unbound++; c.mountedDevice = func(string) string { return "" }; return nil }
	c.Handle(ctx, reader)
	if unbound != 1 || len(*bound) != 3 {
		t.Errorf("stale bind must be unbound and redone: unbound=%d binds=%d", unbound, len(*bound))
	}
	if !deviceMatchesHandle("/dev/nvmesh/csi-abc", "cluster:csi-abc:vol:ns") || deviceMatchesHandle("/dev/nvmesh/csi-old", "cluster:csi-abc:vol:ns") || !deviceMatchesHandle("/dev/md127", "anything") {
		t.Error("deviceMatchesHandle")
	}
}

// A bind that leaves nothing mounted must not un-pend the reader; the
// wait init would otherwise be released against an empty directory.
func TestModelVolumeController_NoUnpendWithoutAMount(t *testing.T) {
	kc, p := writerFixture(t)
	ctx := context.Background()
	cw, _, _ := mvController(t, kc, p, "node-a")
	cw.HandleJob(ctx, downloadJob(1))
	c, _, _ := mvController(t, kc, p, "node-b")
	c.bind = func(string, string) error { return nil } // reports success, mounts nothing
	reader := readerPod("other-ns", "node-b")
	if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, reader)
	got, _ := kc.CoreV1().Pods("other-ns").Get(ctx, "r-1", metav1.GetOptions{})
	if got.Labels[modelvolume.PendingLabel] != "true" {
		t.Error("reader must stay pending when nothing is mounted at the bind target")
	}
}

// PVC reader mode (the default): the reader references the read-only
// claim; the agent mints it in the reader's namespace once the primary is
// detached and un-pends the pod. No bind, no hostPath, any agent serves
// it because the pod may still be unscheduled.
func TestModelVolumeController_PVCReaderMintedWithoutBind(t *testing.T) {
	kc, p := writerFixture(t)
	p.Cfg.Reader = ""
	ctx := context.Background()
	c, attached, bound := mvController(t, kc, p, "node-b")
	reader := readerPod("other-ns", "")
	if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, reader)
	if _, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Fatal("no read-only claim before the download completes")
	}
	pvAbc := "pvc-abc"
	va := &storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "va-1"}, Spec: storagev1.VolumeAttachmentSpec{Attacher: "nvmesh-csi.excelero.com", NodeName: "node-a", Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvAbc}}}
	if _, err := kc.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cw, _, _ := mvController(t, kc, p, "node-a")
	cw.HandleJob(ctx, downloadJob(1))
	c.Handle(ctx, reader)
	if _, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Fatal("no read-only claim while the primary is attached read-write")
	}
	if err := kc.StorageV1().VolumeAttachments().Delete(ctx, "va-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, reader)
	ro, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read-only claim must be minted in the reader namespace: %v", err)
	}
	roPV, _ := kc.CoreV1().PersistentVolumes().Get(ctx, ro.Spec.VolumeName, metav1.GetOptions{})
	if roPV.Spec.CSI.VolumeHandle != "cluster:csi-abc:vol:other-ns" || !roPV.Spec.CSI.ReadOnly {
		t.Errorf("read-only PV must carry the reader namespace's handle: %+v", roPV.Spec.CSI)
	}
	if len(*attached) != 0 || len(*bound) != 0 {
		t.Errorf("PVC mode never attaches a holder or binds: attached=%v bound=%v", *attached, *bound)
	}
	got, _ := kc.CoreV1().Pods("other-ns").Get(ctx, "r-1", metav1.GetOptions{})
	if got.Labels[modelvolume.PendingLabel] != "false" {
		t.Errorf("reader must be un-pended, labels %v", got.Labels)
	}
	c.Handle(ctx, reader) // idempotent
	if len(*attached) != 0 {
		t.Error("nothing to attach on the second pass either")
	}
}

// stagingFixture: a staging Job in a function namespace whose pod on
// node has finished its download init and is held Running, and a fake
// provisioner that binds claims on create the way an Immediate-binding
// CSI class does.
func stagingFixture(t *testing.T, node string) (*fake.Clientset, *modelvolume.Provisioner, *corev1.Pod) {
	t.Helper()
	job := downloadJob(0)
	job.Annotations[modelvolume.StagingAnnotation] = "ngc-models"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-x1", Namespace: "sr-fn", UID: "pod-uid-1",
			Labels:      map[string]string{"job-name": job.Name, "batch.kubernetes.io/job-name": job.Name, modelvolume.IdentityLabel: modelvolume.Key(mvURI)},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI, modelvolume.StagingAnnotation: "ngc-models"}},
		Spec: corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: []corev1.ContainerStatus{{Name: modelvolume.DownloadContainer,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}},
	}
	kc := fake.NewSimpleClientset(job, pod)
	kc.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pvc := action.(k8stesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim)
		pvc = pvc.DeepCopy()
		pvc.Spec.VolumeName = "pvc-staged"
		pvc.Status.Phase = corev1.ClaimBound
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-staged"},
			Spec: corev1.PersistentVolumeSpec{
				Capacity:                      corev1.ResourceList{corev1.ResourceStorage: pvc.Spec.Resources.Requests[corev1.ResourceStorage]},
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
				PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "cluster:csi-staged:vol"}},
			},
		}
		if err := kc.Tracker().Add(pv); err != nil {
			return true, nil, err
		}
		gvr := corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims")
		if err := kc.Tracker().Create(gvr, pvc, pvc.Namespace); err != nil {
			return true, nil, err
		}
		return true, pvc, nil
	})
	p := &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: modelvolume.ModeBlock, StorageClass: "nvcf-sc", Namespace: "nvsnap-system"}}
	return kc, p, pod
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The block-mode completion is the cachedir shape: measure the staged
// emptyDir on the pod's node, create a claim of that size in the nvsnap
// namespace, copy, label the retained PV complete, release the claim,
// drop the Job. Nothing is guessed and nothing is grown.
func TestModelVolumeController_StagingCopiedIntoSizedClaim(t *testing.T) {
	ctx := context.Background()
	kc, p, pod := stagingFixture(t, "node-a")
	c, _, _ := mvController(t, kc, p, "node-a")
	var measured, copied string
	c.stageSize = func(path string) (int64, error) { measured = path; return 30 << 30, nil }
	c.copyStaging = func(_ context.Context, ns, claim, src string) error {
		copied = ns + "/" + claim + "<-" + src
		return nil
	}
	c.Handle(ctx, pod)
	waitUntil(t, "job deleted after copy", func() bool {
		_, err := kc.BatchV1().Jobs("sr-fn").Get(ctx, modelvolume.JobName(mvURI), metav1.GetOptions{})
		return err != nil
	})
	wantSrc := "/var/lib/kubelet/pods/pod-uid-1/volumes/kubernetes.io~empty-dir/ngc-models"
	if measured != "/host"+wantSrc {
		t.Errorf("measured %q, want the staged emptyDir through the host root", measured)
	}
	if copied != "nvsnap-system/"+modelvolume.ClaimName(mvURI)+"<-"+wantSrc {
		t.Errorf("copied %q", copied)
	}
	pv, err := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-staged", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if size := pv.Spec.Capacity[corev1.ResourceStorage]; size.String() != "33Gi" {
		t.Errorf("claim sized from 30 GiB staged plus headroom, got %s", size.String())
	}
	waitUntil(t, "bytes recorded on the staged primary", func() bool {
		pv, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-staged", metav1.GetOptions{})
		return pv != nil && pv.Annotations[modelvolume.BytesAnnotation] == strconv.FormatInt(30<<30, 10)
	})
	if pv.Labels[modelvolume.CompleteLabel] != "true" || pv.Labels[modelvolume.IdentityLabel] != modelvolume.Key(mvURI) || pv.Labels[modelvolume.SourceNamespaceLabel] != "nvsnap-system" || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("primary must be complete and retained: %v", pv.Labels)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Error("primary claim must be released after the copy so the volume detaches")
	}
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("sr-fn").List(ctx, metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Error("nothing is created in the function namespace")
	}
	st, _ := p.Lookup(ctx, mvURI)
	if !st.Complete || st.PrimaryPV != "pvc-staged" {
		t.Errorf("lookup after promote: %+v", st)
	}
}

func TestModelVolumeController_StagingOnAnotherNodeOrUnfinishedIsIgnored(t *testing.T) {
	ctx := context.Background()
	kc, p, pod := stagingFixture(t, "node-b")
	c, _, _ := mvController(t, kc, p, "node-a")
	called := false
	c.stageSize = func(string) (int64, error) { called = true; return 1, nil }
	c.Handle(ctx, pod)
	early := pod.DeepCopy()
	early.Spec.NodeName = "node-a"
	early.Status.InitContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	c.Handle(ctx, early)
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Error("only the agent on the pod's node copies, and only once the download init exited 0")
	}
	if _, err := kc.BatchV1().Jobs("sr-fn").Get(ctx, modelvolume.JobName(mvURI), metav1.GetOptions{}); err != nil {
		t.Error("job must stay for its own node's agent")
	}
}

// A copy that keeps failing is abandoned: the Job (and its emptyDir) and
// the sized claim go away, a failure record keeps new admissions on their
// own download, and readers pending on the claim that will never come
// are deleted so their controllers recreate them on that path.
func TestModelVolumeController_StagingGivesUpAfterAttempts(t *testing.T) {
	ctx := context.Background()
	kc, p, pod := stagingFixture(t, "node-a")
	pending := readerPod("sr-fn", "")
	pending.Name = "mini-service-0"
	if _, err := kc.CoreV1().Pods("sr-fn").Create(ctx, pending, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c, _, _ := mvController(t, kc, p, "node-a")
	c.StagingAttempts = 2
	c.stageSize = func(string) (int64, error) { return 1 << 30, nil }
	fails := 0
	c.copyStaging = func(context.Context, string, string, string) error { fails++; return context.DeadlineExceeded }
	c.Handle(ctx, pod)
	waitUntil(t, "first attempt", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.attempts[mvURI] == 1 })
	if _, err := kc.BatchV1().Jobs("sr-fn").Get(ctx, modelvolume.JobName(mvURI), metav1.GetOptions{}); err != nil {
		t.Fatal("job must survive the first failure for a retry")
	}
	c.Handle(ctx, pod)
	waitUntil(t, "job dropped", func() bool {
		_, err := kc.BatchV1().Jobs("sr-fn").Get(ctx, modelvolume.JobName(mvURI), metav1.GetOptions{})
		return err != nil
	})
	if fails != 2 {
		t.Errorf("attempts %d", fails)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Error("the sized claim is released so the primary can be reaped")
	}
	waitUntil(t, "failure recorded", func() bool { st, _ := p.Lookup(ctx, mvURI); return st.Failed && !st.Complete })
	waitUntil(t, "pending reader released", func() bool {
		_, err := kc.CoreV1().Pods("sr-fn").Get(ctx, "mini-service-0", metav1.GetOptions{})
		return err != nil
	})
}

// A download init that keeps restarting (an engine image that does not
// run on the node, a missing CLI, a bad token) is abandoned as soon as it
// has been retried StagingAttempts times rather than after the Job's
// deadline: same cleanup as a failing copy, and the pending reader is
// released so its owner surfaces the real error.
func TestModelVolumeController_StagingGivesUpWhenDownloadCannotStart(t *testing.T) {
	ctx := context.Background()
	kc, p, pod := stagingFixture(t, "node-a")
	pending := readerPod("sr-fn", "")
	pending.Name = "mini-service-0"
	if _, err := kc.CoreV1().Pods("sr-fn").Create(ctx, pending, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c, _, _ := mvController(t, kc, p, "node-a")
	c.StagingAttempts = 3
	copied := false
	c.copyStaging = func(context.Context, string, string, string) error { copied = true; return nil }
	crash := pod.DeepCopy()
	crash.Status.Phase = corev1.PodPending
	crash.Status.InitContainerStatuses[0].RestartCount = 2
	crash.Status.InitContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	crash.Status.InitContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 128, Reason: "StartError", Message: "exec /bin/sh: exec format error"}}
	c.Handle(ctx, crash)
	time.Sleep(50 * time.Millisecond)
	if _, err := kc.BatchV1().Jobs("sr-fn").Get(ctx, modelvolume.JobName(mvURI), metav1.GetOptions{}); err != nil {
		t.Fatal("two restarts are still a retry; the job must stay")
	}
	crash.Status.InitContainerStatuses[0].RestartCount = 3
	c.Handle(ctx, crash)
	c.Handle(ctx, crash)
	waitUntil(t, "job dropped", func() bool {
		_, err := kc.BatchV1().Jobs("sr-fn").Get(ctx, modelvolume.JobName(mvURI), metav1.GetOptions{})
		return err != nil
	})
	waitUntil(t, "failure recorded", func() bool { st, _ := p.Lookup(ctx, mvURI); return st.Failed && !st.Complete })
	waitUntil(t, "pending reader released", func() bool {
		_, err := kc.CoreV1().Pods("sr-fn").Get(ctx, "mini-service-0", metav1.GetOptions{})
		return err != nil
	})
	if copied {
		t.Error("nothing is copied from a download that never finished")
	}
	rec, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, modelvolume.FailureRecordName(mvURI), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Annotations[modelvolume.FailedAnnotation]; !strings.Contains(got, "exec format error") || !strings.Contains(got, "restarted 3 times") {
		t.Errorf("failure record must carry the kubelet message, got %q", got)
	}
}

// On a shared filesystem the Job writes through a read-write view in its
// namespace into the primary in the nvsnap namespace. Its success
// completes the primary there and retires the writer view.
func TestModelVolumeController_SharedFilesystemJobCompletesPrimary(t *testing.T) {
	ctx := context.Background()
	primary := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-fs"}, Spec: corev1.PersistentVolumeSpec{
		Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Ti")},
		PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "fss.csi.oraclecloud.com", VolumeHandle: "fs:ip:/export"}}}}
	kc := fake.NewSimpleClientset(primary)
	p := &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: modelvolume.ModeRWX, StorageClass: "fs", Size: resource.MustParse("4Ti")}}
	sysNS := p.Cfg.SystemNamespace()
	if _, err := p.EnsureWriterClaim(ctx, mvURI, sysNS); err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{})
	pvc.Spec.VolumeName, pvc.Status.Phase = "pv-fs", corev1.ClaimBound
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	tx, _ := checkpointstore.LookupVolumeHandleTransform("")
	minter := &checkpointstore.SharedVolumePromoter{KubeClient: kc, StorageClass: "fs", Transform: tx, Log: logrus.New()}
	if err := minter.MintViewFromPVLabels(ctx, "pv-fs", p.Cfg.WriterViewPVName(mvURI, "sr-fn"), p.Cfg.WriterViewClaimName(mvURI), "sr-fn", nil, false); err != nil {
		t.Fatal(err)
	}
	// The Job's finished pod, on any node: the writer left the byte count
	// in its termination message, which is all the agents read (the pod's
	// volumes are unmounted the moment it completes).
	jobPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: modelvolume.JobName(mvURI) + "-x1", Namespace: "sr-fn", UID: "job-pod-uid",
			Labels: map[string]string{"batch.kubernetes.io/job-name": modelvolume.JobName(mvURI)}},
		Spec: corev1.PodSpec{NodeName: "node-z"},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: modelvolume.DownloadContainer,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: "65549091410"}}}}},
	}
	if _, err := kc.CoreV1().Pods("sr-fn").Create(ctx, jobPod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordFailure(ctx, mvURI, "an earlier attempt"); err != nil {
		t.Fatal(err)
	}
	c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-a", Log: logrus.New()}
	c.HandleJob(ctx, downloadJob(1))
	st, _ := p.Lookup(ctx, mvURI)
	if !st.Complete || st.PrimaryPV != "pv-fs" {
		t.Fatalf("the primary is complete on the retained PV: %+v", st)
	}
	if st.Failed {
		t.Error("a successful retry clears the earlier failure record")
	}
	if st.PrimaryBytes != 65549091410 {
		t.Errorf("the tree size from the marker is recorded on the primary and served by Lookup, got %d", st.PrimaryBytes)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Error("the primary claim is released")
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(ctx, p.Cfg.WriterViewClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Error("the writer view claim is retired with the Job")
	}
	if _, err := kc.CoreV1().PersistentVolumes().Get(ctx, p.Cfg.WriterViewPVName(mvURI, "sr-fn"), metav1.GetOptions{}); err != nil {
		t.Error("the writer view PV stays for kubelet to unstage; the reaper retires it once Released")
	}
	// A pending reader is served without any detach wait: the filesystem is
	// shared while written.
	reader := readerPod("other-ns", "node-b")
	if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pvName := "pv-fs"
	va := &storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "va-fs"}, Spec: storagev1.VolumeAttachmentSpec{Attacher: "fss.csi.oraclecloud.com", NodeName: "node-a", Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvName}}}
	if _, err := kc.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, reader)
	if _, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{}); err != nil {
		t.Errorf("the read-only view is minted while the primary is still attached elsewhere: %v", err)
	}
}

// cacheFixture is a Ready capture candidate: rank `ordinal` of a group
// of `group` on `node`, created in the fake cluster so the agent can
// annotate and list it.
func cacheFixture(t *testing.T, kc *fake.Clientset, cache *modelvolume.Provisioner, node string, ordinal, group int, ready bool) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("mini-service-kimi-k3-%d", ordinal), Namespace: "sr-fn", UID: types.UID(fmt.Sprintf("uid-%s-%d", node, ordinal)),
			Labels: map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "reader", cacheCaptureLabel: "true", modelvolume.CacheKeyLabel: modelvolume.Key("cache://abc123")},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI, cacheURIAnnotation: "cache://abc123", cacheVolumeAnnotation: "nvsnap-cachedir", cacheSubpathAnnotation: "cache",
				modelvolume.CacheOrdinalAnnotation: strconv.Itoa(ordinal), modelvolume.CacheGroupSizeAnnotation: strconv.Itoa(group)}},
		Spec: corev1.PodSpec{NodeName: node},
	}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	if _, err := kc.CoreV1().Pods("sr-fn").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return pod
}

func cacheController(t *testing.T, node string) (*fake.Clientset, *modelvolume.Provisioner, *ModelVolumeController) {
	t.Helper()
	kc, model, _ := stagingFixture(t, node)
	ccfg := model.Cfg
	ccfg.Kind = modelvolume.KindCache
	cache := &modelvolume.Provisioner{Kube: kc, Cfg: ccfg}
	c := &ModelVolumeController{Kube: kc, Provisioner: &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: modelvolume.ModeBlock}}, Cache: cache, NodeName: node, CacheWarmup: time.Millisecond, CacheSettle: time.Millisecond, Log: logrus.New()}
	return kc, cache, c
}

type setCopier struct {
	mu       sync.Mutex
	local    map[string]string // dst -> src
	excludes []string          // of the last copy
}

func (r *setCopier) Copy(_ context.Context, destRoot string, sources []checkpointstore.CaptureSource) (int64, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.local == nil {
		r.local = map[string]string{}
	}
	r.local[destRoot] = sources[0].SrcPath
	r.excludes = sources[0].Excludes
	return 1, 1, nil
}

// excluded reports whether the last copy excluded rel (relative to the
// collected root).
func (r *setCopier) excluded(rel string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.excludes {
		if e == "/"+rel {
			return true
		}
	}
	return false
}

func rankReady(t *testing.T, kc *fake.Clientset, pod *corev1.Pod) bool {
	t.Helper()
	p, err := kc.CoreV1().Pods(pod.Namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return p.Annotations[modelvolume.CacheRankReadyAnnotation] == "true" && p.Annotations[modelvolume.CacheRankBytesAnnotation] != ""
}

// A Ready pod of a group of one: its tree is measured, the pod is
// annotated rank-ready with its bytes, and since the group is complete the
// same agent wins the election and collects the set: one claim sized from
// the rank, the rank copied into /0/, the PV complete under the cache
// label, the claim released.
func TestModelVolumeController_SingleRankGroupCollectedFromReadyPod(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := cacheController(t, "node-a")
	pod := cacheFixture(t, kc, cache, "node-a", 0, 1, true)
	c.treeStat = func(string) (int64, int64, error) { return 31 << 20, 194, nil }
	cp := &setCopier{}
	c.Copier = cp
	mount := t.TempDir()
	var released int32
	c.collectAttach = func(context.Context, string, string, string) (string, func(context.Context) error, error) {
		return mount, func(context.Context) error { atomic.AddInt32(&released, 1); return nil }, nil
	}
	c.Handle(ctx, pod)
	waitUntil(t, "rank ready annotated", func() bool { return rankReady(t, kc, pod) })
	waitUntil(t, "set complete", func() bool { st, _ := cache.Lookup(ctx, "cache://abc123"); return st.Complete })
	// HOME is the collected root; the NGC CLI config (API key) and the Hub
	// token must never travel in a set shared by every pod of the
	// configuration.
	for _, rel := range []string{".ngc", ".netrc", ".cache/huggingface/token"} {
		if !cp.excluded(rel) {
			t.Errorf("collection must exclude %s", rel)
		}
	}
	want := "/var/lib/kubelet/pods/uid-node-a-0/volumes/kubernetes.io~empty-dir/nvsnap-cachedir/cache"
	if cp.local[filepath.Join(mount, "0")] != want {
		t.Errorf("rank 0 copied into /0/ from its emptyDir, got %v", cp.local)
	}
	if atomic.LoadInt32(&released) != 1 {
		t.Error("the volume is released before completion so it detaches")
	}
	pv, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-staged", metav1.GetOptions{})
	if pv.Labels[modelvolume.CacheLabel] != modelvolume.Key("cache://abc123") || pv.Labels[modelvolume.IdentityLabel] != "" {
		t.Errorf("set PV labelled under the cache label only: %v", pv.Labels)
	}
	if size := pv.Spec.Capacity[corev1.ResourceStorage]; size.String() != "1Gi" {
		t.Errorf("31 MiB set rounds up to the 1Gi floor, got %s", size.String())
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, cache.Cfg.ClaimName("cache://abc123"), metav1.GetOptions{}); err == nil {
		t.Error("set claim released after the copy")
	}
}

// A group of two: nothing is collected while a rank is missing. Once the
// other node's rank is ready, one claim is created for the whole set,
// sized from both ranks; the local rank is copied and the remote rank is
// fetched from its agent into its own directory.
func TestModelVolumeController_GroupCollectedWhenAllRanksReady(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := cacheController(t, "node-a")
	local := cacheFixture(t, kc, cache, "node-a", 0, 2, true)
	remote := cacheFixture(t, kc, cache, "node-b", 1, 2, true)
	c.treeStat = func(string) (int64, int64, error) { return 600 << 20, 1000, nil }
	cp := &setCopier{}
	c.Copier = cp
	mount := t.TempDir()
	c.collectAttach = func(context.Context, string, string, string) (string, func(context.Context) error, error) {
		return mount, func(context.Context) error { return nil }, nil
	}
	var fetched []string
	var fmu sync.Mutex
	c.fetchRankFn = func(_ context.Context, uri string, r rankSource, dst string) error {
		fmu.Lock()
		defer fmu.Unlock()
		fetched = append(fetched, fmt.Sprintf("%s:%d:%s->%s", r.pod.Spec.NodeName, r.ordinal, r.src, dst))
		return nil
	}
	c.Handle(ctx, local)
	waitUntil(t, "local rank ready", func() bool { return rankReady(t, kc, local) })
	time.Sleep(50 * time.Millisecond)
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").List(ctx, metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Fatal("no claim while rank 1 is not ready")
	}
	// node-b's agent marks its rank ready (600 MiB too).
	remote.Annotations[modelvolume.CacheRankReadyAnnotation] = "true"
	remote.Annotations[modelvolume.CacheRankBytesAnnotation] = strconv.FormatInt(600<<20, 10)
	if _, err := kc.CoreV1().Pods("sr-fn").Update(ctx, remote, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, remote) // event from the other node's pod: this agent only collects, never measures it
	waitUntil(t, "set complete", func() bool { st, _ := cache.Lookup(ctx, "cache://abc123"); return st.Complete })
	if cp.local[filepath.Join(mount, "0")] == "" {
		t.Errorf("local rank 0 copied into /0/: %v", cp.local)
	}
	fmu.Lock()
	if len(fetched) != 1 || fetched[0] != "node-b:1:/var/lib/kubelet/pods/uid-node-b-1/volumes/kubernetes.io~empty-dir/nvsnap-cachedir/cache->"+filepath.Join(mount, "1") {
		t.Errorf("remote rank 1 fetched from node-b into /1/: %v", fetched)
	}
	fmu.Unlock()
	pv, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-staged", metav1.GetOptions{})
	if size := pv.Spec.Capacity[corev1.ResourceStorage]; size.String() != "2Gi" {
		t.Errorf("set sized from both ranks (1200 MiB + 10%% -> 2Gi), got %s", size.String())
	}
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").List(ctx, metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Error("exactly one claim, released after collection")
	}
}

// A cache tree that is still growing (a worker that reports Ready before
// its compile finishes) is not marked ready; the next pod event retries.
func TestModelVolumeController_RankWaitsForTreeToSettle(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := cacheController(t, "node-a")
	pod := cacheFixture(t, kc, cache, "node-a", 0, 1, true)
	sizes := []int64{748, 20 << 20, 31 << 20, 31 << 20}
	calls := 0
	c.treeStat = func(string) (int64, int64, error) {
		b := sizes[min(calls, len(sizes)-1)]
		calls++
		return b, b / 1000, nil
	}
	c.Copier = &setCopier{}
	mount := t.TempDir()
	c.collectAttach = func(context.Context, string, string, string) (string, func(context.Context) error, error) {
		return mount, func(context.Context) error { return nil }, nil
	}
	c.Handle(ctx, pod)
	waitUntil(t, "first pass measured twice", func() bool { return calls == 2 })
	time.Sleep(20 * time.Millisecond)
	if rankReady(t, kc, pod) {
		t.Fatal("a changing tree must not be marked ready")
	}
	c.Handle(ctx, pod) // next event: 31 MiB twice
	waitUntil(t, "ready once settled", func() bool { return rankReady(t, kc, pod) })
	waitUntil(t, "collected", func() bool { st, _ := cache.Lookup(ctx, "cache://abc123"); return st.Complete })
}

// Only this node's Ready pods are measured; a not-Ready pod or another
// node's pod is left to its own agent.
func TestModelVolumeController_RankMeasuredOnlyOnOwnNodeWhenReady(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := cacheController(t, "node-a")
	notReady := cacheFixture(t, kc, cache, "node-a", 0, 2, false)
	other := cacheFixture(t, kc, cache, "node-b", 1, 2, true)
	calls := 0
	c.treeStat = func(string) (int64, int64, error) { calls++; return 1 << 20, 3, nil }
	c.Handle(ctx, notReady)
	c.Handle(ctx, other)
	time.Sleep(30 * time.Millisecond)
	if calls != 0 || rankReady(t, kc, notReady) || rankReady(t, kc, other) {
		t.Errorf("nothing measured for a not-Ready pod or another node's pod (calls=%d)", calls)
	}
}

// ServeRank streams a ready rank hosted on this node to the collector;
// a rank that is not ready or not here is 404.
func TestModelVolumeController_ServeRank(t *testing.T) {
	kc, cache, c := cacheController(t, "node-a")
	host := t.TempDir()
	c.HostFSRoot = host
	c.KubeletPodsDir = "/var/lib/kubelet/pods"
	pod := cacheFixture(t, kc, cache, "node-a", 3, 8, true)
	tree := filepath.Join(host, "var/lib/kubelet/pods", string(pod.UID), "volumes/kubernetes.io~empty-dir/nvsnap-cachedir/cache")
	os.MkdirAll(filepath.Join(tree, "triton"), 0o755)
	os.WriteFile(filepath.Join(tree, "triton", "k.cubin"), []byte("cubin"), 0o644)
	r := mux.NewRouter()
	r.HandleFunc("/v1/cache-rank/{key}/{ordinal}", c.ServeRank)
	key := modelvolume.Key("cache://abc123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/cache-rank/"+key+"/3", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("a rank not yet marked ready is not served, got %d", w.Code)
	}
	pod.Annotations[modelvolume.CacheRankReadyAnnotation] = "true"
	kc.CoreV1().Pods("sr-fn").Update(context.Background(), pod, metav1.UpdateOptions{})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/cache-rank/"+key+"/3", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-tar" {
		t.Fatalf("ready rank served as tar, got %d", w.Code)
	}
	dest := t.TempDir()
	if files, _, err := tarstream.Extract(w.Body, dest, false); err != nil || files != 1 {
		t.Fatalf("extract: %d %v", files, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "triton", "k.cubin")); string(b) != "cubin" {
		t.Errorf("content %q", b)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/cache-rank/"+key+"/4", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown ordinal is 404, got %d", w.Code)
	}
}

// An engine that downloads into its own per-replica claim (kimi-k3) is
// captured once Ready: the agent on its node locates the claim's kubelet
// mount, sizes the primary from the tree, copies it in and marks it
// complete. The pod itself is untouched: no Job, no reader wait.
func TestModelVolumeController_CapturesEngineLandingFromReadyPod(t *testing.T) {
	ctx := context.Background()
	kc, p, _ := stagingFixture(t, "node-a")
	uri := "ngc://org/llm_nim/kimi-k3:hf"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "ngc-models-mini-service-kimi-k3-0", Namespace: "sr-fn"},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-kimi-replica-0"}}
	if err := kc.Tracker().Add(pvc); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mini-service-kimi-k3-0", Namespace: "sr-fn", UID: "pod-uid-kimi",
			Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(uri), modelvolume.CaptureLabel: "true"},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: uri, modelvolume.CaptureVolumeAnnotation: "ngc-models"}},
		Spec: corev1.PodSpec{NodeName: "node-a", Volumes: []corev1.Volume{{Name: "ngc-models",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	c, _, _ := mvController(t, kc, p, "node-a")
	var measured, copied string
	c.stageSize = func(path string) (int64, error) { measured = path; return 2000 << 30, nil }
	c.copyStaging = func(_ context.Context, ns, claim, src string) error {
		copied = ns + "/" + claim + "<-" + src
		return nil
	}

	notReady := pod.DeepCopy()
	notReady.Status.Conditions = nil
	c.Handle(ctx, notReady)
	time.Sleep(50 * time.Millisecond)
	if measured != "" {
		t.Fatal("nothing is captured before the pod is Ready: the download may still be running")
	}
	c.Handle(ctx, pod)
	waitUntil(t, "captured", func() bool { st, _ := p.Lookup(ctx, uri); return st.Complete })
	wantSrc := "/var/lib/kubelet/pods/pod-uid-kimi/volumes/kubernetes.io~csi/pvc-kimi-replica-0/mount"
	if measured != "/host"+wantSrc {
		t.Errorf("measured %q, want the replica claim's kubelet mount through the host root", measured)
	}
	if copied != "nvsnap-system/"+modelvolume.ClaimName(uri)+"<-"+wantSrc {
		t.Errorf("copied %q", copied)
	}
	pv, err := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-staged", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if size := pv.Spec.Capacity[corev1.ResourceStorage]; size.String() != "2200Gi" {
		t.Errorf("primary sized from the 2000 GiB tree plus headroom, got %s", size.String())
	}
	waitUntil(t, "bytes recorded on the captured primary", func() bool {
		pv, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-staged", metav1.GetOptions{})
		return pv != nil && pv.Annotations[modelvolume.BytesAnnotation] == strconv.FormatInt(2000<<30, 10)
	})
	if pv.Labels[modelvolume.CompleteLabel] != "true" || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("primary must be complete and retained: %v", pv.Labels)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, modelvolume.ClaimName(uri), metav1.GetOptions{}); err == nil {
		t.Error("primary claim released after the copy so the volume detaches")
	}
	if jobs, _ := kc.BatchV1().Jobs("sr-fn").List(ctx, metav1.ListOptions{}); len(jobs.Items) != 1 { // only the fixture's unrelated staging job
		t.Errorf("capture creates no Job: %d", len(jobs.Items))
	}
	c.Handle(ctx, pod) // complete: a second Ready event is a no-op
	time.Sleep(50 * time.Millisecond)
}

// A rank stamped while its cache was still empty (a follower whose
// readiness comes from a stub) is re-measured at collection time, so the
// set claim is sized from what the ranks hold, not from the stale stamp.
func TestModelVolumeController_SetSizedFromFreshMeasurement(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := cacheController(t, "node-a")
	local := cacheFixture(t, kc, cache, "node-a", 0, 2, true)
	remote := cacheFixture(t, kc, cache, "node-b", 1, 2, true)
	c.treeStat = func(string) (int64, int64, error) { return 700 << 20, 900, nil }
	c.Copier = &setCopier{}
	mount := t.TempDir()
	c.collectAttach = func(context.Context, string, string, string) (string, func(context.Context) error, error) {
		return mount, func(context.Context) error { return nil }, nil
	}
	c.fetchRankFn = func(context.Context, string, rankSource, string) error { return nil }
	c.rankSizeFn = func(_ context.Context, _ string, r rankSource) (int64, error) { return 550 << 20, nil }
	// node-b stamped rank 1 at 1.2 MB when its engine had compiled nothing yet.
	remote.Annotations[modelvolume.CacheRankReadyAnnotation] = "true"
	remote.Annotations[modelvolume.CacheRankBytesAnnotation] = strconv.FormatInt(1206214, 10)
	if _, err := kc.CoreV1().Pods("sr-fn").Update(ctx, remote, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, local)
	waitUntil(t, "set complete", func() bool { st, _ := cache.Lookup(ctx, "cache://abc123"); return st.Complete })
	pv, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-staged", metav1.GetOptions{})
	if size := pv.Spec.Capacity[corev1.ResourceStorage]; size.String() != "2Gi" {
		t.Errorf("set sized from fresh measurements (700 MiB + 550 MiB + 10%% -> 2Gi), not from the 1.2 MB stamp: got %s", size.String())
	}
}

// On a shared filesystem a primary that is bound but not complete is
// already readable. The agent mints the download Job's writer view and the
// pending readers' read-only views as soon as the primary binds, without
// waiting for completion; the readers' wait init watches for the marker.
func TestModelVolumeController_SharedFilesystemMintsViewsOnceBound(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	p := &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: modelvolume.ModeRWX, StorageClass: "fs", Size: resource.MustParse("4Ti")}}
	sysNS := p.Cfg.SystemNamespace()
	if _, err := p.EnsureWriterClaim(ctx, mvURI, sysNS); err != nil {
		t.Fatal(err)
	}
	tx, _ := checkpointstore.LookupVolumeHandleTransform("")
	minter := &checkpointstore.SharedVolumePromoter{KubeClient: kc, StorageClass: "fs", Transform: tx, Log: logrus.New()}
	c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-a", Log: logrus.New()}
	writer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: modelvolume.JobName(mvURI) + "-w1", Namespace: "sr-fn",
		Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "writer"},
		Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI}}, Spec: corev1.PodSpec{NodeName: "node-b"}}
	reader := readerPod("other-ns", "node-c")
	if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Primary not bound yet: nothing to view.
	c.Handle(ctx, writer)
	c.Handle(ctx, reader)
	if _, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(ctx, p.Cfg.WriterViewClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Fatal("no writer view before the primary binds")
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Fatal("no read-only view before the primary binds")
	}
	// The filesystem provisions and the claim binds.
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-fs"}, Spec: corev1.PersistentVolumeSpec{
		Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Ti")},
		PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "fss.csi.oraclecloud.com", VolumeHandle: "fs:ip:/export"}}}}
	if _, err := kc.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{})
	pvc.Spec.VolumeName, pvc.Status.Phase = "pv-fs", corev1.ClaimBound
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, writer)
	wv, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(ctx, p.Cfg.WriterViewClaimName(mvURI), metav1.GetOptions{})
	if err != nil || wv.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Fatalf("the writer view is minted once the primary binds: %v %+v", err, wv)
	}
	c.Handle(ctx, reader)
	ro, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{})
	if err != nil || ro.Spec.AccessModes[0] != corev1.ReadOnlyMany {
		t.Fatalf("the pending reader gets its read-only view before completion: %v %+v", err, ro)
	}
	got, _ := kc.CoreV1().Pods("other-ns").Get(ctx, reader.Name, metav1.GetOptions{})
	if got.Labels[modelvolume.PendingLabel] != "false" {
		t.Errorf("the reader is un-pended: %v", got.Labels)
	}
	c.Handle(ctx, writer) // idempotent
}

// sharedFilesystemFixture is a bound RWX primary on a filesystem class
// with its writer view minted in sr-fn, as the webhook leaves it.
func sharedFilesystemFixture(t *testing.T) (*fake.Clientset, *modelvolume.Provisioner, *checkpointstore.SharedVolumePromoter) {
	t.Helper()
	ctx := context.Background()
	primary := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-fs"}, Spec: corev1.PersistentVolumeSpec{
		Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Ti")},
		PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "fss.csi.oraclecloud.com", VolumeHandle: "fs:ip:/export"}}}}
	kc := fake.NewSimpleClientset(primary)
	p := &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: modelvolume.ModeRWX, StorageClass: "fs", Size: resource.MustParse("4Ti")}}
	sysNS := p.Cfg.SystemNamespace()
	if _, err := p.EnsureWriterClaim(ctx, mvURI, sysNS); err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{})
	pvc.Spec.VolumeName, pvc.Status.Phase = "pv-fs", corev1.ClaimBound
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	tx, _ := checkpointstore.LookupVolumeHandleTransform("")
	minter := &checkpointstore.SharedVolumePromoter{KubeClient: kc, StorageClass: "fs", Transform: tx, Log: logrus.New()}
	if err := minter.MintViewFromPVLabels(ctx, "pv-fs", p.Cfg.WriterViewPVName(mvURI, "sr-fn"), p.Cfg.WriterViewClaimName(mvURI), "sr-fn", nil, false); err != nil {
		t.Fatal(err)
	}
	return kc, p, minter
}

// A fresh shared filesystem is root-owned and 0755; the download Job runs
// as the function's user. The agent on the Job's node opens the primary's
// root through the kubelet mount of the Job pod; agents elsewhere leave it
// alone.
func TestModelVolumeController_WriterPodOpensPrimaryRootOnItsNode(t *testing.T) {
	ctx := context.Background()
	kc, p, minter := sharedFilesystemFixture(t)
	view, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(ctx, p.Cfg.WriterViewClaimName(mvURI), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	jobPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: modelvolume.JobName(mvURI) + "-x1", Namespace: "sr-fn", UID: "job-pod-uid",
			Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "writer"},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI}},
		Spec: corev1.PodSpec{NodeName: "node-a", Volumes: []corev1.Volume{{Name: "model", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: view.Name}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if _, err := kc.CoreV1().Pods("sr-fn").Create(ctx, jobPod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	host := t.TempDir()
	root := filepath.Join(host, "var/lib/kubelet/pods/job-pod-uid/volumes/kubernetes.io~csi", view.Spec.VolumeName, "mount")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	other := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-b", HostFSRoot: host, KubeletPodsDir: "/var/lib/kubelet/pods", Log: logrus.New()}
	other.Handle(ctx, jobPod)
	if st, _ := os.Stat(root); st.Mode().Perm()&0o002 != 0 {
		t.Fatal("an agent on another node does not touch the mount")
	}
	c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-a", HostFSRoot: host, KubeletPodsDir: "/var/lib/kubelet/pods", Log: logrus.New()}
	c.Handle(ctx, jobPod)
	st, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o777 || st.Mode()&os.ModeSticky == 0 {
		t.Errorf("the primary root is opened 1777 for the Job's user, got %v", st.Mode())
	}
}

// A download Job that gave up is recorded as a failure with the reason and
// the download container's last words, and its writer view is retired.
// The primary claim stays so a retry reuses the volume.
func TestModelVolumeController_FailedJobRecordsFailureAndRetiresWriterView(t *testing.T) {
	ctx := context.Background()
	kc, p, minter := sharedFilesystemFixture(t)
	sysNS := p.Cfg.SystemNamespace()
	jobPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: modelvolume.JobName(mvURI) + "-x1", Namespace: "sr-fn",
			Labels: map[string]string{"batch.kubernetes.io/job-name": modelvolume.JobName(mvURI)}},
		Spec: corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{Name: modelvolume.DownloadContainer,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: "PermissionError: [Errno 13] Permission denied: '/model/hub'"}}}}},
	}
	if _, err := kc.CoreV1().Pods("sr-fn").Create(ctx, jobPod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	job := downloadJob(0)
	job.UID = "job-uid-1"
	job.Status.Failed = 6
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"}}
	c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-a", Log: logrus.New()}
	var marked []string
	c.markFailed = func(_ context.Context, uri, ns, claim, reason string) error {
		marked = append(marked, ns+"/"+claim+": "+reason)
		return nil
	}
	c.HandleJob(ctx, job)
	c.HandleJob(ctx, job) // the same failure is handled once
	st, _ := p.Lookup(ctx, mvURI)
	if st.Complete || !st.Failed {
		t.Fatalf("the failure is recorded and the primary is not complete: %+v", st)
	}
	if len(marked) != 1 || !strings.HasPrefix(marked[0], sysNS+"/"+modelvolume.ClaimName(mvURI)+": ") || !strings.Contains(marked[0], "Permission denied") {
		t.Errorf("the failure marker is written once on the primary with the reason: %v", marked)
	}
	rec, err := kc.CoreV1().ConfigMaps(sysNS).Get(ctx, p.Cfg.FailureRecordName(mvURI), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reason := rec.Annotations[modelvolume.FailedAnnotation]
	if !strings.Contains(reason, "BackoffLimitExceeded") || !strings.Contains(reason, "Permission denied: '/model/hub'") {
		t.Errorf("the record carries the Job's reason and the download container's last words, got %q", reason)
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(ctx, p.Cfg.WriterViewClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Error("the writer view claim is retired with the failed Job")
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{}); err != nil {
		t.Error("the primary claim stays for the retry")
	}
}


// The failure marker is written on the primary through a mount on this
// node and carries the reason, so readers can log it.
func TestModelVolumeController_FailureMarkerWrittenThroughTheMount(t *testing.T) {
	ctx := context.Background()
	kc, p, minter := sharedFilesystemFixture(t)
	c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-a", Log: logrus.New()}
	mount := t.TempDir()
	released := false
	c.collectAttach = func(context.Context, string, string, string) (string, func(context.Context) error, error) {
		return mount, func(context.Context) error { released = true; return nil }, nil
	}
	c.init()
	if err := c.markFailed(ctx, mvURI, p.Cfg.SystemNamespace(), modelvolume.ClaimName(mvURI), "BackoffLimitExceeded: exit 1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(mount, modelvolume.FailedMarkerFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "BackoffLimitExceeded: exit 1" || !released {
		t.Errorf("marker %q released=%v", string(b), released)
	}
}

// A Job that fails elsewhere is marked only by the node it ran on; with
// its pods gone, whichever agent sees the failure first marks it.
func TestModelVolumeController_FailureMarkerWrittenByTheJobsNode(t *testing.T) {
	ctx := context.Background()
	kc, p, minter := sharedFilesystemFixture(t)
	jobPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: modelvolume.JobName(mvURI) + "-x1", Namespace: "sr-fn",
		Labels: map[string]string{"batch.kubernetes.io/job-name": modelvolume.JobName(mvURI)}}, Spec: corev1.PodSpec{NodeName: "node-z"}}
	if _, err := kc.CoreV1().Pods("sr-fn").Create(ctx, jobPod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	job := downloadJob(0)
	job.UID = "job-uid-2"
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}
	calls := 0
	for _, node := range []string{"node-a", "node-z"} {
		c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: node, Log: logrus.New()}
		c.markFailed = func(context.Context, string, string, string, string) error { calls++; return nil }
		c.HandleJob(ctx, job)
	}
	if calls != 1 {
		t.Errorf("only the Job's node writes the marker, got %d writers", calls)
	}
}
