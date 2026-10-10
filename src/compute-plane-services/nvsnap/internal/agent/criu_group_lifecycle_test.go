// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func TestCRIUGroupRecaptures(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	for _, tc := range []struct {
		name string
		data map[string]string
		want bool
	}{
		{"complete", map[string]string{"state": "complete", "captures": "1"}, false},
		{"capturing", map[string]string{"state": "capturing", "captures": "1", "startedAt": ago(5 * time.Minute)}, false},
		{"capture stopped without finishing", map[string]string{"state": "capturing", "captures": "1", "startedAt": ago(3 * time.Hour)}, true},
		{"capture failed", map[string]string{"state": "failed", "captures": "1"}, true},
		{"restore failed", map[string]string{"state": "restore-failed", "captures": "2"}, true},
		{"out of attempts", map[string]string{"state": "restore-failed", "captures": "3"}, false},
		{"record before the counter", map[string]string{"state": "failed"}, true},
	} {
		if got := criuGroupRecaptures(tc.data, now); got != tc.want {
			t.Errorf("%s: recaptures = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseCRIUGroup_StalledRestoresStartCold(t *testing.T) {
	data := map[string]string{"state": "complete", "size": "1", "checkpoint.0": "a", "stalls": "2"}
	if _, ok := parseCRIUGroup("k", data); !ok {
		t.Fatal("two stalls must still restore")
	}
	data["stalls"] = "3"
	if _, ok := parseCRIUGroup("k", data); ok {
		t.Error("a capture whose restores keep stalling must start cold")
	}
}

func groupFixture(t *testing.T) (*criuAutoRestorer, *fake.Clientset, *corev1.Pod) {
	t.Helper()
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New()}
	c := a.criuRestorer()
	ctx := context.Background()
	record := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName("k1"), Namespace: "nvsnap-system"},
		Data: map[string]string{"state": "complete", "size": "2", "captures": "1", "checkpoint.0": "c0", "checkpoint.1": "c1"}}
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, record, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var leader *corev1.Pod
	for i, name := range []string{"kimi-0", "kimi-1"} {
		p := stsRank(name, []string{"0", "1"}[i], "2", "sts-a", false)
		p.Annotations[webhook.CRIUGroupAnnotation] = "k1"
		p.Annotations[webhook.CRIURestoreAnnotation] = []string{"c0", "c1"}[i]
		created, err := kc.CoreV1().Pods("fn").Create(ctx, &p, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			leader = created
		}
	}
	other := stsRank("other-0", "0", "1", "sts-b", true)
	if _, err := kc.CoreV1().Pods("fn").Create(ctx, &other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return c, kc, leader
}

func remainingPods(t *testing.T, kc *fake.Clientset) []string {
	t.Helper()
	pods, err := kc.CoreV1().Pods("fn").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range pods.Items {
		names = append(names, p.Name)
	}
	return names
}

func TestFailGroup_StallKeepsCheckpointsAndDeletesEveryRank(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	c.failGroup(ctx, leader, "k1", false, errors.New("placeholders not running"), logrus.NewEntry(logrus.New()))
	if c.Blocked(ctx, "c0") || c.Blocked(ctx, "c1") {
		t.Error("a restore that never ran blocked its checkpoints")
	}
	if got := remainingPods(t, kc); len(got) != 1 || got[0] != "other-0" {
		t.Errorf("pods left = %v, want only the other instance", got)
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if cm.Data["state"] != "complete" || cm.Data["stalls"] != "1" {
		t.Errorf("record = %v, want complete with one stall", cm.Data)
	}
}

func TestFailGroup_RestoreFailureBlocksAndAllowsRecapture(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	c.failGroup(ctx, leader, "k1", true, errors.New("criu restore: exit status 1"), logrus.NewEntry(logrus.New()))
	if !c.Blocked(ctx, "c0") || !c.Blocked(ctx, "c1") {
		t.Error("a failed restore must block every checkpoint of the group")
	}
	if got := remainingPods(t, kc); len(got) != 1 {
		t.Errorf("pods left = %v, want only the other instance", got)
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if cm.Data["state"] != criuGroupStateRestoreFailed || !criuGroupRecaptures(cm.Data, time.Now()) {
		t.Errorf("record = %v, want a restore-failed record that may be captured again", cm.Data)
	}
	// The next Ready instance takes the record over, once.
	p := stsRank("kimi-0", "0", "2", "sts-c", true)
	first, err := c.claimGroupRecord(ctx, "k1", &p, 2)
	if err != nil || first == nil || first.Data["captures"] != "2" || first.Data["state"] != "capturing" {
		t.Fatalf("takeover = %v, %v", first, err)
	}
	if again, err := c.claimGroupRecord(ctx, "k1", &p, 2); err != nil || again != nil {
		t.Errorf("a second claim while capturing = %v, %v; want none", again, err)
	}
}

func TestFailGroup_SparesRestoredPods(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	p, _ := kc.CoreV1().Pods("fn").Get(ctx, "kimi-1", metav1.GetOptions{})
	p.Annotations[webhook.CRIURestoredAnnotation] = "true"
	if _, err := kc.CoreV1().Pods("fn").Update(ctx, p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.failGroup(ctx, leader, "k1", false, errors.New("x"), logrus.NewEntry(logrus.New()))
	got := remainingPods(t, kc)
	if len(got) != 2 {
		t.Errorf("pods left = %v; a restored pod must never be deleted", got)
	}
}

func TestAllRefusedAsRestored(t *testing.T) {
	refused := GroupRestoreMemberResult{Error: "restore: criu-v2: target pod is running a GPU workload (pid 7): " + errNotPlaceholder.Error()}
	if !allRefusedAsRestored([]GroupRestoreMemberResult{refused, refused}) {
		t.Error("every member already restored must read as done")
	}
	if allRefusedAsRestored([]GroupRestoreMemberResult{refused, {Error: "criu restore: exit status 1"}}) {
		t.Error("a member that failed to restore must fail the group")
	}
}

// The record was captured again since these pods were admitted: their
// failure blocks only their own checkpoints and leaves the new capture be.
func TestFailGroup_RecordOfAnotherCapture(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	cm.Data["checkpoint.0"], cm.Data["checkpoint.1"] = "n0", "n1"
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.failGroup(ctx, leader, "k1", true, errors.New("criu restore: exit status 1"), logrus.NewEntry(logrus.New()))
	if !c.Blocked(ctx, "c0") || !c.Blocked(ctx, "c1") {
		t.Error("the pods' own checkpoints must be blocked")
	}
	if c.Blocked(ctx, "n0") || c.Blocked(ctx, "n1") {
		t.Error("a newer capture's checkpoints were blocked")
	}
	cm, _ = kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if cm.Data["state"] != "complete" {
		t.Errorf("the newer capture's record became %q", cm.Data["state"])
	}
}

func TestRecordRestored_ClearsStallsOfItsCapture(t *testing.T) {
	d := map[string]string{"state": "complete", "checkpoint.0": "c0", "stalls": "2"}
	if recordRestored(d, "other") || d["stalls"] != "2" {
		t.Error("a restore of another capture cleared the stalls")
	}
	if !recordRestored(d, "c0") || d["stalls"] != "" {
		t.Error("a restore did not clear its capture's stalls")
	}
}
