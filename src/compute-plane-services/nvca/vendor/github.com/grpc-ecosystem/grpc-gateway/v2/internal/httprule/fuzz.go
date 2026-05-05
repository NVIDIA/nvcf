// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build gofuzz
// +build gofuzz

package httprule

func Fuzz(data []byte) int {
	if _, err := Parse(string(data)); err != nil {
		return 0
	}
	return 0
}
