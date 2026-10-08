// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func criuPlaceholderPod(phase corev1.PodPhase, running bool) *corev1.Pod {
	st := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
	if running {
		st = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-0", Namespace: "sr-1", UID: "u1", Annotations: map[string]string{
			webhook.CRIURestoreAnnotation: "abc__20261008-101010", webhook.CRIURestoreContainerAnnotation: "inference"}},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "utils", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: "inference", State: st},
		}},
	}
}

func TestCRIURestoreTarget_OnlyOnceTheContainerRuns(t *testing.T) {
	if id, c, ok := criuRestoreTarget(criuPlaceholderPod(corev1.PodRunning, true)); !ok || id != "abc__20261008-101010" || c != "inference" {
		t.Errorf("running placeholder: %q %q %v", id, c, ok)
	}
	for name, p := range map[string]*corev1.Pod{
		"pending":           criuPlaceholderPod(corev1.PodPending, false),
		"engine not up yet": criuPlaceholderPod(corev1.PodRunning, false),
		"plain pod":         {Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	} {
		if _, _, ok := criuRestoreTarget(p); ok {
			t.Errorf("%s: restore would start", name)
		}
	}
	deleting := criuPlaceholderPod(corev1.PodRunning, true)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	if _, _, ok := criuRestoreTarget(deleting); ok {
		t.Error("a pod being deleted must not be restored into")
	}
}

func TestCRIUReservePIDArgs(t *testing.T) {
	want := []string{"-t", "42", "-p", "--", "/bin/sh", "-c", "echo 100000 > /proc/sys/kernel/ns_last_pid"}
	if got := criuReservePIDArgs(42); !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v (pid namespace only; above the reserved floor)", got, want)
	}
}

func TestCRIUIsDefault(t *testing.T) {
	for v, want := range map[string]bool{"criu": true, "CRIU": true, "cachedir": false, "": false} {
		t.Setenv("NVSNAP_DEFAULT_CAPTURE_PATH", v)
		if got := criuIsDefault(); got != want {
			t.Errorf("NVSNAP_DEFAULT_CAPTURE_PATH=%q: criuIsDefault = %v", v, got)
		}
	}
}

func TestCRIURestorer_FailedCheckpointsAreBlocked(t *testing.T) {
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New()}
	c := a.criuRestorer()
	ctx := context.Background()
	if c.Blocked(ctx, "c1") {
		t.Fatal("blocked before any failure")
	}
	if err := c.markFailed(ctx, "c1", errors.New("restore failed: exit status 1")); err != nil {
		t.Fatal(err)
	}
	if err := c.markFailed(ctx, "c2", errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	if !c.Blocked(ctx, "c1") || !c.Blocked(ctx, "c2") || c.Blocked(ctx, "c3") {
		t.Error("blocked list wrong after two failures")
	}
	cm, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuRestoreFailedConfigMap, metav1.GetOptions{})
	if err != nil || len(cm.Data) != 2 {
		t.Fatalf("failed-checkpoint list = %v, %v", cm, err)
	}
	// Another agent's failure shows up once the cache expires.
	cm.Data["c3"] = time.Now().UTC().Format(time.RFC3339) + " elsewhere"
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.blockedAt = time.Now().Add(-2 * criuBlockedCacheTTL)
	c.mu.Unlock()
	if !c.Blocked(ctx, "c3") {
		t.Error("a failure recorded by another agent is not seen after the cache expires")
	}
}
