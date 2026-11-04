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

package introspection

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// GetRootFS returns the container's root filesystem path
func GetRootFS(pid int, hostProc string) (string, error) {
	if hostProc == "" {
		hostProc = "/proc"
	}

	rootPath := fmt.Sprintf("%s/%d/root", hostProc, pid)

	if _, err := os.Stat(rootPath); err != nil {
		return "", fmt.Errorf("rootfs not accessible at %s: %w", rootPath, err)
	}

	return rootPath, nil
}

// GetOverlayUpperDir extracts the overlay upperdir from mountinfo
func GetOverlayUpperDir(pid int, hostProc string) (string, error) {
	if hostProc == "" {
		hostProc = "/proc"
	}

	mountinfoPath := fmt.Sprintf("%s/%d/mountinfo", hostProc, pid)
	file, err := os.Open(mountinfoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open mountinfo: %w", err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)

		if len(fields) < 5 {
			continue
		}

		mountPoint := fields[4]
		if mountPoint != "/" {
			continue
		}

		sepIdx := -1
		for i, f := range fields {
			if f == "-" {
				sepIdx = i
				break
			}
		}

		if sepIdx == -1 || sepIdx+2 >= len(fields) {
			continue
		}

		fsType := fields[sepIdx+1]
		if fsType != "overlay" {
			continue
		}

		superOptions := fields[sepIdx+3]
		for _, opt := range strings.Split(superOptions, ",") {
			if strings.HasPrefix(opt, "upperdir=") {
				return strings.TrimPrefix(opt, "upperdir="), nil
			}
		}
	}

	return "", fmt.Errorf("overlay upperdir not found for pid %d", pid)
}
