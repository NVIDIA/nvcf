// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"

	corev1 "k8s.io/api/core/v1"
)

// SnapshotControlDirEnv declares an engine's snapshot point (Dynamo's
// snapshot protocol): the agent captures the instance there and releases
// the engine (internal/agent/snapshot_point.go).
const SnapshotControlDirEnv = "DYN_SNAPSHOT_CONTROL_DIR"

// snapshotPointPatches keeps the snapshot point on rank 0 of a multi-pod
// instance. The other ranks run the engine's workers only, with no runtime
// to hold back, and Dynamo refuses the variable on them (a --headless
// worker exits on it). One pod template serves every rank, so the webhook
// empties it there, in place: an empty value declares no point.
func (m *Mutator) snapshotPointPatches(ctx context.Context, pod *corev1.Pod) []PatchOp {
	var patches []PatchOp
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; !ok || !declaresSnapshotPoint(c) {
			continue
		}
		if podOrdinal(pod) == 0 || m.groupSize(ctx, pod) < 2 {
			return nil
		}
		patches = append(patches, setContainerEnv(i, c, []corev1.EnvVar{{Name: SnapshotControlDirEnv}})...)
	}
	if len(patches) > 0 {
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).Info("snapshot point: not a rank-0 pod; its engine starts without one")
	}
	return patches
}

func declaresSnapshotPoint(c *corev1.Container) bool {
	for _, e := range c.Env {
		if e.Name == SnapshotControlDirEnv {
			return e.Value != "" || e.ValueFrom != nil
		}
	}
	return false
}
