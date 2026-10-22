// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// gatedPlaceholder is a restore placeholder whose readiness waits on the
// restored condition; restored adds the annotation markRestored sets.
func gatedPlaceholder(name string, restored bool) *corev1.Pod {
	pod := restoredLooking(name, "tenant", "t0")
	pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: webhook.CRIURestoredCondition}}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}}
	if !restored {
		delete(pod.Annotations, webhook.CRIURestoredAnnotation)
	}
	return pod
}

// conditionSet runs considerRestoredCondition on pod, waits for it, and
// reports whether the stored pod's condition is True.
func conditionSet(t *testing.T, c *criuAutoRestorer, pod *corev1.Pod) bool {
	t.Helper()
	ctx := context.Background()
	c.considerRestoredCondition(ctx, pod)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, busy := c.attempted.Load("condition/" + string(pod.UID)); !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the restored-condition check did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, err := c.a.kubeClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return restoredConditionTrue(got)
}

// markRestored lets the pod's readiness pass, keeping the pod's other
// conditions.
func TestMarkRestored_SetsTheCondition(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	pod := gatedPlaceholder("p", false)
	if _, err := kc.CoreV1().Pods("tenant").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.a.markRestored(ctx, "tenant", "p", logrus.NewEntry(logrus.New()))
	got, err := kc.CoreV1().Pods("tenant").Get(ctx, "p", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !restoredConditionTrue(got) {
		t.Errorf("restored condition not True: %v", got.Status.Conditions)
	}
	kept := false
	for _, cond := range got.Status.Conditions {
		kept = kept || cond.Type == corev1.ContainersReady
	}
	if len(got.Status.Conditions) != 2 || !kept {
		t.Errorf("the pod's other conditions were not kept: %v", got.Status.Conditions)
	}
	if got.Annotations[webhook.CRIURestoredAnnotation] == "" {
		t.Error("the pod is not marked restored")
	}
	if err := c.a.setRestoredCondition(ctx, "tenant", "gone"); err != nil {
		t.Errorf("a deleted pod is no error: %v", err)
	}
}

// A restored pod found without its condition gets it only when the agent's
// own records say it restored the pod: the annotation is the pod's to
// write.
func TestConsiderRestoredCondition(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	create := func(p *corev1.Pod) *corev1.Pod {
		t.Helper()
		if _, err := kc.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		return p
	}

	restored := create(gatedPlaceholder("restored", true))
	if err := c.recordRestore(restored.UID, restoreRecord{Checkpoint: "t0", Container: "engine", Namespace: "tenant", Pod: "restored"}); err != nil {
		t.Fatal(err)
	}
	if !conditionSet(t, c, restored) {
		t.Error("a pod the agent restored did not get its condition")
	}

	claimed := create(gatedPlaceholder("claimed", true))
	if conditionSet(t, c, claimed) {
		t.Error("a pod that only claims a restore in its annotation got the condition")
	}

	pending := create(gatedPlaceholder("pending", false))
	if err := c.recordRestore(pending.UID, restoreRecord{Checkpoint: "t0", Container: "engine", Namespace: "tenant", Pod: "pending"}); err != nil {
		t.Fatal(err)
	}
	if conditionSet(t, c, pending) {
		t.Error("a pod not yet marked restored got the condition")
	}

	ungated := gatedPlaceholder("ungated", true)
	ungated.Spec.ReadinessGates = nil
	create(ungated)
	if err := c.recordRestore(ungated.UID, restoreRecord{Checkpoint: "t0", Container: "engine", Namespace: "tenant", Pod: "ungated"}); err != nil {
		t.Fatal(err)
	}
	if conditionSet(t, c, ungated) {
		t.Error("a pod admitted without the gate got the condition")
	}
}
