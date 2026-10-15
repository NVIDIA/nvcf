// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sdk

// Invalidator provides access to a type's invalidate method to make it
// invalidate it cache.
//
// e.g aws.SafeCredentialsProvider's Invalidate method.
type Invalidator interface {
	Invalidate()
}
