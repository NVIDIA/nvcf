/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClusterSource_String(t *testing.T) {
	tests := []struct {
		name  string
		input ClusterSource
		want  string
	}{
		{
			name:  "ngc-managed",
			input: ClusterSourceNGCManaged,
			want:  "ngc-managed",
		},
		{
			name:  "helm-managed",
			input: ClusterSourceHelmManaged,
			want:  "helm-managed",
		},
		{
			name:  "self-hosted",
			input: ClusterSourceSelfHosted,
			want:  "self-hosted",
		},
		{
			name:  "self-managed (alias)",
			input: ClusterSourceSelfManaged,
			want:  "self-hosted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.String()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateClusterSource(t *testing.T) {
	tests := []struct {
		name          string
		clusterSource string
		want          ClusterSource
		wantErr       bool
	}{
		{
			name:          "Valid NGCManaged",
			clusterSource: "ngc-managed",
			want:          ClusterSourceNGCManaged,
			wantErr:       false,
		},
		{
			name:          "Valid HelmManaged",
			clusterSource: "helm-managed",
			want:          ClusterSourceHelmManaged,
			wantErr:       false,
		},
		{
			name:          "Valid SelfHosted",
			clusterSource: "self-hosted",
			want:          ClusterSourceSelfHosted,
			wantErr:       false,
		},
		{
			name:          "Valid SelfManaged normalizes to SelfHosted",
			clusterSource: "self-managed",
			want:          ClusterSourceSelfHosted,
			wantErr:       false,
		},
		{
			name:          "Empty",
			clusterSource: "",
			want:          "",
			wantErr:       true,
		},
		{
			name:          "Invalid",
			clusterSource: "invalid",
			want:          "",
			wantErr:       true,
		},
		{
			name:          "Case Sensitive",
			clusterSource: "NGC",
			want:          "",
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateClusterSource(tt.clusterSource)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidClusterSources(t *testing.T) {
	// Test that all valid cluster sources are in the ValidClusterSources slice
	expectedSources := []ClusterSource{
		ClusterSourceNGCManaged,
		ClusterSourceHelmManaged,
		ClusterSourceSelfHosted,
		"self-managed", // Also accept "self-managed" as input
	}

	assert.Equal(t, len(expectedSources), len(ValidClusterSources), "ValidClusterSources should have the correct number of elements")

	for i, source := range ValidClusterSources {
		assert.Equal(t, expectedSources[i], source, "ValidClusterSources[%d] should match expected", i)
	}
}

func TestParseClusterSource(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     ClusterSource
		wantBool bool
	}{
		{
			name:     "ngc-managed",
			input:    "ngc-managed",
			want:     ClusterSourceNGCManaged,
			wantBool: true,
		},
		{
			name:     "helm-managed",
			input:    "helm-managed",
			want:     ClusterSourceHelmManaged,
			wantBool: true,
		},
		{
			name:     "self-hosted",
			input:    "self-hosted",
			want:     ClusterSourceSelfHosted,
			wantBool: true,
		},
		{
			name:     "self-managed normalizes to self-hosted",
			input:    "self-managed",
			want:     ClusterSourceSelfHosted,
			wantBool: true,
		},
		{
			name:     "invalid",
			input:    "invalid",
			want:     ClusterSourceNGCManaged,
			wantBool: false,
		},
		{
			name:     "empty",
			input:    "",
			want:     ClusterSourceNGCManaged,
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotBool := ParseClusterSource(tt.input)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantBool, gotBool)
		})
	}
}

func TestClusterSource_IsValid(t *testing.T) {
	tests := []struct {
		name  string
		input ClusterSource
		want  bool
	}{
		{
			name:  "ngc-managed",
			input: ClusterSourceNGCManaged,
			want:  true,
		},
		{
			name:  "helm-managed",
			input: ClusterSourceHelmManaged,
			want:  true,
		},
		{
			name:  "self-hosted",
			input: ClusterSourceSelfHosted,
			want:  true,
		},
		{
			name:  "self-managed (alias)",
			input: ClusterSourceSelfManaged,
			want:  true,
		},
		{
			name:  "invalid",
			input: "invalid",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.IsValid()
			assert.Equal(t, tt.want, got)
		})
	}
}
