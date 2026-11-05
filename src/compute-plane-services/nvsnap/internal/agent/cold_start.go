// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Cold start in place. When nvsnap gives up on a pod (its restore failed,
// or it never started under the gpushare library), the way out used to be
// to delete it so its controller made a fresh one. A pod nothing recreates
// (a bare pod, one owned by an object that does not replace its pods) is
// then simply gone, and whatever counted on it never gets it back. Such a
// pod is kept: a restore placeholder starts its workload in place, the way
// the pod would have started without nvsnap (cmd/nvsnap-idle), and a pod
// that fails under the library is left as it is.
//
// The placeholder's container is restarted, not signalled: a restore that
// failed after CRIU recreated the processes leaves them running inside it,
// and killing the container's init ends its whole pid namespace, those
// processes with it. Every step can be repeated, so an agent that stops
// half way through is finished by the next one (considerColdStart).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// recreatingOwners are the controller kinds known to replace a pod of
// theirs that is deleted. A pod owned by any other kind, or by none, is
// treated as one nothing recreates: keeping it costs a cold start in place,
// deleting it may cost the pod.
var recreatingOwners = map[string]bool{
	"ReplicaSet":            true,
	"ReplicationController": true,
	"StatefulSet":           true,
	"DaemonSet":             true,
	"Job":                   true,
	"PodClique":             true, // grove.io: Dynamo multi-node graphs
}

// podRecreatedOnDelete reports whether a controller replaces pod when it is
// deleted.
func podRecreatedOnDelete(pod *corev1.Pod) bool {
	for _, o := range pod.OwnerReferences {
		if o.Controller != nil && *o.Controller && recreatingOwners[o.Kind] {
			return true
		}
	}
	return false
}

// coldStarted reports whether pod is a placeholder the agent started cold:
// there is nothing left to restore into it.
func coldStarted(pod *corev1.Pod) bool {
	return pod.Annotations[webhook.ColdStartedAnnotation] != ""
}

// errNoColdStart marks a placeholder that cannot start cold in place: its
// idle program was not given the workload's argv (an older webhook, or a
// capture that did not record it), or the pod's containers would not be
// restarted in place.
var errNoColdStart = errors.New("the placeholder cannot start its workload in place")

// disposeFailedPlaceholder gets placeholder ns/name (uid, when known),
// whose restore failed, going again. Deleting it leaves its replacement to
// its controller; a pod nothing recreates would just be gone, so it starts
// its workload cold in place instead, and is deleted only when it cannot.
// A pod that is no longer the one whose restore failed (deleted, or
// replaced under the same name) is left alone.
func (a *Agent) disposeFailedPlaceholder(ctx context.Context, ns, name string, uid types.UID, container string, log *logrus.Entry) {
	pod, err := a.kubeClient.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.WithError(err).Error("CRIU auto-restore failed; cannot read the pod, so it is left as it is")
		}
		return
	}
	if uid != "" && pod.UID != uid {
		log.Warn("CRIU auto-restore failed in a pod that has since been replaced; the replacement is left alone")
		return
	}
	if !podRecreatedOnDelete(pod) {
		cerr := a.coldStartInPlace(ctx, pod, container, log)
		if cerr == nil {
			log.Warn("CRIU auto-restore failed in a pod nothing would recreate; its workload starts cold in place")
			return
		}
		log.WithError(cerr).Error("CRIU auto-restore failed in a pod nothing would recreate, and it cannot start cold in place; deleting it")
	}
	if err := a.deletePodRetrying(ctx, ns, name, pod.UID); err != nil {
		log.WithError(err).Error("CRIU auto-restore: could not delete the failed pod")
	}
}

// coldStartInPlace starts the workload of placeholder pod's container in
// place: it leaves the idle program's marker on the restore work volume,
// marks the pod so no restore is tried in it again, and kills the
// container's init. The container restarts, and its idle program, finding
// the marker, execs the workload; the pod's readiness then follows the
// workload's own probe.
func (a *Agent) coldStartInPlace(ctx context.Context, pod *corev1.Pod, container string, log *logrus.Entry) error {
	if pod.DeletionTimestamp != nil {
		return errors.New("the pod is being deleted")
	}
	if pod.Spec.ShareProcessNamespace != nil && *pod.Spec.ShareProcessNamespace {
		// The container's init is not the pid namespace's: killing it
		// would leave a failed restore's processes running.
		return fmt.Errorf("%w: the pod shares one pid namespace between its containers", errNoColdStart)
	}
	if pod.Spec.RestartPolicy == corev1.RestartPolicyNever {
		return fmt.Errorf("%w: the pod's containers are never restarted", errNoColdStart)
	}
	pid, marker, err := a.coldStartTarget(ctx, pod, container)
	if err != nil {
		return err
	}
	if err := writeColdStartMarker(pid, marker); err != nil {
		return err
	}
	if !coldStarted(pod) {
		// The uid in the patch makes it fail on a pod replaced under the
		// same name since it was read.
		patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{
			"uid":         pod.UID,
			"annotations": map[string]string{webhook.ColdStartedAnnotation: time.Now().UTC().Format(time.RFC3339)},
		}})
		if _, err := a.kubeClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("mark the pod started cold: %w", err)
		}
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("restart the placeholder container (pid %d): %w", pid, err)
	}
	if err := a.setRestoredConditionReason(ctx, pod.Namespace, pod.Name, "ColdStarted",
		"the restore failed; the workload started cold in place and its own readiness probe decides"); err != nil {
		log.WithError(err).Warn("cold start: cannot set the pod's restored condition yet; the agent sets it on the pod's next event")
	}
	return nil
}

// coldStartTarget returns the host pid of placeholder pod's container
// init, while it is still the idle program given a cold start, and the
// marker path that program checks.
func (a *Agent) coldStartTarget(ctx context.Context, pod *corev1.Pod, container string) (pid int, marker string, err error) {
	if a.runtime == nil {
		return 0, "", errors.New("no container runtime client")
	}
	if container == "" {
		// The runtime would answer with the pod's first container, which
		// may be a sidecar.
		return 0, "", errors.New("the placeholder container is not named")
	}
	info, ferr := a.runtime.FindContainerByPod(ctx, pod.Namespace, pod.Name, container)
	if ferr != nil {
		return 0, "", fmt.Errorf("find the placeholder container: %w", ferr)
	}
	if info == nil || info.PID == 0 {
		return 0, "", errors.New("the placeholder container has no running process")
	}
	if want := containerIDOf(pod, container); want != "" && info.ID != "" && info.ID != want {
		return 0, "", fmt.Errorf("the placeholder container is %s, not the running %s", want, info.ID)
	}
	pid = int(info.PID)
	cmdline, rerr := os.ReadFile(hostProcBase() + "/" + strconv.Itoa(pid) + "/cmdline")
	if rerr != nil {
		return 0, "", fmt.Errorf("read the placeholder's command line: %w", rerr)
	}
	marker, ok := idleColdStartMarker(cmdline)
	if !ok {
		return 0, "", errNoColdStart
	}
	return pid, marker, nil
}

// idleColdStartMarker returns the marker of a NUL-separated command line
// that is the idle program given a workload to start (nvsnap-idle
// --cold-start MARKER -- ...). Only the webhook's own marker path counts:
// the agent writes it as root.
func idleColdStartMarker(cmdline []byte) (string, bool) {
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	if len(args) >= 5 && args[0] == webhook.IdleBin && args[1] == "--cold-start" && args[2] == webhook.ColdStartMarker && args[3] == "--" {
		return args[2], true
	}
	return "", false
}

// writeColdStartMarker creates marker, a path inside the container whose
// init is pid. The directory is the restore work volume's mount point,
// which the container cannot replace; the file itself is never followed if
// it is a symlink.
func writeColdStartMarker(pid int, marker string) error {
	path := filepath.Join(containerRoot(pid), marker)
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("write the cold start marker: %w", err)
	}
	return syscall.Close(fd)
}

// containerRoot is the root filesystem of the container whose init is pid,
// as the agent reaches it.
var containerRoot = func(pid int) string {
	return filepath.Join(hostProcBase(), strconv.Itoa(pid), "root")
}

// considerColdStart finishes a cold start in place that an agent left half
// done: the pod is marked, but its container still runs the idle program.
func (c *criuAutoRestorer) considerColdStart(ctx context.Context, pod *corev1.Pod) {
	if !coldStarted(pod) || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return
	}
	container := pod.Annotations[webhook.CRIURestoreContainerAnnotation]
	running := false
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		running = running || (cs.Name == container && cs.State.Running != nil)
	}
	if !running {
		return
	}
	if _, busy := c.attempted.LoadOrStore("cold-start/"+string(pod.UID), struct{}{}); busy {
		return
	}
	go func() {
		defer c.attempted.Delete("cold-start/" + string(pod.UID))
		if _, _, err := c.a.coldStartTarget(ctx, pod, container); err != nil {
			return // the workload runs, or the container is not the placeholder's
		}
		log := c.a.log.WithField("pod", pod.Namespace+"/"+pod.Name)
		if err := c.a.coldStartInPlace(ctx, pod, container, log); err != nil {
			log.WithError(err).Warn("cold start: a pod marked to start cold still idles, and cannot be restarted yet; trying again on its next event")
			return
		}
		log.Info("cold start: restarted a pod marked to start cold that still idled")
	}()
}

// containerColdEntrypoint returns what the runtime started ns/name's
// container with ahead of the pod's own args: the image entrypoint, and
// its command when the pod gives no args. Nil when the pod names its own
// command (a placeholder runs that), or when it cannot tell.
//
// The runtime's argv has the pod's args with their $(VAR) references
// expanded for that pod; a placeholder gets the entrypoint and its own
// args, which the kubelet expands for it.
func (a *Agent) containerColdEntrypoint(ctx context.Context, ns, name, container string) []string {
	if a.runtime == nil || a.kubeClient == nil || ns == "" || name == "" {
		return nil
	}
	pod, err := a.kubeClient.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	if container == "" {
		container = gpuContainer(pod)
	}
	var spec *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == container {
			spec = &pod.Spec.Containers[i]
		}
	}
	if spec == nil || len(spec.Command) > 0 {
		return nil
	}
	id := containerIDOf(pod, container)
	if id == "" {
		return nil
	}
	oci, err := a.runtime.ContainerSpec(ctx, id)
	if err != nil || oci == nil || oci.Process == nil {
		return nil
	}
	argv := oci.Process.Args
	n := len(argv) - len(spec.Args)
	if n <= 0 || argv[0] == webhook.IdleBin {
		return nil
	}
	return append([]string(nil), argv[:n]...)
}
