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

package prompt_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/golden"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/prompt"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/testhelpers"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

const tokenizerDir = "../../lib/tokenizers/vendor"

// promptToGolden converts a prompt to a stable textual representation for
// snapshot testing. Text parts are emitted verbatim; images are emitted as
// labeled lines with their URL and detail fields.
func promptToGolden(p prompt.Prompt) string {
	var b strings.Builder
	for _, part := range p {
		switch v := part.(type) {
		case models.ContentPartText:
			b.WriteString(string(v))
		case *models.ContentPartImageURL:
			// Ensure images are on their own line for readability.
			if b.Len() > 0 {
				if s := b.String(); s[len(s)-1] != '\n' {
					b.WriteByte('\n')
				}
			}
			_, _ = fmt.Fprintf(&b, "[IMAGE url=%s detail=%s]\n", v.URL, v.Detail)
		default:
			// Fallback for any other content types.
			_, _ = fmt.Fprintf(&b, "[%T]\n", v)
		}
	}
	return b.String()
}

func sanitizeName(s string) string {
	r := strings.NewReplacer(
		" ",
		"_",
		"/",
		"_",
		"\\",
		"_",
		":",
		"_",
		"|",
		"_",
		"<",
		"_",
		">",
		"_",
		"*",
		"_",
		"?",
		"_",
	)
	return r.Replace(s)
}

func TestNormalizeMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		messages          []models.ChatMessage
		padMessages       bool
		supportsSysPrompt bool
		expectedMessages  []models.ChatMessage
	}{
		{
			name: "No system message, no padding",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("foo"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("foo"),
				},
			},
			padMessages: false,
			expectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("foo"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("foo"),
				},
				prompt.NewEmptyAssistantMessage(),
			},
		},
		{
			name: "tool call padding",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user 1"),
				},
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("tool call"),
				},
				{
					Role:    models.ChatCompletionRoleTool,
					Content: models.SingleTextContent("result"),
				},
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("post tool call"),
				},
				{
					Role:    models.ChatCompletionRoleTool,
					Content: models.SingleTextContent("tool call 2"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user 2"),
				},
			},
			padMessages: true,
			expectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user 1"),
				},
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("tool call"),
				},
				{
					Role:    models.ChatCompletionRoleTool,
					Content: models.SingleTextContent("result"),
				},
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("post tool call"),
				},
				{
					Role:    models.ChatCompletionRoleTool,
					Content: models.SingleTextContent("tool call 2"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user 2"),
				},
				prompt.NewEmptyAssistantMessage(),
			},
		},
		{
			name: "No system message, padding",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("foo"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("bar"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("baz"),
				},
			},
			padMessages: true,
			expectedMessages: []models.ChatMessage{
				prompt.NewEmptyUserMessage(),
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("foo"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("bar"),
				},
				prompt.NewEmptyAssistantMessage(),
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("baz"),
				},
				prompt.NewEmptyAssistantMessage(),
			},
		},
		{
			name:              "Many system messages",
			supportsSysPrompt: true,
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleSystem,
					Content: models.SingleTextContent("system1"),
				},
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("foo"),
				},
				{
					Role:    models.ChatCompletionRoleSystem,
					Content: models.SingleTextContent("system2"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("bar"),
				},
				{
					Role:    models.ChatCompletionRoleSystem,
					Content: models.SingleTextContent("system3"),
				},
			},
			expectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleSystem,
					Content: models.SingleTextContent("system1\nsystem2\nsystem3"),
				},
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent("foo"),
				},
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("bar"),
				},
				prompt.NewEmptyAssistantMessage(),
			},
		},
		{
			name:              "merge system prompt with user",
			supportsSysPrompt: false,
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user message"),
				},
				{
					Role:    models.ChatCompletionRoleSystem,
					Content: models.SingleTextContent("system message"),
				},
			},
			expectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("system message user message"),
				},
				prompt.NewEmptyAssistantMessage(),
			},
		},
		{
			name:              "Just a system message",
			supportsSysPrompt: false,
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleSystem,
					Content: models.SingleTextContent("hello"),
				},
			},
			expectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("hello"),
				},
				prompt.NewEmptyAssistantMessage(),
			},
		},
		{
			name:              "No panic on empty messages",
			supportsSysPrompt: false,
			messages:          []models.ChatMessage{},
			expectedMessages: []models.ChatMessage{
				prompt.NewEmptyAssistantMessage(),
			},
		},
	}

	t.Run("group", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				formattedMessages := prompt.NormalizeMessages(
					tt.messages,
					tt.padMessages,
					tt.supportsSysPrompt,
					models.ChatCompletionRoleAssistant,
					false,
				)
				assert.Equal(t, tt.expectedMessages, formattedMessages)
			})
		}
	})

	t.Run("preserve_system_message_order", func(t *testing.T) {
		tests := []struct {
			name     string
			messages []models.ChatMessage
			expected []models.ChatMessage
		}{
			{
				name: "Sequential system messages preserved",
				messages: []models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message 1"),
					},
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("User message"),
					},
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message 2"),
					},
				},
				expected: []models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message 1"),
					},
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("User message"),
					},
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message 2"),
					},
					{
						Role:    models.ChatCompletionRoleAssistant,
						Content: models.SingleTextContent(""),
					},
				},
			},
			{
				name: "Mixed message order preserved",
				messages: []models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("User message 1"),
					},
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message"),
					},
					{
						Role:    models.ChatCompletionRoleAssistant,
						Content: models.SingleTextContent("Assistant message"),
					},
				},
				expected: []models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("User message 1"),
					},
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message"),
					},
					{
						Role:    models.ChatCompletionRoleAssistant,
						Content: models.SingleTextContent("Assistant message"),
					},
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				formattedMessages := prompt.NormalizeMessages(
					tt.messages,
					true,
					true,
					models.ChatCompletionRoleAssistant,
					true,
				)
				assert.Equal(t, tt.expected, formattedMessages)
			})
		}
	})
}

// Two options were added to support Qwen3:
// 1. if the enable_thinking flag, which appends an open and close think tag to prevent thinking when false
// 2. Reasoning fields on assistant messages, which is only rendered on the last assistant messages if tool calls succeed it.
// Other models may do different things with these fields.
// The intent of this test is to ensure that these fields are passed to the template and change the output.
func TestQwenReasoningFields(t *testing.T) {
	t.Parallel()

	templater := templating.NewEngine()
	require.NoError(t, templater.RegisterHFTemplates(tokenizerDir))
	defer templater.Close()
	require.NoError(t, templater.RegisterCustomJinjaTemplates())
	template := must.Get(templater.GetTextTemplate("qwen3-235b-a22b"))

	tests := []struct {
		name     string
		messages []models.ChatMessage
		params   tools.Params
	}{
		{
			name: "smoke",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user message"),
				},
			},
		},
		{
			name: "reasoning disabled",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user message"),
				},
			},
			params: tools.Params{
				EnableThinking: ptr.To(false),
			},
		},
		{
			name: "reasoning enabled",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user message"),
				},
			},
			params: tools.Params{
				EnableThinking: ptr.To(true),
			},
		},
		{
			name: "reasoning in assistant message",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("What's the weather in boston and tokyo?"),
				},
				{
					Role: models.ChatCompletionRoleAssistant,
					Reasoning: ptr.To(
						"First we need to get the weather in boston, then we need to get the weather in tokyo.",
					),
					ToolCalls: &[]models.ChatToolCall{
						{
							ID: "ID1",
							Function: models.ChatToolCallFunction{
								Name:      "get_current_weather",
								Arguments: `{"city": "boston"}`,
							},
						},
						{
							ID: "ID2",
							Function: models.ChatToolCallFunction{
								Name:      "get_current_weather",
								Arguments: `{"city": "tokyo"}`,
							},
						},
					},
				},
				{
					Role:       models.ChatCompletionRoleTool,
					ToolCallID: ptr.To("ID1"),
					Content:    models.SingleTextContent("85"),
				},
				{
					Role:       models.ChatCompletionRoleTool,
					ToolCallID: ptr.To("ID2"),
					Content:    models.SingleTextContent("80"),
				},
			},
			params: tools.Params{
				Tools: []tools.Tool{testhelpers.NewWeatherInfoTool()},
			},
		},
	}

	t.Run("group", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				rendered, err := template.RenderText(tt.messages, tt.params)
				require.NoError(t, err)
				golden.Assert(t, promptToGolden(rendered), fmt.Sprintf(
					"qwen_reasoning_%s.txt",
					sanitizeName(tt.name),
				))
			})
		}
	})
}

// Smoke test all jinja templates to attempt to find any missing functions/issues
// between the templates and the non-python jinja.
// to update golden:
// go test -v ./templating/prompt/... -update
func TestJinjaTemplates(t *testing.T) {
	t.Parallel()

	templater := templating.NewEngine()
	defer templater.Close()

	require.NoError(t, templater.RegisterHFTemplates(tokenizerDir))
	require.NoError(t, templater.RegisterCustomJinjaTemplates())

	for name, template := range templater.TemplatesIter() {
		// Make rand consistent for any templates that generate their own tool call IDs
		t.Run(name, func(t *testing.T) {
			var (
				toolCallID1 = "call_111111111"
				toolCallID2 = "call_222222222"
			)
			if name == "kimi-k2-instruct" {
				toolCallID1 = "functions.weather_info:0"
				toolCallID2 = "functions.weather_info:1"
			}
			t.Run("tool_call", func(t *testing.T) {
				t.Parallel()

				template, ok := template.(prompt.TextTemplate)
				if !ok {
					t.Skipf("skipping non-text template %q", name)
					return
				}

				rendered, err := template.RenderText([]models.ChatMessage{
					{
						Role: models.ChatCompletionRoleUser,
						Content: models.SingleTextContent(
							"What's the weather in boston and tokyo?",
						),
					},
					// Ensure we can handle system messages that are not the first message
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message"),
					},
					// Parallel tool calls
					{
						Role: models.ChatCompletionRoleAssistant,
						ToolCalls: &[]models.ChatToolCall{
							{
								ID: toolCallID1,
								Function: models.ChatToolCallFunction{
									Name:      "weather_info",
									Arguments: `{"location": "boston", "unit": "fahrenheit"}`,
								},
							},
							{
								ID: toolCallID2,
								Function: models.ChatToolCallFunction{
									Name:      "weather_info",
									Arguments: `{"location": "tokyo", "unit": "fahrenheit"}`,
								},
							},
						},
					},
					// Tool responses
					{
						Role:       models.ChatCompletionRoleTool,
						ToolCallID: ptr.To(toolCallID1),
						Content: models.ChatMessageContent{
							models.ContentPartText("8"),
							models.ContentPartText("5"),
						},
					},
					{
						Role:       models.ChatCompletionRoleTool,
						ToolCallID: ptr.To(toolCallID2),
						// Some templates will output a json object as a string rather than a string.
						// Exercising this path with the second tool call
						Content: models.SingleTextContent(` {"degrees": 80} `),
					},
				},
					tools.Params{
						Tools:             []tools.Tool{testhelpers.NewWeatherInfoTool()},
						ParallelToolCalls: true,
						ToolChoice: models.ChatCompletionToolChoiceField{
							String: ptr.To(string(models.ChatToolSelectionAuto)),
						},
						EnableCitations: true,
					},
				)
				require.NoError(t, err)
				golden.Assert(t, promptToGolden(rendered), fmt.Sprintf(
					"jinja_tool_call_%s.txt",
					sanitizeName(name),
				))
			})

			t.Run("function_call_"+name, func(t *testing.T) {
				t.Parallel()
				template, ok := template.(prompt.TextTemplate)
				if !ok {
					t.Skipf("skipping non-text template %q", name)
					return
				}

				switch name {
				case "kimi-k2-instruct", "kimi-k2-thinking":
					t.Skip(
						"templates generate randomly generate tool call IDd for functions so golden tests are not deterministic",
					)
				default:
					// OK
				}
				rendered, err := template.RenderText([]models.ChatMessage{
					{
						Role: models.ChatCompletionRoleUser,
						Content: models.SingleTextContent(
							"What's the weather in boston and tokyo?",
						),
					},
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("System message"),
					},
					{
						Role: models.ChatCompletionRoleAssistant,
						// Most templates will ignore reasoning.
						Reasoning: ptr.To(
							"First we need to get the weather in boston, then we need to get the weather in tokyo.",
						),
						FunctionCall: &models.ChatCompletionFunctionCall{
							Name:      "weather_info",
							Arguments: `{"location": "boston", "unit": "fahrenheit"}`,
						},
					},
					{
						Role:    models.ChatCompletionRoleFunction,
						Name:    ptr.To("weather_info"),
						Content: models.SingleTextContent("85"),
					},
				},
					tools.Params{
						Tools:             []tools.Tool{testhelpers.NewWeatherInfoTool()},
						ParallelToolCalls: true,
						ToolChoice: models.ChatCompletionToolChoiceField{
							String: ptr.To(string(models.ChatToolSelectionAuto)),
						},
					})
				require.NoError(t, err)
				golden.Assert(t, promptToGolden(rendered), fmt.Sprintf(
					"jinja_function_call_%s.txt",
					sanitizeName(name),
				))
			})

			// This test includes:
			// * multiple system prompts throughout the chat
			//     Most templates only 1 sys prompt as the first message
			// * multiple user messages in a row and multiple assistant messages in a row
			//     Most templates require that these roles alternate
			// * ends on a user message
			//     So the appropriate tokens for the model to take its turn
			t.Run("out of order messages", func(t *testing.T) {
				t.Parallel()

				template, ok := template.(prompt.TextTemplate)
				if !ok {
					t.Skipf("skipping non-text template %q", name)
					return
				}

				rendered, err := template.RenderText([]models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("system message 1"),
					},
					{
						Role:      models.ChatCompletionRoleAssistant,
						Reasoning: ptr.To("reasoning 1"),
						Content:   models.SingleTextContent("assistant message 1"),
					},
					{
						Role:    models.ChatCompletionRoleAssistant,
						Content: models.SingleTextContent("assistant message 2"),
					},
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("system message 2"),
					},
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("user message 1"),
					},
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("user message 2"),
					},
				},
					tools.Params{
						ToolChoice: models.ChatCompletionToolChoiceField{
							String: ptr.To(string(models.ChatToolSelectionNone)),
						},
						EnableCitations: false,
					},
				)

				require.NoError(t, err)
				golden.Assert(t, promptToGolden(rendered), fmt.Sprintf(
					"jinja_out_of_order_messages_%s.txt",
					sanitizeName(name),
				))
			})

			t.Run("documents", func(t *testing.T) {
				t.Parallel()
				if !strings.Contains(name, "allam-2-34b") && !strings.Contains(name, "cohere") {
					t.Skipf("skipping %s because it does not support documents", name)
					return
				}

				template, ok := template.(prompt.TextTemplate)
				if !ok {
					t.Skipf("skipping non-text template %q", name)
					return
				}

				const docToolCallID = "call_documents_1"

				rendered, err := template.RenderText(
					[]models.ChatMessage{
						{
							Role:    models.ChatCompletionRoleUser,
							Content: models.SingleTextContent("user message"),
						},
						{
							Role: models.ChatCompletionRoleAssistant,
							ToolCalls: &[]models.ChatToolCall{
								{
									ID: docToolCallID,
									Function: models.ChatToolCallFunction{
										Name:      "search",
										Arguments: `{"query": "doc references"}`,
									},
								},
							},
						},
						{
							Role:       models.ChatCompletionRoleTool,
							ToolCallID: ptr.To(docToolCallID),
							Content: models.SingleTextContent(
								`{"results": ["doc snippet 1", "doc snippet 2"]}`,
							),
						},
					}, tools.Params{Documents: []string{
						`"document 1"`,
						`{"title": "foo", "content": "document 2"}`,
					}})
				require.NoError(t, err)
				golden.Assert(
					t,
					promptToGolden(rendered),
					fmt.Sprintf("documents_%s.txt", sanitizeName(name)),
				)
			})

			t.Run("tool message with document content", func(t *testing.T) {
				t.Parallel()
				if name != "cohere-command-ai-reasoning" {
					t.Skipf(
						"skipping %s because it does not support document content in tool messages",
						name,
					)
					return
				}

				template, ok := template.(prompt.TextTemplate)
				if !ok {
					t.Skipf("skipping non-text template %q", name)
					return
				}

				var (
					toolCallID1 = "call_search_1"
					toolCallID2 = "call_search_2"
				)

				rendered, err := template.RenderText(
					[]models.ChatMessage{
						{
							Role: models.ChatCompletionRoleUser,
							Content: models.SingleTextContent(
								"Search for information about Paris and Tokyo",
							),
						},
						{
							Role: models.ChatCompletionRoleAssistant,
							ToolCalls: &[]models.ChatToolCall{
								{
									ID:   toolCallID1,
									Type: "function",
									Function: models.ChatToolCallFunction{
										Name:      "search",
										Arguments: `{"query": "Paris"}`,
									},
								},
								{
									ID:   toolCallID2,
									Type: "function",
									Function: models.ChatToolCallFunction{
										Name:      "search",
										Arguments: `{"query": "Tokyo"}`,
									},
								},
							},
						},
						{
							Role:       models.ChatCompletionRoleTool,
							ToolCallID: ptr.To(toolCallID1),
							Content: models.ChatMessageContent{
								models.ContentPartText("Search results for Paris:"),
								&models.ContentPartDocument{
									Data: map[string]any{
										"title":   "Paris",
										"content": "Paris is the capital of France",
									},
									ID: ptr.To("doc_paris_1"),
								},
								&models.ContentPartDocument{
									Data: map[string]any{
										"title":   "Eiffel Tower",
										"content": "The Eiffel Tower is in Paris",
									},
								},
							},
						},
						{
							Role:       models.ChatCompletionRoleTool,
							ToolCallID: ptr.To(toolCallID2),
							Content: models.ChatMessageContent{
								&models.ContentPartDocument{
									Data: map[string]any{
										"title":   "Tokyo",
										"content": "Tokyo is the capital of Japan",
									},
									ID: ptr.To("doc_tokyo_1"),
								},
							},
						},
					},
					tools.Params{
						Tools: []tools.Tool{
							{
								Type: "function",
								Function: tools.ChatFunctionSpec{
									Name:        "search",
									Description: ptr.To("Search for information"),
									Parameters: map[string]any{
										"type": "object",
										"properties": map[string]any{
											"query": map[string]any{
												"type":        "string",
												"description": "Search query",
											},
										},
										"required": []any{"query"},
									},
								},
							},
						},
						ParallelToolCalls: true,
					},
				)
				require.NoError(t, err)
				golden.Assert(
					t,
					promptToGolden(rendered),
					fmt.Sprintf("tool_message_document_content_%s.txt", sanitizeName(name)),
				)
			})

			t.Run("thinking", func(t *testing.T) {
				t.Parallel()
				template, ok := template.(prompt.TextTemplate)
				if !ok {
					t.Skipf("skipping non-text template %q", name)
					return
				}

				if template.ReasoningConfig() == nil {
					t.Skipf("skipping %s because it does not support thinking", name)
					return
				}

				rendered, err := template.RenderText([]models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("user message"),
					},
					{
						Role:      models.ChatCompletionRoleAssistant,
						Reasoning: ptr.To("reasoning message"),
						Content:   models.SingleTextContent("assistant message"),
					},
					{
						Role:      models.ChatCompletionRoleAssistant,
						Reasoning: ptr.To("reasoning message 2"),
						Content:   models.SingleTextContent("assistant message 2"),
						ToolCalls: &[]models.ChatToolCall{
							{
								ID: "tool_call_id",
								Function: models.ChatToolCallFunction{
									Name:      "function_name",
									Arguments: `{"argument": "value"}`,
								},
							},
						},
					},
					{
						Role:       models.ChatCompletionRoleTool,
						ToolCallID: ptr.To("tool_call_id"),
						Content:    models.SingleTextContent("tool call result"),
					},
				}, tools.Params{})
				require.NoError(t, err)
				golden.Assert(
					t,
					promptToGolden(rendered),
					fmt.Sprintf("jinja_thinking_%s.txt", sanitizeName(name)),
				)
			})
		})
	}
}

func TestImages(t *testing.T) {
	t.Parallel()

	hfTemplater := templating.NewEngine()
	defer hfTemplater.Close()

	require.NoError(t, hfTemplater.RegisterHFTemplates(tokenizerDir))

	var (
		image1 = &models.ContentPartImageURL{
			URL: "https://example.com/image1.png",
		}
		image2 = &models.ContentPartImageURL{
			URL: "https://example.com/image2.png",
		}
	)

	tests := []struct {
		name     string
		messages []models.ChatMessage
		models   []string
	}{
		{
			name: "no images",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("user message"),
				},
			},
			models: []string{"pixtral-12b"},
		},
		{
			name: "image only",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: []models.ContentPart{image1},
				},
			},
			models: []string{"pixtral-12b"},
		},
		{
			name: "two images in a row",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: []models.ContentPart{image1, image2},
				},
			},
			models: []string{"pixtral-12b"},
		},
	}

	t.Run("group", func(t *testing.T) {
		for _, tt := range tests {
			for _, m := range tt.models {
				if tt.messages[len(tt.messages)-1].Role == models.ChatCompletionRoleUser {
					t.Run(
						fmt.Sprintf("%v - %v jinja2 - Hugging Face", tt.name, m),
						func(t *testing.T) {
							t.Parallel()

							e, err := hfTemplater.GetTextTemplate(m)
							require.NoError(t, err, "Hugging face tokenizer missing for %v", m)
							rendered, err := e.RenderText(tt.messages, tools.Params{})
							require.NoError(t, err)

							golden.Assert(t, promptToGolden(rendered), fmt.Sprintf(
								"images_%s_%s.txt",
								sanitizeName(tt.name),
								sanitizeName(m),
							))

							// Special test to ensure we maintain a pointer to the original image struct
							// and don't make a copy.
							if tt.name == "image only" {
								assert.Same(
									t,
									image1,
									must.As[*models.ContentPartImageURL](rendered[1]),
								)
							}
						},
					)
				}
			}
		}
	})
}

func BenchmarkTemplates(b *testing.B) {
	hfTemplater := templating.NewEngine()
	require.NoError(b, hfTemplater.RegisterHFTemplates(tokenizerDir))
	defer hfTemplater.Close()

	customTemplater := templating.NewEngine()
	require.NoError(b, customTemplater.RegisterCustomTemplates())
	defer customTemplater.Close()

	tests := []struct {
		name     string
		messages []models.ChatMessage
	}{
		{
			name: "short",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("hello1"),
				},
			},
		},
		{
			name: "long",
			messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent(""),
				},
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent(""),
				},
			},
		},
	}

	for _, tt := range tests {
		for m, e := range hfTemplater.TemplatesIter() {
			b.Run(fmt.Sprintf("%v %v jinja", tt.name, m), func(b *testing.B) {
				e, ok := e.(prompt.TextTemplate)
				if !ok {
					b.Skipf("skipping non-text template %q", m)
					return
				}

				for b.Loop() {
					_, err := e.RenderText(tt.messages, tools.Params{})
					if err != nil {
						b.Fatal(err.Error())
					}
				}
			})
		}

		for m, e := range customTemplater.TemplatesIter() {
			b.Run(fmt.Sprintf("%v %v custom", tt.name, m), func(b *testing.B) {
				e, ok := e.(prompt.TextTemplate)
				if !ok {
					b.Skipf("skipping non-text template %q", m)
					return
				}

				for b.Loop() {
					_, err := e.RenderText(tt.messages, tools.Params{})
					if err != nil {
						b.Fatal(err.Error())
					}
				}
			})
		}
	}
}
