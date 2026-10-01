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

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// warmFixture is a pod seeded from a complete set: the reader stamps the
// webhook adds on the complete branch, no capture label.
func warmFixture(t *testing.T, c *ModelVolumeController, node string, ordinal, group int) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("omni-worker-%d", ordinal), Namespace: "sr-warm", UID: types.UID(fmt.Sprintf("warm-%s-%d", node, ordinal)),
			Labels: map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "reader", modelvolume.CacheKeyLabel: modelvolume.Key("cache://abc123")},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI, cacheURIAnnotation: "cache://abc123", cacheVolumeAnnotation: "nvsnap-cachedir", cacheSubpathAnnotation: "cache",
				modelvolume.CacheOrdinalAnnotation: strconv.Itoa(ordinal), modelvolume.CacheGroupSizeAnnotation: strconv.Itoa(group)}},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	if _, err := c.Kube.CoreV1().Pods("sr-warm").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return pod
}

// completeSet plants generation 1 of the set as a complete, released
// primary created `age` ago.
func completeSet(t *testing.T, c *ModelVolumeController, age time.Duration) *corev1.PersistentVolume {
	t.Helper()
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-set-g1", CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			Labels:      map[string]string{modelvolume.CacheLabel: modelvolume.Key("cache://abc123"), modelvolume.CompleteLabel: "true", "app.kubernetes.io/managed-by": "nvsnap"},
			Annotations: map[string]string{modelvolume.IdentityAnnotation: "cache://abc123"}},
		Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "c:g1:v"}}},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}
	if _, err := c.Kube.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return pv
}

// refreshController binds every new claim to a volume named after the
// claim, so a second generation gets its own PV.
func refreshController(t *testing.T) (*fake.Clientset, *modelvolume.Provisioner, *ModelVolumeController) {
	t.Helper()
	kc, cache, c := cacheController(t, "node-a")
	kc.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pvc := action.(k8stesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim).DeepCopy()
		pvc.Spec.VolumeName = "pv-" + pvc.Name
		pvc.Status.Phase = corev1.ClaimBound
		pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: pvc.Spec.VolumeName, CreationTimestamp: metav1.Now()},
			Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: pvc.Spec.Resources.Requests[corev1.ResourceStorage]},
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
				PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "c:" + pvc.Name + ":v"}}}}
		if err := kc.Tracker().Add(pv); err != nil {
			return true, nil, err
		}
		if err := kc.Tracker().Create(corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims"), pvc, pvc.Namespace); err != nil {
			return true, nil, err
		}
		return true, pvc, nil
	})
	c.treeStat = func(string) (int64, int64, error) { return 900 << 20, 1400, nil }
	c.Copier = &setCopier{}
	c.collectAttach = func(context.Context, string, string, string) (string, func(context.Context) error, error) {
		return t.TempDir(), func(context.Context) error { return nil }, nil
	}
	c.fetchRankFn = func(context.Context, string, rankSource, string) error { return nil }
	return kc, cache, c
}

func podAnn(t *testing.T, kc *fake.Clientset, pod *corev1.Pod) map[string]string {
	t.Helper()
	p, err := kc.CoreV1().Pods(pod.Namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return p.Annotations
}

// A warm rank whose cachedir gained files the seed index lacks is marked
// with a delta; once the whole group is ready, one agent collects
// generation 2 from the warm ranks and Lookup serves it. A rank with no
// delta is still marked ready so the group can complete.
func TestModelVolumeController_WarmDeltaRefreshesSet(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := refreshController(t)
	completeSet(t, c, time.Hour)
	c.deltaScan = func(src string) (int, int64, string, bool, error) {
		if strings.Contains(src, "warm-node-a-0") {
			return 7, 3 << 20, "fp-mamba", true, nil
		}
		return 0, 0, "", true, nil
	}
	r0 := warmFixture(t, c, "node-a", 0, 2)
	r1 := warmFixture(t, c, "node-a", 1, 2)
	c.Handle(ctx, r0)
	c.Handle(ctx, r1)
	waitUntil(t, "generation 2 complete", func() bool { st, _ := cache.Lookup(ctx, "cache://abc123"); return st.Complete && st.Generation == 2 })
	a0, a1 := podAnn(t, kc, r0), podAnn(t, kc, r1)
	if a0[modelvolume.CacheRankDeltaAnnotation] != "true" || a0[modelvolume.CacheRankDeltaBytesAnnotation] != strconv.Itoa(3<<20) || a0[modelvolume.CacheRankDeltaFingerprintAnnotation] != "fp-mamba" || a0[modelvolume.CacheRankReadyAnnotation] != "true" {
		t.Errorf("rank 0 carries the delta and is ready: %v", a0)
	}
	if a1[modelvolume.CacheRankDeltaAnnotation] != "" || a1[modelvolume.CacheRankReadyAnnotation] != "true" {
		t.Errorf("rank 1 has no delta but is ready: %v", a1)
	}
	st, _ := cache.Lookup(ctx, "cache://abc123")
	claim := cache.Cfg.GenerationClaimName("cache://abc123", 2)
	if st.PrimaryPV != "pv-"+claim {
		t.Errorf("Lookup serves generation 2 at pv-%s, got %s", claim, st.PrimaryPV)
	}
	pv, _ := kc.CoreV1().PersistentVolumes().Get(ctx, st.PrimaryPV, metav1.GetOptions{})
	if pv.Annotations[modelvolume.GenerationAnnotation] != "2" || pv.Annotations[modelvolume.RefreshedFromAnnotation] != "pv-set-g1" || pv.Annotations[modelvolume.DeltaFingerprintAnnotation] == "" || pv.Labels[modelvolume.CompleteLabel] != "true" {
		t.Errorf("generation 2 records its origin and delta: %v %v", pv.Annotations, pv.Labels)
	}
	if size := pv.Spec.Capacity[corev1.ResourceStorage]; size.String() != "2Gi" {
		t.Errorf("sized from both warm ranks (1800 MiB + 10%% -> 2Gi), got %s", size.String())
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").Get(ctx, claim, metav1.GetOptions{}); err == nil {
		t.Error("the generation claim is released after the copy")
	}
	g1, _ := kc.CoreV1().PersistentVolumes().Get(ctx, "pv-set-g1", metav1.GetOptions{})
	if g1 == nil || g1.Labels[modelvolume.CompleteLabel] != "true" {
		t.Error("generation 1 is untouched; the reaper retires it later")
	}
}

// Inside the cooldown a delta is recorded but no generation is collected.
func TestModelVolumeController_RefreshRespectsCooldown(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := refreshController(t)
	completeSet(t, c, time.Minute)
	c.RefreshCooldown = 30 * time.Minute
	c.deltaScan = func(string) (int, int64, string, bool, error) { return 1, 1, "fp", true, nil }
	r0 := warmFixture(t, c, "node-a", 0, 1)
	c.Handle(ctx, r0)
	waitUntil(t, "delta stamped", func() bool { return podAnn(t, kc, r0)[modelvolume.CacheRankDeltaAnnotation] == "true" })
	time.Sleep(50 * time.Millisecond)
	if st, _ := cache.Lookup(ctx, "cache://abc123"); st.Generation != 1 {
		t.Errorf("no new generation inside the cooldown, got %d", st.Generation)
	}
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").List(ctx, metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Errorf("no refresh claim inside the cooldown: %v", claims.Items)
	}
}

// The same delta after a refresh means the engine rewrites those files on
// every start: the set is marked stable and nothing more is collected.
func TestModelVolumeController_RefreshLoopGuard(t *testing.T) {
	ctx := context.Background()
	kc, cache, c := refreshController(t)
	pv := completeSet(t, c, time.Hour)
	fp := refreshFingerprint(map[int]string{0: "fp-same"})
	pv.Annotations[modelvolume.DeltaFingerprintAnnotation] = fp
	pv.Annotations[modelvolume.GenerationAnnotation] = "2"
	if _, err := kc.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.deltaScan = func(string) (int, int64, string, bool, error) { return 2, 2, "fp-same", true, nil }
	r0 := warmFixture(t, c, "node-a", 0, 1)
	c.Handle(ctx, r0)
	waitUntil(t, "set marked stable", func() bool { st, _ := cache.Lookup(ctx, "cache://abc123"); return st.RefreshStable })
	if st, _ := cache.Lookup(ctx, "cache://abc123"); st.Generation != 2 {
		t.Errorf("no generation 3 for a repeated delta, got %d", st.Generation)
	}
	// A later delta on a stable set changes nothing.
	c.deltaScan = func(string) (int, int64, string, bool, error) { return 2, 2, "fp-other", true, nil }
	if err := kc.CoreV1().Pods("sr-warm").Delete(ctx, r0.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	r1 := warmFixture(t, c, "node-a", 0, 1)
	c.Handle(ctx, r1)
	waitUntil(t, "second delta stamped", func() bool { return podAnn(t, kc, r1)[modelvolume.CacheRankDeltaAnnotation] == "true" })
	time.Sleep(50 * time.Millisecond)
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("nvsnap-system").List(ctx, metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Errorf("a stable set is never refreshed: %v", claims.Items)
	}
}

// Without a seed index (a pod seeded by an older agent) nothing is stamped
// and nothing is proposed; with refresh disabled, likewise.
func TestModelVolumeController_RefreshNeedsIndexAndSwitch(t *testing.T) {
	ctx := context.Background()
	kc, _, c := refreshController(t)
	completeSet(t, c, time.Hour)
	c.deltaScan = func(string) (int, int64, string, bool, error) { return 0, 0, "", false, nil }
	r0 := warmFixture(t, c, "node-a", 0, 1)
	c.Handle(ctx, r0)
	time.Sleep(50 * time.Millisecond)
	if a := podAnn(t, kc, r0); a[modelvolume.CacheRankReadyAnnotation] != "" {
		t.Errorf("no index, no stamps: %v", a)
	}
	c.RefreshDisabled = true
	c.deltaScan = func(string) (int, int64, string, bool, error) { return 5, 5, "fp", true, nil }
	r1 := warmFixture(t, c, "node-a", 1, 2)
	c.Handle(ctx, r1)
	time.Sleep(50 * time.Millisecond)
	if a := podAnn(t, kc, r1); a[modelvolume.CacheRankReadyAnnotation] != "" {
		t.Errorf("refresh disabled, no stamps: %v", a)
	}
}

// The delta scan reports only paths absent from the seed index and skips
// the bookkeeping engines rewrite every start.
func TestScanSeedDelta(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, n int) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, indexed, err := scanSeedDelta(root); err != nil || indexed {
		t.Fatalf("no index: indexed=%v err=%v", indexed, err)
	}
	mk("torchinductor/a.bin", 10)
	mk(".cache/flashinfer/jit/cached_ops/mamba/kernel.so", 700)
	mk(".cache/flashinfer/jit/cached_ops/mamba/.ninja_log", 5)
	mk("torchinductor/locks/x.lock", 0)
	mk(".cache/flashinfer/jit/flashinfer_jit.log", 50)
	mk(".cache/flashinfer/jit/cached_ops/tmp/y", 3)
	if err := os.WriteFile(filepath.Join(root, webhook.SeedIndexFile), []byte("torchinductor/a.bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, bytes, fp, indexed, err := scanSeedDelta(root)
	if err != nil || !indexed {
		t.Fatalf("scan: indexed=%v err=%v", indexed, err)
	}
	if files != 2 || bytes != 705 || fp == "" {
		t.Errorf("delta = the kernel and its ninja log only: files=%d bytes=%d fp=%q", files, bytes, fp)
	}
	again, _, fp2, _, _ := scanSeedDelta(root)
	if again != files || fp2 != fp {
		t.Errorf("the fingerprint is stable for the same delta")
	}
}
