/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package hostlibs names the per-architecture library directories where the
// NVIDIA driver and the container's own libc live. The agent runs on amd64
// and arm64 nodes, so nothing may hard-code x86_64-linux-gnu.
package hostlibs

import "runtime"

// Triplet returns the Debian multiarch directory name for the running
// architecture, for example x86_64-linux-gnu or aarch64-linux-gnu.
func Triplet() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64-linux-gnu"
	case "arm64":
		return "aarch64-linux-gnu"
	default:
		return runtime.GOARCH + "-linux-gnu"
	}
}

// Dir is /usr/lib/<triplet>: the container's own multiarch libdir, which
// must precede any driver directory on LD_LIBRARY_PATH.
func Dir() string {
	return "/usr/lib/" + Triplet()
}

// DriverDir is the multiarch libdir inside a GPU Operator driver root
// such as /run/nvidia/driver.
func DriverDir(root string) string {
	return root + "/usr/lib/" + Triplet()
}
