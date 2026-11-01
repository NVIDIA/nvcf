// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A restore placeholder's readiness waits on webhook.CRIURestoredCondition,
// which only the agent sets: the pod's own readiness probe can pass while
// the restore still runs (a process of the restored tree without GPUs
// answers it while the GPU processes load their memory). The agent sets the
// condition when its restore is done (markRestored), and again for a
// restored pod whose condition it finds missing (the patch failed, or an
// agent from before the condition restored it), from its own restore
// records, never from the pod's annotations. A failed restore deletes the
// pod, so none waits on the condition forever.

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// setRestoredCondition sets pod's webhook.CRIURestoredCondition True.
func (a *Agent) setRestoredCondition(ctx context.Context, ns, pod string) error {
	patch, err := json.Marshal(map[string]any{"status": map[string]any{"conditions": []corev1.PodCondition{{
		Type:               webhook.CRIURestoredCondition,
		Status:             corev1.ConditionTrue,
		Reason:             "Restored",
		Message:            "the agent restored the checkpoint into the pod",
		LastTransitionTime: metav1.NewTime(time.Now()),
	}}}})
	if err != nil {
		return err
	}
	_, err = a.kubeClient.CoreV1().Pods(ns).Patch(ctx, pod, types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "status")
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// restoredConditionTrue reports whether pod's webhook.CRIURestoredCondition
// is True.
func restoredConditionTrue(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		if c := &pod.Status.Conditions[i]; c.Type == webhook.CRIURestoredCondition {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// considerRestoredCondition sets the condition of a restored pod that
// waits on it without it. The pod's next event, or the informer's resync,
// tries again after a failure.
func (c *criuAutoRestorer) considerRestoredCondition(ctx context.Context, pod *corev1.Pod) {
	if !webhook.HasRestoredGate(pod) || restoredConditionTrue(pod) || pod.DeletionTimestamp != nil ||
		pod.Annotations[webhook.CRIURestoredAnnotation] == "" {
		return
	}
	if _, busy := c.attempted.LoadOrStore("condition/"+string(pod.UID), struct{}{}); busy {
		return
	}
	go func() {
		defer c.attempted.Delete("condition/" + string(pod.UID))
		log := c.a.log.WithField("pod", pod.Namespace+"/"+pod.Name)
		_, ok, err := c.restoredBy(ctx, pod)
		if err != nil {
			log.WithError(err).Warn("CRIU restore: cannot read the restore record; the pod's readiness waits for its next event")
			return
		}
		if !ok {
			return // marked restored, but not by a restore of this agent's records
		}
		if err := c.a.setRestoredCondition(ctx, pod.Namespace, pod.Name); err != nil {
			log.WithError(err).Warn("CRIU restore: cannot set the pod's restored condition; its readiness waits for its next event")
			return
		}
		log.Info("CRIU restore: set the restored condition the pod's readiness waits on")
	}()
}
