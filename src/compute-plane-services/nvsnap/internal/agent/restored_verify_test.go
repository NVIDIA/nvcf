// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// restoredPod is a pod restored at restored, Ready since readyAt (zero:
// not Ready).
func restoredPod(uid string, restored, readyAt time.Time) corev1.Pod {
	p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: uid, UID: types.UID(uid),
		Annotations: map[string]string{webhook.CRIURestoredAnnotation: restored.UTC().Format(time.RFC3339)}}}
	cond := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse}
	if !readyAt.IsZero() {
		cond = corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(readyAt)}
	}
	p.Status.Conditions = []corev1.PodCondition{cond}
	return p
}

func TestRestoreServed(t *testing.T) {
	now := time.Now()
	uids := []types.UID{"a", "b"}
	long := now.Add(-restoredServedFor - time.Minute)
	for _, tc := range []struct {
		name      string
		pods      []corev1.Pod
		served    bool
		failsWith string
	}{
		{"both Ready long enough", []corev1.Pod{restoredPod("a", long, long), restoredPod("b", long, long)}, true, ""},
		{"one Ready too briefly", []corev1.Pod{restoredPod("a", long, long), restoredPod("b", long, now.Add(-time.Minute))}, false, ""},
		{"one not Ready, before the deadline", []corev1.Pod{restoredPod("a", long, long), restoredPod("b", now.Add(-5*time.Minute), time.Time{})}, false, ""},
		{"one not Ready past the deadline", []corev1.Pod{restoredPod("a", long, long),
			restoredPod("b", now.Add(-restoredServeDeadline-time.Minute), time.Time{})}, false, "pod b was not Ready"},
		{"one Ready again too late to serve in time", []corev1.Pod{restoredPod("a", long, long),
			restoredPod("b", now.Add(-restoredServeDeadline), now.Add(-time.Minute))}, false, "pod b was not Ready"},
		{"one gone, past the deadline: left to verify", []corev1.Pod{restoredPod("a", now.Add(-restoredServeDeadline-time.Minute), time.Time{})}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served, why := restoreServed(tc.pods, uids, now)
			if served != tc.served || (tc.failsWith == "") != (why == "") || !strings.Contains(why, tc.failsWith) {
				t.Errorf("restoreServed = %v, %q; want %v, %q", served, why, tc.served, tc.failsWith)
			}
		})
	}
}

func TestRestoredPodUIDs(t *testing.T) {
	got := restoredPodUIDs("u1/main,u2/engine,")
	if len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Errorf("got %v", got)
	}
	if restoredPodUIDs("") != nil {
		t.Error("an empty value names pods")
	}
}

// verifyFixture is groupFixture with its capture being verified by a
// restore that made kimi-0 and kimi-1, restored at restored and Ready
// since readyAt.
func verifyFixture(t *testing.T, restored, readyAt time.Time) (*criuAutoRestorer, *fake.Clientset, *corev1.Pod) {
	t.Helper()
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	cms := kc.CoreV1().ConfigMaps("nvsnap-system")
	cm, err := cms.Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["state"], cm.Data["sourceNamespace"] = criuGroupStateVerifying, "fn"
	cm.Data[groupRestoredPodsKey] = "kimi-0/engine,kimi-1/engine"
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kimi-0", "kimi-1"} {
		p, err := kc.CoreV1().Pods("fn").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r := restoredPod(name, restored, readyAt)
		p.Annotations[webhook.CRIURestoredAnnotation] = r.Annotations[webhook.CRIURestoredAnnotation]
		p.Status.Conditions = r.Status.Conditions
		if _, err := kc.CoreV1().Pods("fn").Update(ctx, p, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if name == "kimi-0" {
			leader = p
		}
	}
	return c, kc, leader
}

func groupRecord(t *testing.T, kc *fake.Clientset) map[string]string {
	t.Helper()
	cm, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(context.Background(), criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return cm.Data
}

// Every pod of the restore Ready long enough verifies the capture, for
// every namespace.
func TestConsiderVerify_ServedVerifies(t *testing.T) {
	long := time.Now().Add(-restoredServedFor - time.Minute)
	c, kc, leader := verifyFixture(t, long, long)
	c.considerVerify(context.Background(), leader, restoredRef{container: "engine", group: "k1"})
	if d := groupRecord(t, kc); d["state"] != criuGroupStateComplete || d["verifiedAt"] == "" || d["sourceNamespace"] != "" {
		t.Errorf("record = %v, want the capture verified", d)
	}
}

// Pods restored but Ready only briefly leave the capture being verified.
func TestConsiderVerify_NotYetServed(t *testing.T) {
	c, kc, leader := verifyFixture(t, time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute))
	c.considerVerify(context.Background(), leader, restoredRef{container: "engine", group: "k1"})
	if d := groupRecord(t, kc); d["state"] != criuGroupStateVerifying {
		t.Errorf("state = %q, want it still being verified", d["state"])
	}
	if got := remainingPods(t, kc); len(got) != 3 {
		t.Errorf("pods left = %v, want all", got)
	}
}

// A restore whose pods never served by the deadline fails: its capture is
// a failed restore and its pods are deleted.
func TestConsiderVerify_NeverServedFails(t *testing.T) {
	c, kc, leader := verifyFixture(t, time.Now().Add(-restoredServeDeadline-time.Minute), time.Time{})
	c.considerVerify(context.Background(), leader, restoredRef{container: "engine", group: "k1"})
	d := groupRecord(t, kc)
	if d["state"] != criuGroupStateRestoreFailed || !strings.Contains(d["reason"], "never served") {
		t.Errorf("record = %v, want a failed restore that never served", d)
	}
	if got := remainingPods(t, kc); len(got) != 1 || got[0] != "other-0" {
		t.Errorf("pods left = %v, want only the other instance's", got)
	}
}

// Only the first pod the record names decides, so two nodes' agents do
// not both act.
func TestConsiderVerify_OnlyTheFirstPodActs(t *testing.T) {
	long := time.Now().Add(-restoredServedFor - time.Minute)
	c, kc, _ := verifyFixture(t, long, long)
	second, err := kc.CoreV1().Pods("fn").Get(context.Background(), "kimi-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c.considerVerify(context.Background(), second, restoredRef{container: "engine", group: "k1"})
	if d := groupRecord(t, kc); d["state"] != criuGroupStateVerifying {
		t.Errorf("state = %q after the second pod's check, want it unchanged", d["state"])
	}
}

// A complete capture is not judged again.
func TestConsiderVerify_IgnoresCompleteCapture(t *testing.T) {
	c, kc, leader := verifyFixture(t, time.Now().Add(-restoredServeDeadline-time.Minute), time.Time{})
	ctx := context.Background()
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	cm.Data["state"] = criuGroupStateComplete
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.considerVerify(ctx, leader, restoredRef{container: "engine", group: "k1"})
	if d := groupRecord(t, kc); d["state"] != criuGroupStateComplete {
		t.Errorf("state = %q, want complete", d["state"])
	}
	if got := remainingPods(t, kc); len(got) != 3 {
		t.Errorf("pods left = %v, want all", got)
	}
}
