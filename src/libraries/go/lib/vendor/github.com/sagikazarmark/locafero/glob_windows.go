// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package locafero

// See [filepath.Match]:
//
//	On Windows, escaping is disabled. Instead, '\\' is treated as path separator.
const globMatch = "*?[]^"
