// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// Two frontends of one Deployment share a configuration: their alias names
// both slots, and an etcd's names its own.
func TestCompanionAliasNamesIdenticalSlots(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New()}
	uris := []string{"cache://worker", "cache://frontend", "cache://etcd", "cache://frontend"}
	key := criuGroupKey(instanceConfigURIOf(uris), "fv")
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key)},
		Data: map[string]string{"state": "complete", "size": "4", "checkpoint.0": "w", "checkpoint.1": "f1", "checkpoint.2": "e", "checkpoint.3": "f2",
			"companion.1": "true", "companion.2": "true", "companion.3": "true"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	a.criuRestorer().writeGroupAliases(ctx, key, "fv", uris, 1, logrus.NewEntry(logrus.New()))
	g, ok := a.lookupCRIUGroup(ctx, criuGroupKey("cache://frontend", "fv"))
	if !ok || !slices.Equal(g.CompanionSlots(), []int{1, 3}) {
		t.Fatalf("frontend alias: slots %v (%v)", g.Slots, ok)
	}
	if g, ok := a.lookupCRIUGroup(ctx, criuGroupKey("cache://etcd", "fv")); !ok || !slices.Equal(g.CompanionSlots(), []int{2}) {
		t.Fatalf("etcd alias: slots %v (%v)", g.Slots, ok)
	}
	alias, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName(criuGroupKey("cache://frontend", "fv")), metav1.GetOptions{})
	if err != nil || alias.Data["slot"] != "" {
		t.Errorf("an alias of several slots must not name one slot for older agents: %v %v", alias.Data, err)
	}
}

func TestParseSlots(t *testing.T) {
	for _, tc := range []struct {
		d    map[string]string
		want []int
	}{
		{map[string]string{}, nil},
		{map[string]string{"slot": "2"}, []int{2}},
		{map[string]string{"slots": "1,3", "slot": "9"}, []int{1, 3}},
		{map[string]string{"slots": "1,x"}, nil},
	} {
		if got := parseSlots(tc.d); !slices.Equal(got, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.d, got, tc.want)
		}
	}
}

func TestPickCompanionSlot(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	old := now.Add(-companionClaimTTL - time.Second).UTC().Format(time.RFC3339Nano)
	d := map[string]string{"state": "complete", claimKey("fn", 1): recent, claimKey("other", 3): recent, claimKey("gone", 1): old, "claim.bad.1": "x"}
	if got := pickCompanionSlot(d, "fn", []int{1, 3}, nil, now); got != 3 {
		t.Errorf("got slot %d, want 3: slot 1 is claimed here, slot 3 only in another namespace", got)
	}
	if _, ok := d[claimKey("gone", 1)]; ok {
		t.Error("an expired claim was kept")
	}
	if _, ok := d["claim.bad.1"]; ok {
		t.Error("an unreadable claim was kept")
	}
	if d["state"] != "complete" || d[claimKey("other", 3)] == "" {
		t.Errorf("the record's other entries changed: %v", d)
	}
	if got := pickCompanionSlot(d, "fn", []int{1, 3}, map[int]bool{3: true}, now); got != -1 {
		t.Errorf("got slot %d, want none: 1 claimed, 3 held", got)
	}
	if got := pickCompanionSlot(map[string]string{claimKey("fn", 1): old}, "fn", []int{1, 3}, nil, now); got != 1 {
		t.Errorf("got slot %d, want 1: its claim expired", got)
	}
}

func companionPlaceholder(name, key, slot string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fn", Annotations: map[string]string{
		webhook.CRIUGroupAnnotation: key, webhook.CRIUGroupOrdinalAnnotation: slot, webhook.CRIUGroupSizeAnnotation: "4",
		webhook.CRIUGroupCompanionAnnotation: "true"}}}
}

// Pods admitted together get distinct slots, a slot a placeholder holds is
// skipped, and with every slot taken the pod gets none.
func TestClaimCompanionSlot(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	casConfigMaps(kc)
	a := &Agent{kubeClient: kc, log: logrus.New()}
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName("k1"), ResourceVersion: "1"},
		Data: map[string]string{"state": "complete"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	g := webhook.CRIUGroup{Key: "k1"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "fn"}}
	var mu sync.Mutex
	var got []int
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := a.claimCompanionSlot(ctx, pod, g, []int{1, 3})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			got = append(got, s)
			mu.Unlock()
		}()
	}
	wg.Wait()
	slices.Sort(got)
	if !slices.Equal(got, []int{1, 3}) {
		t.Fatalf("concurrent claims got %v, want [1 3]", got)
	}
	if _, err := a.claimCompanionSlot(ctx, pod, g, []int{1, 3}); err == nil || !strings.Contains(err.Error(), "taken") {
		t.Fatalf("a third claim: %v, want every slot taken", err)
	}

	// Once the claims expire, the pods hold their slots.
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	for k := range cm.Data {
		if strings.HasPrefix(k, companionClaimPrefix) {
			cm.Data[k] = time.Now().Add(-companionClaimTTL - time.Second).UTC().Format(time.RFC3339Nano)
		}
	}
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := kc.CoreV1().Pods("fn").Create(ctx, companionPlaceholder("fe-a", "k1", "1"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if s, err := a.claimCompanionSlot(ctx, pod, g, []int{1, 3}); err != nil || s != 3 {
		t.Fatalf("got slot %d (%v), want 3: slot 1 is held by fe-a", s, err)
	}

	cm, _ = kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	cm.Data["state"] = criuGroupStateRestoreFailed
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.claimCompanionSlot(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "fn2"}}, g, []int{1, 3}); err == nil {
		t.Error("claimed a slot of a record that is no longer complete")
	}
}

// casConfigMaps makes the fake API server refuse a ConfigMap update made
// from a stale read, as the real one does.
func casConfigMaps(kc *fake.Clientset) {
	gvr := corev1.SchemeGroupVersion.WithResource("configmaps")
	kc.PrependReactor("update", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		cm := action.(k8stesting.UpdateAction).GetObject().(*corev1.ConfigMap).DeepCopy()
		cur, err := kc.Tracker().Get(gvr, cm.Namespace, cm.Name)
		if err != nil {
			return true, nil, err
		}
		rv := cur.(*corev1.ConfigMap).ResourceVersion
		if cm.ResourceVersion != rv {
			return true, nil, apierrors.NewConflict(gvr.GroupResource(), cm.Name, fmt.Errorf("stale resource version %s, now %s", cm.ResourceVersion, rv))
		}
		n, _ := strconv.Atoi(rv)
		cm.ResourceVersion = strconv.Itoa(n + 1)
		return true, cm, kc.Tracker().Update(gvr, cm, cm.Namespace)
	})
}

func TestDoubledSlot(t *testing.T) {
	leader := companionPlaceholder("w-0", "k1", "0")
	delete(leader.Annotations, webhook.CRIUGroupCompanionAnnotation)
	pods := []corev1.Pod{*leader, *companionPlaceholder("fe-a", "k1", "1"), *companionPlaceholder("fe-b", "k1", "3"), *companionPlaceholder("other", "k2", "1")}
	if err := doubledSlot(pods, leader, "k1"); err != nil {
		t.Fatalf("distinct slots: %v", err)
	}
	gone := companionPlaceholder("fe-old", "k1", "1")
	gone.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	if err := doubledSlot(append(pods, *gone), leader, "k1"); err != nil {
		t.Fatalf("a deleted placeholder holds no slot: %v", err)
	}
	if err := doubledSlot(append(pods, *companionPlaceholder("fe-c", "k1", "3")), leader, "k1"); err == nil || !strings.Contains(err.Error(), "slot 3") {
		t.Fatalf("two placeholders of slot 3: %v", err)
	}
}
