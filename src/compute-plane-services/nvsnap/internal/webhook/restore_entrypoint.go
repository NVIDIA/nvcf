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

// Restore-entrypoint injection for the L2 path (nvsnap#147 completion).
//
// tryL2Mount mounts the rox PVC at /nvsnap-checkpoint and sets
// CHECKPOINT_PATH — but the workload's main container still runs its
// own image entrypoint (vllm serve / nim run / ...). Without a command
// override the mounted PVC is consumed by nothing and the workload
// cold-starts from scratch, wasting the L2 plumbing.
//
// restoreBundleInitPatches finishes the loop without requiring the
// workload pod to pull any nvsnap-owned image:
//
//   - hostPath volume "nvsnap-tools" → /nvsnap on the main container.
//     Backed by /var/lib/nvsnap/bundle/nvsnap, staged by the nvsnap-agent
//     DaemonSet on every node (see deploy/helm/nvsnap/templates/
//     agent-daemonset.yaml — initContainer nvsnap-bundle-stage runs
//     scripts/restore-bundle-init.sh against a hostPath mount on
//     agent startup).
//   - hostPath volume "nvsnap-lib"  → /nvsnap-lib on the main container.
//     Same backing pattern, separate directory. REQUIRED: the
//     captured process has libnvsnap_intercept.so mmapped from
//     /nvsnap-lib/libnvsnap_intercept.so; CRIU restore re-mmaps from the
//     same path, so the file MUST be present at that exact path on
//     the restored node.
//   - main container command rewritten to ["/nvsnap/restore-entrypoint"]
//   - original command/args preserved as NVSNAP_ORIG_COMMAND /
//     NVSNAP_ORIG_ARGS env vars so restore-entrypoint can exec the
//     workload's original entrypoint if the dump is unreadable /
//     restore fails (see attemptColdStartFallback in
//     cmd/restore-entrypoint/main.go).
//   - CHECKPOINT_ID="" explicit so an inherited env var can't
//     redirect restore-entrypoint to a wrong subdir.
//   - CRIU_BUNDLE_PATH=/nvsnap so restore-entrypoint finds its tooling.
//   - securityContext.privileged=true + runAsUser=0 (CRIU restore
//     needs CAP_SYS_ADMIN; non-root in privileged container has no
//     effective capabilities. NIM images default to uid=1000.)
//
// Why hostPath rather than an init container that copies into an
// emptyDir? Two reasons:
//   1. Image-pull constraint. Function pods (e.g. NVCA-managed
//      inference pods in nvcf-backend) only have a per-pod regcred
//      that covers their tenant's registry path. They can't pull a
//      nvsnap-owned image to run an init container. The DaemonSet, by
//      contrast, runs in nvsnap-system with a cluster-wide regcred
//      that CAN pull nvsnap images. By moving the bundle copy into
//      the DaemonSet's own initContainer, we never ask function
//      pods to pull our image.
//   2. Cost. Staging once per node, then mounting via hostPath, is
//      a single ~500MB cp per agent upgrade. The init-container-
//      per-restore-pod variant would cost the same cp on every
//      pod admission, even when the agent is idle.
//
// Skip rules:
//   - main container already has a volumeMount at /nvsnap or named
//     "nvsnap-tools" / "nvsnap-lib": operator pre-wired by hand,
//     don't double-inject.
//
// Atomicity / upgrades. The DaemonSet stages atomically via
// directory rename (see scripts/restore-bundle-init.sh). Kubelet
// pins hostPath mounts to the directory's inode at mount time, so
// function pods admitted before an agent upgrade keep using the
// old bundle (their inode is still alive) and pods admitted after
// see the new one. No corruption window.
//
// Callers MUST invoke tryL2Mount FIRST. That call adds CHECKPOINT_PATH
// to the main container env, which means by the time these patches are
// applied the env array is guaranteed non-empty — every env op below
// can safely use "/env/-" without needing to bootstrap the slice.

package webhook

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

const (
	// nvsnapToolsVolumeName is the function-pod volume name for the
	// restore-tools tree (criu, restore-entrypoint, cuda-checkpoint,
	// plugins). Distinct from nvsnapLibVolumeName (defined in
	// auto_inject.go) — the two payloads have separate lifetimes
	// and live under separate hostPath dirs.
	nvsnapToolsVolumeName = "nvsnap-tools"

	// nvsnapToolsMountPath is where restore-entrypoint and its
	// auxiliary binaries land. Matches CRIU_BUNDLE_PATH and the
	// hardcoded default in cmd/restore-entrypoint/main.go.
	nvsnapToolsMountPath = "/nvsnap"

	// DefaultHostBundleRoot is where the nvsnap-agent DaemonSet
	// stages the bundle on every node. Function pods mount from
	// {root}/nvsnap and {root}/nvsnap-lib. Hardcoded across the
	// DaemonSet and the Mutator — chart operators don't get a
	// knob, by design: changing the path requires coordinated
	// edits in both places, which a single config value can't
	// safely express.
	DefaultHostBundleRoot = "/var/lib/nvsnap/bundle"

	// envOrigCommand and envOrigArgs are read by restore-entrypoint's
	// cold-start fallback path (cmd/restore-entrypoint/main.go).
	// Values are JSON-encoded string arrays — empty arrays / unset
	// env vars mean "no fallback known, exit non-zero on restore
	// failure" which surfaces as CrashLoopBackOff (a noisy signal
	// that something's actually broken, vs. a silent cold start
	// that would mask L2 problems).
	envOrigCommand = "NVSNAP_ORIG_COMMAND"
	envOrigArgs    = "NVSNAP_ORIG_ARGS"
)

// restoreBundleInjectPatches returns the JSON Patch ops needed to
// wire restore-entrypoint into the main container via hostPath
// staging.
//
// Returns (nil, nil) when the injection is skipped (operator
// pre-wired or webhook disabled). Never returns an error today —
// the signature carries one so future validation has a place to
// land.
func (m *Mutator) restoreBundleInjectPatches(pod *corev1.Pod) ([]PatchOp, error) {
	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("restoreBundleInjectPatches: MainContainer index %d out of range (have %d containers)",
			m.MainContainer, len(pod.Spec.Containers))
	}
	main := pod.Spec.Containers[m.MainContainer]

	// Idempotency: if the workload pod already mounts something at
	// /nvsnap or /nvsnap-lib (operator pre-wired) or already has either
	// volume by name, skip cleanly. Better to fail-open than to
	// clobber a hand-crafted pod and produce a worse outcome.
	for _, vm := range main.VolumeMounts {
		if vm.MountPath == nvsnapToolsMountPath || vm.Name == nvsnapToolsVolumeName {
			return nil, nil
		}
		if vm.MountPath == nvsnapLibMountPath || vm.Name == nvsnapLibVolumeName {
			return nil, nil
		}
	}

	root := m.HostBundleRoot
	if root == "" {
		root = DefaultHostBundleRoot
	}
	hostPathDir := corev1.HostPathDirectory

	var patches []PatchOp

	// 1. Two hostPath volumes. type=Directory means kubelet will
	//    REFUSE to start the pod if the host path is missing —
	//    which is the correct behavior: if the nvsnap-agent
	//    DaemonSet hasn't staged the bundle on this node, the
	//    function pod shouldn't try to restore. The agent's
	//    initContainer creates these dirs on every startup, so
	//    the only failure mode is "agent isn't running on this
	//    node" — which the operator wants to see clearly as a
	//    pod-stuck event rather than a corrupt restore.
	toolsVol := corev1.Volume{
		Name: nvsnapToolsVolumeName,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: root + "/nvsnap",
			Type: &hostPathDir,
		}},
	}
	libVol := corev1.Volume{
		Name: nvsnapLibVolumeName,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: root + "/nvsnap-lib",
			Type: &hostPathDir,
		}},
	}
	// Always append via /-. tryL2Mount (the caller) has already
	// added the rox-PVC volume, bootstrapping the slice when needed.
	// Two "add" ops to the bare array path would conflict (the
	// second silently clobbers the first under JSON Patch).
	// 2. (combined below) Mount both on the main container — read-only.
	//    The agent DaemonSet's initContainer is the only writer.
	//    tryL2Mount already emitted a volumeMount patch for the rox PVC,
	//    bootstrapping the slice when needed; by the time these patches
	//    apply against the pod, /spec/containers/%d/volumeMounts is
	//    guaranteed non-empty.
	patches = append(patches,
		PatchOp{
			Op:    "add",
			Path:  "/spec/volumes/-",
			Value: toolsVol,
		},
		PatchOp{
			Op:    "add",
			Path:  "/spec/volumes/-",
			Value: libVol,
		},
		PatchOp{
			Op:   "add",
			Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
			Value: corev1.VolumeMount{
				Name:      nvsnapToolsVolumeName,
				MountPath: nvsnapToolsMountPath,
				ReadOnly:  true,
			},
		},
		PatchOp{
			Op:   "add",
			Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
			Value: corev1.VolumeMount{
				Name:      nvsnapLibVolumeName,
				MountPath: nvsnapLibMountPath,
				ReadOnly:  true,
			},
		})

	// 3. Env vars for restore-entrypoint and its cold-start fallback.
	//    NVSNAP_ORIG_COMMAND / NVSNAP_ORIG_ARGS are JSON-encoded arrays;
	//    restore-entrypoint json-decodes and execs them when the dump
	//    is unreadable or CRIU restore fails. We only set them when
	//    the customer pod provided an explicit command/args — if both
	//    are nil, the workload image's ENTRYPOINT/CMD is the fallback,
	//    which kubelet runs only when our injected command is removed
	//    (not on this code path; restore-entrypoint will surface the
	//    failure as a non-zero exit instead).
	if len(main.Command) > 0 {
		cmdJSON, err := json.Marshal(main.Command)
		if err != nil {
			return nil, fmt.Errorf("marshal original command: %w", err)
		}
		patches = append(patches, appendEnv(m.MainContainer, corev1.EnvVar{
			Name:  envOrigCommand,
			Value: string(cmdJSON),
		}))
	}
	if len(main.Args) > 0 {
		argsJSON, err := json.Marshal(main.Args)
		if err != nil {
			return nil, fmt.Errorf("marshal original args: %w", err)
		}
		patches = append(patches, appendEnv(m.MainContainer, corev1.EnvVar{
			Name:  envOrigArgs,
			Value: string(argsJSON),
		}))
	}
	// CHECKPOINT_ID="" explicit: if anything upstream (operator, NVCA
	// stamping, customer) set CHECKPOINT_ID, restore-entrypoint
	// computes checkpointPath = $CHECKPOINT_PATH/$CHECKPOINT_ID and
	// looks for inventory.img there. The L2 rox PVC has the dump at
	// its ROOT (DstSubpath="" in internal/agent/l2_promote_async.go),
	// so any non-empty CHECKPOINT_ID would miss the dump. Setting it
	// to "" forces the join to short-circuit to checkpointPath ==
	// CHECKPOINT_PATH (see cmd/restore-entrypoint/main.go:530-533).
	// Duplicate env-var names: kubelet keeps the LAST one in
	// container.env, so this overrides any inherited setting.
	patches = append(patches,
		appendEnv(m.MainContainer, corev1.EnvVar{
			Name:  "CRIU_BUNDLE_PATH",
			Value: nvsnapToolsMountPath,
		}),
		appendEnv(m.MainContainer, corev1.EnvVar{
			Name:  "CHECKPOINT_ID",
			Value: "",
		}))

	// 4. Rewrite command + clear args. "replace" only works on an
	//    existing path; for pods where command was nil (image
	//    ENTRYPOINT in use) we need "add" instead. Same logic for
	//    args.
	cmdOp := "replace"
	if main.Command == nil {
		cmdOp = "add"
	}
	patches = append(patches, PatchOp{
		Op:    cmdOp,
		Path:  fmt.Sprintf("/spec/containers/%d/command", m.MainContainer),
		Value: []string{nvsnapToolsMountPath + "/restore-entrypoint"},
	})
	if main.Args != nil {
		patches = append(patches, PatchOp{
			Op:   "remove",
			Path: fmt.Sprintf("/spec/containers/%d/args", m.MainContainer),
		})
	}

	// 5. Pin securityContext for CRIU restore (privileged + runAsUser=0).
	//    See nvsnap#147 comment block in mutate.go for the full
	//    rationale — short version: CRIU needs CAP_SYS_ADMIN +
	//    CAP_CHECKPOINT_RESTORE, and non-root in privileged
	//    containers gets no effective caps (NIM defaults to
	//    uid=1000). CRIU preserves credentials in the dump so the
	//    restored process tree gets its original uid back, making
	//    this override safe even for images that default to root.
	patches = append(patches, securityContextPatches(m.MainContainer, main.SecurityContext, true, nil)...)

	return patches, nil
}

// appendEnv is the env-add helper. Always appends via "/env/-" because
// every call site here is downstream of tryL2Mount, which has already
// added CHECKPOINT_PATH and thus guaranteed the slice exists. The
// defensive bootstrap (Op=add against the array path) lives in
// tryL2Mount itself, not here.
func appendEnv(containerIdx int, e corev1.EnvVar) PatchOp {
	return PatchOp{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/env/-", containerIdx),
		Value: e,
	}
}

// rootfsWriteCaps are the file capabilities the engine needs to write
// into the restored tree WITHOUT privileged. The captured rootfs is
// root-owned and parts of the image (e.g. /opt/nim, owned by the image's
// uid 1000) are not; NVCA hardens the pod with capabilities drop ALL, so
// even uid 0 has no CAP_DAC_OVERRIDE and is subject to the permission
// bits — `mkdir /opt/nim/workspace` then fails with EACCES
// (whisper-large-v3, GCP-H100-a 2026-06-11). DAC_OVERRIDE bypasses
// file read/write/execute checks; CHOWN/FOWNER cover the ownership ops
// NIM/Riva perform while extracting engines. This is the targeted
// subset of what privileged used to grant — and, unlike privileged, it
// does NOT expose extra GPU device nodes, so single-GPU isolation holds.
var rootfsWriteCaps = []corev1.Capability{"DAC_OVERRIDE", "CHOWN", "FOWNER"}

// rootfsOverlayCaps are rootfsWriteCaps plus SYS_ADMIN, for the
// whole-rootfs overlay restore path (B′): the nvsnap-rootfs-restore shim
// runs in the workload and needs mount(2)/pivot_root (CAP_SYS_ADMIN) to
// assemble and enter the overlay. SYS_ADMIN is NOT privileged — it grants
// mount power but does not relax the device cgroup or add host device
// nodes, so the device plugin's single-GPU isolation (v0.0.66) holds.
var rootfsOverlayCaps = append(append([]corev1.Capability{}, rootfsWriteCaps...), "SYS_ADMIN")

// securityContextPatches returns the ops to set runAsUser=0 on the
// workload container, plus either privileged=true (CRIU path) or the
// rootfsWriteCaps capability add (rootfs paths).
//
// privileged MUST be false for the rootfs paths. A privileged main
// container bypasses the NVIDIA container runtime's device-node
// filtering: even though the device plugin assigned exactly one GPU
// via NVIDIA_VISIBLE_DEVICES=<uuid>, privileged exposes ALL /dev/nvidiaN
// host nodes. With CUDA_VISIBLE_DEVICES unset (the device plugin does
// not set it), the workload then enumerates every GPU and defaults to
// ordinal 0 — so N fanned-out restore pods on one node all collide on
// physical GPU 0 → OOM, while GPUs 1..7 sit idle (allocation/usage
// fragmentation). Only the CRIU restore-entrypoint path needs
// privileged (CRIU restore requires CAP_SYS_ADMIN). The rootfs paths
// instead run the engine as uid 0 with rootfsWriteCaps, which is enough
// to write the root-owned tree while keeping GPU isolation intact.
func securityContextPatches(containerIdx int, current *corev1.SecurityContext, privileged bool, addCaps []corev1.Capability) []PatchOp {
	truePtr := true
	zero := int64(0)
	scPath := fmt.Sprintf("/spec/containers/%d/securityContext", containerIdx)

	// Build the merged capabilities for the non-privileged rootfs case,
	// preserving whatever NVCA already set (drop ALL + add
	// NET_BIND_SERVICE) and appending the caps this path needs.
	var caps *corev1.Capabilities
	if !privileged {
		merged := corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
		if current != nil && current.Capabilities != nil {
			merged = *current.Capabilities
		}
		for _, c := range addCaps {
			if !hasCapability(merged.Add, c) {
				merged.Add = append(merged.Add, c)
			}
		}
		caps = &merged
	}

	if current == nil {
		sc := corev1.SecurityContext{RunAsUser: &zero}
		if privileged {
			sc.Privileged = &truePtr
		} else {
			sc.Capabilities = caps
		}
		return []PatchOp{{Op: "add", Path: scPath, Value: sc}}
	}

	ops := []PatchOp{{
		Op:    "add",
		Path:  scPath + "/runAsUser",
		Value: zero,
	}}
	if privileged {
		ops = append(ops, PatchOp{
			Op:    "add",
			Path:  scPath + "/privileged",
			Value: truePtr,
		})
	} else {
		ops = append(ops, PatchOp{
			Op:    "add",
			Path:  scPath + "/capabilities",
			Value: *caps,
		})
	}
	return ops
}

func hasCapability(list []corev1.Capability, c corev1.Capability) bool {
	for _, x := range list {
		if x == c {
			return true
		}
	}
	return false
}
