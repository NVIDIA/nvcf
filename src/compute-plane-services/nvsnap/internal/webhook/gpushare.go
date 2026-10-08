/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// gpushare: pods annotated nvsnap.io/gpushare=true run their GPU containers
// under libnvsnap_gpushare.so, so the agent can checkpoint processes that
// share GPU memory (NCCL P2P and NVLS, CUDA IPC). The library must be loaded
// from process start, which only admission can arrange. See docs/GPUSHARE.md
// and internal/agent/gpushare.go.
const (
	// GPUShareAnnotation opts a pod in.
	GPUShareAnnotation = "nvsnap.io/gpushare"
	// GPUShareFabricAnnotation marks a pod whose GPU memory is shared with
	// pods on other nodes over multi-node NVLink. The library then shares
	// that memory as fabric handles, which a multi-node suspend can
	// re-establish (see the agent's group checkpoint).
	GPUShareFabricAnnotation = "nvsnap.io/gpushare-fabric"
	// GPUShareStorePath is where a GPU container sees its chunk store: a
	// per-pod directory on the node's local checkpoint disk. A restore
	// placeholder mounts the checkpoint's copy at the same path. It is a
	// top-level directory on purpose: the agent reaches it through
	// /proc/<pid>/root, where an absolute symlink on the way (/var/run is
	// one in many images) resolves against the agent's root instead.
	GPUShareStorePath = "/nvsnap-gpushare"
	// GPUShareLibPath is the preloaded library, from the node bundle the
	// agent DaemonSet stages and mounts at nvsnapToolsMountPath.
	GPUShareLibPath = nvsnapToolsMountPath + "/libnvsnap_gpushare.so"
	// DefaultGPUShareHostRoot is the node directory holding the per-pod
	// stores (the agent's checkpoint root plus gpushare-pods).
	DefaultGPUShareHostRoot = "/var/lib/containerd/nvsnap-checkpoints/gpushare-pods"

	gpushareLibVolume   = "nvsnap-gpushare-lib"
	gpushareStoreVolume = "nvsnap-gpushare-store"
	gpusharePodUIDEnv   = "NVSNAP_POD_UID"
	gpushareFabricEnv   = "NVSNAP_GPUSHARE_FABRIC"
)

// gpushareWanted reports whether a pod runs under the gpushare library: when
// annotated, or by default where CRIU is the capture method (the library
// keeps GPU memory out of the CRIU image), unless annotated "false".
func (m *Mutator) gpushareWanted(pod *corev1.Pod) bool {
	switch pod.Annotations[GPUShareAnnotation] {
	case "true":
		return true
	case "false":
		return false
	}
	return m.GPUShareByDefault
}

// gpusharePatches places the library and the store into every container of
// an opted-in pod that requests GPUs.
func (m *Mutator) gpusharePatches(pod *corev1.Pod) []PatchOp {
	if !m.gpushareWanted(pod) {
		return nil
	}
	gpuContainers := make([]int, 0, len(pod.Spec.Containers))
	preloads := map[int]corev1.EnvVar{}
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; !ok {
			continue
		}
		preload, ok := ldPreloadWith(c.Env, GPUShareLibPath)
		if !ok {
			m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("container", c.Name).
				Warn("gpushare: LD_PRELOAD is set from a reference the webhook cannot read; not placing the library, so this container cannot be checkpointed")
			continue
		}
		gpuContainers = append(gpuContainers, i)
		preloads[i] = preload
	}
	if len(gpuContainers) == 0 {
		return nil
	}
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == gpushareStoreVolume {
			return nil // already placed (re-admission, or written into the manifest)
		}
	}
	bundleRoot := m.HostBundleRoot
	if bundleRoot == "" {
		bundleRoot = DefaultHostBundleRoot
	}
	storeRoot := m.GPUShareHostRoot
	if storeRoot == "" {
		storeRoot = DefaultGPUShareHostRoot
	}
	dir, dirOrCreate := corev1.HostPathDirectory, corev1.HostPathDirectoryOrCreate

	var patches []PatchOp
	vols := []corev1.Volume{
		{Name: gpushareStoreVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: storeRoot, Type: &dirOrCreate}}},
	}
	libMountedAt := map[int]bool{}
	for _, i := range gpuContainers {
		for _, vm := range pod.Spec.Containers[i].VolumeMounts {
			if vm.MountPath == nvsnapToolsMountPath {
				libMountedAt[i] = true // the node bundle is already there
			}
		}
	}
	if len(libMountedAt) < len(gpuContainers) {
		vols = append(vols, corev1.Volume{Name: gpushareLibVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: bundleRoot + "/nvsnap", Type: &dir}}})
	}
	patches = append(patches, addVolumes(pod, vols)...)

	for _, i := range gpuContainers {
		c := &pod.Spec.Containers[i]
		mounts := []corev1.VolumeMount{
			// Per-pod directory, so one pod never sees another's saved memory.
			{Name: gpushareStoreVolume, MountPath: GPUShareStorePath, SubPathExpr: "$(" + gpusharePodUIDEnv + ")"},
		}
		if !libMountedAt[i] {
			mounts = append(mounts, corev1.VolumeMount{Name: gpushareLibVolume, MountPath: nvsnapToolsMountPath, ReadOnly: true})
		}
		patches = append(patches, addContainerMounts(i, c, mounts)...)
		uid := corev1.EnvVar{Name: gpusharePodUIDEnv, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}}
		vars := []corev1.EnvVar{uid, preloads[i]}
		// A value the pod sets itself wins: it may switch fabric sharing off.
		if pod.Annotations[GPUShareFabricAnnotation] == "true" && !hasEnv(c, gpushareFabricEnv) {
			vars = append(vars, corev1.EnvVar{Name: gpushareFabricEnv, Value: "1"})
		}
		patches = append(patches, setContainerEnv(i, c, vars)...)
	}
	return patches
}

// ldPreloadWith returns the container's LD_PRELOAD with lib appended. ok is
// false when LD_PRELOAD comes from a ConfigMap or Secret reference: its
// value is not known here, and replacing it would drop the image's own
// preloads.
func ldPreloadWith(env []corev1.EnvVar, lib string) (corev1.EnvVar, bool) {
	for _, e := range env {
		if e.Name != "LD_PRELOAD" {
			continue
		}
		if e.ValueFrom != nil {
			return corev1.EnvVar{}, false
		}
		for _, p := range strings.FieldsFunc(e.Value, func(r rune) bool { return r == ':' || r == ' ' }) {
			if p == lib {
				return e, true
			}
		}
		v := lib
		if strings.TrimSpace(e.Value) != "" {
			v = e.Value + ":" + lib
		}
		return corev1.EnvVar{Name: "LD_PRELOAD", Value: v}, true
	}
	return corev1.EnvVar{Name: "LD_PRELOAD", Value: lib}, true
}

func addVolumes(pod *corev1.Pod, vols []corev1.Volume) []PatchOp {
	if len(vols) == 0 {
		return nil
	}
	if pod.Spec.Volumes == nil {
		return []PatchOp{{Op: "add", Path: "/spec/volumes", Value: vols}}
	}
	out := make([]PatchOp, 0, len(vols))
	for i := range vols {
		out = append(out, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: vols[i]})
	}
	return out
}

func addContainerMounts(i int, c *corev1.Container, mounts []corev1.VolumeMount) []PatchOp {
	if c.VolumeMounts == nil {
		return []PatchOp{{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts", i), Value: mounts}}
	}
	out := make([]PatchOp, 0, len(mounts))
	for _, vm := range mounts {
		out = append(out, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/-", i), Value: vm})
	}
	return out
}

// setContainerEnv adds env vars, replacing an existing entry of the same name
// in place. Kubernetes resolves a SubPathExpr against the container's whole
// env, so the pod-uid variable only has to exist, not come first.
func setContainerEnv(i int, c *corev1.Container, vars []corev1.EnvVar) []PatchOp {
	out := make([]PatchOp, 0, len(vars))
	add := make([]corev1.EnvVar, 0, len(vars))
	for _, v := range vars {
		replaced := false
		for j, e := range c.Env {
			if e.Name == v.Name {
				out = append(out, PatchOp{Op: "replace", Path: fmt.Sprintf("/spec/containers/%d/env/%d", i, j), Value: v})
				replaced = true
				break
			}
		}
		if !replaced {
			add = append(add, v)
		}
	}
	if len(add) == 0 {
		return out
	}
	if c.Env == nil {
		return append(out, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/env", i), Value: add})
	}
	for _, v := range add {
		out = append(out, appendEnv(i, v))
	}
	return out
}
