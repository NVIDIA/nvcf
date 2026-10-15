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

// Package mountinfo parses /proc/<pid>/mountinfo to resolve overlay upperdirs
// and the set of bind-injected mountpoints used by cross-pod restore.
package mountinfo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultProcRoot returns the proc root the agent should read from.
// "/host/proc" is preferred when present (agent runs in a container that
// mounts the host /proc there); otherwise falls back to "/proc".
func DefaultProcRoot() string {
	if _, err := os.Stat("/host/proc"); err == nil {
		return "/host/proc"
	}
	return "/proc"
}

// ProcMountinfoPath returns the default-proc-root path to /proc/<pid>/mountinfo.
// Equivalent to MountinfoPathForRoot(DefaultProcRoot(), pid).
func ProcMountinfoPath(pid int) string {
	return MountinfoPathForRoot(DefaultProcRoot(), pid)
}

// MountinfoPathForRoot returns the path to <procRoot>/<pid>/mountinfo. Used
// by callers that want to override the proc root (e.g. unit tests with a
// fake /proc tree).
func MountinfoPathForRoot(procRoot string, pid int) string { //nolint:revive // exported name kept for API stability
	return filepath.Join(procRoot, fmt.Sprintf("%d/mountinfo", pid))
}

// MountInfo represents the subset of mountinfo we need.
type MountInfo struct {
	MountPoint string
	FsType     string
	// Opts is field (6) of /proc/<pid>/mountinfo — per-mount options like
	// "rw,nosuid,nodev,relatime". Used by cross-pod restore to skip ro
	// mounts when classifying replay candidates.
	Opts      string
	SuperOpts string
}

// ParseMountinfo reads a mountinfo file and returns mount points.
//
// mountinfo format (man proc):
//
//	(1)mount ID (2)parent ID (3)major:minor (4)root (5)mount point (6)opts (7+)optional fields - (fstype) (src) (superopts)
//
// We only need field (5) mount point.
func ParseMountinfo(path string) ([]MountInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []MountInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// Split around " - " first so we don't care about variable optional fields count.
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) < 1 {
			continue
		}
		left := strings.Fields(parts[0])
		// Need at least: id parent major:minor root mountpoint opts
		if len(left) < 6 {
			continue
		}
		mp := left[4]
		if mp == "" {
			continue
		}
		mi := MountInfo{MountPoint: mp, Opts: left[5]}
		if len(parts) == 2 {
			right := strings.Fields(parts[1])
			// fstype source superopts
			if len(right) >= 1 {
				mi.FsType = right[0]
			}
			if len(right) >= 3 {
				mi.SuperOpts = right[2]
			}
		}
		out = append(out, mi)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveOverlayUpperdir parses the mountinfo for the given pid and returns
// the upperdir absolute path of the overlay mounted at "/" (the container
// root). Empty string + nil error means "no overlay root" (e.g., the agent
// itself, or a non-overlay storage driver). The upperdir is the writable
// diff layer — everything the container wrote at runtime — and is the
// authoritative source of state that won't otherwise exist on a different
// pod's overlay of the same image.
func ResolveOverlayUpperdir(pid int) (string, error) {
	return ResolveOverlayUpperdirAtRoot(DefaultProcRoot(), pid)
}

// ResolveOverlayUpperdirAtRoot is the procRoot-overridable form of
// ResolveOverlayUpperdir. Tests pass a fake /proc tree.
func ResolveOverlayUpperdirAtRoot(procRoot string, pid int) (string, error) {
	mis, err := ParseMountinfo(MountinfoPathForRoot(procRoot, pid))
	if err != nil {
		return "", err
	}
	for _, mi := range mis {
		if mi.MountPoint != "/" {
			continue
		}
		if mi.FsType != "overlay" {
			return "", nil
		}
		for _, kv := range strings.Split(mi.SuperOpts, ",") {
			if strings.HasPrefix(kv, "upperdir=") {
				return strings.TrimPrefix(kv, "upperdir="), nil
			}
		}
		return "", nil
	}
	return "", nil
}

// NonRootMountPoints returns the unique non-"/" mountpoints visible to the
// given pid. These are the paths the container runtime / nvidia-CDI /
// kubelet bind-injected on top of the container's overlay rootfs (e.g.
// /etc/hostname, /usr/bin/nvidia-smi, /run/secrets/kubernetes.io/...).
//
// Used by the cross-pod restore mirror to exclude these paths from the
// upperdir tar at both ends:
//   - dump-side: stub/whiteout entries that overlay leaves behind to
//     support the bind aren't workload state and shouldn't travel.
//   - restore-side: the destination's runtime has already bound read-only
//     copies onto these paths; extracting over them fails with EROFS.
func NonRootMountPoints(pid int) ([]string, error) {
	return NonRootMountPointsAtRoot(DefaultProcRoot(), pid)
}

// NonRootMountPointsAtRoot is the procRoot-overridable form of
// NonRootMountPoints. Tests pass a fake /proc tree.
func NonRootMountPointsAtRoot(procRoot string, pid int) ([]string, error) {
	mis, err := ParseMountinfo(MountinfoPathForRoot(procRoot, pid))
	if err != nil {
		return nil, err
	}
	mps := UniqueMountPoints(mis)
	out := mps[:0]
	for _, mp := range mps {
		if mp == "/" {
			continue
		}
		out = append(out, mp)
	}
	return out, nil
}

// UniqueMountPoints returns a sorted list of unique mountpoints.
func UniqueMountPoints(mis []MountInfo) []string {
	seen := make(map[string]struct{}, len(mis))
	mps := make([]string, 0, len(mis))
	for _, mi := range mis {
		mp := mi.MountPoint
		if mp == "" {
			continue
		}
		if _, ok := seen[mp]; ok {
			continue
		}
		seen[mp] = struct{}{}
		mps = append(mps, mp)
	}
	sort.Strings(mps)
	return mps
}
