// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"

	corev1 "k8s.io/api/core/v1"
)

// BundleToolsMountPath is where a pod a capture may dump, or a restore
// restores into, has the node bundle: the tools the agent runs as root
// inside its mount namespace (CRIU, nvsnap-gpu-suspend, tar, mkdir), built
// to load their libraries from this path. It is the version the pod mounts
// at /nvsnap, read-only, so the agent never copies a tool into a filesystem
// the workload can write, and the GPU tool always matches the library in
// the processes it drives. The agent checks the mount before it runs a tool
// there (internal/agent/bundle_tools.go).
const BundleToolsMountPath = "/criu-bundle"

// bundleToolsVolume is the volume of that mount. It is the same directory
// as gpushareLibVolume under its own name: mounts are deduplicated by
// volume name when patches merge.
const bundleToolsVolume = "nvsnap-criu-bundle"

// bundleTools returns the volume of bundle version version and its
// read-only mount at BundleToolsMountPath, for a builder to add with its
// own volumes and mounts. ok is false for a pod that declares the volume
// or mounts anything at that path itself: it is left alone, and the agent
// then refuses to run tools in it.
func (m *Mutator) bundleTools(pod *corev1.Pod, version string) (vol corev1.Volume, mount corev1.VolumeMount, ok bool) {
	if why := bundleToolsConflict(pod); why != "" {
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("reason", why).
			Warn("node bundle: the pod uses the bundle's mount path or volume itself; not mounting it, so the pod cannot be captured or restored")
		return vol, mount, false
	}
	dir := corev1.HostPathDirectory
	vol = corev1.Volume{Name: bundleToolsVolume, VolumeSource: corev1.VolumeSource{
		HostPath: &corev1.HostPathVolumeSource{Path: m.bundleDir(version), Type: &dir}}}
	return vol, corev1.VolumeMount{Name: bundleToolsVolume, MountPath: BundleToolsMountPath, ReadOnly: true}, true
}

// bundleToolsConflict names the pod's own use of the bundle's volume name
// or mount path, "" when it has none.
func bundleToolsConflict(pod *corev1.Pod) string {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == bundleToolsVolume {
			return "declares volume " + bundleToolsVolume
		}
	}
	check := func(kind, name string, mounts []corev1.VolumeMount) string {
		for _, vm := range mounts {
			if vm.MountPath == BundleToolsMountPath || vm.Name == bundleToolsVolume {
				return kind + " " + name + " mounts " + vm.MountPath
			}
		}
		return ""
	}
	for i := range pod.Spec.InitContainers {
		if why := check("init container", pod.Spec.InitContainers[i].Name, pod.Spec.InitContainers[i].VolumeMounts); why != "" {
			return why
		}
	}
	for i := range pod.Spec.Containers {
		if why := check("container", pod.Spec.Containers[i].Name, pod.Spec.Containers[i].VolumeMounts); why != "" {
			return why
		}
	}
	for i := range pod.Spec.EphemeralContainers {
		c := &pod.Spec.EphemeralContainers[i]
		if why := check("ephemeral container", c.Name, c.VolumeMounts); why != "" {
			return why
		}
	}
	return ""
}

// companionToolsPatches gives a pod without GPUs that a capture may dump as
// a companion of an instance (CaptureCandidate) the node bundle, in every
// container: which one the capture dumps is decided when it runs. GPU pods
// get it with the gpushare placement (gpusharePatches).
func (m *Mutator) companionToolsPatches(ctx context.Context, pod *corev1.Pod) []PatchOp {
	if gpuRequest(pod) > 0 || m.CaptureCandidate == nil || !m.CaptureCandidate(ctx, pod) {
		return nil
	}
	vol, mount, ok := m.bundleTools(pod, m.BundleID)
	if !ok {
		return nil
	}
	patches := addVolumes(pod, []corev1.Volume{vol})
	for i := range pod.Spec.Containers {
		patches = append(patches, addContainerMounts(i, &pod.Spec.Containers[i], []corev1.VolumeMount{mount})...)
	}
	return patches
}

// mountAtPath reports whether any container of the pod (init, regular,
// ephemeral) mounts something at path.
func mountAtPath(pod *corev1.Pod, path string) bool {
	at := func(mounts []corev1.VolumeMount) bool {
		for i := range mounts {
			if mounts[i].MountPath == path {
				return true
			}
		}
		return false
	}
	for i := range pod.Spec.InitContainers {
		if at(pod.Spec.InitContainers[i].VolumeMounts) {
			return true
		}
	}
	for i := range pod.Spec.Containers {
		if at(pod.Spec.Containers[i].VolumeMounts) {
			return true
		}
	}
	for i := range pod.Spec.EphemeralContainers {
		if at(pod.Spec.EphemeralContainers[i].VolumeMounts) {
			return true
		}
	}
	return false
}
