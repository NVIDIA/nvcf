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

// Package introspection provides kernel-level container introspection via /proc.
package introspection

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// NamespaceType represents a Linux namespace type
type NamespaceType string

// Namespace types for the supported Linux namespaces.
const (
	NamespaceNet    NamespaceType = "net"
	NamespacePID    NamespaceType = "pid"
	NamespaceMnt    NamespaceType = "mnt"
	NamespaceUTS    NamespaceType = "uts"
	NamespaceIPC    NamespaceType = "ipc"
	NamespaceUser   NamespaceType = "user"
	NamespaceCgroup NamespaceType = "cgroup"
)

// NamespaceInfo holds namespace identification information
type NamespaceInfo struct {
	Type       NamespaceType
	Inode      uint64
	Path       string
	IsExternal bool
}

// GetNamespaceInode returns the inode number for a namespace
func GetNamespaceInode(pid int, nsType NamespaceType, hostProc string) (uint64, error) {
	if hostProc == "" {
		hostProc = "/proc"
	}

	nsPath := fmt.Sprintf("%s/%d/ns/%s", hostProc, pid, nsType)
	var stat unix.Stat_t
	if err := unix.Stat(nsPath, &stat); err != nil {
		return 0, fmt.Errorf("failed to stat namespace %s: %w", nsPath, err)
	}

	return stat.Ino, nil
}

// GetNamespaceInfo returns detailed namespace information
func GetNamespaceInfo(pid int, nsType NamespaceType, hostProc string) (*NamespaceInfo, error) {
	if hostProc == "" {
		hostProc = "/proc"
	}

	nsPath := fmt.Sprintf("%s/%d/ns/%s", hostProc, pid, nsType)

	var stat unix.Stat_t
	if err := unix.Stat(nsPath, &stat); err != nil {
		return nil, fmt.Errorf("failed to stat namespace %s: %w", nsPath, err)
	}

	link, err := os.Readlink(nsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to readlink %s: %w", nsPath, err)
	}

	initNsPath := fmt.Sprintf("%s/1/ns/%s", hostProc, nsType)
	var initStat unix.Stat_t
	isExternal := false
	if err := unix.Stat(initNsPath, &initStat); err == nil {
		isExternal = stat.Ino != initStat.Ino
	}

	return &NamespaceInfo{
		Type:       nsType,
		Inode:      stat.Ino,
		Path:       link,
		IsExternal: isExternal,
	}, nil
}

// GetAllNamespaces returns information about all namespaces for a process
func GetAllNamespaces(pid int, hostProc string) (map[NamespaceType]*NamespaceInfo, error) {
	nsTypes := []NamespaceType{
		NamespaceNet,
		NamespacePID,
		NamespaceMnt,
		NamespaceUTS,
		NamespaceIPC,
		NamespaceUser,
		NamespaceCgroup,
	}

	namespaces := make(map[NamespaceType]*NamespaceInfo)
	for _, nsType := range nsTypes {
		info, err := GetNamespaceInfo(pid, nsType, hostProc)
		if err != nil {
			continue
		}
		namespaces[nsType] = info
	}

	return namespaces, nil
}

// OpenNamespaceFD opens a file descriptor to a namespace
func OpenNamespaceFD(pid int, nsType NamespaceType, hostProc string) (*os.File, error) {
	if hostProc == "" {
		hostProc = "/proc"
	}

	nsPath := fmt.Sprintf("%s/%d/ns/%s", hostProc, pid, nsType)
	return os.Open(nsPath)
}

// FormatExternalNamespace formats namespace info for CRIU's External option
func FormatExternalNamespace(nsType NamespaceType, inode uint64) string {
	key := formatNamespaceKey(nsType)
	return fmt.Sprintf("%s[%d]:%s", nsType, inode, key)
}

func formatNamespaceKey(nsType NamespaceType) string {
	nsName := string(nsType)
	if nsName != "" {
		nsName = strings.ToUpper(nsName[:1]) + nsName[1:]
	}
	return "ext" + nsName + "Ns"
}
