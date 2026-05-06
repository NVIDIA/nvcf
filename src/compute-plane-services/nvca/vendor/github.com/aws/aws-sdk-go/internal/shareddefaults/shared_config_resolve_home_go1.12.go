// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build go1.12
// +build go1.12

package shareddefaults

import (
	"os"
)

func userHomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}
