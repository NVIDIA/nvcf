// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build windows
// +build windows

package workloadapi

// WithNamedPipeName provides a Pipe Name for the Workload API
// endpoint in the form \\.\pipe\<pipeName>.
func WithNamedPipeName(pipeName string) ClientOption {
	return clientOption(func(c *clientConfig) {
		c.namedPipeName = pipeName
	})
}
