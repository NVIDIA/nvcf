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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// coldStarted reports whether pod is a placeholder the agent already
// started cold: there is nothing left to restore into it.
func coldStarted(pod *corev1.Pod) bool {
	return pod.Annotations[webhook.ColdStartedAnnotation] != ""
}

// errNoColdStart marks a placeholder that cannot start cold in place: its
// idle program was not given the workload's argv (an older webhook, or a
// capture that did not record it).
var errNoColdStart = errors.New("the placeholder cannot start its workload in place")

// coldStartInPlace starts the workload of placeholder ns/name's container
// in place: it marks the pod, so no restore is tried in it again, signals
// the container's idle program to exec the workload, and lets the pod's
// readiness follow the workload's own probe.
func (a *Agent) coldStartInPlace(ctx context.Context, ns, name, container string, log *logrus.Entry) error {
	if a.runtime == nil {
		return errors.New("no container runtime client")
	}
	if container == "" {
		// The runtime would answer with the pod's first container, which
		// may be a sidecar.
		return errors.New("the placeholder container is not named")
	}
	info, err := a.runtime.FindContainerByPod(ctx, ns, name, container)
	if err != nil {
		return fmt.Errorf("find the placeholder container: %w", err)
	}
	if info == nil || info.PID == 0 {
		return errors.New("the placeholder container has no running process")
	}
	pid := int(info.PID)
	cmdline, err := os.ReadFile(hostProcBase() + "/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return fmt.Errorf("read the placeholder's command line: %w", err)
	}
	if !idleCanColdStart(cmdline) {
		return errNoColdStart
	}
	// Marked first: once the workload runs, a restore into the pod must
	// never be tried again, by this agent or a restarted one.
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
		webhook.ColdStartedAnnotation: time.Now().UTC().Format(time.RFC3339),
	}}})
	if _, err := a.kubeClient.CoreV1().Pods(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("mark the pod started cold: %w", err)
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("signal the placeholder (pid %d): %w", pid, err)
	}
	if err := a.setRestoredConditionReason(ctx, ns, name, "ColdStarted",
		"the restore failed; the workload started cold in place and its own readiness probe decides"); err != nil {
		log.WithError(err).Warn("cold start: cannot set the pod's restored condition yet; the agent sets it on the pod's next event")
	}
	return nil
}

// idleCanColdStart reports whether the NUL-separated command line is the
// idle program given a workload to start (nvsnap-idle --cold-start ...).
func idleCanColdStart(cmdline []byte) bool {
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	return len(args) >= 5 && args[0] == webhook.IdleBin && args[1] == "--cold-start" && args[3] == "--"
}

// containerColdArgv returns the argv the runtime started ns/name's
// container with (its OCI spec's process.args), or nil when it cannot tell.
func (a *Agent) containerColdArgv(ctx context.Context, ns, name, container string) []string {
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
	id := containerIDOf(pod, container)
	if id == "" {
		return nil
	}
	spec, err := a.runtime.ContainerSpec(ctx, id)
	if err != nil || spec == nil || spec.Process == nil || len(spec.Process.Args) == 0 || spec.Process.Args[0] == webhook.IdleBin {
		return nil
	}
	return spec.Process.Args
}

// keepPodNothingRecreates reports whether ns/name is a pod no controller
// would replace if deleted, reading it when pod is nil. A pod that cannot
// be read is not kept: it is gone, or the delete that follows fails too.
func (a *Agent) keepPodNothingRecreates(ctx context.Context, ns, name string, pod *corev1.Pod) (*corev1.Pod, bool) {
	if pod == nil {
		p, err := a.kubeClient.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				a.log.WithError(err).WithField("pod", ns+"/"+name).Warn("cannot read the pod to tell whether anything recreates it")
			}
			return nil, false
		}
		pod = p
	}
	return pod, !podRecreatedOnDelete(pod)
}
