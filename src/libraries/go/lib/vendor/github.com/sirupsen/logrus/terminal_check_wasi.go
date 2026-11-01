// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build wasi
// +build wasi

package logrus

func isTerminal(fd int) bool {
	return false
}
