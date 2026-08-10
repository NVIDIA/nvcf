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

package cuda

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ParseLibCudaDir scans the contents of a /proc/<pid>/maps stream for the
// first line referencing libcuda.so* and returns the directory portion of
// the loaded path. Returns "" with nil error if no libcuda mapping is
// present (e.g. process hasn't done cuInit yet).
//
// /proc/<pid>/maps line format:
//
//	<start>-<end> <perms> <offset> <dev> <inode>  <path>
//
// The path component is everything after the inode; we anchor on the
// first '/' to skip the fixed-width prefix. Library names match anything
// starting with "libcuda.so" — covers libcuda.so.1, libcuda.so.580.x,
// libcuda.so etc. Deliberately does NOT match libcudart.so (CUDA Runtime,
// different library) — we anchor on the dot to make that distinct.
func ParseLibCudaDir(maps io.Reader) (string, error) {
	scanner := bufio.NewScanner(maps)
	// Maps lines fit in 4 KiB easily but the default bufio buffer can be
	// small; bump it once to avoid scanner errors on long lines.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.IndexByte(line, '/')
		if idx < 0 {
			continue
		}
		path := strings.TrimSpace(line[idx:])
		base := filepath.Base(path)
		if strings.HasPrefix(base, "libcuda.so") {
			return filepath.Dir(path), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan maps: %w", err)
	}
	return "", nil
}

// DiscoverCudaLibDir reads /proc/<gpuPID>/maps under hostProcDir (typically
// "/host/proc" when the agent runs in a container with hostPID + /host mount,
// or "/proc" when running on the host directly) and returns the directory
// holding the loaded libcuda.so.
//
// Two return values:
//   - hostView: the path as it appears in the workload's /proc/maps — i.e.
//     a host-namespace absolute path like "/usr/local/nvidia/lib64".
//     This is what nsenter'd subprocesses (the agent's direct cuda-checkpoint
//     path) should use for LD_LIBRARY_PATH.
//   - containerView: the same path prefixed with "/host" — i.e.
//     "/host/usr/local/nvidia/lib64". This is what subprocesses running in
//     the agent's container mount namespace (the CRIU-plugin path) should
//     use, since they see the host via the /host bind-mount.
//
// Returns ("", "", nil) if the process has no libcuda mapping. Callers
// should fall back to their previous probing logic in that case.
//
// hostProcRoot is the directory containing PID subdirs (e.g. "/host/proc").
// hostMountPrefix is the in-container path where the host fs is mounted
// (e.g. "/host"). For a non-containerized agent, set hostMountPrefix=""
// and containerView == hostView.
func DiscoverCudaLibDir(hostProcRoot, hostMountPrefix string, gpuPID int) (containerView, hostView string, err error) {
	mapsPath := filepath.Join(hostProcRoot, fmt.Sprintf("%d", gpuPID), "maps")
	f, err := os.Open(mapsPath)
	if err != nil {
		return "", "", fmt.Errorf("open %s: %w", mapsPath, err)
	}
	defer func() { _ = f.Close() }()
	dir, err := ParseLibCudaDir(f)
	if err != nil {
		return "", "", err
	}
	if dir == "" {
		return "", "", nil
	}
	return hostMountPrefix + dir, dir, nil
}
