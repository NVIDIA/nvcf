// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package internal

type RouteKey struct {
	Method string
	URL    string
}

var NoResponder RouteKey

func (r RouteKey) String() string {
	if r == NoResponder {
		return "NO_RESPONDER"
	}
	return r.Method + " " + r.URL
}
