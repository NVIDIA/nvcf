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

package agent

import (
	"os"
	"strings"
)

// MountClass categorizes how a source-pod mount is handled by the cross-pod
// restore mechanism. See docs/archive/CROSS-POD-MOUNT-REPLAY-DESIGN.md.
type MountClass int

const (
	// MountClassSkip marks a mount as not snapshotted. The mount is either runtime-injected
	// (CDI binds, K8s identity files, secrets) or a kernel-virtual fs
	// whose contents are reconstructed by the kernel on each mount.
	MountClassSkip MountClass = iota

	// MountClassRootfs is the container's overlay root at "/". Handled by
	// mirrorOverlayDir + mirrorIntoMntns over the resolved upperdir.
	MountClassRootfs

	// MountClassReplay is tarred at checkpoint into <ckpt>/mounts/<x>.tar
	// and untarred into the placeholder's mntns at the same path before
	// CRIU restore.
	MountClassReplay
)

// virtualFsTypes are kernel-virtual filesystems we never snapshot regardless
// of allowlist contents — their bytes are reconstructed by the kernel on
// each mount and capturing them would corrupt the placeholder.
var virtualFsTypes = map[string]bool{
	"proc":       true,
	"sysfs":      true,
	"cgroup":     true,
	"cgroup2":    true,
	"devpts":     true,
	"mqueue":     true,
	"bpf":        true,
	"debugfs":    true,
	"tracefs":    true,
	"fusectl":    true,
	"securityfs": true,
}

// defaultReplayAllowlist is the built-in set of mountpoints whose contents
// must travel from source to placeholder for restore to succeed:
//   - /dev/shm, the smallest superset that unblocks multi-GPU NCCL/PSM
//     workloads;
//   - /opt/nvsnap, the cachedir the webhook injects for model-volume
//     readers and capture pods. Engines keep JIT artefacts open or mapped
//     there (Triton launchers, FlashInfer logs), and a placeholder has no
//     copy of that emptyDir, so CRIU failed to reopen fd 113 at restore
//     ("Can't open file opt/nvsnap/cache/.cache/flashinfer/.../flashinfer_jit.log",
//     2026-10-04). Pods without the mount are unaffected: the classifier
//     only replays mountpoints that exist and are writable.
//
// Further entries are config-only via NVSNAP_REPLAY_MOUNTS.
var defaultReplayAllowlist = []string{"/dev/shm", "/opt/nvsnap"}

// ReplayMountAllowlist returns the active allowlist. If the env var
// NVSNAP_REPLAY_MOUNTS is set, it overrides the default; entries are
// comma-separated absolute paths. Empty entries are dropped.
func ReplayMountAllowlist() []string {
	v := strings.TrimSpace(os.Getenv("NVSNAP_REPLAY_MOUNTS"))
	if v == "" {
		return defaultReplayAllowlist
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// classifyMount returns the cross-pod-restore class of a single source-pod
// mountpoint. The classifier is intentionally conservative: anything not
// explicitly on the allowlist falls into MountClassSkip, so an operator
// adding a custom volume to a pod doesn't accidentally pull GBs of
// arbitrary content into every checkpoint.
func classifyMount(mp, fsType, opts string, allowlist []string) MountClass {
	if mp == "/" {
		return MountClassRootfs
	}
	if virtualFsTypes[fsType] {
		return MountClassSkip
	}
	for _, allowed := range allowlist {
		if mp == allowed {
			if !mountIsRW(opts) {
				return MountClassSkip
			}
			return MountClassReplay
		}
	}
	return MountClassSkip
}

// mountIsRW returns true if the mountinfo per-mount opts string indicates a
// writable mount. opts are comma-separated; "rw" or "ro" appears as the
// first entry.
func mountIsRW(opts string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == "rw" {
			return true
		}
		if o == "ro" {
			return false
		}
	}
	return false
}
