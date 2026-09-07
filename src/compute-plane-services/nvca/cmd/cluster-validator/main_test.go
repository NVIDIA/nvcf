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

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
)

func TestParseRole(t *testing.T) {
	tests := []struct {
		in        string
		want      clustervalidator.Role
		wantKnown bool
	}{
		// Known roles are normalized and reported as known.
		{"control-plane", clustervalidator.RoleControlPlane, true},
		{"CONTROL-PLANE", clustervalidator.RoleControlPlane, true},
		{" control-plane ", clustervalidator.RoleControlPlane, true},
		{"compute-plane", clustervalidator.RoleComputePlane, true},
		{"COMPUTE-PLANE", clustervalidator.RoleComputePlane, true},
		// Unknown values fall back to compute-plane and are reported as unknown.
		{"", clustervalidator.RoleComputePlane, false},
		{"gpu", clustervalidator.RoleComputePlane, false},
		{"both", clustervalidator.RoleComputePlane, false},
		{"control_plane", clustervalidator.RoleComputePlane, false}, // underscore, not hyphen
	}
	for _, tt := range tests {
		got, gotKnown := parseRole(tt.in)
		if got != tt.want || gotKnown != tt.wantKnown {
			t.Errorf("parseRole(%q) = (%q, %v), want (%q, %v)", tt.in, got, gotKnown, tt.want, tt.wantKnown)
		}
	}
}

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
