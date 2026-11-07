// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/fnstatus"
)

// identityAgent is an agent whose cluster has namespace ns labelled as a
// function version's, and the given records.
func identityAgent(t *testing.T, ns string, records ...*corev1.ConfigMap) (*Agent, *fake.Clientset) {
	t.Helper()
	recordNamed.Clear()
	nsLabelsCache.Delete(ns)
	t.Cleanup(func() { recordNamed.Clear(); nsLabelsCache.Delete(ns) })
	cs := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{
		"function-version-id": "v-1", "function-id": "f-1", "nca-id": "tenant-1"}}})
	a := &Agent{config: Config{TenantLabel: "nca-id"}, kubeClient: cs, log: logrus.New()}
	for _, r := range records {
		r.Namespace = a.criuFailedNamespace()
		if _, err := cs.CoreV1().ConfigMaps(r.Namespace).Create(context.Background(), r, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return a, cs
}

func namedRecord(key string, labels, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key), Labels: labels}, Data: data}
}

var wantIdentity = map[string]string{
	"app.kubernetes.io/managed-by": "nvsnap",
	fnstatus.LabelNamespace:        "sr-1", fnstatus.LabelFunctionVersion: "v-1",
	fnstatus.LabelFunction: "f-1", fnstatus.LabelTenant: "tenant-1",
}

func recordLabels(t *testing.T, a *Agent, key string) map[string]string {
	t.Helper()
	cm, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(context.Background(), criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return cm.Labels
}

func TestNameRecord(t *testing.T) {
	managed := map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}
	a, cs := identityAgent(t, "sr-1",
		namedRecord("k1", managed, map[string]string{"state": criuGroupStateComplete}),
		namedRecord("k2", managed, map[string]string{"state": criuGroupStateAlias, "alias": "k1"}))
	ctx := context.Background()

	// An alias names the instance's record too.
	a.nameRecord(ctx, criuGroupConfigMapName("k2"), "sr-1", true)
	for _, k := range []string{"k1", "k2"} {
		if got := recordLabels(t, a, k); !reflect.DeepEqual(got, wantIdentity) {
			t.Errorf("record %s labels = %v, want %v", k, got, wantIdentity)
		}
	}

	// A record already named is not written again.
	cs.ClearActions()
	a.nameRecord(ctx, criuGroupConfigMapName("k1"), "sr-1", true)
	for _, act := range cs.Actions() {
		if act.GetVerb() == "patch" {
			t.Errorf("a named record was patched: %v", act)
		}
	}

	// A record that is gone is left alone.
	a.nameRecord(ctx, criuGroupConfigMapName("gone"), "sr-1", true)
}

func TestNameRecordLater(t *testing.T) {
	a, cs := identityAgent(t, "sr-1", namedRecord("k1", nil, map[string]string{"state": criuGroupStateFailed}))
	a.nameRecordLater("k1", "sr-1")
	deadline := time.Now().Add(5 * time.Second)
	for recordLabels(t, a, "k1")[fnstatus.LabelFunctionVersion] != "v-1" {
		if time.Now().After(deadline) {
			t.Fatalf("record not named: %v", recordLabels(t, a, "k1"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Within recordNamedTTL a second admission does not read the record.
	cs.ClearActions()
	a.nameRecordLater("k1", "sr-1")
	time.Sleep(50 * time.Millisecond)
	if n := len(cs.Actions()); n != 0 {
		t.Errorf("a record named within the hour was read again: %d actions", n)
	}
}

func TestClaimRecordName(t *testing.T) {
	recordNamed.Clear()
	t.Cleanup(recordNamed.Clear)
	now := time.Now()
	if !claimRecordName("r", now) {
		t.Fatal("first claim refused")
	}
	if claimRecordName("r", now.Add(time.Minute)) {
		t.Error("claimed again within the hour")
	}
	if !claimRecordName("r", now.Add(recordNamedTTL+time.Second)) {
		t.Error("not claimed again after the hour")
	}
}

func TestNamed(t *testing.T) {
	want := map[string]string{"a": "1"}
	for _, tc := range []struct {
		labels map[string]string
		named  bool
	}{
		{nil, false},
		{map[string]string{"a": "2"}, false},
		{map[string]string{"a": "1"}, true},
		{map[string]string{"a": "1", "b": "x"}, true},
	} {
		if got := named(tc.labels, want); got != tc.named {
			t.Errorf("named(%v) = %v, want %v", tc.labels, got, tc.named)
		}
	}
}

// TestClaimGroupRecordNamesIt: a capture's record is named for the
// leader's function version.
func TestClaimGroupRecordNamesIt(t *testing.T) {
	a, _ := identityAgent(t, "sr-1")
	c := &criuAutoRestorer{a: a}
	leader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "sr-1", Name: "p-0"}}
	rec, err := c.claimGroupRecord(context.Background(), "k1", leader, 1)
	if err != nil || rec == nil {
		t.Fatalf("claim: %v %v", rec, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for recordLabels(t, a, "k1")[fnstatus.LabelFunctionVersion] != "v-1" {
		if time.Now().After(deadline) {
			t.Fatalf("record not named: %v", recordLabels(t, a, "k1"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFnstatusMatchesTheAgent keeps the projection's view of the records
// the agent's: names, states, and which records admission restores.
func TestFnstatusMatchesTheAgent(t *testing.T) {
	for got, want := range map[string]string{
		fnstatus.GroupRecordPrefix: criuGroupConfigMapPrefix, fnstatus.CachedirRecordPrefix: cachedirRecordPrefix,
		fnstatus.StateAlias: criuGroupStateAlias, fnstatus.StateCapturing: criuGroupStateCapturing,
		fnstatus.StateVerifying: criuGroupStateVerifying, fnstatus.StateComplete: criuGroupStateComplete,
		fnstatus.StateFailed: criuGroupStateFailed, fnstatus.StateRestoreFailed: criuGroupStateRestoreFailed,
		fnstatus.StateGPUShareFailed: criuGroupStateGPUShareFailed, fnstatus.StateComplete + "/rank": cachedirComplete + "/rank",
		strconv.Itoa(fnstatus.MaxStalls): strconv.Itoa(criuGroupMaxStalls),
	} {
		if got != want {
			t.Errorf("fnstatus has %q, the agent %q", got, want)
		}
	}
	for name, d := range map[string]map[string]string{
		"complete":            {"state": "complete", "size": "2", "checkpoint.0": "a", "checkpoint.1": "b"},
		"missing checkpoint":  {"state": "complete", "size": "2", "checkpoint.0": "a"},
		"stalled":             {"state": "complete", "size": "1", "checkpoint.0": "a", "stalls": "3"},
		"stalls under bound":  {"state": "complete", "size": "1", "checkpoint.0": "a", "stalls": "2"},
		"verifying":           {"state": "verifying", "size": "1", "checkpoint.0": "a", "sourceNamespace": "ns"},
		"verifying, no owner": {"state": "verifying", "size": "1", "checkpoint.0": "a"},
		"capturing":           {"state": "capturing", "size": "1"},
		"failed":              {"state": "failed", "size": "1", "checkpoint.0": "a"},
		"restore-failed":      {"state": "restore-failed", "size": "1", "checkpoint.0": "a"},
		"no size":             {"state": "complete", "checkpoint.0": "a"},
	} {
		_, agent := parseCRIUGroup("k", d)
		if got := fnstatus.Restorable(d); got != agent {
			t.Errorf("%s: fnstatus.Restorable = %v, the agent restores: %v", name, got, agent)
		}
	}
}
