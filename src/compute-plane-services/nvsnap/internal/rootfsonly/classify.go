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

// Package rootfsonly is the capture orchestrator for the rootfs-only
// multi-GPU restore path (Phase 1 of the rootfs design — see
// docs/MULTI-GPU-ROOTFS-FANOUT-DESIGN.md). It snapshots a running pod's
// overlay upperdir + classified user-data volumes and writes them to a
// checkpointstore.Backend.
//
// This file is the pure-logic piece: classifying which volumes get
// captured vs skipped, and detecting NIM images. No I/O, no K8s client.
package rootfsonly

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// VolumeKind is how a volume should be treated during capture.
type VolumeKind int

const (
	// VolumeSkip means the volume is irrelevant to capture (nvsnap's own
	// tooling, /dev, /sys, /proc, runtime injections, dev-shm).
	VolumeSkip VolumeKind = iota

	// VolumeRootfs is the rootfs upperdir — captured by reading
	// /proc/<pid>/mountinfo for the source pod and tarring the overlay
	// upperdir, not from a named volume. Captured for ALL images,
	// including NIM: whisper-large-v3 and other NIMs keep their warmed
	// model/cache in /opt/nim/.cache, which is a plain directory in the
	// upperdir — NOT a named volume — so skipping the upperdir for NIM
	// captured nothing (the L2 PVC came out near-empty, GCP-H100-a
	// 2026-06-10). The original USER-1000-can't-restore-root-owned-
	// files concern is handled by OverlayFS-at-restore (nvsnap#194/#195):
	// the captured tree mounts read-only as a lower layer under a
	// per-pod writable upper, so the non-root process never needs to
	// own it.
	VolumeRootfs

	// VolumeUserData is a named hostPath or non-Memory emptyDir whose
	// mountPath inside the container holds workload state we need to
	// preserve (HF cache, NIM cache, etc.).
	VolumeUserData
)

// String returns the kind name for logging.
func (k VolumeKind) String() string {
	switch k {
	case VolumeSkip:
		return "skip"
	case VolumeRootfs:
		return "rootfs"
	case VolumeUserData:
		return "user-data"
	default:
		return "unknown"
	}
}

// Classified is a single volume's classification result.
type Classified struct {
	// Name is the Kubernetes volume name from pod.spec.volumes[].name.
	Name string

	// MountPath is where the volume is mounted in the engine container.
	MountPath string

	// Kind is how to treat the volume during capture.
	Kind VolumeKind

	// VolumeType is the Kubernetes volume source type, one of
	// "hostPath", "emptyDir", or empty if neither (e.g. configMap).
	// Used to drive backend-specific source-path resolution.
	VolumeType string

	// HostPath is the host filesystem path for hostPath volumes; empty
	// for emptyDir (resolved at capture time from pod UID).
	HostPath string
}

// SkipMounts are container-side mount paths we never capture: nvsnap's own
// tooling lives here.
var SkipMounts = []string{
	"/checkpoints",
	"/nvsnap-lib",
	"/nvsnap-system",
	"/nvsnap",
}

// SkipPrefixes are container-side mount-path prefixes we never capture:
// kernel/runtime injections that don't carry workload state.
var SkipPrefixes = []string{
	"/dev/",
	"/sys",
	"/proc",
	"/run/",
	"/etc/",
}

// IsNIMImage reports whether image is an NVIDIA NIM container. Used by
// the restore-side composer for NIM-specific overlay handling; NOT used
// to decide whether to capture the rootfs upperdir — that is captured
// for all images (see VolumeRootfs). NIM warm state lives in
// /opt/nim/.cache, a plain upperdir directory, so the upperdir is
// exactly what we must capture.
func IsNIMImage(image string) bool {
	return strings.HasPrefix(image, "nvcr.io/nim/") ||
		strings.HasPrefix(image, "stg.nvcr.io/nim/")
}

// classifyMountPath returns true if the given container-side mountPath should
// be captured (i.e. not in the skip set).
func classifyMountPath(mountPath string) bool {
	if mountPath == "" {
		return false
	}
	for _, m := range SkipMounts {
		if mountPath == m {
			return false
		}
		if strings.HasPrefix(mountPath, m+"/") {
			return false
		}
	}
	for _, p := range SkipPrefixes {
		if strings.HasPrefix(mountPath, p) {
			return false
		}
	}
	return true
}

// ClassifyVolumes inspects a pod spec and returns one Classified entry per
// pod.spec.volumes that's a candidate for capture, plus a VolumeRootfs
// entry if the rootfs upperdir should also be captured (i.e. image is not
// NIM and at least one container exists).
//
// The mainContainer index selects which container's volumeMounts and image
// drive classification. Most nvsnap workloads have a single primary container
// at index 0; multi-container pods would require a different policy
// (out of scope today).
func ClassifyVolumes(pod *corev1.PodSpec, mainContainer int) []Classified {
	if pod == nil || mainContainer < 0 || mainContainer >= len(pod.Containers) {
		return nil
	}
	main := pod.Containers[mainContainer]

	// Build mountPath lookup (volume name → container mount path). Volumes
	// not mounted in the main container are ignored.
	mounts := make(map[string]string, len(main.VolumeMounts))
	for _, vm := range main.VolumeMounts {
		mounts[vm.Name] = vm.MountPath
	}

	out := make([]Classified, 0, len(pod.Volumes)+1)

	// Always capture the rootfs upperdir, for every image including NIM.
	// The NIM cache location is workload-dependent: nim-llama mounts an
	// emptyDir at /opt/nim/.cache (captured as a user-data volume), but
	// whisper-large-v3 writes /opt/nim/.cache into the plain upperdir
	// with no volume there. Skipping the upperdir for NIM (the old #88
	// behavior) captured nothing for whisper. Bind-mounted volumes are
	// excluded from the upperdir copy via mountinfo, so a NIM that does
	// mount /opt/nim/.cache is not double-captured.
	out = append(out, Classified{
		Name:      "rootfs",
		MountPath: "/",
		Kind:      VolumeRootfs,
	})

	for i := range pod.Volumes {
		v := &pod.Volumes[i]
		mp, mounted := mounts[v.Name]
		if !mounted {
			continue
		}
		if !classifyMountPath(mp) {
			continue
		}

		var (
			vtype    string
			hostPath string
		)
		switch {
		case v.HostPath != nil:
			vtype = "hostPath"
			hostPath = v.HostPath.Path
		case v.EmptyDir != nil:
			// /dev/shm is typically emptyDir(medium=Memory); never workload state.
			if v.EmptyDir.Medium == corev1.StorageMediumMemory {
				continue
			}
			vtype = "emptyDir"
		default:
			// configMap, secret, projected, etc. — not capturable.
			continue
		}

		out = append(out, Classified{
			Name:       v.Name,
			MountPath:  mp,
			Kind:       VolumeUserData,
			VolumeType: vtype,
			HostPath:   hostPath,
		})
	}

	return out
}
