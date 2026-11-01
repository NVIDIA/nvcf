// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package jwt

// TokenOption is a reserved type, which provides some forward compatibility,
// if we ever want to introduce token creation-related options.
type TokenOption func(*Token)
