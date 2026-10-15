/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import "testing"

func TestPreflightMode(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		// Unset / falsey: not a preflight run → the validator emits metrics.
		{"", false},
		{"false", false},
		{"0", false},
		{"no", false},
		{"garbage", false},
		// Truthy: preflight invocation (set by the CLI) → skip the write.
		{"true", true},
		{"TRUE", true},
		{" true ", true},
		{"1", true},
		{"yes", true},
	}
	for _, tt := range tests {
		if got := preflightMode(tt.in); got != tt.want {
			t.Errorf("preflightMode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
