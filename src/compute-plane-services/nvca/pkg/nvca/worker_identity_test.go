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

package nvca

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
)

func TestWorkerSAName(t *testing.T) {
	if got := workerSAName("inst-abc123"); got != "nvcf-worker-inst-abc123" {
		t.Errorf("workerSAName = %q, want %q", got, "nvcf-worker-inst-abc123")
	}
}

func TestInjectWorkerIdentity(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "inference"},
				{Name: "utils"},
			},
		},
	}
	clusterID := "cl-test123"
	podName := "inst-789"

	injectWorkerIdentity(pod, clusterID, podName)

	if pod.Spec.ServiceAccountName != "nvcf-worker-inst-789" {
		t.Errorf("ServiceAccountName = %q", pod.Spec.ServiceAccountName)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("AutomountServiceAccountToken should be false")
	}

	// Volume injected
	var found bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == workerTokenVolumeName {
			found = true
			proj := v.VolumeSource.Projected
			if proj == nil || len(proj.Sources) == 0 {
				t.Error("projected volume has no sources")
			} else {
				aud := proj.Sources[0].ServiceAccountToken.Audience
				if aud != "nvcf-icms:cl-test123" {
					t.Errorf("audience = %q, want %q", aud, "nvcf-icms:cl-test123")
				}
			}
		}
	}
	if !found {
		t.Errorf("volume %q not injected", workerTokenVolumeName)
	}

	// Env vars and volume mounts injected into all containers
	for _, c := range pod.Spec.Containers {
		var hasMount, hasTokenEnv, hasSourceEnv bool
		for _, m := range c.VolumeMounts {
			if m.Name == workerTokenVolumeName {
				hasMount = true
			}
		}
		for _, e := range c.Env {
			if e.Name == WorkerTokenFilePathEnvKey {
				hasTokenEnv = true
			}
			if e.Name == WorkerIdentitySourceEnvKey && e.Value == WorkerIdentitySourcePSAT {
				hasSourceEnv = true
			}
		}
		if !hasMount {
			t.Errorf("container %q missing volume mount", c.Name)
		}
		if !hasTokenEnv {
			t.Errorf("container %q missing %s", c.Name, WorkerTokenFilePathEnvKey)
		}
		if !hasSourceEnv {
			t.Errorf("container %q missing %s", c.Name, WorkerIdentitySourceEnvKey)
		}
	}
}

func TestEnsureWorkerServiceAccount_Create(t *testing.T) {
	fakeK8s := fake.NewSimpleClientset()
	clients := &kubeclients.KubeClients{K8s: fakeK8s}
	ctx := context.Background()

	sa, err := ensureWorkerServiceAccount(ctx, clients, "nvcf-backend", "inst-001")
	if err != nil {
		t.Fatalf("ensureWorkerServiceAccount: %v", err)
	}
	if sa.Name != "nvcf-worker-inst-001" {
		t.Errorf("SA name = %q", sa.Name)
	}
	if sa.Namespace != "nvcf-backend" {
		t.Errorf("SA namespace = %q", sa.Namespace)
	}

	// Idempotent: second call should return existing SA without error.
	sa2, err := ensureWorkerServiceAccount(ctx, clients, "nvcf-backend", "inst-001")
	if err != nil {
		t.Fatalf("second ensureWorkerServiceAccount: %v", err)
	}
	if sa2.Name != sa.Name {
		t.Errorf("idempotent SA name mismatch: %q vs %q", sa2.Name, sa.Name)
	}
}

func TestBuildWorkerAuth_NoWorkerSA(t *testing.T) {
	fakeK8s := fake.NewSimpleClientset()
	clients := &kubeclients.KubeClients{K8s: fakeK8s}
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-001", UID: types.UID("pod-uid-1")},
		Spec:       corev1.PodSpec{ServiceAccountName: "default"},
	}

	if got := buildWorkerAuth(ctx, clients, "nvcf-backend", pod); got != nil {
		t.Errorf("expected nil WorkerAuth for non-worker SA, got %+v", got)
	}
}

func TestEnsureWorkerRBAC_Create(t *testing.T) {
	fakeK8s := fake.NewSimpleClientset()
	clients := &kubeclients.KubeClients{K8s: fakeK8s}
	ctx := context.Background()

	if err := ensureWorkerRBAC(ctx, clients, "nvcf-backend", "inst-001"); err != nil {
		t.Fatalf("ensureWorkerRBAC: %v", err)
	}

	role, err := fakeK8s.RbacV1().Roles("nvcf-backend").Get(ctx, "nvcf-worker-inst-001", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Role: %v", err)
	}
	if len(role.Rules) != 0 {
		t.Errorf("expected empty Rules, got %v", role.Rules)
	}

	rb, err := fakeK8s.RbacV1().RoleBindings("nvcf-backend").Get(ctx, "nvcf-worker-inst-001", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get RoleBinding: %v", err)
	}
	if rb.RoleRef.Name != "nvcf-worker-inst-001" {
		t.Errorf("RoleRef.Name = %q", rb.RoleRef.Name)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Name != "nvcf-worker-inst-001" {
		t.Errorf("unexpected Subjects: %v", rb.Subjects)
	}

	// Idempotent: second call must not error even though objects already exist.
	if err := ensureWorkerRBAC(ctx, clients, "nvcf-backend", "inst-001"); err != nil {
		t.Fatalf("second ensureWorkerRBAC: %v", err)
	}
}

func TestEnsureWorkerRBAC_RoleHasEmptyRules(t *testing.T) {
	fakeK8s := fake.NewSimpleClientset()
	clients := &kubeclients.KubeClients{K8s: fakeK8s}
	ctx := context.Background()

	if err := ensureWorkerRBAC(ctx, clients, "nvcf-backend", "inst-002"); err != nil {
		t.Fatalf("ensureWorkerRBAC: %v", err)
	}

	role, err := fakeK8s.RbacV1().Roles("nvcf-backend").Get(ctx, "nvcf-worker-inst-002", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Role: %v", err)
	}
	// nil and empty slice are both acceptable - neither grants any permissions.
	if len(role.Rules) != 0 {
		t.Errorf("Role.Rules should be empty, got: %v", role.Rules)
	}
}

func TestCleanupWorkerIdentity(t *testing.T) {
	saName := "nvcf-worker-inst-003"
	ns := "nvcf-backend"
	fakeK8s := fake.NewSimpleClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ns}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ns}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ns}},
	)
	clients := &kubeclients.KubeClients{K8s: fakeK8s}
	ctx := context.Background()

	cleanupWorkerIdentity(ctx, clients, ns, "inst-003")

	if _, err := fakeK8s.CoreV1().ServiceAccounts(ns).Get(ctx, saName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("SA should be deleted, got err: %v", err)
	}
	if _, err := fakeK8s.RbacV1().Roles(ns).Get(ctx, saName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("Role should be deleted, got err: %v", err)
	}
	if _, err := fakeK8s.RbacV1().RoleBindings(ns).Get(ctx, saName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("RoleBinding should be deleted, got err: %v", err)
	}
}

func TestCleanupWorkerIdentity_ToleratesNotFound(t *testing.T) {
	// Nothing pre-exists - cleanup should not panic or error.
	fakeK8s := fake.NewSimpleClientset()
	clients := &kubeclients.KubeClients{K8s: fakeK8s}
	cleanupWorkerIdentity(context.Background(), clients, "nvcf-backend", "inst-004")
}

func TestBuildWorkerAuth_WithWorkerSA(t *testing.T) {
	fakeK8s := fake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-worker-inst-001",
			Namespace: "nvcf-backend",
			UID:       types.UID("sa-uid-abc"),
		},
	})
	clients := &kubeclients.KubeClients{K8s: fakeK8s}
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-001", UID: types.UID("pod-uid-1")},
		Spec:       corev1.PodSpec{ServiceAccountName: "nvcf-worker-inst-001"},
	}

	auth := buildWorkerAuth(ctx, clients, "nvcf-backend", pod)
	if auth == nil {
		t.Fatal("expected non-nil WorkerAuth")
	}
	if auth.Sub != "system:serviceaccount:nvcf-backend:nvcf-worker-inst-001" {
		t.Errorf("Sub = %q", auth.Sub)
	}
	if auth.SAuid != "sa-uid-abc" {
		t.Errorf("SAuid = %q", auth.SAuid)
	}
	if len(auth.WorkerIdentifiers) != 1 {
		t.Fatalf("WorkerIdentifiers len = %d", len(auth.WorkerIdentifiers))
	}
	if auth.WorkerIdentifiers[0].Name != "inst-001" {
		t.Errorf("WorkerIdentifier.Name = %q", auth.WorkerIdentifiers[0].Name)
	}
	if auth.WorkerIdentifiers[0].UID != "pod-uid-1" {
		t.Errorf("WorkerIdentifier.UID = %q", auth.WorkerIdentifiers[0].UID)
	}
}
