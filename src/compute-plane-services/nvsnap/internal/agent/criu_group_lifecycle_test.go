// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
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
		{"capture failed under this agent version", map[string]string{"state": "failed", "captures": "1", "agentBundle": "v2"}, false},
		{"capture failed under another agent version", map[string]string{"state": "failed", "captures": "1", "agentBundle": "v1"}, true},
		{"capture failed, recorded before the version", map[string]string{"state": "failed", "captures": "1"}, true},
		{"capture failed under another version, out of attempts", map[string]string{"state": "failed", "captures": "3", "agentBundle": "v1"}, false},
		{"restore failed", map[string]string{"state": "restore-failed", "captures": "2", "agentBundle": "v2"}, true},
		{"out of attempts", map[string]string{"state": "restore-failed", "captures": "3"}, false},
		{"record before the counter", map[string]string{"state": "failed"}, true},
		{"being verified", map[string]string{"state": "verifying", "captures": "1", "sourceNamespace": "fn"}, false},
		{"its verification never ran", map[string]string{"state": "verifying", "captures": "1", "stalls": "3"}, true},
	} {
		if got := criuGroupRecaptures(tc.data, now, "v2"); got != tc.want {
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
	kc := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fn"}})
	a := &Agent{kubeClient: kc, log: logrus.New()}
	c := a.criuRestorer()
	ctx := context.Background()
	// What a capture of the instance leaves behind: the checkpoints'
	// capture records in the namespace's scope, and its configuration's
	// record naming the instance record k1.
	vouchCRIU(t, c, "fn", true, "c0", "c1")
	aliasGroup(t, kc, "cache://k1", "fn", "k1")
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

// setRecord sets fields of group record k1.
func setRecord(t *testing.T, kc *fake.Clientset, fields map[string]string) {
	t.Helper()
	ctx := context.Background()
	cm, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		cm.Data[k] = v
	}
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// A verified capture at its last allowed failure: the failed restore
// blocks it and lets the configuration be captured again.
func TestFailGroup_RestoreFailureBlocksAndAllowsRecapture(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	setRecord(t, kc, map[string]string{groupRestoreFailuresKey: strconv.Itoa(criuGroupMaxRestoreFailures - 1)})
	c.failGroup(ctx, leader, "k1", true, errors.New("criu restore: exit status 1"), logrus.NewEntry(logrus.New()))
	if !c.Blocked(ctx, "c0") || !c.Blocked(ctx, "c1") {
		t.Error("a failed restore must block every checkpoint of the group")
	}
	if got := remainingPods(t, kc); len(got) != 1 {
		t.Errorf("pods left = %v, want only the other instance", got)
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if cm.Data["state"] != criuGroupStateRestoreFailed || !criuGroupRecaptures(cm.Data, time.Now(), "") || cm.Data[groupRestoreFailuresKey] != "" {
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

// A failed capture is not tried again by agents of the release it failed
// under (each attempt freezes the live instance); an agent of another
// release tries it once more.
func TestClaimGroupRecord_FailedCaptureWaitsForAnotherRelease(t *testing.T) {
	c, kc, _ := groupFixture(t)
	ctx := context.Background()
	c.a.config.Webhook.HostBundleRoot = t.TempDir()
	release := func(id string) {
		t.Helper()
		stageVersion(t, c.a, id, time.Hour)
		if err := os.WriteFile(filepath.Join(c.a.bundleRoot(), "current-bundle"), []byte(id), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	release("a1")
	p := stsRank("kimi-0", "0", "2", "sts-c", true)
	first, err := c.claimGroupRecord(ctx, "k2", &p, 2)
	if err != nil || first == nil || first.Data[groupAgentBundleKey] != "a1" {
		t.Fatalf("first claim = %v, %v; want one naming release a1", first, err)
	}
	first.Data["state"], first.Data["reason"] = criuGroupStateFailed, "inet: Unsupported proto 262"
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, first, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if again, err := c.claimGroupRecord(ctx, "k2", &p, 2); err != nil || again != nil {
		t.Fatalf("the release a capture failed under claimed it again = %v, %v", again, err)
	}
	release("b1")
	next, err := c.claimGroupRecord(ctx, "k2", &p, 2)
	if err != nil || next == nil || next.Data["captures"] != "2" || next.Data[groupAgentBundleKey] != "b1" ||
		next.Data["previousReason"] != "inet: Unsupported proto 262" {
		t.Fatalf("another release's claim = %v, %v; want the second capture under b1", next, err)
	}
}

// A verified capture whose restore fails, with failures to spare: its
// checkpoints are not blocked and it stays usable, the failure is counted,
// and the instance's pods are replaced so they restore from it again.
func TestFailGroup_VerifiedCaptureSurvivesAFailedRestore(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	c.failGroup(ctx, leader, "k1", true, errors.New("gpushare open: no reply"), logrus.NewEntry(logrus.New()))
	if c.Blocked(ctx, "c0") || c.Blocked(ctx, "c1") {
		t.Error("one failed restore blocked a verified capture")
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if cm.Data["state"] != criuGroupStateComplete || cm.Data[groupRestoreFailuresKey] != "1" ||
		!strings.Contains(cm.Data[groupLastRestoreFailureKey], "no reply") || criuGroupRecaptures(cm.Data, time.Now(), "") {
		t.Errorf("record = %v, want it complete with one failure counted", cm.Data)
	}
	if got := remainingPods(t, kc); len(got) != 1 || got[0] != "other-0" {
		t.Errorf("pods left = %v, want only the other instance", got)
	}
}

// A capture still being verified has never served: its first failed
// restore ends it.
func TestFailGroup_CaptureBeingVerifiedFailsAtOnce(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	setRecord(t, kc, map[string]string{"state": criuGroupStateVerifying, "sourceNamespace": "fn"})
	c.failGroup(ctx, leader, "k1", true, errors.New("criu restore: exit status 1"), logrus.NewEntry(logrus.New()))
	if !c.Blocked(ctx, "c0") || !c.Blocked(ctx, "c1") {
		t.Error("a failed restore of a capture being verified must block it")
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if cm.Data["state"] != criuGroupStateRestoreFailed {
		t.Errorf("record = %v, want restore-failed", cm.Data)
	}
}

func TestRecordFailure_CountsFailuresOfAVerifiedCapture(t *testing.T) {
	d := map[string]string{"state": "complete", "checkpoint.0": "c0"}
	cause := errors.New("boom")
	for i := 1; i < criuGroupMaxRestoreFailures; i++ {
		if !recordFailure(d, "c0", true, cause) || d["state"] != "complete" || d[groupRestoreFailuresKey] != strconv.Itoa(i) {
			t.Fatalf("failure %d: record %v", i, d)
		}
	}
	if !recordFailure(d, "c0", true, cause) || d["state"] != criuGroupStateRestoreFailed || d[groupRestoreFailuresKey] != "" {
		t.Errorf("failure %d: record %v, want restore-failed", criuGroupMaxRestoreFailures, d)
	}
	// A restore that did not run counts a stall, not a failure.
	d = map[string]string{"state": "complete", "checkpoint.0": "c0"}
	if !recordFailure(d, "c0", false, cause) || d[groupRestoreFailuresKey] != "" || d["stalls"] != "1" {
		t.Errorf("stall: record %v", d)
	}
	// Another capture's record is not touched.
	d = map[string]string{"state": "complete", "checkpoint.0": "n0"}
	if recordFailure(d, "c0", true, cause) || d[groupRestoreFailuresKey] != "" {
		t.Errorf("another capture: record %v", d)
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
	if recordRestored(d, "other", "u0/main") || d["stalls"] != "2" {
		t.Error("a restore of another capture cleared the stalls")
	}
	if !recordRestored(d, "c0", "u0/main") || d["stalls"] != "" {
		t.Error("a restore did not clear its capture's stalls")
	}
}

// A restore of a capture being verified records its pods and leaves the
// capture being verified: restored processes do not verify it, served
// pods do (restored_verify.go). A restore of another capture changes
// nothing.
func TestRecordRestored_LeavesItsCaptureBeingVerified(t *testing.T) {
	d := map[string]string{"state": "verifying", "sourceNamespace": "fn", "checkpoint.0": "c0", "checkpoint.1": "c1"}
	if recordRestored(d, "other", "u0/main,u1/main") || d[groupRestoredPodsKey] != "" {
		t.Fatal("a restore of another capture changed this one")
	}
	if !recordRestored(d, "c0", "u0/main,u1/main") || d[groupRestoredPodsKey] != "u0/main,u1/main" {
		t.Fatalf("record = %v, want the restore's pods", d)
	}
	if d["state"] != "verifying" || d["sourceNamespace"] != "fn" || d["verifiedAt"] != "" {
		t.Fatalf("record = %v, want it still being verified", d)
	}
	if recordRestored(d, "c0", "u0/main,u1/main") {
		t.Error("the same restore recorded again changed the record")
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
	setRecord(t, kc, map[string]string{groupRestoreFailuresKey: strconv.Itoa(criuGroupMaxRestoreFailures - 1)})
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
	if !recordRestored(record(), "c0", "u0/main") {
		t.Error("a restore did not clear the replacements")
	}
}

// A companion replaced for an address collision gives its slot to its
// replacement: its claim leaves the record, others' stay.
func TestReplaceCollidingReleasesTheCompanionsSlot(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	fe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "frontend-a", Namespace: "fn", Annotations: map[string]string{
		webhook.CRIUGroupAnnotation: "k1", webhook.CRIUGroupCompanionAnnotation: "true", webhook.CRIUGroupOrdinalAnnotation: "4"}}}
	if _, err := kc.CoreV1().Pods("fn").Create(ctx, fe, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cm, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	cm.Data[claimKey("fn", 4)], cm.Data[claimKey("fn", 5)] = now, now
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.replaceColliding(ctx, leader, "k1", &addressCollision{Pods: []string{"fn/frontend-a"}}, logrus.NewEntry(logrus.New()))
	cm, _ = kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if _, held := cm.Data[claimKey("fn", 4)]; held {
		t.Error("the replaced companion's slot is still claimed")
	}
	if _, held := cm.Data[claimKey("fn", 5)]; !held {
		t.Error("another companion's claim was dropped")
	}
	if got := remainingPods(t, kc); slices.Contains(got, "frontend-a") {
		t.Errorf("pods left = %v, want frontend-a replaced", got)
	}
}

// vouchCRIU records checkpoints ids as CRIU captures of namespace ns's
// tenant scope, as the agent does when it captures them: their metadata
// on this node and their capture records.
func vouchCRIU(t *testing.T, c *criuAutoRestorer, ns string, instance bool, ids ...string) {
	t.Helper()
	if c.a.config.CheckpointDir == "" {
		c.a.config.CheckpointDir = t.TempDir()
	}
	if c.a.captureBackend == nil {
		local, err := checkpointstore.NewLocal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		c.a.captureBackend = checkpointstore.NewConfigMapBackend(local, c.a.kubeClient, "nvsnap-system", logrus.New())
	}
	for _, id := range ids {
		sum := sha256.Sum256([]byte(id))
		hash := hex.EncodeToString(sum[:])
		if err := os.MkdirAll(filepath.Join(c.a.config.CheckpointDir, id), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(CheckpointMetadata{ID: id, Hash: hash})
		if err := os.WriteFile(filepath.Join(c.a.config.CheckpointDir, id, "metadata.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		meta := map[string]string{"engine": "criu", "checkpoint_id": id, checkpointstore.CRIUScopeMetaKey: criuScopeOf(ns, nil)}
		if instance {
			meta[checkpointstore.InstanceCaptureMetaKey] = "true"
		}
		if err := c.a.captureBackend.(checkpointstore.ManifestRecorder).RecordManifest(context.Background(), hash,
			checkpointstore.Manifest{Hash: hash, CaptureMethod: "criu", SourcePodMeta: meta}); err != nil {
			t.Fatal(err)
		}
	}
}

// aliasGroup makes configuration uri's record in namespace ns's scope name
// the instance record key, as a capture of an instance whose ranks differ
// in configuration does.
func aliasGroup(t *testing.T, kc *fake.Clientset, uri, ns, key string) {
	t.Helper()
	own := criuGroupKey(uri, criuScopeOf(ns, nil))
	if own == key {
		return
	}
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(own), Namespace: "nvsnap-system"},
		Data:       map[string]string{"state": criuGroupStateAlias, "alias": key},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// A failure is recorded only on a group record the pod may act under: a
// pod naming another tenant's record and checkpoint adds nothing to it.
func TestFailGroup_RecordsOnlyOnItsOwnRecord(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	foreign := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName("k2"), Namespace: "nvsnap-system"},
		Data: map[string]string{"state": "complete", "size": "1", "captures": "1", "checkpoint.0": "x9"}}
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	leader.Annotations[webhook.CRIURestoreAnnotation] = "x9"
	leader.Annotations[webhook.CRIUGroupAnnotation] = "k2"
	c.failGroup(ctx, leader, "k2", false, errors.New("placeholders not running"), logrus.NewEntry(logrus.New()))
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k2"), metav1.GetOptions{})
	if cm.Data["stalls"] != "" || cm.Data["state"] != "complete" {
		t.Errorf("another tenant's record changed: %v", cm.Data)
	}
	if c.Blocked(ctx, "x9") {
		t.Error("another tenant's checkpoint blocked")
	}
}

// A group's failure, as its record keeps it, names every member with the
// start of its own error, however long each member's error is: the member
// that failed on its own is not hidden behind the others' aborts.
func TestMembersFailedKeepsEveryMember(t *testing.T) {
	abort := "checkpoint failed: CRIU dump failed (criu-v2): action.sh exited, status=1 " + strings.Repeat("Unseizing 4174664 into 1 | ", 400)
	failed := []string{
		memberFailure("fn/worker-0", abort),
		memberFailure("fn/etcd-0", abort),
		memberFailure("fn/nats-0", "checkpoint failed: first errors: inet: Unsupported proto 262 for socket 8808bc3 "+strings.Repeat("x", 5000)),
	}
	reason := truncate(membersFailed("group checkpoint", "s1", 3, failed).Error(), groupReasonMax)
	if !strings.HasPrefix(reason, "group checkpoint s1: 3 of 3 members failed: fn/worker-0: ") {
		t.Errorf("reason starts %q", reason[:80])
	}
	for _, want := range []string{"fn/worker-0: checkpoint failed", "fn/etcd-0: checkpoint failed", "fn/nats-0: checkpoint failed: first errors: inet: Unsupported proto 262"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the recorded reason lost %q", want)
		}
	}
	if len(reason) > groupReasonMax || len(reason) > 3*(groupMemberErrorMax+16)+64 {
		t.Errorf("reason of %d bytes, want each member bounded", len(reason))
	}
}

// A capture being verified restores only into its own namespace. Once that
// namespace is gone (the function undeployed, or scaled to zero, before a
// restore of it served), the configuration is captured again by the next
// Ready instance instead of starting cold for good. While the namespace
// exists, or the attempts are spent, the record stands.
func TestGroupRecaptures_VerifyingWithoutItsNamespace(t *testing.T) {
	ctx := context.Background()
	terminating := metav1.Now()
	kc := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fn"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "leaving", DeletionTimestamp: &terminating, Finalizers: []string{"kubernetes"}}},
	)
	c := (&Agent{kubeClient: kc, log: logrus.New()}).criuRestorer()
	verifying := func(ns, captures string) map[string]string {
		return map[string]string{"state": criuGroupStateVerifying, "captures": captures, "sourceNamespace": ns}
	}
	for _, tc := range []struct {
		name string
		data map[string]string
		want bool
	}{
		{"its namespace exists", verifying("fn", "1"), false},
		{"its namespace is gone", verifying("gone", "1"), true},
		{"its namespace is being deleted", verifying("leaving", "1"), true},
		{"its namespace is gone, attempts spent", verifying("gone", strconv.Itoa(criuGroupMaxCaptures)), false},
		{"complete, whatever the namespace", map[string]string{"state": criuGroupStateComplete, "captures": "1", "sourceNamespace": "gone"}, false},
		{"capturing, whatever the namespace", map[string]string{"state": criuGroupStateCapturing, "captures": "1", "startedAt": time.Now().UTC().Format(time.RFC3339), "sourceNamespace": "gone"}, false},
	} {
		if got := c.groupRecaptures(ctx, tc.data); got != tc.want {
			t.Errorf("%s: recaptures = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A namespace that cannot be read is not taken for gone.
func TestGroupRecaptures_UnreadableNamespaceKeepsTheRecord(t *testing.T) {
	kc := fake.NewSimpleClientset()
	kc.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("namespaces"), "fn", errors.New("no"))
	})
	c := (&Agent{kubeClient: kc, log: logrus.New()}).criuRestorer()
	data := map[string]string{"state": criuGroupStateVerifying, "captures": "1", "sourceNamespace": "fn"}
	if c.groupRecaptures(context.Background(), data) {
		t.Error("a namespace the agent cannot read must not release the record")
	}
}

// The next Ready instance of the configuration, in another namespace,
// takes over a record whose verification lost its namespace.
func TestClaimGroupRecord_TakesOverVerifyingWithoutItsNamespace(t *testing.T) {
	c, kc, _ := groupFixture(t)
	ctx := context.Background()
	setRecord(t, kc, map[string]string{"state": criuGroupStateVerifying, "sourceNamespace": "undeployed"})
	p := stsRank("kimi-0", "0", "2", "sts-c", true)
	p.Namespace = "fn"
	claimed, err := c.claimGroupRecord(ctx, "k1", &p, 2)
	if err != nil || claimed == nil {
		t.Fatalf("claim = %v, %v; want the record taken over", claimed, err)
	}
	if claimed.Data["state"] != criuGroupStateCapturing || claimed.Data["captures"] != "2" {
		t.Errorf("record = %v, want a second capture under way", claimed.Data)
	}
	setRecord(t, kc, map[string]string{"state": criuGroupStateVerifying, "sourceNamespace": "fn"})
	if again, err := c.claimGroupRecord(ctx, "k1", &p, 2); err != nil || again != nil {
		t.Errorf("claim = %v, %v; a record still verified in its namespace is not taken over", again, err)
	}
}
