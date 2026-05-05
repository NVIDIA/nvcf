// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package httpsnoop provides an easy way to capture http related metrics (i.e.
// response time, bytes written, and http status code) from your application's
// http.Handlers.
//
// Doing this requires non-trivial wrapping of the http.ResponseWriter
// interface, which is also exposed for users interested in a more low-level
// API.
package httpsnoop

//go:generate go run codegen/main.go
