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

package k8sutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAnnotations(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
		expErr   string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:  "single key-value pair",
			input: "key=value",
			expected: map[string]string{
				"key": "value",
			},
		},
		{
			name:  "multiple key-value pairs",
			input: "key1=value1,key2=value2",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:  "key-value pair with no value",
			input: "key=",
			expected: map[string]string{
				"key": "",
			},
		},
		{
			name:  "invalid input (no =)",
			input: "key",
			expected: map[string]string{
				"key": "",
			},
		},
		{
			name:  "key-value pair with multiple =",
			input: "key=value=value",
			expected: map[string]string{
				"key": "value=value",
			},
		},
		{
			name:  "multiple key-value pairs with empty values",
			input: "key1=value1,key2=,key3=value3",
			expected: map[string]string{
				"key1": "value1",
				"key2": "",
				"key3": "value3",
			},
		},
		{
			name:  "spaces",
			input: "foo/bar=baz,a=b, c,d = e",
			expected: map[string]string{
				"foo/bar": "baz",
				"a":       "b",
				"c":       "",
				"d":       "e",
			},
		},
		{
			name:  "key-value pair with no key",
			input: "=value",
			expErr: "[metadata.annotations: Invalid value: \"\": name part must be non-empty, metadata.annotations: " +
				"Invalid value: \"\": name part must consist of alphanumeric characters, '-', '_' or '.', " +
				"and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', " +
				"regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')]",
		},
		{
			name:  "extra quotes",
			input: `"foo=bar"`,
			expErr: "metadata.annotations: Invalid value: \"\\\"foo\": name part must consist of alphanumeric characters, " +
				"'-', '_' or '.', and must start and end with an alphanumeric character " +
				"(e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := ParseAnnotations(tt.input)
			if err != nil {
				assert.EqualError(t, err, tt.expErr)
			} else {
				assert.Equal(t, tt.expected, actual)
			}
		})
	}
}
