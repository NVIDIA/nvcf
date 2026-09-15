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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu/mountinfo"
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
// must travel from source to placeholder for restore to succeed. Default
// ships only /dev/shm — the smallest superset that unblocks multi-GPU
// NCCL/PSM workloads. Future entries are config-only via NVSNAP_REPLAY_MOUNTS.
var defaultReplayAllowlist = []string{"/dev/shm"}

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

// shouldSkipMount returns true for mountpoints that are re-created on the
// restore side by the container runtime / K8s kubelet / nvidia-container-toolkit
// CDI injection — NOT by the checkpointed application. Dumping these as
// external means CRIU tries to re-bind them on restore from source paths
// that only exist inside the source pod's mount namespace. On a different
// node or in a placeholder pod where CRIU runs from a different mntns, the
// source paths don't resolve and restore fails with "No such file or directory".
//
// Skipping them at dump time keeps the restore simple: CRIU leaves these
// mounts alone, and the placeholder pod already has them by virtue of being
// a normal K8s pod with the same spec (image + volumes + GPU request).
//
// This covers nvidia-CDI bind mounts (libcuda.so, firmware, nvidia-smi, etc.)
// plus runtime-injected paths (/dev, /proc, /sys subpaths, /run K8s paths).
// It does NOT skip user-specified volume mounts — those still need --external.
func shouldSkipMount(mp string) bool {
	// We keep the skip list narrow. Restore uses a recursive bind (--rbind)
	// of the placeholder's rootfs to expose its full mount hierarchy to CRIU;
	// most mount sources resolve without intervention. The only mounts we
	// skip at dump time are those where the PATH CONTENT differs between
	// pods by definition (K8s per-pod identity files) or where nvidia-CDI
	// re-injection on the restore side makes the checkpoint's recorded mount
	// obsolete (new driver version, different firmware subdirectory, etc.).

	// Per-pod K8s-injected identity files. Placeholder has its own.
	if mp == "/etc/hostname" || mp == "/etc/hosts" || mp == "/etc/resolv.conf" ||
		mp == "/dev/termination-log" || mp == "/run/.containerenv" {
		return true
	}
	if strings.HasPrefix(mp, "/run/secrets/kubernetes.io/") {
		return true
	}

	// /proc/driver/nvidia is dynamically populated by the nvidia kernel
	// module on each node. Paths under it reference per-GPU identifiers
	// (e.g., PCI addresses) that may differ across nodes.
	if mp == "/proc/driver/nvidia" || strings.HasPrefix(mp, "/proc/driver/nvidia/") {
		return true
	}

	// NVSNAP's own shared volume populated by init containers (libnvsnap_intercept.so,
	// patched uvloop/libuv/libzmq). Not part of the container image, so CRIU can't
	// re-create the mountpoint inside its workspace overlay. VMAs for the loaded
	// libraries are MAP_PRIVATE with pages dumped into the checkpoint — file
	// presence isn't needed to restore memory. Placeholder has its own /nvsnap-lib
	// mount if the restored process opens new handles.
	if mp == "/nvsnap-lib" || strings.HasPrefix(mp, "/nvsnap-lib/") {
		return true
	}

	// nvidia-CDI driver/firmware/binary bind mounts. Version-specific paths
	// (e.g., /usr/lib/firmware/nvidia/580.95.05/). Placeholder's CDI re-injects
	// the correct version for the node's driver.
	if strings.HasPrefix(mp, "/usr/lib/firmware/nvidia/") {
		return true
	}
	// /dev/nvidia* device-node bind mounts: nvidiactl, nvidia-uvm,
	// nvidia-uvm-tools, and per-GPU nodes (nvidia0..nvidia7). The GPU
	// index assigned by the K8s device plugin can differ between dump
	// and restore (dump pod got GPU 0, placeholder gets GPU 7). CDI
	// re-injects the correct device into the placeholder; including
	// the dump-side path in the ext-mount-map causes restore to fail
	// with "No such file or directory" on the renamed device.
	if strings.HasPrefix(mp, "/dev/nvidia") {
		return true
	}
	if strings.HasPrefix(mp, "/usr/bin/nvidia-") || strings.HasPrefix(mp, "/usr/bin/nv-") {
		return true
	}
	if strings.HasPrefix(mp, "/run/nvidia-") || strings.HasPrefix(mp, "/run/nvidia/") {
		return true
	}
	// NOTE (upstream criu): driver lib file bind-mounts (libnvidia-*, libcuda*,
	// libnv-*) are NOT skipped anymore. The workload maps them (pynvml loads
	// libnvidia-ml), and a mapped file on a skip-mnt'd mount fails dump on
	// current CRIU ("Can't lookup mount") — the old fork tolerated it,
	// upstream does not. They ride ExtMnt like every other mount; same-node
	// restore resolves the identical path via CDI re-injection.
	// Cross-driver-version restore will need a path remap instead of a skip.
	return false
}

// buildDumpExtMnt builds explicit ExtMnt maps (Key=mountpoint, Val=mountpoint) from /proc/<pid>/mountinfo.
// This is the k8s-runc-bypass style approach needed to avoid CRIU mount propagation validation failures
// on NVIDIA + Kubernetes setups.
func (a *Agent) buildDumpExtMnt(pid int, containerRootFS string) (ext []criu.ExtMountMap, mountPoints, skipMounts []string, root string, err error) {
	miPath := mountinfo.ProcMountinfoPath(pid)
	mis, err := mountinfo.ParseMountinfo(miPath)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("parse mountinfo %s: %w", miPath, err)
	}
	mps := mountinfo.UniqueMountPoints(mis)
	if len(mps) == 0 {
		return nil, nil, nil, "", fmt.Errorf("no mountpoints found in mountinfo %s", miPath)
	}

	ext = make([]criu.ExtMountMap, 0, len(mps))
	mountPoints = make([]string, 0, len(mps))
	skipMounts = make([]string, 0, 16)
	for _, mp := range mps {
		if shouldSkipMount(mp) {
			skipMounts = append(skipMounts, mp)
			continue
		}
		ext = append(ext, criu.ExtMountMap{Key: mp, Val: mp})
		mountPoints = append(mountPoints, mp)
	}

	// Prefer a real root path (containerRootFS from OCI spec is often relative like "rootfs").
	// Use /proc/<pid>/root (or /host/proc/<pid>/root in containerized agent) which always reflects
	// the process's filesystem view.
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	root = filepath.Join(procBase, fmt.Sprintf("%d/root", pid))

	// If containerRootFS is absolute and exists, it's OK to use it, but /proc/<pid>/root is safer.
	_ = containerRootFS

	return ext, mountPoints, skipMounts, root, nil
}

var netnsInodeRe = regexp.MustCompile(`\[(\d+)\]`)

// getNetnsInode returns the inode number for /proc/<pid>/ns/net.
func getNetnsInode(pid int) (uint64, error) {
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	linkPath := filepath.Join(procBase, fmt.Sprintf("%d/ns/net", pid))
	target, err := os.Readlink(linkPath)
	if err != nil {
		return 0, fmt.Errorf("readlink %s: %w", linkPath, err)
	}
	m := netnsInodeRe.FindStringSubmatch(target)
	if len(m) != 2 {
		return 0, fmt.Errorf("unexpected netns link format %q", target)
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse netns inode %q: %w", m[1], err)
	}
	return n, nil
}
