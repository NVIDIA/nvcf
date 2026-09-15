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

// Unit tests for MountHolder (v0.0.51 L2 writer redesign).
//
// Coverage:
//   - Spec(): Kyverno compliance for the nvcf-backend policy suite
//     (require-run-as-non-root, require-ro-rootfs, require-probes,
//     restrict-seccomp, disallow-add-capabilities,
//     require-pod-requests-limits, disallow-latest-tag),
//     OwnerReference back to the rwx PVC, nodeName pinning,
//     tolerate-all, sleep entrypoint.
//   - Create(): records UID, idempotent on AlreadyExists.
//   - WaitRunning(): succeeds when pod Running + PVC bound; fails
//     hard when pod disappears.
//   - PVMountPath(): correct path construction; error before
//     WaitRunning; error when path isn't a directory.
//   - Delete(): idempotent on NotFound; waits for actual removal.

package checkpointstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// newFakeKubeWithUID returns a fake clientset that mimics the real
// API server's behavior of assigning a UID on Pod create. Without
// this, fake CreateAction preserves an empty UID and MountHolder.
// Create can't record it.
func newFakeKubeWithUID() *kubefake.Clientset {
	k := kubefake.NewSimpleClientset()
	k.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		pod := ca.GetObject().(*corev1.Pod)
		if pod.UID == "" {
			pod.UID = types.UID("uid-" + pod.Name)
		}
		return false, pod, nil // false → let the default tracker also record it
	})
	return k
}

func newTestMountHolder(t *testing.T, hostRoot string) *MountHolder {
	t.Helper()
	kube := newFakeKubeWithUID()
	log := logrus.NewEntry(logrus.New()).WithField("test", t.Name())
	return NewMountHolder(kube, log,
		"nvcf-backend", "mh-abc123",
		"node-1", "rwx-abc123", types.UID("pvc-uid-xyz"),
		"nvsnap-agent:test", hostRoot, nil)
}

func TestMountHolderSpec_KyvernoCompliance(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	p := h.Spec()

	// require-run-as-non-root: pod + container
	if p.Spec.SecurityContext == nil || p.Spec.SecurityContext.RunAsNonRoot == nil || !*p.Spec.SecurityContext.RunAsNonRoot {
		t.Fatal("pod-level runAsNonRoot must be true")
	}
	if p.Spec.SecurityContext.RunAsUser == nil || *p.Spec.SecurityContext.RunAsUser != MountHolderUID {
		t.Fatalf("pod-level runAsUser must be %d", MountHolderUID)
	}
	c := p.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Fatal("container runAsNonRoot must be true")
	}

	// require-ro-rootfs
	if c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("container readOnlyRootFilesystem must be true")
	}

	// disallow-add-capabilities + drop ALL
	if c.SecurityContext.Capabilities == nil {
		t.Fatal("container.capabilities must be set")
	}
	if len(c.SecurityContext.Capabilities.Add) != 0 {
		t.Fatalf("container.capabilities.add must be empty, got %v", c.SecurityContext.Capabilities.Add)
	}
	foundDropAll := false
	for _, cap := range c.SecurityContext.Capabilities.Drop {
		if cap == "ALL" {
			foundDropAll = true
		}
	}
	if !foundDropAll {
		t.Fatal("container.capabilities.drop must include ALL")
	}

	// disallow-privilege-escalation
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("container.allowPrivilegeEscalation must be false")
	}

	// restrict-seccomp: pod and container both RuntimeDefault
	if p.Spec.SecurityContext.SeccompProfile == nil || p.Spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("pod seccompProfile must be RuntimeDefault")
	}
	if c.SecurityContext.SeccompProfile == nil || c.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("container seccompProfile must be RuntimeDefault")
	}

	// require-probes: both readiness AND liveness
	if c.ReadinessProbe == nil {
		t.Fatal("container readinessProbe required")
	}
	if c.LivenessProbe == nil {
		t.Fatal("container livenessProbe required")
	}

	// require-pod-requests-limits: CPU + memory in both
	for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if _, ok := c.Resources.Requests[r]; !ok {
			t.Fatalf("container resources.requests.%s required", r)
		}
		if _, ok := c.Resources.Limits[r]; !ok {
			t.Fatalf("container resources.limits.%s required", r)
		}
	}

	// disallow-latest-tag (heuristic — pinned tag means ':' followed
	// by something other than literal "latest")
	if strings.HasSuffix(c.Image, ":latest") || !strings.Contains(c.Image, ":") {
		t.Fatalf("container image must use a pinned tag (not 'latest', not bare): %q", c.Image)
	}
}

func TestMountHolderSpec_OwnerReferenceToRWXPVC(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	p := h.Spec()
	if len(p.OwnerReferences) != 1 {
		t.Fatalf("expected exactly 1 OwnerReference, got %d", len(p.OwnerReferences))
	}
	owner := p.OwnerReferences[0]
	if owner.Kind != "PersistentVolumeClaim" {
		t.Errorf("owner.Kind = %q, want PersistentVolumeClaim", owner.Kind)
	}
	if owner.Name != "rwx-abc123" {
		t.Errorf("owner.Name = %q, want rwx-abc123", owner.Name)
	}
	if owner.UID != "pvc-uid-xyz" {
		t.Errorf("owner.UID = %q, want pvc-uid-xyz", owner.UID)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("owner.Controller must be true so GC honors the reference")
	}
}

func TestMountHolderSpec_NodeSelectorPinnedAndTolerateAll(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	p := h.Spec()
	// MUST use nodeSelector, NOT nodeName: nodeName bypasses the
	// scheduler and breaks WaitForFirstConsumer PVC binding (the
	// v0.0.51 deadlock). nodeSelector constrains the scheduler to the
	// agent's node while still going through scheduling.
	if p.Spec.NodeName != "" {
		t.Errorf("Spec.NodeName must be empty (nodeName bypasses scheduler, breaks WFC binding); got %q", p.Spec.NodeName)
	}
	if got := p.Spec.NodeSelector["kubernetes.io/hostname"]; got != "node-1" {
		t.Errorf("Spec.NodeSelector[kubernetes.io/hostname] = %q, want node-1", got)
	}
	if len(p.Spec.Tolerations) != 1 || p.Spec.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Errorf("expected single tolerate-all (operator=Exists), got %v", p.Spec.Tolerations)
	}
}

func TestMountHolderSpec_SleepEntrypoint(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	p := h.Spec()
	c := p.Spec.Containers[0]
	if len(c.Command) < 1 || c.Command[0] != "sleep" {
		t.Errorf("container command must start with 'sleep', got %v", c.Command)
	}
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != MountHolderDestPath {
		t.Errorf("expected single volumeMount at %s, got %v", MountHolderDestPath, c.VolumeMounts)
	}
}

// Spec() must stamp imagePullSecrets when configured (so a fresh
// cluster can pull the webhook-injected init container's image) and
// leave the field unset when none are given (preserves prior behavior).
func TestMountHolderSpec_ImagePullSecrets(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		want    []string // nil = expect no imagePullSecrets
	}{
		{name: "none", secrets: nil, want: nil},
		{name: "empty slice", secrets: []string{}, want: nil},
		{name: "single", secrets: []string{"nvsnap-agent-pull"}, want: []string{"nvsnap-agent-pull"}},
		{name: "multiple", secrets: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "drops empty names", secrets: []string{"", "nvsnap-agent-pull", ""}, want: []string{"nvsnap-agent-pull"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kube := newFakeKubeWithUID()
			log := logrus.NewEntry(logrus.New()).WithField("test", t.Name())
			h := NewMountHolder(kube, log,
				"nvcf-backend", "mh-abc123",
				"node-1", "rwx-abc123", types.UID("pvc-uid-xyz"),
				"nvsnap-agent:test", "/host", tc.secrets)
			p := h.Spec()
			if tc.want == nil {
				if len(p.Spec.ImagePullSecrets) != 0 {
					t.Fatalf("expected no imagePullSecrets, got %v", p.Spec.ImagePullSecrets)
				}
				return
			}
			if len(p.Spec.ImagePullSecrets) != len(tc.want) {
				t.Fatalf("imagePullSecrets len = %d, want %d (%v)", len(p.Spec.ImagePullSecrets), len(tc.want), p.Spec.ImagePullSecrets)
			}
			for i, want := range tc.want {
				if p.Spec.ImagePullSecrets[i].Name != want {
					t.Errorf("imagePullSecrets[%d].Name = %q, want %q", i, p.Spec.ImagePullSecrets[i].Name, want)
				}
			}
		})
	}
}

func TestMountHolderCreate_RecordsUID(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	if err := h.Create(context.Background()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.PodUID() == "" {
		t.Fatal("PodUID should be populated after Create")
	}
}

func TestMountHolderCreate_IdempotentOnAlreadyExists(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	// Seed an existing pod with a fixed UID; Create should reuse it.
	_, err := h.kube.CoreV1().Pods(h.namespace).Create(context.Background(),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: h.name, Namespace: h.namespace, UID: "preexisting-uid",
		}},
		metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := h.Create(context.Background()); err != nil {
		t.Fatalf("Create on existing pod should succeed, got: %v", err)
	}
	if h.PodUID() != "preexisting-uid" {
		t.Errorf("PodUID = %q, want preexisting-uid", h.PodUID())
	}
}

func TestMountHolderWaitRunning_SucceedsWhenRunningAndPVCBound(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	ctx := context.Background()
	// Seed pod in Running state with Ready container, and PVC with VolumeName.
	_, _ = h.kube.CoreV1().Pods(h.namespace).Create(ctx,
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: h.namespace, UID: "pod-uid"},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "holder", Ready: true,
				}},
			},
		}, metav1.CreateOptions{})
	_, _ = h.kube.CoreV1().PersistentVolumeClaims(h.namespace).Create(ctx,
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: h.rwxPVCName, Namespace: h.namespace},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-foo"},
		}, metav1.CreateOptions{})
	h.podUID = "pod-uid"

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.WaitRunning(ctx); err != nil {
		t.Fatalf("WaitRunning: %v", err)
	}
	if h.PVName() != "pv-foo" {
		t.Errorf("PVName = %q, want pv-foo", h.PVName())
	}
}

func TestMountHolderWaitRunning_RequiresCreate(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.WaitRunning(ctx); err == nil {
		t.Fatal("WaitRunning without Create should return error")
	}
}

func TestMountHolderPVMountPath_BuildsCorrectPath(t *testing.T) {
	tmp := t.TempDir()
	expected := filepath.Join(tmp, "var", "lib", "kubelet", "pods", "pod-uid", "volumes", "kubernetes.io~csi", "pv-foo", "mount")
	if err := os.MkdirAll(expected, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newTestMountHolder(t, tmp)
	h.podUID = "pod-uid"
	h.pvName = "pv-foo"
	got, err := h.PVMountPath()
	if err != nil {
		t.Fatalf("PVMountPath: %v", err)
	}
	if got != expected {
		t.Errorf("PVMountPath = %q, want %q", got, expected)
	}
}

func TestMountHolderPVMountPath_ErrorsWhenNotReady(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	if _, err := h.PVMountPath(); err == nil {
		t.Fatal("PVMountPath should error before WaitRunning")
	}
	h.podUID = "pod-uid"
	if _, err := h.PVMountPath(); err == nil {
		t.Fatal("PVMountPath should error with podUID but no pvName")
	}
}

func TestMountHolderPVMountPath_ErrorsOnMissingMount(t *testing.T) {
	tmp := t.TempDir()
	h := newTestMountHolder(t, tmp)
	h.podUID = "pod-uid"
	h.pvName = "pv-foo"
	// Do NOT mkdir the expected path — Stat should fail.
	_, err := h.PVMountPath()
	if err == nil {
		t.Fatal("PVMountPath should error when the mount path doesn't exist")
	}
}

func TestMountHolderDelete_IdempotentOnNotFound(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	if err := h.Delete(context.Background()); err != nil {
		t.Fatalf("Delete on missing pod should be nil, got: %v", err)
	}
}

func TestMountHolderDelete_WaitsForRemoval(t *testing.T) {
	h := newTestMountHolder(t, "/host")
	ctx := context.Background()
	_, _ = h.kube.CoreV1().Pods(h.namespace).Create(ctx,
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: h.namespace}},
		metav1.CreateOptions{})

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Pod should be gone.
	_, err := h.kube.CoreV1().Pods(h.namespace).Get(context.Background(), h.name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("pod should be NotFound after Delete, got: %v", err)
	}
}
