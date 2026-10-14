// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// livenessBudgetPatches widens liveness probes too tight to survive a
// capture. Capturing a running pod freezes its processes for seconds (GPU
// suspend, CRIU dump), and a probe that fails after one miss (the Dynamo
// operator gives its workers periodSeconds 5, failureThreshold 1) has the
// kubelet restart the engine being captured. Each probe gets the failure
// threshold that makes it tolerate CaptureLivenessBudget seconds of
// silence; a probe that already does is left alone. This costs no
// performance, only a slower restart of an engine that really hangs.
func (m *Mutator) livenessBudgetPatches(pod *corev1.Pod) []PatchOp {
	if m.CaptureLivenessBudget <= 0 {
		return nil
	}
	var patches []PatchOp
	for i, c := range pod.Spec.Containers {
		p := c.LivenessProbe
		if p == nil {
			continue
		}
		period, threshold := p.PeriodSeconds, p.FailureThreshold
		if period <= 0 {
			period = 10 // the API default
		}
		if threshold <= 0 {
			threshold = 3 // the API default
		}
		need := int32((m.CaptureLivenessBudget + int(period) - 1) / int(period))
		if threshold >= need {
			continue
		}
		op := "replace"
		if p.FailureThreshold == 0 {
			op = "add"
		}
		patches = append(patches, PatchOp{Op: op, Path: fmt.Sprintf("/spec/containers/%d/livenessProbe/failureThreshold", i), Value: need})
	}
	if len(patches) > 0 {
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("budgetSeconds", m.CaptureLivenessBudget).
			Info("liveness probe widened to outlast a capture's freeze")
	}
	return patches
}
