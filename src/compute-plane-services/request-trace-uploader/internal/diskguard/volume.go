// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package diskguard

import (
	"fmt"
	"syscall"
)

type statVolume struct{ path string }

// StatVolume reports the size of the volume that holds path. Free space is
// what an unprivileged writer can use, so reserved blocks count as used.
func StatVolume(path string) Volume { return statVolume{path: path} }

func (v statVolume) Usage() (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(v.path, &st); err != nil {
		return Usage{}, fmt.Errorf("stat spool volume: %w", err)
	}
	blockSize := uint64(st.Bsize) //nolint:unconvert // Bsize is uint32 on darwin and int64 on linux.
	return Usage{Total: st.Blocks * blockSize, Free: st.Bavail * blockSize}, nil
}
