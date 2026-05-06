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

func TestMinimaxM2ToolPattern(t *testing.T) {
	var (
		parser = tools.NewMinimaxM2ParseConfig()
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
				name: "Single function call from example",
				input: `Let me help you query the weather.
<minimax:tool_call>
<invoke name="get_weather">
<parameter name="location">San Francisco</parameter>
<parameter name="unit">celsius</parameter>
</invoke>
</minimax:tool_call>`,
				want: []tools.Call{
					{
						Name: "get_weather",
						Arguments: map[string]any{
							"location": "San Francisco",
							"unit":     "celsius",
						},
					},
				},
				wantPreceding: "Let me help you query the weather.",
				wantErr:       false,
			},
			{
				name: "Single function call with no preceding text",
				input: `<minimax:tool_call>
<invoke name="search_web">
<parameter name="query">latest weather</parameter>
</invoke>
</minimax:tool_call>`,
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
				name: "Tool call with no arguments",
				input: `<minimax:tool_call>
<invoke name="list_files">
</invoke>
</minimax:tool_call>`,
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
				name: "Multiple tool calls with text between them",
				input: `Let me help you with that.

<minimax:tool_call>
<invoke name="search_web">
<parameter name="query">weather forecast</parameter>
</invoke>
</minimax:tool_call>

Now I'll get some additional information.

<minimax:tool_call>
<invoke name="get_location">
<parameter name="ip_address">192.168.1.1</parameter>
</invoke>
</minimax:tool_call>

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
				name: "Multiple consecutive tool calls",
				input: `<minimax:tool_call>
<invoke name="first_function">
<parameter name="param">value1</parameter>
</invoke>
</minimax:tool_call><minimax:tool_call>
<invoke name="second_function">
<parameter name="param">value2</parameter>
</invoke>
</minimax:tool_call>`,
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
				name: "Complex arguments with JSON object",
				input: `<minimax:tool_call>
<invoke name="create_user">
<parameter name="profile">{"name": "John Doe", "age": 30, "active": true}</parameter>
<parameter name="tags">["admin", "developer", "reviewer"]</parameter>
<parameter name="count">42</parameter>
<parameter name="description">A simple description string</parameter>
</invoke>
</minimax:tool_call>`,
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
				name: "Tool call with nested JSON",
				input: `<minimax:tool_call>
<invoke name="complex_operation">
<parameter name="config">{"database": {"host": "localhost", "port": 5432}, "features": ["auth", "logging"]}</parameter>
</invoke>
</minimax:tool_call>`,
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
				name: "Tool call with multiline parameter value",
				input: `<minimax:tool_call>
<invoke name="write_code">
<parameter name="code">
def hello():
    print("Hello, World!")
</parameter>
</invoke>
</minimax:tool_call>`,
				want: []tools.Call{
					{
						Name: "write_code",
						Arguments: map[string]any{
							"code": "def hello():\n    print(\"Hello, World!\")",
						},
					},
				},
				wantPreceding: "",
				wantErr:       false,
			},
			{
				name: "Error: first tool call has empty function name",
				input: `<minimax:tool_call>
<invoke name="">
<parameter name="param">value</parameter>
</invoke>
</minimax:tool_call>`,
				wantErr: true,
			},
			{
				name:    "Error: first tool call malformed - no invoke",
				input:   `<minimax:tool_call></minimax:tool_call>`,
				wantErr: true,
			},
			{
				name: "Error: first tool call missing name attribute",
				input: `<minimax:tool_call>
<invoke>
<parameter name="param">value</parameter>
</invoke>
</minimax:tool_call>`,
				wantErr: true,
			},
			{
				name: "Partial success: first tool succeeds, second fails",
				input: `<minimax:tool_call>
<invoke name="good_function">
<parameter name="param">value</parameter>
</invoke>
</minimax:tool_call><minimax:tool_call>
<invoke>
</invoke>
</minimax:tool_call>`,
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
		input := `<minimax:tool_call>
<invoke name="first_function">
<parameter name="param">value1</parameter>
</invoke>
</minimax:tool_call><minimax:tool_call>
<invoke name="second_function">
<parameter name="param">value2</parameter>
</invoke>
</minimax:tool_call>`

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
