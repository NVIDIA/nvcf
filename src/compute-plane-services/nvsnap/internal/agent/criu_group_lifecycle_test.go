// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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
		{"being verified", map[string]string{"state": "verifying", "captures": "1", "sourceNamespace": "fn"}, false},
		{"its verification never ran", map[string]string{"state": "verifying", "captures": "1", "stalls": "3"}, true},
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

// The first restore of a capture being verified makes it complete, for
// every instance; a restore of another capture changes nothing.
func TestRecordRestored_VerifiesItsCapture(t *testing.T) {
	d := map[string]string{"state": "verifying", "sourceNamespace": "fn", "checkpoint.0": "c0", "checkpoint.1": "c1"}
	if recordRestored(d, "other") || d["state"] != "verifying" {
		t.Fatal("a restore of another capture verified this one")
	}
	if !recordRestored(d, "c0") || d["state"] != "complete" || d["sourceNamespace"] != "" || d["verifiedAt"] == "" {
		t.Fatalf("record = %v, want complete and verified", d)
	}
	if recordRestored(d, "c0") {
		t.Error("a later restore changed a verified record with nothing to clear")
	}
}

// A capture being verified is restored only into its own instance's
// namespace; once verified, into any.
func TestLookupCRIUGroupFor_VerifyingOnlyItsNamespace(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New()}
	for _, ns := range []string{"fn-a", "fn-b"} {
		if _, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns,
			Labels: map[string]string{"function-version-id": "fv-verify"}}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := a.criuGroupScope(ctx, "fn-a")
	if err != nil {
		t.Fatal(err)
	}
	key := criuGroupKey("cache://w", scope)
	record := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key), Namespace: "nvsnap-system"},
		Data: map[string]string{"state": "verifying", "sourceNamespace": "fn-a", "size": "1", "checkpoint.0": "c0"}}
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, record, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if g, ok := a.lookupCRIUGroupFor(ctx, "fn-a", "cache://w"); !ok || g.VerifyNamespace != "fn-a" {
		t.Fatalf("its own namespace: %+v %v", g, ok)
	}
	if _, ok := a.lookupCRIUGroupFor(ctx, "fn-b", "cache://w"); ok {
		t.Fatal("another instance restored from a capture not verified yet")
	}
	record.Data["state"] = "complete"
	delete(record.Data, "sourceNamespace")
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, record, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if g, ok := a.lookupCRIUGroupFor(ctx, "fn-b", "cache://w"); !ok || g.VerifyNamespace != "" {
		t.Fatalf("a verified capture is for every instance: %+v %v", g, ok)
	}
	if _, ok := parseCRIUGroup("k", map[string]string{"state": "verifying", "size": "1", "checkpoint.0": "c0"}); ok {
		t.Error("a capture being verified without its namespace is usable")
	}
}

// An agent restarted during a capture of an instance with a rank on its
// node fails the capture and replaces the instance; captures elsewhere and
// captures begun since it started are left alone.
func TestRecoverInterruptedCaptures(t *testing.T) {
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New(), config: Config{NodeName: "n1"}}
	c := a.criuRestorer()
	ctx := context.Background()
	started := time.Now()
	rec := func(key, source string, at time.Time) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key), Namespace: "nvsnap-system",
			Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
			Data: map[string]string{"state": "capturing", "source": source, "startedAt": at.UTC().Format(time.RFC3339)}}
		if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	pod := func(name, owner, node string) {
		p := stsRank(name, "0", "2", types.UID(owner), true)
		p.Spec.NodeName = node
		if _, err := kc.CoreV1().Pods("fn").Create(ctx, &p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	pod("here-0", "sts-a", "n2")
	pod("here-1", "sts-a", "n1") // a rank on this node
	pod("away-0", "sts-b", "n3")
	pod("new-0", "sts-c", "n1")
	rec("here", "fn/here-0", started.Add(-time.Minute))
	rec("away", "fn/away-0", started.Add(-time.Minute))
	rec("new", "fn/new-0", started.Add(time.Minute))
	c.recoverInterruptedCaptures(ctx, started)

	left := map[string]bool{}
	for _, n := range remainingPods(t, kc) {
		left[n] = true
	}
	if left["here-0"] || left["here-1"] || !left["away-0"] || !left["new-0"] {
		t.Errorf("pods left = %v; want the interrupted instance replaced, the others kept", left)
	}
	for key, want := range map[string]string{"here": "failed", "away": "capturing", "new": "capturing"} {
		cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
		if cm.Data["state"] != want {
			t.Errorf("record %s = %q, want %q", key, cm.Data["state"], want)
		}
	}
}

// A replacement pod is admitted as soon as its predecessor is gone, so the
// checkpoints must be blocked before any pod of the group is deleted.
func TestFailGroup_BlocksBeforeDeleting(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	var deletedUnblocked []string
	kc.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		name := action.(k8stesting.DeleteAction).GetName()
		obj, err := kc.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), c.a.criuFailedNamespace(), criuRestoreFailedConfigMap)
		if cm, ok := obj.(*corev1.ConfigMap); err != nil || !ok || cm.Data["c0"] == "" || cm.Data["c1"] == "" {
			deletedUnblocked = append(deletedUnblocked, name)
		}
		return false, nil, nil
	})
	c.failGroup(ctx, leader, "k1", true, errors.New("criu restore: exit status 1"), logrus.NewEntry(logrus.New()))
	if len(deletedUnblocked) > 0 {
		t.Errorf("pods %v deleted before their checkpoints were blocked", deletedUnblocked)
	}
	if got := remainingPods(t, kc); len(got) != 1 {
		t.Errorf("pods left = %v, want only the other instance", got)
	}
}

// An address collision replaces only the target pods it names, counts the
// replacement on the capture's record, and past the bound gives up like a
// restore that did not run: every pod goes, the checkpoints stay.
func TestReplaceColliding(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := logrus.NewEntry(logrus.New())
	record := func() map[string]string {
		cm, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return cm.Data
	}
	c.replaceColliding(ctx, leader, "k1", &addressCollision{Pods: []string{"fn/kimi-1"}}, log)
	if got := remainingPods(t, kc); !slices.Contains(got, "kimi-0") || slices.Contains(got, "kimi-1") {
		t.Fatalf("pods left = %v, want the leader without kimi-1", got)
	}
	if d := record(); d["replacements"] != "1" || d["state"] != "complete" {
		t.Fatalf("record = %v, want one replacement and the capture still complete", d)
	}
	for i := 0; i < criuGroupMaxReplacements; i++ {
		c.replaceColliding(ctx, leader, "k1", &addressCollision{Pods: []string{"fn/kimi-0"}}, log)
	}
	if got := remainingPods(t, kc); slices.Contains(got, "kimi-0") {
		t.Errorf("pods left = %v after the bound, want the instance gone", got)
	}
	if d := record(); d["state"] != "complete" || d["stalls"] != "1" {
		t.Errorf("record = %v, want the capture kept and one stall", d)
	}
	if !recordRestored(record(), "c0") {
		t.Error("a restore did not clear the replacements")
	}
}
