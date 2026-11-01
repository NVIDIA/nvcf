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

package operator

import (
	"testing"
)

func TestSanitizeStringWithoutExtraSpaces(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		separator string
		expected  string
	}{
		{
			name:      "trim spaces around comma separator",
			input:     "option1, option2, option3",
			separator: ",",
			expected:  "option1,option2,option3",
		},
		{
			name:      "handle extra spaces",
			input:     "  option1  ,  option2  ,  option3  ",
			separator: ",",
			expected:  "option1,option2,option3",
		},
		{
			name:      "handle empty parts",
			input:     "option1,,option2,,,option3",
			separator: ",",
			expected:  "option1,option2,option3",
		},
		{
			name:      "single value no spaces",
			input:     "option1",
			separator: ",",
			expected:  "option1",
		},
		{
			name:      "single value with spaces",
			input:     "  option1  ",
			separator: ",",
			expected:  "option1",
		},
		{
			name:      "empty string",
			input:     "",
			separator: ",",
			expected:  "",
		},
		{
			name:      "only separators",
			input:     ",,,",
			separator: ",",
			expected:  "",
		},
		{
			name:      "different separator colon",
			input:     "key1: value1 : key2: value2",
			separator: ":",
			expected:  "key1:value1:key2:value2",
		},
		{
			name:      "spaces at start and end with content",
			input:     " ,option1, option2, ",
			separator: ",",
			expected:  ",option1,option2,",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeStringWithoutExtraSpaces(tt.input, tt.separator)
			if result != tt.expected {
				t.Errorf("sanitizeStringWithoutExtraSpaces(%q, %q) = %q, want %q", tt.input, tt.separator, result, tt.expected)
			}
		})
	}
}
