// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build enable_antithesis_sdk && (!linux || !amd64 || !cgo)

package internal

func init_in_antithesis() libHandler {
	return nil
}
