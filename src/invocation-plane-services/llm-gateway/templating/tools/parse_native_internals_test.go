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

package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLlama31FunctionRegex(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:  "Single function call",
			input: "<function=spotify_trending_songs>{\"n\": \"5\"}</function>",
			expected: []string{
				"<function=spotify_trending_songs>{\"n\": \"5\"}</function>",
				"spotify_trending_songs",
				"{\"n\": \"5\"}",
			},
		},
		{
			name:  "Multiple function calls",
			input: "Some text <function=weather_info>{\"location\": \"New York\"}</function> more text <function=time_now>{}</function>",
			expected: []string{
				"<function=weather_info>{\"location\": \"New York\"}</function>",
				"weather_info",
				"{\"location\": \"New York\"}",
			},
		},
		{
			name:  "Function call with newlines",
			input: "<function=complex_query>{\n\"query\": \"SELECT * FROM users\",\n\"limit\": 10\n}</function>",
			expected: []string{
				"<function=complex_query>{\n\"query\": \"SELECT * FROM users\",\n\"limit\": 10\n}</function>",
				"complex_query",
				"{\n\"query\": \"SELECT * FROM users\",\n\"limit\": 10\n}",
			},
		},
		{
			name:  "Function call with newlines directly after opening tag",
			input: "<function=some-func>\n{\"param1\": \"value1\",\n\"param2\": 42}</function>",
			expected: []string{
				"<function=some-func>\n{\"param1\": \"value1\",\n\"param2\": 42}</function>",
				"some-func",
				"\n{\"param1\": \"value1\",\n\"param2\": 42}",
			},
		},
		{
			name:     "No function call",
			input:    "This is just some regular text without any function calls.",
			expected: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			matches := _llamaFunctionRegex.FindStringSubmatch(tc.input)
			require.Equal(t, tc.expected, matches)
		})
	}
}
