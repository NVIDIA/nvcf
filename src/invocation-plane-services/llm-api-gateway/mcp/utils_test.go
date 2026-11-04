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

package mcp

import "testing"

func TestRedactServerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "internal connector is preserved",
			input:    "http://mcp-connectors.orion.svc.cluster.local:8080/googlecalendar/",
			expected: "http://mcp-connectors.orion.svc.cluster.local:8080/googlecalendar/",
		},
		{
			name:     "external path is redacted",
			input:    "https://external-server.com/api/v1/endpoint",
			expected: "https://external-server.com/<redacted>",
		},
		{
			name:     "external query is redacted",
			input:    "https://external-server.com?token=secret",
			expected: "https://external-server.com/?<redacted>",
		},
		{
			name:     "external root is preserved",
			input:    "https://external-server.com/",
			expected: "https://external-server.com/",
		},
		{
			name:     "invalid url is preserved",
			input:    "not-a-url",
			expected: "not-a-url",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := RedactServerURL(tt.input); got != tt.expected {
				t.Fatalf("RedactServerURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
