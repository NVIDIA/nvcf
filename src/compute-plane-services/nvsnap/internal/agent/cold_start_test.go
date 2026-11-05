// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/runtime"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func ownedBy(kind string, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{Kind: kind, Name: "o", Controller: &controller}
}

func TestPodRecreatedOnDelete(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owners []metav1.OwnerReference
		want   bool
	}{
		{"bare pod", nil, false},
		{"ReplicaSet", []metav1.OwnerReference{ownedBy("ReplicaSet", true)}, true},
		{"StatefulSet", []metav1.OwnerReference{ownedBy("StatefulSet", true)}, true},
		{"Job", []metav1.OwnerReference{ownedBy("Job", true)}, true},
		{"DaemonSet", []metav1.OwnerReference{ownedBy("DaemonSet", true)}, true},
		{"PodClique", []metav1.OwnerReference{ownedBy("PodClique", true)}, true},
		// An owner that does not replace its pods: a request object that
		// reports a deleted pod as a terminated instance.
		{"custom owner", []metav1.OwnerReference{ownedBy("ICMSRequest", true)}, false},
		{"ReplicaSet, not controller", []metav1.OwnerReference{ownedBy("ReplicaSet", false)}, false},
	} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{OwnerReferences: tc.owners}}
		if got := podRecreatedOnDelete(pod); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIdleCanColdStart(t *testing.T) {
	nul := func(args ...string) []byte { return []byte(strings.Join(args, "\x00") + "\x00") }
	for _, tc := range []struct {
		cmdline []byte
		want    bool
	}{
		{nul(webhook.IdleBin, "--cold-start", webhook.ColdStartMarker, "--", "vllm"), true},
		{nul(webhook.IdleBin), false},                                // an older webhook: no argv
		{nul("vllm", "--cold-start", "m", "--", "x"), false},         // the workload itself
		{nul(webhook.IdleBin, "--cold-start", "m", "x", "y"), false}, // malformed
	} {
		if got := idleCanColdStart(tc.cmdline); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.cmdline, got, tc.want)
		}
	}
}

// pidRuntime answers FindContainerByPod with one container's pid.
type pidRuntime struct {
	runtime.Runtime
	pid uint32
}

func (r pidRuntime) FindContainerByPod(context.Context, string, string, string) (*runtime.ContainerInfo, error) {
	return &runtime.ContainerInfo{Name: "inference", PID: r.pid}, nil
}

func (r pidRuntime) ContainerSpec(context.Context, string) (*specs.Spec, error) {
	return nil, runtime.ErrNotFound
}

// startPlaceholder runs the real nvsnap-idle as a placeholder would: argv[0]
// is its in-container path, then the cold start the webhook passes.
func startPlaceholder(t *testing.T, args ...string) (cmd *exec.Cmd, marker string) {
	t.Helper()
	src, err := filepath.Abs("../../cmd/nvsnap-idle/c/nvsnap-idle.c")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Skipf("idle source not available: %v", statErr)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "nvsnap-idle")
	if out, err := exec.CommandContext(t.Context(), cc, "-Os", "-o", bin, src).CombinedOutput(); err != nil { //nolint:gosec // builds the program under test
		t.Fatalf("compile: %v\n%s", err, out)
	}
	marker = filepath.Join(dir, "cold-start")
	argv := []string{webhook.IdleBin}
	if args != nil {
		argv = append(argv, append([]string{"--cold-start", marker, "--"}, args...)...)
	}
	cmd = &exec.Cmd{Path: bin, Args: argv}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(200 * time.Millisecond)
	return cmd, marker
}

// procPID is cmd's pid as the runtime reports it.
func procPID(cmd *exec.Cmd) uint32 {
	return uint32(cmd.Process.Pid) //nolint:gosec // a pid is positive and fits
}

func placeholderPod(owners ...metav1.OwnerReference) *corev1.Pod {
	p := criuPlaceholderPod(corev1.PodRunning, true)
	p.OwnerReferences = owners
	return p
}

// A failed restore in a pod nothing recreates starts the workload in place:
// the pod stays, is marked so no restore is tried in it again, and its
// readiness follows the workload's own probe.
func TestDisposeFailedPlaceholder_ColdStartsAPodNothingRecreates(t *testing.T) {
	out := filepath.Join(t.TempDir(), "started")
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "touch "+out+"; exec sleep 30")
	pod := placeholderPod(ownedBy("ICMSRequest", true))
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	ctx := context.Background()

	a.disposeFailedPlaceholder(ctx, pod.Namespace, pod.Name, "inference", logrus.NewEntry(a.log))

	got, err := kc.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the pod was deleted: %v", err)
	}
	if !coldStarted(got) {
		t.Error("the pod is not marked started cold")
	}
	if _, _, ok := criuRestoreTarget(got); ok {
		t.Error("a pod started cold is still a restore target")
	}
	if !restoredConditionTrue(got) {
		t.Error("the readiness gate still holds the pod")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal("the workload did not start")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("no cold-start marker for a container restart")
	}
}

// A pod whose controller replaces it is deleted, as before.
func TestDisposeFailedPlaceholder_DeletesAPodItsControllerReplaces(t *testing.T) {
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "exec sleep 30")
	pod := placeholderPod(ownedBy("ReplicaSet", true))
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	ctx := context.Background()

	a.disposeFailedPlaceholder(ctx, pod.Namespace, pod.Name, "inference", logrus.NewEntry(a.log))

	if _, err := kc.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
		t.Error("the pod was kept; its controller would have replaced it")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a pod being replaced was started cold")
	}
}

// A pod nothing recreates whose placeholder cannot start cold (admitted by
// an older webhook) is deleted, as before: there is no other way out.
func TestDisposeFailedPlaceholder_DeletesWhenItCannotColdStart(t *testing.T) {
	cmd, _ := startPlaceholder(t)
	pod := placeholderPod()
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	ctx := context.Background()

	a.disposeFailedPlaceholder(ctx, pod.Namespace, pod.Name, "inference", logrus.NewEntry(a.log))

	if _, err := kc.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
		t.Error("kept a pod that cannot start cold")
	}
	if cmd.ProcessState != nil {
		t.Error("the placeholder process was disturbed")
	}
}

// A restored pod nothing recreates whose engine later dies starts cold in
// place too, rather than being deleted.
func TestFailRestored_ColdStartsAPodNothingRecreates(t *testing.T) {
	out := filepath.Join(t.TempDir(), "started")
	cmd, _ := startPlaceholder(t, "/bin/sh", "-c", "touch "+out+"; exec sleep 30")
	pod := placeholderPod()
	pod.Annotations[webhook.CRIURestoredAnnotation] = time.Now().UTC().Format(time.RFC3339)
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	c := a.criuRestorer()
	ctx := context.Background()

	c.failRestored(ctx, pod, restoredRef{checkpoint: "abc__20261008-101010"}, errors.New("the restored engine is gone"))

	got, err := kc.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the pod was deleted: %v", err)
	}
	if !coldStarted(got) || podStartMethod(got) != "cold" {
		t.Errorf("not started cold: annotations %v, method %s", got.Annotations, podStartMethod(got))
	}
	if !c.Blocked(ctx, "abc__20261008-101010") {
		t.Error("the checkpoint is not blocked")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the workload did not start")
}

// The capture records the argv the runtime started the container with, for
// a cold start in place of a placeholder whose image names the entrypoint.
func TestContainerColdArgv(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "inference"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "inference", ContainerID: "containerd://c1"},
		}},
	}
	rt := specRuntime{specs: map[string]*specs.Spec{"c1": {Process: &specs.Process{Args: []string{"/opt/nim/start.sh", "--port", "8000"}}}}}
	a := &Agent{kubeClient: fake.NewSimpleClientset(pod), log: logrus.New(), runtime: rt}
	ctx := context.Background()
	if got := a.containerColdArgv(ctx, "ns", "p", "inference"); !reflect.DeepEqual(got, []string{"/opt/nim/start.sh", "--port", "8000"}) {
		t.Errorf("argv = %v", got)
	}
	if got := a.containerColdArgv(ctx, "ns", "p", "other"); got != nil {
		t.Errorf("unknown container: %v", got)
	}
	// A placeholder's own argv is never recorded as the workload's.
	rt.specs["c1"].Process.Args = []string{webhook.IdleBin}
	if got := a.containerColdArgv(ctx, "ns", "p", "inference"); got != nil {
		t.Errorf("placeholder argv recorded: %v", got)
	}
}
