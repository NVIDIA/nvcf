// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package ptr provides utilities for converting scalar literal type values to and from pointers inline.
package ptr

//go:generate go run -tags codegen generate.go
//go:generate gofmt -w -s .
