// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A restored pod's container keeps the placeholder's shell as its pid 1,
// and the webhook removed its liveness probe, so the kubelet would not
// notice the restored engine dying: the pod would stay Running and never
// Ready. The agent watches its node's restored pods instead. When nothing
// but the placeholder's shell is left in the container, or the container
// restarted (the shell came back without the engine), the restore failed:
// its checkpoints are blocked and its pods deleted, so their replacements
// start fresh.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// placeholderComms are the processes of an idle placeholder.
var placeholderComms = map[string]bool{"sh": true, "sleep": true}

// considerRestored checks a restored pod's engine.
func (c *criuAutoRestorer) considerRestored(ctx context.Context, pod *corev1.Pod) {
	id, container := pod.Annotations[webhook.CRIURestoreAnnotation], pod.Annotations[webhook.CRIURestoreContainerAnnotation]
	if pod.Annotations[webhook.CRIURestoredAnnotation] == "" || id == "" || container == "" ||
		pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return
	}
	if _, busy := c.attempted.LoadOrStore("watch/"+string(pod.UID), struct{}{}); busy {
		return
	}
	go func() {
		defer c.attempted.Delete("watch/" + string(pod.UID))
		reason := c.restoredEngineGone(ctx, pod, container)
		if reason == "" {
			return
		}
		c.failRestored(ctx, pod, fmt.Errorf("the restored engine is gone: %s", reason))
	}()
}

// restoredEngineGone returns why the pod's restored engine is gone, or ""
// while it runs or when that cannot be told.
func (c *criuAutoRestorer) restoredEngineGone(ctx context.Context, pod *corev1.Pod, container string) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == container && cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil &&
			cs.LastTerminationState.Terminated.FinishedAt.After(restoredAt(pod).Time) {
			return fmt.Sprintf("container %s restarted after the restore", container)
		}
	}
	info, err := c.a.runtime.FindContainerByPod(ctx, pod.Namespace, pod.Name, container)
	if err != nil || info == nil || info.PID == 0 {
		return ""
	}
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	if onlyPlaceholderLeft(procBase, int(info.PID)) {
		return "only the placeholder's shell runs in container " + container
	}
	return ""
}

func restoredAt(pod *corev1.Pod) (t metav1.Time) {
	_ = t.UnmarshalQueryParameter(pod.Annotations[webhook.CRIURestoredAnnotation])
	return t
}

// onlyPlaceholderLeft reports whether the container whose pid 1 is hostPID
// runs nothing but the placeholder's shell and its sleeps.
func onlyPlaceholderLeft(procBase string, hostPID int) bool {
	tree := processTree(procBase, hostPID)
	if len(tree) == 0 {
		return false
	}
	for _, p := range tree {
		comm, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(p), "comm"))
		if err != nil {
			continue // exited meanwhile
		}
		if !placeholderComms[strings.TrimSpace(string(comm))] {
			return false
		}
	}
	return true
}

// failRestored ends a restored pod whose engine died: a lone pod, or every
// pod of its group.
func (c *criuAutoRestorer) failRestored(ctx context.Context, pod *corev1.Pod, cause error) {
	ctx = context.WithoutCancel(ctx)
	log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "checkpoint": pod.Annotations[webhook.CRIURestoreAnnotation]})
	if key := pod.Annotations[webhook.CRIUGroupAnnotation]; key != "" && isGroupPlaceholder(pod) {
		c.endGroup(ctx, pod, key, true, true, cause, log.WithField("criuGroup", key))
		return
	}
	log.WithError(cause).Error("CRIU restore failed after it ran; blocking the checkpoint and deleting the pod so its replacement starts fresh")
	if err := c.markFailed(ctx, pod.Annotations[webhook.CRIURestoreAnnotation], cause); err != nil {
		log.WithError(err).Error("CRIU restore: could not block the checkpoint")
	}
	if err := c.a.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		log.WithError(err).Error("CRIU restore: could not delete the pod")
	}
}
