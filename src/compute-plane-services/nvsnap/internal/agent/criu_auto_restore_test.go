// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func criuPlaceholderPod(phase corev1.PodPhase, running bool) *corev1.Pod {
	st := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
	if running {
		st = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-0", Namespace: "sr-1", UID: "u1", Annotations: map[string]string{
			webhook.CRIURestoreAnnotation: "abc__20261008-101010", webhook.CRIURestoreContainerAnnotation: "inference"}},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "utils", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: "inference", State: st},
		}},
	}
}

func TestCRIURestoreTarget_OnlyOnceTheContainerRuns(t *testing.T) {
	if id, c, ok := criuRestoreTarget(criuPlaceholderPod(corev1.PodRunning, true)); !ok || id != "abc__20261008-101010" || c != "inference" {
		t.Errorf("running placeholder: %q %q %v", id, c, ok)
	}
	for name, p := range map[string]*corev1.Pod{
		"pending":           criuPlaceholderPod(corev1.PodPending, false),
		"engine not up yet": criuPlaceholderPod(corev1.PodRunning, false),
		"plain pod":         {Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	} {
		if _, _, ok := criuRestoreTarget(p); ok {
			t.Errorf("%s: restore would start", name)
		}
	}
	deleting := criuPlaceholderPod(corev1.PodRunning, true)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	if _, _, ok := criuRestoreTarget(deleting); ok {
		t.Error("a pod being deleted must not be restored into")
	}
}

func TestCRIUReservePIDArgs(t *testing.T) {
	want := []string{"-t", "42", "-p", "--", "/bin/sh", "-c", "echo 100000 > /proc/sys/kernel/ns_last_pid"}
	if got := criuReservePIDArgs(42); !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v (pid namespace only; above the reserved floor)", got, want)
	}
}

func TestCRIUIsDefault(t *testing.T) {
	for v, want := range map[string]bool{"criu": true, "CRIU": true, "cachedir": false, "": false} {
		t.Setenv("NVSNAP_DEFAULT_CAPTURE_PATH", v)
		if got := criuIsDefault(); got != want {
			t.Errorf("NVSNAP_DEFAULT_CAPTURE_PATH=%q: criuIsDefault = %v", v, got)
		}
	}
}

func TestCRIURestorer_FailedCheckpointsAreBlocked(t *testing.T) {
	kc := fake.NewSimpleClientset()
	a := &Agent{kubeClient: kc, log: logrus.New()}
	c := a.criuRestorer()
	ctx := context.Background()
	if c.Blocked(ctx, "c1") {
		t.Fatal("blocked before any failure")
	}
	if err := c.markFailed(ctx, "c1", errors.New("restore failed: exit status 1")); err != nil {
		t.Fatal(err)
	}
	if err := c.markFailed(ctx, "c2", errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	if !c.Blocked(ctx, "c1") || !c.Blocked(ctx, "c2") || c.Blocked(ctx, "c3") {
		t.Error("blocked list wrong after two failures")
	}
	cm, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuRestoreFailedConfigMap, metav1.GetOptions{})
	if err != nil || len(cm.Data) != 2 {
		t.Fatalf("failed-checkpoint list = %v, %v", cm, err)
	}
	// Another agent's failure shows up once the cache expires.
	cm.Data["c3"] = time.Now().UTC().Format(time.RFC3339) + " elsewhere"
	if _, err := kc.CoreV1().ConfigMaps("nvsnap-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.blockedAt = time.Now().Add(-2 * criuBlockedCacheTTL)
	c.mu.Unlock()
	if !c.Blocked(ctx, "c3") {
		t.Error("a failure recorded by another agent is not seen after the cache expires")
	}
}

// A second capture of the same hash leaves the record on the first
// checkpoint, the one the content-addressed catalog keeps.
func TestRecordCRIUCapture_FirstCaptureOfAHashWins(t *testing.T) {
	local, err := checkpointstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cm := checkpointstore.NewConfigMapBackend(local, fake.NewSimpleClientset(), "nvsnap-system", logrus.New())
	a := &Agent{captureBackend: cm, log: logrus.New(), config: Config{NodeName: "n1"}}
	ctx, log := context.Background(), logrus.NewEntry(logrus.New())
	req := CheckpointRequest{Namespace: "ns", PodName: "p"}
	a.recordCRIUCapture(ctx, "d9a846172fb1663b4e26ea05c4425ab454386b7dd45d5c01bafbd42445439f93", "d9a8__first", req, "img", false, log)
	a.recordCRIUCapture(ctx, "d9a846172fb1663b4e26ea05c4425ab454386b7dd45d5c01bafbd42445439f93", "d9a8__second", req, "img", false, log)
	got, err := cm.Stat(ctx, "d9a846172fb1663b4e26ea05c4425ab454386b7dd45d5c01bafbd42445439f93")
	if err != nil {
		t.Fatal(err)
	}
	if got.CaptureMethod != "criu" || got.SourcePodMeta["checkpoint_id"] != "d9a8__first" {
		t.Errorf("record = %+v, want the first checkpoint", got)
	}
}

// The record carries the tenant scope of the capture's namespace, which
// the webhook matches against a restoring pod's namespace: the function
// version when the platform labelled the namespace, else the namespace.
func TestRecordCRIUCapture_RecordsTheTenantScope(t *testing.T) {
	const hash = "5c0be5c0be5c0be5c0be5c0be5c0be5c0be5c0be5c0be5c0be5c0be5c0be5c0"
	ctx, log := context.Background(), logrus.NewEntry(logrus.New())
	labelled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "scope-fv-ns", Labels: map[string]string{"function-version-id": "fv-1"}}}
	plain := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "scope-plain-ns"}}
	for ns, want := range map[string]string{"scope-fv-ns": "function-version/fv-1", "scope-plain-ns": "namespace/scope-plain-ns"} {
		local, err := checkpointstore.NewLocal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cm := checkpointstore.NewConfigMapBackend(local, fake.NewSimpleClientset(), "nvsnap-system", logrus.New())
		a := &Agent{captureBackend: cm, kubeClient: fake.NewSimpleClientset(labelled, plain), log: logrus.New(), config: Config{NodeName: "n1"}}
		a.recordCRIUCapture(ctx, hash, "5c0b__1", CheckpointRequest{Namespace: ns, PodName: "p"}, "img", false, log)
		got, err := cm.Stat(ctx, hash)
		if err != nil {
			t.Fatal(err)
		}
		if got.SourcePodMeta[checkpointstore.CRIUScopeMetaKey] != want {
			t.Errorf("%s: recorded scope %q, want %q", ns, got.SourcePodMeta[checkpointstore.CRIUScopeMetaKey], want)
		}
	}

	// A scope that cannot be read leaves no record: the capture could
	// never be restored, and an unscoped record must not exist.
	local, err := checkpointstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cm := checkpointstore.NewConfigMapBackend(local, fake.NewSimpleClientset(), "nvsnap-system", logrus.New())
	a := &Agent{captureBackend: cm, kubeClient: fake.NewSimpleClientset(), log: logrus.New(), config: Config{NodeName: "n1"}}
	a.recordCRIUCapture(ctx, hash, "5c0b__1", CheckpointRequest{Namespace: "scope-missing-ns", PodName: "p"}, "img", false, log)
	if _, err := cm.Stat(ctx, hash); !errors.Is(err, checkpointstore.ErrNotFound) {
		t.Errorf("capture recorded although its scope was unreadable: %v", err)
	}
}

// A pod admitted with the gpushare library and store mounted whose
// processes never loaded the library (their GPU state went through the
// CUDA plugin) is recorded as gpushare all the same: the dump saved
// those mounts, and the restore placeholder needs their mountpoints, or
// CRIU fails with "Can't stat mountpoint .../nvsnap".
func TestRecordCRIUCapture_RecordsGPUShareMountsTheProcessDidNotUse(t *testing.T) {
	const hash = "7a117a117a117a117a117a117a117a117a117a117a117a117a117a117a117a11"
	ctx, log := context.Background(), logrus.NewEntry(logrus.New())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fn-ns"}}
	podWith := func(name string, mounts ...string) *corev1.Pod {
		var vms []corev1.VolumeMount
		for _, m := range mounts {
			vms = append(vms, corev1.VolumeMount{Name: "v", MountPath: m})
		}
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fn-ns"},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "inference", VolumeMounts: vms}}}}
	}
	for _, tc := range []struct {
		name     string
		pod      *corev1.Pod
		gpushare bool
		want     bool
	}{
		{"mounted, library unused", podWith("mounted", "/nvsnap", "/nvsnap-gpushare"), false, true},
		{"mounted, library used", podWith("used", "/nvsnap", "/nvsnap-gpushare"), true, true},
		{"not mounted", podWith("plain", "/var/inf"), false, false},
	} {
		local, err := checkpointstore.NewLocal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cm := checkpointstore.NewConfigMapBackend(local, fake.NewSimpleClientset(), "nvsnap-system", logrus.New())
		a := &Agent{captureBackend: cm, kubeClient: fake.NewSimpleClientset(ns, tc.pod), log: logrus.New(), config: Config{NodeName: "n1"}}
		req := CheckpointRequest{Namespace: "fn-ns", PodName: tc.pod.Name, ContainerName: "inference"}
		a.recordCRIUCapture(ctx, hash, "7a11__1", req, "img", tc.gpushare, log)
		got, err := cm.Stat(ctx, hash)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (got.SourcePodMeta["gpushare"] == "true") != tc.want {
			t.Errorf("%s: recorded gpushare=%q, want %v", tc.name, got.SourcePodMeta["gpushare"], tc.want)
		}
	}
}
