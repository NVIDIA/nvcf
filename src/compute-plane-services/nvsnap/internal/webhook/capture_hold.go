// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/capturehold"
)

// captureHoldPatches gives a pod a CRIU capture may freeze the
// capturehold.Condition readiness gate, so that it is captured before it
// serves rather than while it serves: the capture stops it, and its
// requests would fail until its replacement restores.
//
// Only a pod under the gpushare library (placed now, placedGPUShare, or
// already in its spec) is held: an instance is captured under gpushare only. A restore
// placeholder is never held; it is admitted on another path and serves
// once its restore is done.
//
// A pod CaptureHoldAdmits refuses is not held: a StatefulSet that creates
// its pods in order waits for each to be Ready before the next, and a held
// rank 0 would never let the instance whose capture it waits for start.
func (m *Mutator) captureHoldPatches(ctx context.Context, pod *corev1.Pod, placedGPUShare bool) []PatchOp {
	if !m.CaptureHold || m.CaptureCandidate == nil || capturehold.HasGate(pod) {
		return nil
	}
	if !placedGPUShare && !underGPUShare(pod) {
		return nil
	}
	if m.CaptureHoldAdmits != nil {
		if ok, why := m.CaptureHoldAdmits(ctx, pod); !ok {
			m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("reason", why).
				Info("capture hold: not held; the pod serves once it is Ready")
			return nil
		}
	}
	gate := corev1.PodReadinessGate{ConditionType: capturehold.Condition}
	if len(pod.Spec.ReadinessGates) == 0 {
		return []PatchOp{{Op: "add", Path: "/spec/readinessGates", Value: []corev1.PodReadinessGate{gate}}}
	}
	return []PatchOp{{Op: "add", Path: "/spec/readinessGates/-", Value: gate}}
}

// underGPUShare reports whether a GPU container of pod already preloads
// the gpushare library (the agent's test for a capturable pod).
func underGPUShare(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if _, gpu := c.Resources.Limits["nvidia.com/gpu"]; !gpu {
			continue
		}
		for _, e := range c.Env {
			if e.Name == "LD_PRELOAD" && strings.Contains(e.Value, GPUShareLibPath) {
				return true
			}
		}
	}
	return false
}
