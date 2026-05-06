// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build gofuzz
// +build gofuzz

package ini

import (
	"bytes"
)

func Fuzz(data []byte) int {
	b := bytes.NewReader(data)

	if _, err := Parse(b); err != nil {
		return 0
	}

	return 1
}
