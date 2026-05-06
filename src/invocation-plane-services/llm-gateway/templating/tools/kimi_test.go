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

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

func TestKimiToolPattern(t *testing.T) {
	var (
		parser = tools.NewKimiParseConfig()
		tests  = []struct {
			name          string
			input         string
			want          []tools.Call
			wantPreceding string
		}{
			{
				name:  "basic",
				input: `<|tool_calls_section_begin|><|tool_call_begin|>functions.tavily-search:0<|tool_call_argument_begin|>{"days": 3, "query": "Groq launched model", "topic": "news"}<|tool_call_end|><|tool_calls_section_end|>`,
				want: []tools.Call{
					{
						Name: "tavily-search",
						ID:   "functions.tavily-search:0",
						Arguments: map[string]any{
							"days":  3.0,
							"query": "Groq launched model",
							"topic": "news",
						},
					},
				},
			},
			{
				name:  "function name with `:`",
				input: `<|tool_calls_section_begin|><|tool_call_begin|>functions.tavily:search:0<|tool_call_argument_begin|>{"days": 3, "query": "Groq launched model", "topic": "news"}<|tool_call_end|><|tool_calls_section_end|>`,
				want: []tools.Call{
					{
						Name: "tavily:search",
						ID:   "functions.tavily:search:0",
						Arguments: map[string]any{
							"days":  3.0,
							"query": "Groq launched model",
							"topic": "news",
						},
					},
				},
			},
			{
				name:  "function name with `.`",
				input: `<|tool_calls_section_begin|><|tool_call_begin|>functions.tavily.search:0<|tool_call_argument_begin|>{"days": 3, "query": "Groq launched model", "topic": "news"}<|tool_call_end|><|tool_calls_section_end|>`,
				want: []tools.Call{
					{
						Name: "tavily.search",
						ID:   "functions.tavily.search:0",
						Arguments: map[string]any{
							"days":  3.0,
							"query": "Groq launched model",
							"topic": "news",
						},
					},
				},
			},
			{
				name:  "parallel tool calls",
				input: `<|tool_calls_section_begin|><|tool_call_begin|>functions.Read:1<|tool_call_argument_begin|>{"file_path":"/TestOpenAIChatCompletionMessagesFromAnthropicMessages__simple_string_content__input.json"}<|tool_call_end|><|tool_call_begin|>functions.Read:2<|tool_call_argument_begin|>{"file_path":"/TestOpenAIChatCompletionMessagesFromAnthropicMessages__text_block_content__input.json"}<|tool_call_end|><|tool_call_begin|>functions.Read:3<|tool_call_argument_begin|>{"file_path":"/TestOpenAIChatCompletionMessagesFromAnthropicMessages__tool_use_block__input.json"}<|tool_call_end|><|tool_call_begin|>functions.Read:4<|tool_call_argument_begin|>{"file_path":"/TestOpenAIChatCompletionMessagesFromAnthropicMessages__tool_result_block__input.json"}<|tool_call_end|><|tool_calls_section_end|>"}}`,

				want: []tools.Call{
					{
						Name: "Read",
						ID:   "functions.Read:1",
						Arguments: map[string]any{
							"file_path": "/TestOpenAIChatCompletionMessagesFromAnthropicMessages__simple_string_content__input.json",
						},
					},
					{
						Name: "Read",
						ID:   "functions.Read:2",
						Arguments: map[string]any{
							"file_path": "/TestOpenAIChatCompletionMessagesFromAnthropicMessages__text_block_content__input.json",
						},
					},
					{
						Name: "Read",
						ID:   "functions.Read:3",
						Arguments: map[string]any{
							"file_path": "/TestOpenAIChatCompletionMessagesFromAnthropicMessages__tool_use_block__input.json",
						},
					},
					{
						Name: "Read",
						ID:   "functions.Read:4",
						Arguments: map[string]any{
							"file_path": "/TestOpenAIChatCompletionMessagesFromAnthropicMessages__tool_result_block__input.json",
						},
					},
				},
			},
			{
				name:          "No function call",
				input:         `foobar`,
				want:          nil,
				wantPreceding: "foobar",
			},
			{
				name:  "preceding text",
				input: `Let me search for information about this topic.<|tool_calls_section_begin|><|tool_call_begin|>functions.tavily-search:0<|tool_call_argument_begin|>{"query": "example"}<|tool_call_end|><|tool_calls_section_end|>`,
				want: []tools.Call{
					{
						Name: "tavily-search",
						ID:   "functions.tavily-search:0",
						Arguments: map[string]any{
							"query": "example",
						},
					},
				},
				wantPreceding: "Let me search for information about this topic.",
			},
			{
				name:  "repair unquoted JSON",
				input: `<|tool_calls_section_begin|><|tool_call_begin|>functions.Grep:0<|tool_call_argument_begin|>{"pattern":"Handlers",output_mode":"files_with_matches"}<|tool_call_end|><|tool_calls_section_end|>`,
				want: []tools.Call{
					{
						Name: "Grep",
						ID:   "functions.Grep:0",
						Arguments: map[string]any{
							"pattern":     "Handlers",
							"output_mode": "files_with_matches",
						},
					},
				},
			},
		}
	)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			precedingText, parsedTools, err := parser.ParseTools(
				tools.Params{ParallelToolCalls: true},
				test.input,
			)
			require.NoError(t, err)
			t.Logf("parsed: %+v", parsedTools)
			assert.Equal(t, test.want, parsedTools)
			assert.Equal(t, test.wantPreceding, precedingText)
		})
	}
}

func TestConvertToolIDsToKimiID(t *testing.T) {
	tests := []struct {
		name  string
		input []models.ChatMessage
		want  []models.ChatMessage
	}{
		{
			name: "invalid and valid tool call IDs",
			input: []models.ChatMessage{
				{
					Role:    "user",
					Content: models.SingleTextContent("hello"),
				},
				{
					Role: "assistant",
					ToolCalls: &[]models.ChatToolCall{
						{
							ID: "foo",
							Function: models.ChatToolCallFunction{
								Name:      "tavily-search",
								Arguments: `{"query": "example"}`,
							},
						},
						{
							ID: "bar",
							Function: models.ChatToolCallFunction{
								Name:      "tavily-search",
								Arguments: `{"query": "example2"}`,
							},
						},
					},
				},
				{
					Role:       "tool",
					ToolCallID: ptr.To("foo"),
					Content:    models.SingleTextContent("foo"),
				},
				{
					Role:       "tool",
					ToolCallID: ptr.To("baz"),
					Content:    models.SingleTextContent("baz"),
				},
				{
					Role:       "tool",
					ToolCallID: ptr.To("functions.sum:25"),
					Content:    models.SingleTextContent("100"),
				},
			},
			want: []models.ChatMessage{
				{
					Role:    "user",
					Content: models.SingleTextContent("hello"),
				},
				{
					Role: "assistant",
					ToolCalls: &[]models.ChatToolCall{
						{
							ID: "functions.tavily-search:0",
							Function: models.ChatToolCallFunction{
								Name:      "tavily-search",
								Arguments: `{"query": "example"}`,
							},
						},
						{
							ID: "functions.tavily-search:1",
							Function: models.ChatToolCallFunction{
								Name:      "tavily-search",
								Arguments: `{"query": "example2"}`,
							},
						},
					},
				},
				{
					Role:       "tool",
					ToolCallID: ptr.To("functions.tavily-search:0"),
					Content:    models.SingleTextContent("foo"),
				},
				{
					Role:       "tool",
					ToolCallID: ptr.To("functions.unknown:2"),
					Content:    models.SingleTextContent("baz"),
				},
				{
					Role:       "tool",
					ToolCallID: ptr.To("functions.sum:25"),
					Content:    models.SingleTextContent("100"),
				},
			},
		},
		{
			name: "ensure we skip numbers from valid tool call IDs",
			input: []models.ChatMessage{
				{
					Role: "assistant",
					ToolCalls: &[]models.ChatToolCall{
						{
							ID: "invalid 1",
							Function: models.ChatToolCallFunction{
								Name: "myfunc2",
							},
						},
						{
							ID: "invalid 2",
							Function: models.ChatToolCallFunction{
								Name: "myfunc3",
							},
						},
						{
							ID: "functions.myfunc:1",
							Function: models.ChatToolCallFunction{
								Name: "myfunc",
							},
						},
					},
				},
			},
			want: []models.ChatMessage{
				{
					Role: "assistant",
					ToolCalls: &[]models.ChatToolCall{
						{
							ID: "functions.myfunc2:0",
							Function: models.ChatToolCallFunction{
								Name: "myfunc2",
							},
						},
						{
							ID: "functions.myfunc3:2",
							Function: models.ChatToolCallFunction{
								Name: "myfunc3",
							},
						},
						{
							ID: "functions.myfunc:1",
							Function: models.ChatToolCallFunction{
								Name: "myfunc",
							},
						},
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tools.ConvertToolIDsToKimiID(test.input)
			assert.Equal(t, test.want, test.input)
		})
	}
}
