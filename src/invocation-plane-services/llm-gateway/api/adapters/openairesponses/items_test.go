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

package openairesponses

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/golden"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
)

// to update test
// go test ./api/adapters/openai_responses -run TestOutputItem_MarshalJSON -update
func TestOutputItem_MarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		item OutputItem
	}{
		// Function call tests
		{
			name: "function_call_with_all_fields",
			item: OutputItem{
				Type:   ItemTypeFunctionCall,
				ID:     "fc_123",
				Status: ptr.To("completed"),
				ToolCallData: FunctionToolCall{
					Type:      ToolTypeFunction,
					CallID:    "call_abc123",
					Name:      "get_weather",
					Arguments: `{"location": "San Francisco, CA"}`,
				},
			},
		},
		{
			name: "function_call_without_status",
			item: OutputItem{
				Type: ItemTypeFunctionCall,
				ID:   "fc_456",
				ToolCallData: FunctionToolCall{
					Type:      ToolTypeFunction,
					CallID:    "call_def456",
					Name:      "get_stock_price",
					Arguments: `{"ticker": "AAPL"}`,
				},
			},
		},

		// Message type tests
		{
			name: "message_with_content",
			item: OutputItem{
				Type:   ItemTypeMessage,
				ID:     "msg_789",
				Status: ptr.To("in_progress"),
				Role:   "assistant",
				Content: []OutputContent{
					{
						Type:        ContentTypeOutputText,
						Text:        "Hello, world!",
						Annotations: []Annotation{},
						Logprobs:    []string{},
					},
				},
			},
		},
		{
			name: "message_with_empty_fields",
			item: OutputItem{
				Type: ItemTypeMessage,
				ID:   "msg_empty",
				// No status, role defaults to empty string, content to empty array
			},
		},

		// Web search call
		{
			name: "web_search_call",
			item: OutputItem{
				Type:   ItemTypeWebSearchCall,
				ID:     "ws_101",
				Status: ptr.To("completed"),
				ToolCallData: WebSearchToolCall{
					Type:   "web_search",
					ID:     "ws_101",
					Status: "completed",
				},
			},
		},

		// Code interpreter
		{
			name: "code_interpreter_with_outputs",
			item: OutputItem{
				Type:   ItemTypeCodeInterpreterCall,
				ID:     "ci_303",
				Status: ptr.To("completed"),
				ToolCallData: CodeInterpreterToolCall{
					Type:        ToolTypeCodeInterpreter,
					ID:          "ci_303",
					Status:      "completed",
					ContainerID: "container_123",
					Code:        ptr.To("print('Hello, world!')"),
					Outputs: []CodeInterpreterOutput{
						"Output line 1",
						"Output line 2",
					},
				},
			},
		},

		// Reasoning type
		{
			name: "reasoning_with_summary",
			item: OutputItem{
				Type:    ItemTypeReasoning,
				ID:      "rs_202",
				Summary: []any{"Step 1", "Step 2"},
			},
		},

		// Edge case: unknown type
		{
			name: "unknown_type_fallback",
			item: OutputItem{
				Type:   "unknown_type",
				ID:     "unk_404",
				Status: ptr.To("error"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal the item
			data, err := json.MarshalIndent(&tt.item, "", "  ")
			require.NoError(t, err)

			// Compare with golden file
			golden.Assert(
				t,
				string(data),
				fmt.Sprintf("openai_responses__output_item__%s.golden.json", tt.name),
			)
		})
	}
}

// to update test
// go test ./api/adapters/openai_responses -run TestOutputItem_MarshalJSON_NilSafety -update
func TestOutputItem_MarshalJSON_NilSafety(t *testing.T) {
	// Critical test: Verify that nil slices don't cause panics
	// and are properly converted to empty arrays in JSON
	tests := []struct {
		name string
		item OutputItem
	}{
		{
			name: "code_interpreter_nil_outputs",
			item: OutputItem{
				Type: ItemTypeCodeInterpreterCall,
				ID:   "ci_nil",
				ToolCallData: CodeInterpreterToolCall{
					Type:        ToolTypeCodeInterpreter,
					ID:          "ci_nil",
					ContainerID: "container_nil",
					// Outputs is nil - gets omitted due to omitempty when empty
				},
			},
		},
		{
			name: "reasoning_nil_summary",
			item: OutputItem{
				Type: ItemTypeReasoning,
				ID:   "rs_nil",
				// Summary is nil - should marshal as empty array per implementation
			},
		},
		{
			name: "message_nil_content",
			item: OutputItem{
				Type: ItemTypeMessage,
				ID:   "msg_nil",
				Role: "assistant",
				// Content is nil - should marshal as empty array
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that marshaling doesn't panic
			var (
				data []byte
				err  error
			)

			assert.NotPanics(t, func() {
				data, err = json.MarshalIndent(&tt.item, "", "  ")
			})
			require.NoError(t, err)

			// Compare with golden file
			golden.Assert(
				t,
				string(data),
				fmt.Sprintf("openai_responses__nil_safety__%s.golden.json", tt.name),
			)
		})
	}
}

// Tests for InputItem validation

// TestInputItem_UnmarshalJSON_Polymorphic tests the polymorphic unmarshaling behavior for all input formats
// To update golden files: go test ./api/adapters/openairesponses -run TestInputItem_UnmarshalJSON_Polymorphic -update
func TestInputItem_UnmarshalJSON_Polymorphic(t *testing.T) {
	tests := []struct {
		name        string
		json        string
		expected    InputItem
		expectError bool
	}{
		// EasyInputMessage format (no type field)
		{
			name: "easy_message_string_content",
			json: `{"role": "user", "content": "Hello world"}`,
			expected: InputItem{
				Role: "user",
				MessageContent: []InputContent{
					NewInputTextContent("Hello world"),
				},
			},
			expectError: false,
		},
		{
			name: "easy_message_array_content",
			json: `{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Hello"},
					{"type": "input_text", "text": "World"}
				]
			}`,
			expected: InputItem{
				Role: "user",
				MessageContent: []InputContent{
					NewInputTextContent("Hello"),
					NewInputTextContent("World"),
				},
			},
			expectError: false,
		},
		{
			name: "easy_message_assistant_role",
			json: `{"role": "assistant", "content": "I can help"}`,
			expected: InputItem{
				Role: "assistant",
				MessageContent: []InputContent{
					NewInputTextContent("I can help"),
				},
			},
			expectError: false,
		},

		// InputMessage format (with type="message")
		{
			name: "typed_message_with_text_content",
			json: `{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Hello world"}
				]
			}`,
			expected: InputItem{
				Type: "message",
				Role: "user",
				MessageContent: []InputContent{
					NewInputTextContent("Hello world"),
				},
			},
			expectError: false,
		},
		{
			name: "typed_message_with_mixed_content",
			json: `{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "What is this?"},
					{"type": "input_image", "image_url": "https://example.com/img.jpg"}
				]
			}`,
			expected: InputItem{
				Type: "message",
				Role: "user",
				MessageContent: []InputContent{
					NewInputTextContent("What is this?"),
					NewInputImageContent(ptr.To("https://example.com/img.jpg"), nil, ""),
				},
			},
			expectError: false,
		},

		// ItemReferenceParam format
		{
			name: "item_reference",
			json: `{"type": "item_reference", "id": "ref_123"}`,
			expected: InputItem{
				Type: "item_reference",
				ID:   "ref_123",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				item InputItem
				err  = json.Unmarshal([]byte(tt.json), &item)
			)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Check key fields
				assert.Equal(t, tt.expected.Type, item.Type)
				assert.Equal(t, tt.expected.Role, item.Role)
				assert.Equal(t, tt.expected.ID, item.ID)

				assert.Equal(t, tt.expected.MessageContent, item.MessageContent)
			}
		})
	}
}

func TestInputItem_Validate(t *testing.T) {
	tests := []struct {
		name        string
		item        InputItem
		expectError bool
		errorMsg    string
	}{
		// Valid cases - EasyInputMessage format (no Type)
		{
			name: "valid_user_role_easy_format",
			item: InputItem{
				Role: "user",
				MessageContent: []InputContent{
					NewInputTextContent("test"),
				},
			},
			expectError: false,
		},
		{
			name: "valid_assistant_role_easy_format",
			item: InputItem{
				Role: "assistant",
				MessageContent: []InputContent{
					NewInputTextContent("test response"),
				},
			},
			expectError: false,
		},
		{
			name: "valid_system_role_easy_format",
			item: InputItem{
				Role: "system",
				MessageContent: []InputContent{
					NewInputTextContent("You are helpful"),
				},
			},
			expectError: false,
		},
		{
			name: "valid_developer_role_easy_format",
			item: InputItem{
				Role: "developer",
				MessageContent: []InputContent{
					NewInputTextContent("Be truthful"),
				},
			},
			expectError: false,
		},

		// Valid cases - InputMessage format (with Type="message")
		// These use MessageContent field, not Content field
		{
			name: "valid_user_role_typed_format_with_message_content",
			item: InputItem{
				Type: "message",
				Role: "user",
				MessageContent: []InputContent{
					NewInputTextContent("test"),
				},
			},
			expectError: false,
		},
		{
			name: "valid_system_role_typed_format_with_message_content",
			item: InputItem{
				Type: "message",
				Role: "system",
				MessageContent: []InputContent{
					NewInputTextContent("You are helpful"),
				},
			},
			expectError: false,
		},
		{
			name: "valid_developer_role_typed_format_with_message_content",
			item: InputItem{
				Type: "message",
				Role: "developer",
				MessageContent: []InputContent{
					NewInputTextContent("Be truthful"),
				},
			},
			expectError: false,
		},
		{
			name: "valid_typed_format_with_multiple_content_parts",
			item: InputItem{
				Type: "message",
				Role: "user",
				MessageContent: []InputContent{
					NewInputTextContent("What is in this image?"),
					NewInputImageContent(ptr.To("https://example.com/image.jpg"), nil, "auto"),
				},
			},
			expectError: false,
		},
		{
			name: "valid_typed_message_with_assistant_role",
			item: InputItem{
				Type: "message",
				Role: "assistant",
				MessageContent: []InputContent{
					NewInputTextContent("I can help with that!"),
				},
			},
			expectError: false,
		},

		// Invalid cases
		// Note: assistant role with type="message" is now valid per OpenAI spec
		{
			name: "invalid_typed_message_missing_content",
			item: InputItem{
				Type: "message",
				Role: "user",
				// MessageContent is empty/nil
			},
			expectError: true,
			errorMsg:    "content`: is required for messages",
		},
		{
			name: "invalid_easy_message_missing_content",
			item: InputItem{
				// No Type (EasyInputMessage format)
				Role: "user",
				// MessageContent is empty/nil
			},
			expectError: true,
			errorMsg:    "content`: is required for messages",
		},
		{
			name: "invalid_unknown_roles",
			item: InputItem{
				Role: "admin", // Common misunderstanding - not a valid role
				MessageContent: []InputContent{
					NewInputTextContent("test"),
				},
			},
			expectError: true,
			errorMsg:    "invalid role 'admin'",
		},
		{
			name: "missing_role_for_message_type",
			item: InputItem{
				Type: "message",
				MessageContent: []InputContent{
					NewInputTextContent("test"),
				},
			},
			expectError: true,
			errorMsg:    "is required for message items",
		},
		{
			name: "missing_role_for_easy_message",
			item: InputItem{
				// No Type field (EasyInputMessage)
				MessageContent: []InputContent{
					NewInputTextContent("test"),
				},
			},
			expectError: true,
			errorMsg:    "is required for message items",
		},

		// Edge cases for other item types
		{
			name: "function_call_type_without_role",
			item: InputItem{
				Type: "function_call",
				// Function calls don't have roles
			},
			expectError: false,
		},
		{
			name: "item_reference_type_without_role",
			item: InputItem{
				Type: "item_reference",
				ID:   "ref_123",
			},
			expectError: false,
		},
		{
			name: "case_sensitive_role_validation",
			item: InputItem{
				Role: "User", // Wrong case
				MessageContent: []InputContent{
					NewInputTextContent("test"),
				},
			},
			expectError: true,
			errorMsg:    "invalid role 'User'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.item.Validate()
			if tt.expectError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMCPCallInputUnmarshal(t *testing.T) {
	t.Run("unmarshal mcp_call item in input", func(t *testing.T) {
		jsonStr := `{
			"type": "mcp_call",
			"id": "mcpc_123",
			"server_label": "stripe",
			"name": "create_product",
			"arguments": "{\"name\":\"Test Product\"}",
			"output": "{\"id\":\"prod_456\"}"
		}`

		var (
			item InputItem
			err  = json.Unmarshal([]byte(jsonStr), &item)
		)
		require.NoError(t, err)

		require.Equal(t, ItemTypeMCPCall, item.Type)
		require.NotNil(t, item.ToolCallData)

		mcpCall, ok := item.ToolCallData.(MCPToolCall)
		require.True(t, ok)
		require.Equal(t, "mcpc_123", mcpCall.ID)
		require.Equal(t, "stripe", mcpCall.ServerLabel)
		require.Equal(t, "create_product", mcpCall.Name)
		require.Equal(t, `{"name":"Test Product"}`, mcpCall.Arguments)

		output := ptr.Deref(mcpCall.Output)
		require.Equal(t, `{"id":"prod_456"}`, output)
	})

	t.Run("unmarshal input array with mcp_call items", func(t *testing.T) {
		jsonStr := `{
			"model": "test-model",
			"input": [
				{
					"role": "user",
					"content": "create a payment link"
				},
				{
					"type": "mcp_call",
					"id": "mcpc_1",
					"server_label": "stripe",
					"name": "create_product",
					"arguments": "{\"name\":\"Custom Payment\"}",
					"output": "{\"id\":\"prod_123\"}"
				},
				{
					"type": "mcp_call",
					"id": "mcpc_2",
					"server_label": "stripe",
					"name": "create_price",
					"arguments": "{\"product\":\"prod_123\",\"unit_amount\":1700,\"currency\":\"usd\"}",
					"output": "{\"id\":\"price_456\"}"
				}
			]
		}`

		var (
			req CreateRequest
			err = json.Unmarshal([]byte(jsonStr), &req)
		)
		require.NoError(t, err)

		require.Equal(t, "test-model", req.Model)
		require.Len(t, req.Input.Items, 3)

		// Check user message
		require.Equal(t, "user", req.Input.Items[0].Role)

		// Check first mcp_call
		require.Equal(t, ItemTypeMCPCall, req.Input.Items[1].Type)
		mcpCall1, ok := req.Input.Items[1].ToolCallData.(MCPToolCall)
		require.True(t, ok)
		require.Equal(t, "mcpc_1", mcpCall1.ID)
		require.Equal(t, "stripe", mcpCall1.ServerLabel)
		require.Equal(t, "create_product", mcpCall1.Name)

		// Check second mcp_call
		require.Equal(t, ItemTypeMCPCall, req.Input.Items[2].Type)
		mcpCall2, ok := req.Input.Items[2].ToolCallData.(MCPToolCall)
		require.True(t, ok)
		require.Equal(t, "mcpc_2", mcpCall2.ID)
		require.Equal(t, "create_price", mcpCall2.Name)
	})
}
