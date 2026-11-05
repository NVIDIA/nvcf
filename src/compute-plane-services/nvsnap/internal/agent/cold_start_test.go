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

func TestIdleColdStartMarker(t *testing.T) {
	nul := func(args ...string) []byte { return []byte(strings.Join(args, "\x00") + "\x00") }
	for _, tc := range []struct {
		cmdline []byte
		want    bool
	}{
		{nul(webhook.IdleBin, "--cold-start", webhook.ColdStartMarker, "--", "vllm"), true},
		{nul(webhook.IdleBin), false},                                                    // an older webhook: no argv
		{nul("vllm", "--cold-start", webhook.ColdStartMarker, "--", "x"), false},         // the workload itself
		{nul(webhook.IdleBin, "--cold-start", webhook.ColdStartMarker, "x", "y"), false}, // malformed
		{nul(webhook.IdleBin, "--cold-start", "/etc/passwd", "--", "vllm"), false},       // any other path: the agent writes it as root
	} {
		if _, got := idleColdStartMarker(tc.cmdline); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.cmdline, got, tc.want)
		}
	}
}

// pidRuntime answers FindContainerByPod with one container's pid and id.
type pidRuntime struct {
	runtime.Runtime
	pid uint32
	id  string
}

func (r pidRuntime) FindContainerByPod(context.Context, string, string, string) (*runtime.ContainerInfo, error) {
	return &runtime.ContainerInfo{Name: "inference", PID: r.pid, ID: r.id}, nil
}

func (r pidRuntime) ContainerSpec(context.Context, string) (*specs.Spec, error) {
	return nil, runtime.ErrNotFound
}

// startPlaceholder runs the real nvsnap-idle as a placeholder would: argv[0]
// is its in-container path, then the cold start the webhook passes. The
// container's root is a temporary directory holding the restore work
// volume; it returns the marker's path there.
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
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(filepath.Join(root, webhook.RestoreWorkMountPath), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := containerRoot
	containerRoot = func(int) string { return root }
	t.Cleanup(func() { containerRoot = prev })
	argv := []string{webhook.IdleBin}
	if args != nil {
		argv = append(argv, append([]string{"--cold-start", webhook.ColdStartMarker, "--"}, args...)...)
	}
	cmd = &exec.Cmd{Path: bin, Args: argv}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	time.Sleep(200 * time.Millisecond)
	return cmd, filepath.Join(root, webhook.ColdStartMarker)
}

// procPID is cmd's pid as the runtime reports it.
func procPID(cmd *exec.Cmd) uint32 {
	return uint32(cmd.Process.Pid) //nolint:gosec // a pid is positive and fits
}

// exited reports whether cmd's process ends within a second.
func exited(cmd *exec.Cmd) bool {
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(time.Second):
		return false
	}
}

func placeholderPod(owners ...metav1.OwnerReference) *corev1.Pod {
	p := criuPlaceholderPod(corev1.PodRunning, true)
	p.OwnerReferences = owners
	return p
}

// A failed restore in a pod nothing recreates starts the workload cold in
// place: the marker is left for the idle program, the pod is marked so no
// restore is tried in it again, its readiness follows the workload, and the
// container's init is killed so its pid namespace, a failed restore's
// processes included, ends and the container restarts.
func TestDisposeFailedPlaceholder_ColdStartsAPodNothingRecreates(t *testing.T) {
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "exec sleep 30")
	pod := placeholderPod(ownedBy("ICMSRequest", true))
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	ctx := context.Background()

	a.disposeFailedPlaceholder(ctx, pod.Namespace, pod.Name, pod.UID, "inference", logrus.NewEntry(a.log))

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
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("no cold start marker: %v", err)
	}
	if !exited(cmd) {
		t.Error("the placeholder's init still runs; the container was not restarted")
	}
}

// A pod whose controller replaces it is deleted, as before, and its
// placeholder left untouched.
func TestDisposeFailedPlaceholder_DeletesAPodItsControllerReplaces(t *testing.T) {
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "exec sleep 30")
	pod := placeholderPod(ownedBy("ReplicaSet", true))
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	ctx := context.Background()

	a.disposeFailedPlaceholder(ctx, pod.Namespace, pod.Name, pod.UID, "inference", logrus.NewEntry(a.log))

	if _, err := kc.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
		t.Error("the pod was kept; its controller would have replaced it")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a pod being replaced was marked to start cold")
	}
}

// The pod is deleted, as before, when it cannot start cold in place: its
// placeholder was given no workload (an older webhook), its containers
// share one pid namespace, or they are never restarted.
func TestDisposeFailedPlaceholder_DeletesWhenItCannotColdStart(t *testing.T) {
	share := true
	for name, tc := range map[string]struct {
		args  []string
		setup func(*corev1.Pod)
	}{
		"no workload given": {nil, func(*corev1.Pod) {}},
		"shared pid ns":     {[]string{"/bin/true"}, func(p *corev1.Pod) { p.Spec.ShareProcessNamespace = &share }},
		"never restarted":   {[]string{"/bin/true"}, func(p *corev1.Pod) { p.Spec.RestartPolicy = corev1.RestartPolicyNever }},
	} {
		t.Run(name, func(t *testing.T) {
			cmd, marker := startPlaceholder(t, tc.args...)
			pod := placeholderPod()
			tc.setup(pod)
			kc := fake.NewSimpleClientset(pod)
			a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
			ctx := context.Background()

			a.disposeFailedPlaceholder(ctx, pod.Namespace, pod.Name, pod.UID, "inference", logrus.NewEntry(a.log))

			if _, err := kc.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
				t.Error("kept a pod that cannot start cold")
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("marked a pod that cannot start cold")
			}
		})
	}
}

// A pod replaced under the same name since its restore failed is left
// alone: the replacement may already serve.
func TestDisposeFailedPlaceholder_LeavesAReplacementAlone(t *testing.T) {
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "exec sleep 30")
	pod := placeholderPod()
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	ctx := context.Background()

	a.disposeFailedPlaceholder(ctx, pod.Namespace, pod.Name, "uid-of-the-failed-pod", "inference", logrus.NewEntry(a.log))

	got, err := kc.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the replacement was deleted: %v", err)
	}
	if coldStarted(got) {
		t.Error("the replacement was marked to start cold")
	}
	if _, err := os.Stat(marker); err == nil || exited(cmd) {
		t.Error("the replacement's container was touched")
	}
}

// A container other than the one the pod's status names (it restarted
// since) is not touched.
func TestColdStartInPlace_OnlyTheNamedContainer(t *testing.T) {
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "exec sleep 30")
	pod := placeholderPod()
	pod.Status.ContainerStatuses[1].ContainerID = "containerd://new"
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd), id: "old"}}
	if err := a.coldStartInPlace(context.Background(), pod, "inference", logrus.NewEntry(a.log)); err == nil {
		t.Error("cold started a container the pod's status does not name")
	}
	if _, err := os.Stat(marker); err == nil || exited(cmd) {
		t.Error("the container was touched")
	}
}

// A cold start an agent left half done (the pod marked, its container
// still idling) is finished on the pod's next event.
func TestConsiderColdStart_FinishesAHalfDoneColdStart(t *testing.T) {
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "exec sleep 30")
	pod := placeholderPod()
	pod.Annotations[webhook.ColdStartedAnnotation] = time.Now().UTC().Format(time.RFC3339)
	kc := fake.NewSimpleClientset(pod)
	a := &Agent{kubeClient: kc, log: logrus.New(), runtime: pidRuntime{pid: procPID(cmd)}}
	a.criuRestorer().considerColdStart(context.Background(), pod)
	if !exited(cmd) {
		t.Fatal("the idling container was not restarted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("no cold start marker: %v", err)
	}
}

// A restored pod nothing recreates whose engine later dies starts cold in
// place too, rather than being deleted.
func TestFailRestored_ColdStartsAPodNothingRecreates(t *testing.T) {
	cmd, marker := startPlaceholder(t, "/bin/sh", "-c", "exec sleep 30")
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
	if _, err := os.Stat(marker); err != nil || !exited(cmd) {
		t.Errorf("not restarted to start cold: marker %v", err)
	}
}

// The capture records what the runtime ran ahead of the pod's own args:
// the image entrypoint. The args themselves come from the restoring pod.
func TestContainerColdEntrypoint(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "inference", Args: []string{"--bind=$(POD_IP)"}}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "inference", ContainerID: "containerd://c1"},
		}},
	}
	rt := specRuntime{specs: map[string]*specs.Spec{"c1": {Process: &specs.Process{Args: []string{"/opt/nim/start.sh", "--bind=10.0.0.7"}}}}}
	a := &Agent{kubeClient: fake.NewSimpleClientset(pod), log: logrus.New(), runtime: rt}
	ctx := context.Background()
	if got := a.containerColdEntrypoint(ctx, "ns", "p", "inference"); !reflect.DeepEqual(got, []string{"/opt/nim/start.sh"}) {
		t.Errorf("entrypoint = %v", got)
	}
	if got := a.containerColdEntrypoint(ctx, "ns", "p", "other"); got != nil {
		t.Errorf("unknown container: %v", got)
	}
	// A pod that names its command needs none: the placeholder runs it.
	withCommand := pod.DeepCopy()
	withCommand.Name = "q"
	withCommand.Spec.Containers[0].Command = []string{"vllm"}
	a.kubeClient = fake.NewSimpleClientset(withCommand)
	if got := a.containerColdEntrypoint(ctx, "ns", "q", "inference"); got != nil {
		t.Errorf("pod with a command: %v", got)
	}
	// A placeholder's own argv is never recorded as the workload's.
	a.kubeClient = fake.NewSimpleClientset(pod)
	rt.specs["c1"].Process.Args = []string{webhook.IdleBin, "--bind=10.0.0.7"}
	if got := a.containerColdEntrypoint(ctx, "ns", "p", "inference"); got != nil {
		t.Errorf("placeholder argv recorded: %v", got)
	}
}
