// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build go1.9
// +build go1.9

package aws

import "context"

// Context is an alias of the Go stdlib's context.Context interface.
// It can be used within the SDK's API operation "WithContext" methods.
//
// See https://golang.org/pkg/context on how to use contexts.
type Context = context.Context
