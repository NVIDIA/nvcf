// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// +build !linux,!darwin,!windows,!freebsd,!dragonfly,!netbsd,!openbsd

package memory

func sysTotalMemory() uint64 {
	return 0
}
func sysFreeMemory() uint64 {
	return 0
}
