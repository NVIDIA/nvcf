// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

// CRIU restore of a scaled-up pod. When a pod's nvsnap.io/restore-from
// resolves to a CRIU capture, the pod becomes its own restore placeholder:
// its main container idles instead of starting the engine, the checkpoints
// directory is mounted, and the agent on its node restores the captured
// process into it once it runs (internal/agent/criu_auto_restore.go). The
// restored process then answers the pod's readiness probe like a fresh
// start would, only sooner.

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

const (
	// CRIURestoreAnnotation names the checkpoint the agent restores into
	// this pod; CRIURestoreContainerAnnotation names the container.
	CRIURestoreAnnotation          = "nvsnap.io/criu-restore"
	CRIURestoreContainerAnnotation = "nvsnap.io/criu-restore-container"

	criuCheckpointsVolume = "nvsnap-criu-checkpoints"
	// criuCheckpointsMount is where the restore reads the images
	// (/checkpoints/<id>), the same path the agent's placeholder uses.
	criuCheckpointsMount = "/checkpoints"

	// criuPlaceholderScript keeps the container alive until the restored
	// process takes over. The sleeps fork, so once the agent raises the
	// namespace's next pid the shell shows the reserved range.
	criuPlaceholderScript = "trap 'exit 0' TERM; while true; do sleep 2; done"
)

// criuCheckpointID returns the CRIU checkpoint a capture record points to,
// or "" when the record is not a CRIU capture.
func criuCheckpointID(man checkpointstore.Manifest) string {
	if man.CaptureMethod != "criu" && man.SourcePodMeta["engine"] != "criu" {
		return ""
	}
	return man.SourcePodMeta["checkpoint_id"]
}

// criuRestoreFor looks up the capture record for hash on the L2 backend
// (where CRIU captures are recorded) and returns its checkpoint id when the
// pod should be restored with CRIU.
func (m *Mutator) criuRestoreFor(ctx context.Context, pod *corev1.Pod, hash string, log logrus.FieldLogger) (string, checkpointstore.Manifest) {
	var man checkpointstore.Manifest
	var id string
	for _, b := range []checkpointstore.Backend{m.Backend, m.L2Backend} {
		if b == nil {
			continue
		}
		got, err := b.Stat(ctx, hash)
		if err != nil {
			continue
		}
		if id = criuCheckpointID(got); id != "" {
			man = got
			break
		}
	}
	if id == "" {
		return "", man
	}
	if m.CRIURestoreBlocked != nil && m.CRIURestoreBlocked(ctx, id) {
		log.WithField("checkpoint", id).Warn("CRIU restore: this checkpoint failed to restore before; cold start")
		return "", man
	}
	return id, man
}

// criuRestorePatches turns the pod into the restore placeholder for
// checkpointID. The liveness and startup probes go: they would kill the
// container before the restored process answers them. The readiness probe
// stays, so the pod becomes Ready when the restored engine serves.
func (m *Mutator) criuRestorePatches(pod *corev1.Pod, checkpointID string, man checkpointstore.Manifest) ([]PatchOp, error) {
	// The capture checkpoints the first container with GPUs; that one
	// becomes the placeholder, wherever the chart put it.
	i := gpuContainerIndex(pod, m.MainContainer)
	if i < 0 || i >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range (have %d containers)", i, len(pod.Spec.Containers))
	}
	if m.CheckpointHostRoot == "" {
		return nil, fmt.Errorf("CRIU restore: the checkpoint host root is not configured")
	}
	c := &pod.Spec.Containers[i]
	base := fmt.Sprintf("/spec/containers/%d", i)
	setOp := func(present bool) string {
		if present {
			return "replace"
		}
		return "add"
	}
	patches := []PatchOp{
		{Op: setOp(c.Command != nil), Path: base + "/command", Value: []string{"/bin/sh", "-c"}},
		{Op: setOp(c.Args != nil), Path: base + "/args", Value: []string{criuPlaceholderScript}},
	}
	if c.LivenessProbe != nil {
		patches = append(patches, PatchOp{Op: "remove", Path: base + "/livenessProbe"})
	}
	if c.StartupProbe != nil {
		patches = append(patches, PatchOp{Op: "remove", Path: base + "/startupProbe"})
	}
	// Only this checkpoint's directory: the node's other checkpoints hold
	// other workloads' memory. It need not exist yet: on a node without
	// the checkpoint the pod starts, and the agent fetches the checkpoint
	// into the directory before it restores.
	dir, orCreate := corev1.HostPathDirectory, corev1.HostPathDirectoryOrCreate
	vols := []corev1.Volume{{
		Name:         criuCheckpointsVolume,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: m.CheckpointHostRoot + "/" + checkpointID, Type: &orCreate}},
	}}
	mounts := []corev1.VolumeMount{
		// Writable: CRIU writes restore.log into the images directory.
		{Name: criuCheckpointsVolume, MountPath: criuCheckpointsMount + "/" + checkpointID},
	}
	if man.SourcePodMeta["gpushare"] == "true" {
		// The restored processes re-map the library from where it was and
		// load their GPU memory from the checkpoint's chunk store, at the
		// store path they saved it from.
		bundleRoot := m.HostBundleRoot
		if bundleRoot == "" {
			bundleRoot = DefaultHostBundleRoot
		}
		vols = append(vols, corev1.Volume{Name: gpushareStoreVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: m.CheckpointHostRoot + "/" + checkpointID + "/gpushare", Type: &orCreate}}})
		mounts = append(mounts, corev1.VolumeMount{Name: gpushareStoreVolume, MountPath: GPUShareStorePath})
		libMounted := false
		for _, vm := range c.VolumeMounts {
			libMounted = libMounted || vm.MountPath == nvsnapToolsMountPath
		}
		if !libMounted {
			vols = append(vols, corev1.Volume{Name: gpushareLibVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: bundleRoot + "/nvsnap", Type: &dir}}})
			mounts = append(mounts, corev1.VolumeMount{Name: gpushareLibVolume, MountPath: nvsnapToolsMountPath, ReadOnly: true})
		}
	}
	patches = append(patches, addVolumes(pod, vols)...)
	patches = append(patches, addContainerMounts(i, c, mounts)...)
	if pod.Annotations == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/metadata/annotations", Value: map[string]string{}})
	}
	patches = append(patches,
		PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIURestoreAnnotation), Value: checkpointID},
		PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIURestoreContainerAnnotation), Value: c.Name},
	)
	patches = append(patches, criuNodePreference(pod, man.CapturedOnNodes)...)
	return patches, nil
}

// gpuContainerIndex is the index of the first container with GPUs, else
// fallback.
func gpuContainerIndex(pod *corev1.Pod, fallback int) int {
	for i, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; ok {
			return i
		}
	}
	return fallback
}

// criuNodePreference prefers the nodes that already hold the checkpoint.
// Preferred, not required: on any other node the agent fetches it first,
// and a full capture node must not leave the pod unschedulable.
func criuNodePreference(pod *corev1.Pod, nodes []string) []PatchOp {
	if len(nodes) == 0 {
		return nil
	}
	term := corev1.PreferredSchedulingTerm{
		Weight: 100,
		Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: nodes,
		}}},
	}
	switch {
	case pod.Spec.Affinity == nil:
		return []PatchOp{{Op: "add", Path: "/spec/affinity", Value: corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{term}}}}}
	case pod.Spec.Affinity.NodeAffinity == nil:
		return []PatchOp{{Op: "add", Path: "/spec/affinity/nodeAffinity", Value: corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{term}}}}
	case pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution == nil:
		return []PatchOp{{Op: "add", Path: "/spec/affinity/nodeAffinity/preferredDuringSchedulingIgnoredDuringExecution",
			Value: []corev1.PreferredSchedulingTerm{term}}}
	default:
		return []PatchOp{{Op: "add", Path: "/spec/affinity/nodeAffinity/preferredDuringSchedulingIgnoredDuringExecution/-", Value: term}}
	}
}

// jsonPointerEscape escapes a key for a JSON Patch path (RFC 6901).
func jsonPointerEscape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}
