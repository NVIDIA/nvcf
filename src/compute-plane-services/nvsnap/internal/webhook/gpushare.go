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
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// gpushare: pods annotated nvsnap.io/gpushare=true run their GPU containers
// under libnvsnap_gpushare.so, so the agent can checkpoint processes that
// share GPU memory (NCCL P2P and NVLS, CUDA IPC). The library must be loaded
// from process start, which only admission can arrange. See docs/design/GPUSHARE.md
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

// gpushareMode reports whether a pod runs under the gpushare library and
// whether the library shares its GPU memory over multi-node NVLink
// (fabric). The library runs when annotated, or by default where CRIU is
// the capture method (it keeps GPU memory out of the CRIU image), unless
// annotated "false". An engine that spans pods needs fabric: without it
// the library hides multi-node NVLink memory, and the engine never
// finishes its distributed setup (kimi-k3 TP=8 on two GB300 nodes hung 30
// minutes in its first collective, 2026-10-08). nvsnap.io/gpushare-fabric
// set either way wins.
func (m *Mutator) gpushareMode(ctx context.Context, pod *corev1.Pod) (wanted, fabric bool) {
	switch pod.Annotations[GPUShareAnnotation] {
	case "true":
		wanted = true
	case "false":
		return false, false
	default:
		// A pod that runs to completion (a Job's, an NVCF task's) is never
		// captured: the library would only add risk to it.
		wanted = m.GPUShareByDefault && !runsToCompletion(pod)
	}
	if !wanted {
		return false, false
	}
	switch pod.Annotations[GPUShareFabricAnnotation] {
	case "true":
		return true, true
	case "false":
		return true, false
	}
	if flag := multiNodeFlag(pod); flag != "" || m.groupSize(ctx, pod) > 1 {
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("flag", flag).
			Info("gpushare: the engine spans pods; sharing its GPU memory over multi-node NVLink (fabric)")
		return true, true
	}
	return true, false
}

// runsToCompletion reports a pod that is not restarted when it exits: a
// Job's or a task's, not a server's (every serving controller runs its
// pods with restartPolicy Always, the default).
func runsToCompletion(pod *corev1.Pod) bool {
	return pod.Spec.RestartPolicy == corev1.RestartPolicyNever || pod.Spec.RestartPolicy == corev1.RestartPolicyOnFailure
}

// multiNodeEngineFlags only appear on an engine that spans nodes.
var multiNodeEngineFlags = []string{"--nnodes", "--node-rank", "--node_rank", "--dist-init-addr", "--master-addr", "--master_addr",
	"--data-parallel-address", "--headless"}

// multiNodeFlag returns the first multi-node flag on a GPU container's
// command line, or "".
func multiNodeFlag(pod *corev1.Pod) string {
	for _, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; !ok {
			continue
		}
		for _, a := range append(append([]string{}, c.Command...), c.Args...) {
			for _, w := range strings.Fields(a) {
				for _, f := range multiNodeEngineFlags {
					if w == f || strings.HasPrefix(w, f+"=") {
						return f
					}
				}
			}
		}
	}
	return ""
}

// gpusharePatches places the library and the store into every container of
// an opted-in pod that requests GPUs.
func (m *Mutator) gpusharePatches(ctx context.Context, pod *corev1.Pod) []PatchOp {
	wanted, fabric := m.gpushareMode(ctx, pod)
	if !wanted {
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
	storeRoot := m.GPUShareHostRoot
	if storeRoot == "" {
		storeRoot = DefaultGPUShareHostRoot
	}
	placed, unsafe := gpushareStoreState(pod, storeRoot, gpuContainers)
	if unsafe != "" {
		// The store volume is every pod's saved GPU memory on the node; the
		// per-pod subPathExpr confines only the mounts placed here. Placing
		// it beside a reference the pod brought would hand that container
		// the whole directory. The pod is left without the library (and a
		// mount of a volume it does not declare is refused by the API).
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("reason", unsafe).
			Warn("gpushare: the pod references the reserved store volume itself; not placing the library or the store")
		return nil
	}
	if placed {
		return nil // already placed (re-admission, or written into the manifest)
	}
	bundleRoot := m.HostBundleRoot
	if bundleRoot == "" {
		bundleRoot = DefaultHostBundleRoot
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
		vols = append(vols, corev1.Volume{Name: gpushareLibVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: m.bundleDir(m.BundleID), Type: &dir}}})
	}
	// The tools a capture runs in these containers (tools_mount.go).
	toolsVol, toolsMount, tools := m.bundleTools(pod, m.BundleID)
	if tools {
		vols = append(vols, toolsVol)
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
		if tools {
			mounts = append(mounts, toolsMount)
		}
		patches = append(patches, addContainerMounts(i, c, mounts)...)
		uid := corev1.EnvVar{Name: gpusharePodUIDEnv, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}}
		vars := []corev1.EnvVar{uid, preloads[i]}
		// A value the pod sets itself wins: it may switch fabric sharing off.
		if fabric && !hasEnv(c, gpushareFabricEnv) {
			vars = append(vars, corev1.EnvVar{Name: gpushareFabricEnv, Value: "1"})
		}
		patches = append(patches, setContainerEnv(i, c, vars)...)
	}
	return patches
}

// gpushareStoreState checks the pod's own references to the reserved store
// volume, which only gpusharePatches may make: a hostPath of storeRoot
// mounted at GPUShareStorePath through the per-pod subPathExpr, in a
// container whose pod-uid variable is the downward-API uid and nothing
// else. placed reports a pod that already carries exactly that placement
// (a manifest written from an admitted pod). unsafe names the first
// reference that is not it, or a GPU container (gpuContainers) whose
// pod-uid variable the placement could not make the pod's own.
func gpushareStoreState(pod *corev1.Pod, storeRoot string, gpuContainers []int) (placed bool, unsafe string) {
	declared := false
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.Name != gpushareStoreVolume {
			continue
		}
		if v.HostPath == nil || v.HostPath.Path != storeRoot {
			return false, "the pod declares a volume under the reserved store volume name"
		}
		declared = true
	}
	for i := range pod.Spec.InitContainers {
		if c := &pod.Spec.InitContainers[i]; mountsVolume(c.VolumeMounts, gpushareStoreVolume) {
			return false, "init container " + c.Name + " mounts the store volume"
		}
	}
	for i := range pod.Spec.EphemeralContainers {
		if c := &pod.Spec.EphemeralContainers[i]; mountsVolume(c.VolumeMounts, gpushareStoreVolume) {
			return false, "ephemeral container " + c.Name + " mounts the store volume"
		}
	}
	mounted := false
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		for _, vm := range c.VolumeMounts {
			if vm.Name != gpushareStoreVolume {
				continue
			}
			if !declared {
				return false, "container " + c.Name + " mounts the store volume, which the pod does not declare"
			}
			if vm.MountPath != GPUShareStorePath || vm.SubPath != "" || vm.SubPathExpr != "$("+gpusharePodUIDEnv+")" || !podUIDFromDownwardAPI(c.Env) {
				return false, "container " + c.Name + " mounts the store volume outside its own per-pod directory"
			}
			mounted = true
		}
	}
	if declared {
		if !mounted {
			return false, "the pod declares the store volume without the per-pod mount"
		}
		return true, ""
	}
	for _, i := range gpuContainers {
		n := 0
		for _, e := range pod.Spec.Containers[i].Env {
			if e.Name == gpusharePodUIDEnv {
				n++
			}
		}
		if n > 1 {
			// The placement replaces one entry; the last of several is the
			// one Kubernetes resolves the subPathExpr against.
			return false, "container " + pod.Spec.Containers[i].Name + " sets " + gpusharePodUIDEnv + " more than once"
		}
	}
	return false, ""
}

func mountsVolume(mounts []corev1.VolumeMount, name string) bool {
	for _, vm := range mounts {
		if vm.Name == name {
			return true
		}
	}
	return false
}

// podUIDFromDownwardAPI reports env setting the pod-uid variable exactly
// once, to the pod's own uid.
func podUIDFromDownwardAPI(env []corev1.EnvVar) bool {
	n, own := 0, false
	for _, e := range env {
		if e.Name != gpusharePodUIDEnv {
			continue
		}
		n++
		own = e.Value == "" && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.uid"
	}
	return n == 1 && own
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
