// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

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
	p := &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: modelvolume.ModeBlock, StorageClass: sc, Size: resource.MustParse("512Gi")}}
	if _, err := p.EnsureWriterClaim(context.Background(), mvURI, "sr-fn"); err != nil {
		t.Fatal(err)
	}
	pvc, _ := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(context.Background(), modelvolume.ClaimName(mvURI), metav1.GetOptions{})
	pvc.Spec.VolumeName = "pvc-abc"
	if _, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Update(context.Background(), pvc, metav1.UpdateOptions{}); err != nil {
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
	c = &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: node, HostRoot: filepath.Join(t.TempDir(), "models"), Log: logrus.New()}
	c.attach = func(_ context.Context, ns, claim string) (string, error) {
		att = append(att, ns+"/"+claim)
		return "/host/var/lib/kubelet/pods/h/volumes/kubernetes.io~csi/pv/mount", nil
	}
	c.bind = func(src, dst string) error { bnd = append(bnd, [2]string{src, dst}); return nil }
	return c, &att, &bnd
}

func writerPod(exit *int32) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "w-0", Namespace: "sr-fn",
			Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "writer"},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI, modelvolume.DownloadInitAnnotation: "download-ngc-model"}},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	if exit != nil {
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "download-ngc-model", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: *exit}}}}
	}
	return p
}

func readerPod(ns, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "r-1", Namespace: ns,
			Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "reader", modelvolume.PendingLabel: "true"},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI, modelvolume.LandingAnnotation: "/config/models"}},
		Spec: corev1.PodSpec{NodeName: node},
	}
}

func TestModelVolumeController_WriterCompletionMintsReadOnly(t *testing.T) {
	kc, p := writerFixture(t)
	c, _, _ := mvController(t, kc, p, "node-a")
	ctx := context.Background()

	c.Handle(ctx, writerPod(nil)) // init still running
	if st, _ := p.Lookup(ctx, mvURI); st.Complete {
		t.Fatal("no completion before the download init exits")
	}
	one := int32(1)
	c.Handle(ctx, writerPod(&one)) // init failed
	if st, _ := p.Lookup(ctx, mvURI); st.Complete {
		t.Fatal("a failed download must not complete the volume")
	}
	zero := int32(0)
	c.Handle(ctx, writerPod(&zero))
	st, _ := p.Lookup(ctx, mvURI)
	if !st.Complete {
		t.Fatal("exit 0 must mark the claim complete")
	}
	ro, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read-only claim must be minted in the writer namespace: %v", err)
	}
	pv, err := kc.CoreV1().PersistentVolumes().Get(ctx, ro.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.CSI.VolumeHandle != "cluster:csi-abc:vol:sr-fn" || !pv.Spec.CSI.ReadOnly || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("read-only PV: %+v", pv.Spec)
	}
	primary, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pvc-abc", metav1.GetOptions{})
	if primary.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Error("the writer's PV must be retained; it is the artifact")
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(ctx, modelvolume.ClaimName(mvURI), metav1.GetOptions{}); err != nil {
		t.Error("the writer claim stays: the writer is still running on it")
	}
	c.Handle(ctx, writerPod(&zero)) // idempotent
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
	zero := int32(0)
	cw, _, _ := mvController(t, kc, p, "node-a")
	cw.Handle(ctx, writerPod(&zero))

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
	if _, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, modelvolume.ReadOnlyClaimName(mvURI), metav1.GetOptions{}); err != nil {
		t.Errorf("read-only claim must be minted in the reader namespace: %v", err)
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
