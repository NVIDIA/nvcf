// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// trustFixture is an agent with two tenants: "victim" has a lone capture
// v0 and an instance rank capture r0; "tenant" has its own lone capture t0.
func trustFixture(t *testing.T) (*criuAutoRestorer, *fake.Clientset) {
	t.Helper()
	kc := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "victim"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}})
	a := &Agent{kubeClient: kc, log: logrus.New()}
	c := a.criuRestorer()
	vouchCRIU(t, c, "victim", false, "v0")
	vouchCRIU(t, c, "victim", true, "r0")
	vouchCRIU(t, c, "tenant", false, "t0")
	return c, kc
}

// restoredLooking is a pod whose annotations say the agent restored id into
// its container "engine", which then restarted: the restored-pod watch's
// fast path.
func restoredLooking(name, ns, id string) *corev1.Pod {
	at := time.Now().Add(-time.Hour).UTC()
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name), Annotations: map[string]string{
			webhook.CRIURestoreAnnotation:          id,
			webhook.CRIURestoreContainerAnnotation: "engine",
			webhook.CRIURestoredAnnotation:         at.Format(time.RFC3339),
		}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "engine", RestartCount: 1,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(at.Add(time.Minute))}},
		}}},
	}
}

// watched runs the restored-pod watch on pod and waits for it to finish.
func watched(t *testing.T, c *criuAutoRestorer, pod *corev1.Pod) {
	t.Helper()
	c.considerRestored(context.Background(), pod)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, busy := c.attempted.Load("watch/" + string(pod.UID)); !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the restored-pod watch did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The reviewer's case: a pod the agent never restored names another
// tenant's checkpoint in its annotations, says it was restored, and
// restarts. Nothing is blocked and the pod is left alone.
func TestRestoredWatch_IgnoresAPodTheAgentDidNotRestore(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	pod := restoredLooking("forger", "tenant", "v0")
	if _, err := kc.CoreV1().Pods("tenant").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	watched(t, c, pod)
	if c.Blocked(ctx, "v0") {
		t.Error("another tenant's checkpoint was blocked because of a pod the agent did not restore")
	}
	if _, err := kc.CoreV1().Pods("tenant").Get(ctx, "forger", metav1.GetOptions{}); err != nil {
		t.Errorf("the pod was acted on: %v", err)
	}
}

// A pod the agent did restore is watched, and its failure blocks the
// checkpoint the agent restored, whatever the pod's annotations say now.
func TestRestoredWatch_ActsOnTheAgentsOwnRecord(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	pod := restoredLooking("restored", "tenant", "v0") // annotation edited after the restore
	if _, err := kc.CoreV1().Pods("tenant").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.recordRestore(pod.UID, restoreRecord{Checkpoint: "t0", Container: "engine", Namespace: "tenant", Pod: "restored"}); err != nil {
		t.Fatal(err)
	}
	watched(t, c, pod)
	if !c.Blocked(ctx, "t0") {
		t.Error("the checkpoint the agent restored must be blocked when its engine dies")
	}
	if c.Blocked(ctx, "v0") {
		t.Error("the checkpoint the annotation names now was blocked")
	}
	if _, err := kc.CoreV1().Pods("tenant").Get(ctx, "restored", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the failed pod must be deleted, err=%v", err)
	}
	if _, ok := c.restoredRecordOf(pod.UID); ok {
		t.Error("the record of an ended restore must go")
	}
}

// A lone restore runs only from a capture of the pod's own tenant scope,
// and never from one rank of an instance capture: the rule the webhook
// applies, applied again by the agent before it fetches or restores.
func TestRestoreRefusal_Lone(t *testing.T) {
	c, _ := trustFixture(t)
	ctx := context.Background()
	// A checkpoint not on this node is resolved through the catalog: t0's
	// metadata goes, and the catalog serves its hash; it knows no "nope".
	hashOf := map[string]string{}
	for _, id := range []string{"t0"} {
		b, err := os.ReadFile(filepath.Join(c.a.config.CheckpointDir, id, "metadata.json"))
		if err != nil {
			t.Fatal(err)
		}
		var md CheckpointMetadata
		if err := json.Unmarshal(b, &md); err != nil {
			t.Fatal(err)
		}
		hashOf[id] = md.Hash
		if err := os.RemoveAll(filepath.Join(c.a.config.CheckpointDir, id)); err != nil {
			t.Fatal(err)
		}
	}
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/checkpoints/")
		h, ok := hashOf[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "hash": h})
	}))
	defer catalog.Close()
	c.a.config.CatalogURL = catalog.URL
	for _, tc := range []struct {
		ns, id string
		want   bool
	}{
		{"victim", "v0", true},
		{"tenant", "t0", true},
		{"tenant", "v0", false},   // another tenant's capture
		{"victim", "r0", false},   // a rank restores only with its instance
		{"tenant", "nope", false}, // no capture record vouches for it
	} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tc.id, Namespace: tc.ns, UID: types.UID(tc.ns + "-" + tc.id),
			Annotations: map[string]string{webhook.CRIURestoreAnnotation: tc.id}}}
		why, err := c.restoreRefusal(ctx, pod)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.ns, tc.id, err)
		}
		if (why == "") != tc.want {
			t.Errorf("%s restoring %s: refusal %q, want admitted=%v", tc.ns, tc.id, why, tc.want)
		}
	}
}

// Refused, a pod gets nothing: no fetch is started for it.
func TestAdmitted_RefusedPodIsNotFetchedFor(t *testing.T) {
	c, _ := trustFixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "tenant", UID: "p",
		Annotations: map[string]string{webhook.CRIURestoreAnnotation: "v0", webhook.CRIURestoreContainerAnnotation: "engine"}},
		Spec:   corev1.PodSpec{NodeName: "n1"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if c.admitted(context.Background(), pod) {
		t.Fatal("a pod naming another tenant's checkpoint was admitted")
	}
	c.consider(context.Background(), pod)
	if _, started := c.attempted.Load("fetch/v0"); started {
		t.Error("the refused pod's checkpoint is being fetched")
	}
	if _, started := c.attempted.Load(pod.UID); started {
		t.Error("a restore was started for the refused pod")
	}
}

// A group key must be the pod's own configuration's in its scope, or the
// instance record that one names; a rank's checkpoint must be a capture of
// its scope.
func TestRestoreRefusal_Group(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	victimKey := criuGroupKey("cache://cfg", criuScopeOf("victim", nil))
	aliasGroup(t, kc, "cache://other-rank", "victim", victimKey)
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(victimKey), Namespace: "nvsnap-system"},
		Data:       map[string]string{"state": "complete", "checkpoint.0": "r0"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	rank := func(ns, uri, key, id string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: ns + uri + id, Namespace: ns, UID: types.UID(ns + uri + id),
			Annotations: map[string]string{cacheURIAnnotation: uri, webhook.CRIUGroupAnnotation: key, webhook.CRIURestoreAnnotation: id}}}
	}
	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"own configuration", rank("victim", "cache://cfg", victimKey, "r0"), true},
		{"a rank whose configuration names the instance record", rank("victim", "cache://other-rank", victimKey, "r0"), true},
		{"another scope's record", rank("tenant", "cache://cfg", victimKey, "t0"), false},
		{"own record, another tenant's checkpoint", rank("tenant", "cache://cfg", criuGroupKey("cache://cfg", criuScopeOf("tenant", nil)), "r0"), false},
		// A companion carries no configuration: the record must list its
		// checkpoint, which must be a capture of its scope.
		{"member without a configuration", rank("victim", "", victimKey, "r0"), true},
		{"member naming a checkpoint the record does not list", rank("victim", "", victimKey, "v0"), false},
		{"member of another scope's record", rank("tenant", "", victimKey, "r0"), false},
		{"member naming no checkpoint", rank("victim", "", victimKey, ""), false},
	} {
		why, err := c.restoreRefusal(ctx, tc.pod)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (why == "") != tc.want {
			t.Errorf("%s: refusal %q, want admitted=%v", tc.name, why, tc.want)
		}
	}
}

// A group rank is watched only when its group record names it restored.
func TestRestoredBy_GroupRankNeedsTheRecord(t *testing.T) {
	c, kc, _ := groupFixture(t)
	ctx := context.Background()
	p, err := kc.CoreV1().Pods("fn").Get(ctx, "kimi-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Annotations[webhook.CRIUGroupOrdinalAnnotation], p.Annotations[webhook.CRIUGroupSizeAnnotation] = "1", "2"
	if _, ok, rerr := c.restoredBy(ctx, p); rerr != nil || ok {
		t.Fatalf("a rank the record does not name: ok=%v err=%v", ok, rerr)
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	cm.Data[groupRestoredPodsKey] = "kimi-0/engine,kimi-1/engine"
	if _, uerr := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); uerr != nil {
		t.Fatal(uerr)
	}
	ref, ok, err := c.restoredBy(ctx, p)
	if err != nil || !ok || ref.group != "k1" || ref.container != "engine" {
		t.Errorf("a rank the record names: %+v ok=%v err=%v", ref, ok, err)
	}
}

// A failed group blocks only checkpoints its pods may restore: a pod that
// joined the group naming another tenant's checkpoint does not get it
// blocked.
func TestEndGroup_BlocksOnlyVouchedCheckpoints(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	if _, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "victim"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	vouchCRIU(t, c, "victim", true, "victim-rank")
	extra := stsRank("kimi-2", "1", "2", "sts-a", false)
	extra.Annotations[webhook.CRIUGroupAnnotation] = "k1"
	extra.Annotations[webhook.CRIURestoreAnnotation] = "victim-rank"
	if _, err := kc.CoreV1().Pods("fn").Create(ctx, &extra, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// A failure that blocks the capture: its last allowed one.
	setRecord(t, kc, map[string]string{groupRestoreFailuresKey: strconv.Itoa(criuGroupMaxRestoreFailures - 1)})
	c.failGroup(ctx, leader, "k1", true, context.DeadlineExceeded, logrus.NewEntry(logrus.New()))
	if !c.Blocked(ctx, "c0") || !c.Blocked(ctx, "c1") {
		t.Error("the group's own checkpoints must be blocked")
	}
	if c.Blocked(ctx, "victim-rank") {
		t.Error("another tenant's checkpoint was blocked")
	}
}

// Records of pods gone while no agent ran are dropped.
func TestSweepRestoreRecords(t *testing.T) {
	c, _ := trustFixture(t)
	for _, uid := range []types.UID{"here", "gone"} {
		if err := c.recordRestore(uid, restoreRecord{Checkpoint: "t0", Container: "engine"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.recordRestore("../x", restoreRecord{Checkpoint: "t0", Container: "engine"}); err == nil {
		t.Error("a UID that is a path must not name a record")
	}
	c.sweepRestoreRecords(map[types.UID]bool{"here": true})
	if _, ok := c.restoredRecordOf("here"); !ok {
		t.Error("the record of a pod on the node was dropped")
	}
	if _, ok := c.restoredRecordOf("gone"); ok {
		t.Error("the record of a pod gone was kept")
	}
}
