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

// snapshotPointNCCLEnv is what Dynamo sets in an engine with a snapshot
// point, so that CRIU and the GPU checkpoint can hold its NCCL state
// (dynamo.common.snapshot.lifecycle.configure_snapshot_capture_env).
var snapshotPointNCCLEnv = []corev1.EnvVar{
	{Name: "NCCL_CUMEM_ENABLE", Value: "0"},
	{Name: "NCCL_NVLS_ENABLE", Value: "0"},
	{Name: "NCCL_IB_DISABLE", Value: "1"},
	{Name: "NCCL_RAS_ENABLE", Value: "0"},
	{Name: "TORCH_NCCL_ENABLE_MONITORING", Value: "0"},
	{Name: "TORCH_NCCL_DUMP_ON_TIMEOUT", Value: "0"},
}

// snapshotPointPatches keeps the snapshot point on rank 0 of a multi-pod
// instance. The other ranks run the engine's workers only, with no runtime
// to hold back, and Dynamo refuses the variable on them (a --headless
// worker exits on it). One pod template serves every rank, so the webhook
// empties it there, in place: an empty value declares no point. Those ranks
// get the NCCL settings rank 0's engine gives itself instead: ranks that
// disagree on transports hang in NCCL's init.
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
		vars := append([]corev1.EnvVar{{Name: SnapshotControlDirEnv}}, snapshotPointNCCLEnv...)
		patches = append(patches, setContainerEnv(i, c, vars)...)
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
