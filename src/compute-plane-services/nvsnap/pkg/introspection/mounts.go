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

// MountMapping represents an external mount for CRIU
type MountMapping struct {
	InsidePath  string
	OutsidePath string
	FSType      string
	Source      string
	Options     string
}

// AllMountInfo represents a mount entry from /proc/<pid>/mountinfo
type AllMountInfo struct {
	MountID      string
	ParentID     string
	MountPoint   string
	Root         string
	FSType       string
	Source       string
	Options      string
	SuperOptions string
}

var systemMountTypes = map[string]bool{
	"proc": true, "sysfs": true, "devpts": true, "mqueue": true,
	"tmpfs": true, "cgroup": true, "cgroup2": true, "securityfs": true,
	"debugfs": true, "tracefs": true, "fusectl": true, "configfs": true,
	"devtmpfs": true, "hugetlbfs": true, "pstore": true, "bpf": true,
}

var systemMountPaths = map[string]bool{
	"/proc": true, "/sys": true, "/dev": true, "/dev/pts": true,
	"/dev/shm": true, "/dev/mqueue": true, "/run": true, "/run/secrets": true,
}

// ParseMountInfo parses /proc/<pid>/mountinfo and returns bind mounts
func ParseMountInfo(pid int, hostProc string) ([]MountMapping, error) {
	if hostProc == "" {
		hostProc = "/proc"
	}

	mountinfoPath := fmt.Sprintf("%s/%d/mountinfo", hostProc, pid)
	file, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open mountinfo: %w", err)
	}
	defer func() { _ = file.Close() }()
	var mounts []MountMapping
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		mount, skip := parseMountInfoLine(scanner.Text())
		if skip {
			continue
		}
		mounts = append(mounts, mount)
	}

	return mounts, scanner.Err()
}

func parseMountInfoLine(line string) (MountMapping, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return MountMapping{}, true
	}

	root := fields[3]
	mountPoint := fields[4]
	mountOptions := fields[5]

	sepIdx := -1
	for i, f := range fields {
		if f == "-" {
			sepIdx = i
			break
		}
	}

	if sepIdx == -1 || sepIdx+2 >= len(fields) {
		return MountMapping{}, true
	}

	fsType := fields[sepIdx+1]
	source := fields[sepIdx+2]
	superOptions := ""
	if sepIdx+3 < len(fields) {
		superOptions = fields[sepIdx+3]
	}

	if systemMountTypes[fsType] || systemMountPaths[mountPoint] {
		return MountMapping{}, true
	}

	if strings.HasPrefix(mountPoint, "/sys/") || strings.HasPrefix(mountPoint, "/proc/") {
		return MountMapping{}, true
	}

	if fsType == "overlay" && mountPoint == "/" {
		return MountMapping{}, true
	}

	outsidePath := root
	if root == "/" {
		outsidePath = source
	}

	return MountMapping{
		InsidePath:  mountPoint,
		OutsidePath: outsidePath,
		FSType:      fsType,
		Source:      source,
		Options:     mountOptions + "," + superOptions,
	}, false
}

// GetKubernetesVolumeMounts returns mounts that appear to be Kubernetes volumes
func GetKubernetesVolumeMounts(pid int, hostProc string) ([]MountMapping, error) {
	mounts, err := ParseMountInfo(pid, hostProc)
	if err != nil {
		return nil, err
	}

	var k8sMounts []MountMapping
	for _, m := range mounts {
		if strings.Contains(m.OutsidePath, "/kubelet/pods/") ||
			strings.Contains(m.OutsidePath, "/kubernetes.io~") ||
			strings.Contains(m.OutsidePath, "/containerd/io.containerd") {
			k8sMounts = append(k8sMounts, m)
		}
	}

	return k8sMounts, nil
}

// GetAllMountsFromMountinfo parses /proc/<pid>/mountinfo and returns ALL mounts
func GetAllMountsFromMountinfo(pid int, hostProc string) ([]AllMountInfo, error) {
	if hostProc == "" {
		hostProc = "/proc"
	}

	mountinfoPath := fmt.Sprintf("%s/%d/mountinfo", hostProc, pid)
	file, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open mountinfo: %w", err)
	}
	defer func() { _ = file.Close() }()
	var mounts []AllMountInfo
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		mount, err := parseAllMountInfoLine(scanner.Text())
		if err != nil {
			continue
		}
		mounts = append(mounts, mount)
	}

	return mounts, scanner.Err()
}

func parseAllMountInfoLine(line string) (AllMountInfo, error) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return AllMountInfo{}, fmt.Errorf("malformed mountinfo line")
	}

	sepIdx := -1
	for i, f := range fields {
		if f == "-" {
			sepIdx = i
			break
		}
	}

	if sepIdx == -1 || sepIdx+2 >= len(fields) {
		return AllMountInfo{}, fmt.Errorf("malformed mountinfo line")
	}

	superOptions := ""
	if sepIdx+3 < len(fields) {
		superOptions = fields[sepIdx+3]
	}

	return AllMountInfo{
		MountID:      fields[0],
		ParentID:     fields[1],
		MountPoint:   fields[4],
		Root:         fields[3],
		FSType:       fields[sepIdx+1],
		Source:       fields[sepIdx+2],
		Options:      fields[5],
		SuperOptions: superOptions,
	}, nil
}
