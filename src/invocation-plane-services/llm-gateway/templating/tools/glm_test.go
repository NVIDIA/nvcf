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

package tools_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

func TestGLMToolPattern(t *testing.T) {
	var (
		parser = tools.NewGLMParseConfig()
		tests  = []struct {
			name          string
			input         string
			want          []tools.Call
			wantPreceding string
			wantErr       bool
		}{
			{
				name:          "No function call",
				input:         `This is just regular text without any tool calls.`,
				want:          nil,
				wantPreceding: "This is just regular text without any tool calls.",
				wantErr:       false,
			},
			{
				name: "Single function call",
				input: `<tool_call>search_web
<arg_key>query</arg_key>
<arg_value>latest weather</arg_value>
</tool_call>`,
				want: []tools.Call{
					{
						Name: "search_web",
						Arguments: map[string]any{
							"query": "latest weather",
						},
					},
				},
				wantPreceding: "",
				wantErr:       false,
			},
			{
				name: "Complex arguments with JSON object and array",
				input: `<tool_call>create_user
<arg_key>profile</arg_key>
<arg_value>{"name": "John Doe", "age": 30, "active": true}</arg_value>
<arg_key>tags</arg_key>
<arg_value>["admin", "developer", "reviewer"]</arg_value>
<arg_key>count</arg_key>
<arg_value>42</arg_value>
<arg_key>description</arg_key>
<arg_value>A simple description string</arg_value>
</tool_call>`,
				want: []tools.Call{
					{
						Name: "create_user",
						Arguments: map[string]any{
							"profile": map[string]any{
								"name":   "John Doe",
								"age":    float64(30),
								"active": true,
							},
							"tags":        []any{"admin", "developer", "reviewer"},
							"count":       float64(42),
							"description": "A simple description string",
						},
					},
				},
				wantPreceding: "",
				wantErr:       false,
			},
			{
				name: "Tool call with preceding text",
				input: `I need to search for information about the weather.

<tool_call>search_web
<arg_key>query</arg_key>
<arg_value>current weather forecast</arg_value>
</tool_call>`,
				want: []tools.Call{
					{
						Name: "search_web",
						Arguments: map[string]any{
							"query": "current weather forecast",
						},
					},
				},
				wantPreceding: "I need to search for information about the weather.",
				wantErr:       false,
			},
			{
				name: "Multiple tool calls with text between them",
				input: `Let me help you with that.

<tool_call>search_web
<arg_key>query</arg_key>
<arg_value>weather forecast</arg_value>
</tool_call>

Now I'll get some additional information.

<tool_call>get_location
<arg_key>ip_address</arg_key>
<arg_value>192.168.1.1</arg_value>
</tool_call>

This text should also be ignored.`,
				want: []tools.Call{
					{
						Name: "search_web",
						Arguments: map[string]any{
							"query": "weather forecast",
						},
					},
					{
						Name: "get_location",
						Arguments: map[string]any{
							"ip_address": "192.168.1.1",
						},
					},
				},
				wantPreceding: "Let me help you with that.",
				wantErr:       false,
			},
			{
				name: "Tool call with no arguments",
				input: `<tool_call>list_files
</tool_call>`,
				want: []tools.Call{
					{
						Name:      "list_files",
						Arguments: map[string]any{},
					},
				},
				wantPreceding: "",
				wantErr:       false,
			},
			{
				name: "Tool call with nested JSON",
				input: `<tool_call>complex_operation
<arg_key>config</arg_key>
<arg_value>{"database": {"host": "localhost", "port": 5432}, "features": ["auth", "logging"]}</arg_value>
</tool_call>`,
				want: []tools.Call{
					{
						Name: "complex_operation",
						Arguments: map[string]any{
							"config": map[string]any{
								"database": map[string]any{
									"host": "localhost",
									"port": float64(5432),
								},
								"features": []any{"auth", "logging"},
							},
						},
					},
				},
				wantPreceding: "",
				wantErr:       false,
			},
			{
				name: "Multiple consecutive tool calls",
				input: `<tool_call>first_function
<arg_key>param</arg_key>
<arg_value>value1</arg_value>
</tool_call><tool_call>second_function
<arg_key>param</arg_key>
<arg_value>value2</arg_value>
</tool_call>`,
				want: []tools.Call{
					{
						Name: "first_function",
						Arguments: map[string]any{
							"param": "value1",
						},
					},
					{
						Name: "second_function",
						Arguments: map[string]any{
							"param": "value2",
						},
					},
				},
				wantPreceding: "",
				wantErr:       false,
			},
			{
				name: "Error: first tool call has whitespace-only function name",
				input: `<tool_call>   
<arg_key>param</arg_key>
<arg_value>value</arg_value>
</tool_call>`,
				wantErr: true,
			},
			{
				name:    "Error: first tool call malformed",
				input:   `<tool_call></tool_call>`,
				wantErr: true,
			},
			{
				name: "Partial success: first tool succeeds, second fails",
				input: `<tool_call>good_function
<arg_key>param</arg_key>
<arg_value>value</arg_value>
</tool_call><tool_call>
</tool_call>`,
				want: []tools.Call{
					{
						Name: "good_function",
						Arguments: map[string]any{
							"param": "value",
						},
					},
				},
				wantPreceding: "",
				wantErr:       false,
			},
		}
	)

	// Test with ParallelToolCalls disabled
	t.Run("ParallelToolCalls disabled", func(t *testing.T) {
		input := `<tool_call>first_function
<arg_key>param</arg_key>
<arg_value>value1</arg_value>
</tool_call><tool_call>second_function
<arg_key>param</arg_key>
<arg_value>value2</arg_value>
</tool_call>`

		precedingText, parsedTools, err := parser.ParseTools(
			tools.Params{ParallelToolCalls: false},
			input,
		)
		require.NoError(t, err)
		require.Len(t, parsedTools, 1)
		assert.Equal(t, "first_function", parsedTools[0].Name)
		assert.Equal(t, map[string]any{"param": "value1"}, parsedTools[0].Arguments)
		assert.Empty(t, precedingText)
	})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			precedingText, parsedTools, err := parser.ParseTools(
				tools.Params{ParallelToolCalls: true},
				test.input,
			)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			t.Logf("parsed: %+v", parsedTools)

			// Check length and content, but not IDs since they're auto-generated
			require.Len(t, parsedTools, len(test.want))
			for i, call := range parsedTools {
				assert.Equal(t, test.want[i].Name, call.Name)
				assert.Equal(t, test.want[i].Arguments, call.Arguments)
				if test.want[i].ID != "" {
					assert.Equal(t, test.want[i].ID, call.ID)
				}
			}
			assert.Equal(t, test.wantPreceding, precedingText)
		})
	}
}
