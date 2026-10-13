// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func rankPod(name, uri, owner string) *corev1.Pod {
	yes := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fn",
		Annotations:     map[string]string{cacheURIAnnotation: uri},
		OwnerReferences: []metav1.OwnerReference{{Kind: "PodClique", Name: owner, UID: types.UID(owner), Controller: &yes}}}}
}

// An instance's configuration is its ranks' shared one, so the records of
// uniform instances keep their keys; ranks that differ (a Grove leader and
// its workers) get one derived from all of them, in rank order.
func TestInstanceConfigURI(t *testing.T) {
	a, b := rankPod("r0", "cache://a", "ldr"), rankPod("r1", "cache://a", "ldr")
	if got := instanceConfigURI([]*corev1.Pod{a, b}); got != "cache://a" {
		t.Errorf("uniform instance = %q, want the shared configuration", got)
	}
	ldr, wkr := rankPod("r0", "cache://a", "ldr"), rankPod("r1", "cache://b", "wkr")
	mixed := instanceConfigURI([]*corev1.Pod{ldr, wkr})
	if !strings.HasPrefix(mixed, "cache://group-") || mixed == instanceConfigURI([]*corev1.Pod{wkr, ldr}) {
		t.Errorf("mixed instance = %q; must be derived and depend on rank order", mixed)
	}
	if mixed != instanceConfigURI([]*corev1.Pod{rankPod("x0", "cache://a", "o1"), rankPod("x1", "cache://b", "o2")}) {
		t.Error("the same configurations must give the same instance configuration")
	}
}

// Pods of one template that differ in configuration are a rollout in
// progress, not an instance to capture; different templates may differ.
func TestRanksAgreePerTemplate(t *testing.T) {
	if err := ranksAgreePerTemplate([]*corev1.Pod{rankPod("r0", "cache://a", "ldr"), rankPod("r1", "cache://b", "wkr")}); err != nil {
		t.Errorf("Grove leader and worker cliques may differ: %v", err)
	}
	if err := ranksAgreePerTemplate([]*corev1.Pod{rankPod("r0", "cache://a", "sts"), rankPod("r1", "cache://b", "sts")}); err == nil {
		t.Error("two revisions of one StatefulSet must not be captured together")
	}
}

// Each rank of a mixed instance finds the instance's record through the
// alias of its own configuration; a real record under a rank's key is
// left alone; the reaper drops aliases whose record is gone.
func TestGroupAliases(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New()}
	c := a.criuRestorer()
	ldr, wkr := rankPod("r0", "cache://a", "ldr"), rankPod("r1", "cache://b", "wkr")
	ranks := []*corev1.Pod{ldr, wkr}
	key := criuGroupKey(instanceConfigURI(ranks), "fv")
	cms := kc.CoreV1().ConfigMaps("nvsnap-system")
	mk := func(name string, data map[string]string) {
		t.Helper()
		if _, err := cms.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}}, Data: data}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mk(criuGroupConfigMapName(key), map[string]string{"state": "complete", "size": "2", "captures": "1", "checkpoint.0": "c0", "checkpoint.1": "c1"})
	// A uniform instance of the worker's configuration has its own record.
	wkrKey := criuGroupKey("cache://b", "fv")
	mk(criuGroupConfigMapName(wkrKey), map[string]string{"state": "complete", "size": "1", "captures": "1", "checkpoint.0": "solo"})

	uris := []string{ranks[0].Annotations[cacheURIAnnotation], ranks[1].Annotations[cacheURIAnnotation]}
	c.writeGroupAliases(ctx, key, "fv", uris, len(uris), logrus.NewEntry(logrus.New()))

	g, ok := a.lookupCRIUGroup(ctx, criuGroupKey("cache://a", "fv"))
	if !ok || g.Key != key || len(g.Checkpoints) != 2 || g.Checkpoints[1] != "c1" {
		t.Fatalf("the leader's configuration must find the instance's record: %+v %v", g, ok)
	}
	if g, ok := a.lookupCRIUGroup(ctx, wkrKey); !ok || g.Checkpoints[0] != "solo" {
		t.Errorf("a real record under a rank's key must be left alone: %+v %v", g, ok)
	}

	// The instance's record is retired: its alias goes with it.
	if err := cms.Delete(ctx, criuGroupConfigMapName(key), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	a.config.CheckpointDir = t.TempDir()
	if _, err := a.retirableCheckpoints(ctx, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cms.Get(ctx, criuGroupConfigMapName(criuGroupKey("cache://a", "fv")), metav1.GetOptions{}); err == nil {
		t.Error("an alias whose record is gone must be deleted")
	}
	if _, err := cms.Get(ctx, criuGroupConfigMapName(wkrKey), metav1.GetOptions{}); err != nil {
		t.Errorf("a real record must survive the alias sweep: %v", err)
	}
}
