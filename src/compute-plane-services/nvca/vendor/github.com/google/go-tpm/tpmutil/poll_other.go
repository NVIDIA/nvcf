// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package tpmutil

import (
	"os"
)

// Not implemented on Windows.
func poll(_ *os.File) error { return nil }
