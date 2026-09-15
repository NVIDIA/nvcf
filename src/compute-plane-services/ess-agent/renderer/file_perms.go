//go:build !windows
// +build !windows

package renderer

/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
SPDX-License-Identifier: MPL-2.0
*/

import (
	"os"
	"syscall"
)

func preserveFilePermissions(path string, fileInfo os.FileInfo) error {
	sysInfo := fileInfo.Sys()
	if sysInfo != nil {
		stat, ok := sysInfo.(*syscall.Stat_t)
		if ok {
			if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil {
				return err
			}
		}
	}

	return nil
}
