// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
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
)

const (
	markerUID      = "0f3c2a9e-7d41-4b8e-9a1f-2c5d6e7f8a90"
	otherMarkerUID = "1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d"
)

// A marker lives exactly as long as its dump.
func TestMarkCapturing(t *testing.T) {
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	clearMarker, err := a.markCapturing(captureMarker{Namespace: "ns", Pod: "w-0", UID: markerUID})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.captureMarkerDir(), markerUID+".json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("no marker during the dump: %v", err)
	}
	clearMarker()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("marker left after the dump: %v", err)
	}
	if _, err := a.markCapturing(captureMarker{Namespace: "ns", Pod: "w-0", UID: "../x"}); err == nil {
		t.Error("a uid that is not one names a file")
	}
}

// At start, the pod of each surviving marker is deleted, bound to its
// uid; a pod gone or replaced is left alone; every handled marker goes.
func TestRecoverInterruptedSources(t *testing.T) {
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	log := logrus.NewEntry(logrus.New())
	for _, m := range []captureMarker{
		{Namespace: "ns", Pod: "stopped", UID: markerUID},
		{Namespace: "ns", Pod: "gone", UID: otherMarkerUID},
	} {
		if _, err := a.markCapturing(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(a.captureMarkerDir(), "junk.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "stopped", UID: types.UID(markerUID)}})
	var preconditions []string
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		d := action.(k8stesting.DeleteActionImpl)
		if p := d.GetDeleteOptions().Preconditions; p != nil && p.UID != nil {
			preconditions = append(preconditions, d.GetName()+"="+string(*p.UID))
		}
		return false, nil, nil
	})
	a.kubeClient = cs
	a.recoverInterruptedSources(t.Context(), log)
	if _, err := cs.CoreV1().Pods("ns").Get(t.Context(), "stopped", metav1.GetOptions{}); err == nil {
		t.Error("the stopped source's pod was not deleted")
	}
	if len(preconditions) != 2 || preconditions[0] == preconditions[1] {
		t.Errorf("deletes not bound to the pods' uids: %v", preconditions)
	}
	if ents, _ := os.ReadDir(a.captureMarkerDir()); len(ents) != 0 {
		t.Errorf("markers left: %v", ents)
	}
}

// A group checkpoint that left a member unusable deletes every member's
// pod it captured, bound to its uid: a pod since replaced is left alone.
func TestDeleteMembers(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "w-0", UID: types.UID(markerUID)}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "w-1", UID: "replaced"}},
	)
	var deletes []string
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		d := action.(k8stesting.DeleteActionImpl)
		p := d.GetDeleteOptions().Preconditions
		if p == nil || p.UID == nil {
			t.Errorf("delete of %s not bound to a uid", d.GetName())
			return true, nil, nil
		}
		deletes = append(deletes, d.GetName()+"="+string(*p.UID))
		return false, nil, nil
	})
	a := &Agent{kubeClient: cs}
	a.deleteMembers(t.Context(), []CheckpointRequest{{Namespace: "ns", PodName: "w-0"}, {Namespace: "ns", PodName: "w-1"}},
		[]types.UID{types.UID(markerUID), types.UID(otherMarkerUID)}, logrus.NewEntry(logrus.New()))
	if len(deletes) != 2 || deletes[0] != "w-0="+markerUID || deletes[1] != "w-1="+otherMarkerUID {
		t.Errorf("deletes = %v", deletes)
	}
}

// shortRetries makes the agent's retries fast for a test.
func shortRetries(t *testing.T) {
	t.Helper()
	pd, pr := deleteRetry, recoverRetry
	deleteRetry.first, deleteRetry.max, deleteRetry.total = time.Millisecond, 5*time.Millisecond, 2*time.Second
	recoverRetry.first, recoverRetry.max, recoverRetry.total = time.Millisecond, 5*time.Millisecond, 2*time.Second
	t.Cleanup(func() { deleteRetry, recoverRetry = pd, pr })
}

// A delete that fails for a reason that may pass is retried until it goes
// through; a pod gone or replaced is done at once.
func TestDeletePodRetrying(t *testing.T) {
	shortRetries(t)
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "w-0"}})
	fails := 3
	cs.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if fails > 0 {
			fails--
			return true, nil, apierrors.NewServiceUnavailable("api server restarting")
		}
		return false, nil, nil
	})
	a := &Agent{kubeClient: cs}
	if err := a.deletePodRetrying(t.Context(), "ns", "w-0", ""); err != nil || fails != 0 {
		t.Fatalf("delete: %v (failures left %d)", err, fails)
	}
	if err := a.deletePodRetrying(t.Context(), "ns", "gone", ""); err != nil {
		t.Errorf("a pod already gone: %v", err)
	}
}

// Recovery at start retries until every marker's pod is deleted.
func TestRecoverInterruptedSourcesRetries(t *testing.T) {
	shortRetries(t)
	a := &Agent{config: Config{CheckpointDir: t.TempDir()}}
	if _, err := a.markCapturing(captureMarker{Namespace: "ns", Pod: "stopped", UID: markerUID}); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "stopped", UID: types.UID(markerUID)}})
	fails := 2
	cs.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if fails > 0 {
			fails--
			return true, nil, apierrors.NewServiceUnavailable("api server restarting")
		}
		return false, nil, nil
	})
	a.kubeClient = cs
	a.recoverInterruptedSources(t.Context(), logrus.NewEntry(logrus.New()))
	if _, err := cs.CoreV1().Pods("ns").Get(t.Context(), "stopped", metav1.GetOptions{}); err == nil {
		t.Error("the pod was not deleted after the API came back")
	}
	if ents, _ := os.ReadDir(a.captureMarkerDir()); len(ents) != 0 {
		t.Errorf("markers left: %v", ents)
	}
}
