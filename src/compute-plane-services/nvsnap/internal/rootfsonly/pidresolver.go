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

package rootfsonly

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrPodNotRunning is returned by ResolvePodPID when no process can be
// found for the given pod UID.
var ErrPodNotRunning = errors.New("rootfsonly: no process found for pod UID")

// PIDResolver finds the main process of a Kubernetes pod by scanning a
// procfs root for cgroup membership.
//
// This replaces the spike's `ps -ef | grep -E 'vllm serve|...'` regex
// hack — that approach silently fails on engines whose process command
// line doesn't match the regex (e.g. NIM's /opt/nim/start_server.sh).
// Cgroup-based resolution is engine-agnostic.
type PIDResolver struct {
	// ProcRoot is the directory containing per-PID subdirectories.
	// Production: "/proc" (or "/host/proc" when the agent is in a
	// container without hostPID propagation). Tests: a temp dir.
	ProcRoot string
}

// NewPIDResolver returns a resolver rooted at /proc, falling back to
// /host/proc if it exists (matches the convention in
// internal/criu/mountinfo/ProcMountinfoPath).
func NewPIDResolver() *PIDResolver {
	root := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		root = "/host/proc"
	}
	return &PIDResolver{ProcRoot: root}
}

// sandboxComms are /proc/<pid>/comm values for K8s sandbox processes
// that share a pod's cgroup but aren't the workload. Their overlay
// upperdirs are essentially empty (sandbox/runtime placeholders), so
// resolving to one of them yields a 0-file capture. We skip them and
// return the lowest non-sandbox PID instead.
//
// "pause" is the K8s sandbox container that holds the pod's network
// namespace. "containerd-shim" is the per-container shim process; its
// upperdir is the runtime layer, not the workload's.
var sandboxComms = map[string]struct{}{
	"pause":           {},
	"containerd-shim": {},
}

// ResolvePodPID returns the lowest non-sandbox PID belonging to the pod
// identified by podUID. Caller can read /proc/<pid>/mountinfo,
// /proc/<pid>/root/, etc. for capture purposes.
//
// Lowest PID is chosen because it's typically the container's PID 1
// (or its closest ancestor) — sufficient for upperdir resolution and
// stable across the container's lifetime. We explicitly skip the pause
// sandbox (always the lowest PID) since its upperdir is empty.
//
// podUID is the Kubernetes metadata.uid (e.g.
// "a082a22a-2f66-4f4e-b80c-d2fc8aa4a010"). Cgroup paths use either:
//
//	cgroup v1:           /kubepods/.../pod<uid>/<containerID>/
//	cgroup v2 systemd:   /kubepods.slice/.../pod<uid_with_underscores>.slice/...
//
// We match either form by checking for both literal `pod<uid>` and
// `pod<uid_with_underscores>` in any cgroup line.
func (r *PIDResolver) ResolvePodPID(podUID string) (int, error) {
	if podUID == "" {
		return 0, errors.New("rootfsonly: ResolvePodPID: empty podUID")
	}

	needles := []string{
		"pod" + podUID, // cgroup v1 / containerd-flat
		"pod" + strings.ReplaceAll(podUID, "-", "_"), // cgroup v2 systemd slice
	}

	entries, err := os.ReadDir(r.ProcRoot)
	if err != nil {
		return 0, fmt.Errorf("read proc root %q: %w", r.ProcRoot, err)
	}

	lowestPID := -1
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a PID directory
		}
		match, err := pidMatchesAny(filepath.Join(r.ProcRoot, e.Name(), "cgroup"), needles)
		if err != nil {
			// /proc entries can disappear mid-walk (process exit). Skip
			// and keep scanning — don't fail the whole resolve on a
			// transient ENOENT.
			continue
		}
		if !match {
			continue
		}
		if isSandboxPID(filepath.Join(r.ProcRoot, e.Name(), "comm")) {
			continue
		}
		if lowestPID == -1 || pid < lowestPID {
			lowestPID = pid
		}
	}
	if lowestPID == -1 {
		return 0, ErrPodNotRunning
	}
	return lowestPID, nil
}

// isSandboxPID reads /proc/<pid>/comm and reports whether the comm name
// matches a known K8s sandbox/runtime process (pause, containerd-shim).
// Errors (process exited mid-walk, etc.) are treated as "not sandbox" so
// transient races don't drop legitimate workload PIDs.
func isSandboxPID(commPath string) bool {
	data, err := os.ReadFile(commPath)
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(data))
	if _, ok := sandboxComms[comm]; ok {
		return true
	}
	// containerd-shim variants include "containerd-shim-runc-v2" etc.
	if strings.HasPrefix(comm, "containerd-shim") {
		return true
	}
	return false
}

// pidMatchesAny reads a /proc/<pid>/cgroup file and reports whether any
// line contains any of the needles (substring match).
func pidMatchesAny(cgroupPath string, needles []string) (bool, error) {
	f, err := os.Open(cgroupPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		for _, n := range needles {
			if strings.Contains(line, n) {
				return true, nil
			}
		}
	}
	return false, scanner.Err()
}
