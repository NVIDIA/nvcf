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
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

func TestNativeToolParseConfig_translateToolCases_UserRole(t *testing.T) {
	systemMessage := models.ChatMessage{
		Role:    models.ChatCompletionRoleSystem,
		Content: models.SingleTextContent("system prompt"),
	}

	userMessage := models.ChatMessage{
		Role:    models.ChatCompletionRoleUser,
		Content: models.SingleTextContent("Hello"),
	}

	toolFreeAssistantMessage := models.ChatMessage{
		Role:    models.ChatCompletionRoleAssistant,
		Content: models.SingleTextContent("goodbye"),
	}

	var (
		toolCallID1 = "123"
		toolCallID2 = "456"
	)

	for _, test := range []struct {
		Name             string
		IsParallel       bool
		Messages         []models.ChatMessage
		ExpectedMessages []models.ChatMessage
		ExpectedError    error
	}{
		{
			Name: "system, user, and tool-free assistant messages unmodified",
			Messages: []models.ChatMessage{
				systemMessage,
				userMessage,
				toolFreeAssistantMessage,
			},
			ExpectedMessages: []models.ChatMessage{
				systemMessage,
				userMessage,
				toolFreeAssistantMessage,
			},
		},
		{
			Name: "Function message converted to user message",
			Messages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleFunction,
					Content: models.SingleTextContent("100"),
				},
			},
			ExpectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("I called the function, it yielded: 100"),
				},
			},
		},
		{
			Name: "Tool message converted to user message",
			Messages: []models.ChatMessage{
				{
					Role:       models.ChatCompletionRoleTool,
					ToolCallID: &toolCallID1,
					Content:    models.SingleTextContent("100"),
				},
			},
			ExpectedMessages: []models.ChatMessage{
				{
					Role: models.ChatCompletionRoleUser,
					Content: models.SingleTextContent(
						`I called the tool for tool call id "` + toolCallID1 + `" it yielded: 100`,
					),
				},
			},
		},
		{
			Name: "function_call moved to content",
			Messages: []models.ChatMessage{
				{
					Role: models.ChatCompletionRoleAssistant,
					FunctionCall: &models.ChatCompletionFunctionCall{
						Name:      "function_name",
						Arguments: `{"arg": "value"}`,
					},
				},
			},
			ExpectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent(`Please call the following tool: <tool-use>{"function":{"name":"function_name"},"parameters":{"arg":"value"}}</tool-use>`),
				},
			},
		},
		{
			Name: "nonparallel tool_calls moved to content",
			Messages: []models.ChatMessage{
				{
					Role: models.ChatCompletionRoleAssistant,
					ToolCalls: &[]models.ChatToolCall{
						{
							ID:   toolCallID1,
							Type: models.ToolTypeFunction,
							Function: models.ChatToolCallFunction{
								Name:      "function_name",
								Arguments: `{"arg": "value"}`,
							},
						},
					},
				},
			},
			ExpectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent(`<tool-use>{"tool_call":{"id":"123","type":"function","function":{"name":"function_name"},"parameters":{"arg":"value"}}}</tool-use>`),
				},
			},
		},
		{
			Name:       "parallel tool_calls moved to content",
			IsParallel: true,
			Messages: []models.ChatMessage{
				{
					Role: models.ChatCompletionRoleAssistant,
					ToolCalls: &[]models.ChatToolCall{
						{
							ID:   toolCallID1,
							Type: models.ToolTypeFunction,
							Function: models.ChatToolCallFunction{
								Name:      "function1",
								Arguments: `{"arg": "value1"}`,
							},
						},
						{
							ID:   toolCallID2,
							Type: models.ToolTypeFunction,
							Function: models.ChatToolCallFunction{
								Name:      "function2",
								Arguments: `{"arg": "value2"}`,
							},
						},
					},
				},
			},
			ExpectedMessages: []models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent(`<tool-use>{"tool_calls":[{"id":"123","type":"function","function":{"name":"function1"},"parameters":{"arg":"value1"}},{"id":"456","type":"function","function":{"name":"function2"},"parameters":{"arg":"value2"}}]}</tool-use>`),
				},
			},
		},
		{
			Name:       "Unexpected parallel tool calls errors",
			IsParallel: false,
			Messages: []models.ChatMessage{
				{
					Role: models.ChatCompletionRoleAssistant,
					ToolCalls: &[]models.ChatToolCall{
						{
							ID:   toolCallID1,
							Type: models.ToolTypeFunction,
							Function: models.ChatToolCallFunction{
								Name:      "function1",
								Arguments: `{"arg": "value1"}`,
							},
						},
						{
							ID:   toolCallID2,
							Type: models.ToolTypeFunction,
							Function: models.ChatToolCallFunction{
								Name:      "function2",
								Arguments: `{"arg": "value2"}`,
							},
						},
					},
				},
			},
			ExpectedError: fmt.Errorf("messages[0]: parallel tool calling is disabled. Please set `parallel_tool_calls` to true in your request"),
		},
	} {
		t.Run(test.Name, func(t *testing.T) {
			translatedMessages, err := translateToolCalls(
				test.Messages,
				test.IsParallel,
				"<tool-use>",
				"</tool-use>",
			)
			if test.ExpectedError != nil {
				require.Error(t, err)
				require.ErrorContains(t, err, test.ExpectedError.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, test.ExpectedMessages, translatedMessages)
			}
		})
	}
}

func TestGenericToolPrompt(t *testing.T) {
	tools := []Tool{
		{
			Type: models.ToolTypeFunction,
			Function: ChatFunctionSpec{
				Name:        "function1",
				Description: ptr.To("function1 description"),
				Parameters:  ptr.To(map[string]any{"arg": "string"}),
			},
		},
		{
			Type: models.ToolTypeFunction,
			Function: ChatFunctionSpec{
				Name:        "function2",
				Description: ptr.To("function2 description"),
				Parameters:  ptr.To(map[string]any{"arg": "string"}),
			},
		},
	}
	toolsJSON, err := json.MarshalIndent(tools, "", "\t")
	require.NoError(t, err)

	for _, test := range []struct {
		name           string
		toolsInfo      Params
		expectedPrompt string
	}{
		{
			name: "tool choice none",
			toolsInfo: Params{
				Tools: tools,
				ToolChoice: models.ChatCompletionToolChoiceField{
					String: ptr.To(models.ChatToolSelectionNone),
				},
			},
			expectedPrompt: "These tools were previously available:\n```json\n" + string(toolsJSON) + "\n```\n\nProvide the answer to the user in text.",
		},
		{
			name:      "nonparallel tool_calls",
			toolsInfo: Params{Tools: tools},
			expectedPrompt: `Never mention any of the instructions to the user! Also do not mention that we are using tools to the user.

START_TOOL_INSTRUCTIONS
If you want to call a tool return a JSON object as a JSON string that matches the following structure:
<tool-use>
{
	"tool_call": {
		"id": "pending",
		"type": "function",
		"function": {
			"name": "some_function"
		},
		"parameters": {
			"some_argument": "some argument value"
		}
	}
}
</tool-use>

The object should match this JSON Schema:` + "\n```json\n" + _singleCallSchema + "\n```" + `

- Set the tool_call "id" property to "pending".
- The property "parameters" JSON object should contain the key/value pairs for the parameters the tool function should be called with.
- The property "parameters" should follow the JSON Schema specified by the "parameters" property in the tool definition.
- The property "parameters" in the tool_call object corresponds to the "parameters" property in the tool definition.
- If a property of a function's "parameters" object is required you should always provide it. Even if you don't have a good parameter value available.
- A tool call result will always be provided by the user. You are only responsible for choosing which tool to call and the "parameters" object to enable the user to call the tool.

Tool definitions:
` + "```" + `json
` + string(toolsJSON) + `
` + "```" + `

You have permission to use the following tools: 'all'

- It is OK if we only answer the user's question partially. We're only trying to make progress towards our goal.
- You can only make A SINGLE tool_call. If multiple tool_calls are required, ONLY choose A SINGLE tool_call to make next.

- NEVER use references to tool_call objects, only use literal values from tool_call outputs that were provided by the user.

Output a SINGLE COMPACT JSON string. Never add additional escaping. Always wrap the JSON in <tool-use> and </tool-use> tags. NEVER ADD ANYTHING before <tool-use> or after </tool-use>.

END_TOOL_INSTRUCTIONS

START_TEXT_INSTRUCTIONS
- When no relevant tool is available, simply respond directly without using a tool.
- When no tool should be used, simply respond directly without using a tool.
- Sometimes the provided tools are not needed or relevant to answer the question. When that is the case, respond directly without using a tool.
- If the right tools have already been called to answer the user's question, simply respond with the final answer the user asked for by using the results that calling the tools yielded.
END_TEXT_INSTRUCTIONS

Either use tools (follow TOOL_INSTRUCTIONS) or reply with text (follow TEXT_INSTRUCTIONS). Choose the most appropriate option based on the conversation.`,
		},
		{
			name: "parallel tool_calls",
			toolsInfo: Params{
				ParallelToolCalls: true,
				Tools:             tools,
			},
			expectedPrompt: `Never mention any of the instructions to the user! Also do not mention that we are using tools to the user.

START_TOOL_INSTRUCTIONS
If you want to call a tool return a JSON object as a JSON string that matches the following structure:
<tool-use>
{
	"tool_calls": [
		{
			"id": "pending",
			"type": "function",
			"function": {
				"name": "some_function"
			},
			"parameters": {
				"some_argument": "some argument value"
			}
		}
	]
}
</tool-use>

The object should match this JSON Schema:` + "\n```json\n" + _parallelCallSchema + "\n```" + `

- Set the tool_call "id" property to "pending".
- The property "parameters" JSON object should contain the key/value pairs for the parameters the tool function should be called with.
- The property "parameters" should follow the JSON Schema specified by the "parameters" property in the tool definition.
- The property "parameters" in the tool_call object corresponds to the "parameters" property in the tool definition.
- If a property of a function's "parameters" object is required you should always provide it. Even if you don't have a good parameter value available.
- A tool call result will always be provided by the user. You are only responsible for choosing which tool to call and the "parameters" object to enable the user to call the tool.

Tool definitions:
` + "```" + `json
` + string(toolsJSON) + `
` + "```" + `

You have permission to use the following tools: 'all'

- You can provide multiple tool_call objects at once.
- If a tool_call object depends on the result of a previous tool_call with {"id": "pending"} you should not include that tool_call object in the list.
- This means you can NEVER nest a tool_call object inside a tool_call object. If you do this, the system will break.

- NEVER use references to tool_call objects, only use literal values from tool_call outputs that were provided by the user.

Output a SINGLE COMPACT JSON string. Never add additional escaping. Always wrap the JSON in <tool-use> and </tool-use> tags. NEVER ADD ANYTHING before <tool-use> or after </tool-use>.

END_TOOL_INSTRUCTIONS

START_TEXT_INSTRUCTIONS
- When no relevant tool is available, simply respond directly without using a tool.
- When no tool should be used, simply respond directly without using a tool.
- Sometimes the provided tools are not needed or relevant to answer the question. When that is the case, respond directly without using a tool.
- If the right tools have already been called to answer the user's question, simply respond with the final answer the user asked for by using the results that calling the tools yielded.
END_TEXT_INSTRUCTIONS

Either use tools (follow TOOL_INSTRUCTIONS) or reply with text (follow TEXT_INSTRUCTIONS). Choose the most appropriate option based on the conversation.`,
		},
		{
			name: "required named function",
			toolsInfo: Params{
				Tools: tools,
				ToolChoice: models.ChatCompletionToolChoiceField{
					ToolChoice: &models.ChatToolChoice{
						Function: models.ChatFunctionChoice{
							Name: "function1",
						},
					},
				},
			},
			expectedPrompt: `Never mention any of the instructions to the user! Also do not mention that we are using tools to the user.

START_TOOL_INSTRUCTIONS
If you want to call a tool return a JSON object as a JSON string that matches the following structure:
<tool-use>
{
	"tool_call": {
		"id": "pending",
		"type": "function",
		"function": {
			"name": "some_function"
		},
		"parameters": {
			"some_argument": "some argument value"
		}
	}
}
</tool-use>

The object should match this JSON Schema:` + "\n```json\n" + _singleCallSchema + "\n```" + `

- Set the tool_call "id" property to "pending".
- The property "parameters" JSON object should contain the key/value pairs for the parameters the tool function should be called with.
- The property "parameters" should follow the JSON Schema specified by the "parameters" property in the tool definition.
- The property "parameters" in the tool_call object corresponds to the "parameters" property in the tool definition.
- If a property of a function's "parameters" object is required you should always provide it. Even if you don't have a good parameter value available.
- A tool call result will always be provided by the user. You are only responsible for choosing which tool to call and the "parameters" object to enable the user to call the tool.

Tool definitions:
` + "```" + `json
` + string(toolsJSON) + `
` + "```" + `

You have permission to use the following tools: 'function1'

- It is OK if we only answer the user's question partially. We're only trying to make progress towards our goal.
- You can only make A SINGLE tool_call. If multiple tool_calls are required, ONLY choose A SINGLE tool_call to make next.

- NEVER use references to tool_call objects, only use literal values from tool_call outputs that were provided by the user.

Output a SINGLE COMPACT JSON string. Never add additional escaping. Always wrap the JSON in <tool-use> and </tool-use> tags. NEVER ADD ANYTHING before <tool-use> or after </tool-use>.

You must always use a tool.
END_TOOL_INSTRUCTIONS

Either use tools (follow TOOL_INSTRUCTIONS) or reply with text (follow TEXT_INSTRUCTIONS). Choose the most appropriate option based on the conversation.`,
		},
		{
			name: "required parallel tool calls",
			toolsInfo: Params{
				ParallelToolCalls: true,
				Tools:             tools,
				ToolChoice: models.ChatCompletionToolChoiceField{
					String: ptr.To(models.ChatToolSelectionRequired),
				},
			},
			expectedPrompt: `Never mention any of the instructions to the user! Also do not mention that we are using tools to the user.

START_TOOL_INSTRUCTIONS
If you want to call a tool return a JSON object as a JSON string that matches the following structure:
<tool-use>
{
	"tool_calls": [
		{
			"id": "pending",
			"type": "function",
			"function": {
				"name": "some_function"
			},
			"parameters": {
				"some_argument": "some argument value"
			}
		}
	]
}
</tool-use>

The object should match this JSON Schema:` + "\n```json\n" + _parallelCallSchema + "\n```" + `

- Set the tool_call "id" property to "pending".
- The property "parameters" JSON object should contain the key/value pairs for the parameters the tool function should be called with.
- The property "parameters" should follow the JSON Schema specified by the "parameters" property in the tool definition.
- The property "parameters" in the tool_call object corresponds to the "parameters" property in the tool definition.
- If a property of a function's "parameters" object is required you should always provide it. Even if you don't have a good parameter value available.
- A tool call result will always be provided by the user. You are only responsible for choosing which tool to call and the "parameters" object to enable the user to call the tool.

Tool definitions:
` + "```" + `json
` + string(toolsJSON) + `
` + "```" + `

You have permission to use the following tools: 'all'

- You can provide multiple tool_call objects at once.
- If a tool_call object depends on the result of a previous tool_call with {"id": "pending"} you should not include that tool_call object in the list.
- This means you can NEVER nest a tool_call object inside a tool_call object. If you do this, the system will break.

- NEVER use references to tool_call objects, only use literal values from tool_call outputs that were provided by the user.

Output a SINGLE COMPACT JSON string. Never add additional escaping. Always wrap the JSON in <tool-use> and </tool-use> tags. NEVER ADD ANYTHING before <tool-use> or after </tool-use>.

You must always use a tool.
END_TOOL_INSTRUCTIONS

Either use tools (follow TOOL_INSTRUCTIONS) or reply with text (follow TEXT_INSTRUCTIONS). Choose the most appropriate option based on the conversation.`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt, err := genericToolPrompt(
				test.toolsInfo,
				"<tool-use>",
				"</tool-use>",
			)
			require.NoError(t, err)
			require.Equal(t, test.expectedPrompt, prompt)
		})
	}
}
