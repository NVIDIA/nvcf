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
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// The rootfs L2 restore (rootfs_l2_overlay.go) runs nvsnap-rootfs-restore
// from the node-staged bundle. The bundle is a hostPath, not an init
// container image, for two reasons:
//  1. Image-pull constraint. Function pods only have a per-pod regcred
//     for their tenant registry path and cannot pull an nvsnap-owned
//     image; the agent DaemonSet (nvsnap-system, cluster regcred) stages
//     /criu-bundle onto every node instead (initContainer
//     nvsnap-bundle-stage runs scripts/restore-bundle-init.sh).
//  2. Version skew. The staged bundle always matches the running agent.

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
	// Under the containerd root so the bundle lands on the node's local disk
	// rather than the boot volume, matching agent.hostPaths in the chart.
	DefaultHostBundleRoot = "/var/lib/containerd/nvsnap-bundle"

	// envRuntimeDirs carries the capture's recorded runtime directories to the
	// restore shim, which recreates them before exec. Must match the constant
	// in cmd/nvsnap-rootfs-restore.
	envRuntimeDirs = "NVSNAP_RUNTIME_DIRS"
)

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

// runtimeDirsJSON encodes the capture's recorded runtime directories for the
// restore shim. Returns "" when there are none, so the env var is present but
// empty and the shim skips the step -- and so captures taken before this was
// recorded keep working unchanged.
//
// Marshal cannot fail for this type; on the impossible error we return "" and
// let restore proceed, since a missing runtime dir degrades one workload
// rather than failing every restore.
func runtimeDirsJSON(dirs []checkpointstore.EntryRuntimeDir) string {
	if len(dirs) == 0 {
		return ""
	}
	b, err := json.Marshal(dirs)
	if err != nil {
		return ""
	}
	return string(b)
}
