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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/golden"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
)

func newAdapterTextItem(text string) InputItem {
	return InputItem{
		Role: RoleUser,
		MessageContent: []InputContent{
			NewInputTextContent(text),
		},
	}
}

func TestCreateRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		request CreateRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid request with text input",
			request: CreateRequest{
				Model: "gpt-4",
				Input: Input{Items: []InputItem{newAdapterTextItem("Hello, world!")}},
			},
			wantErr: false,
		},
		{
			name: "valid request with items input",
			request: CreateRequest{
				Model: "gpt-4",
				Input: Input{
					Items: []InputItem{
						{
							Role: RoleUser,
							MessageContent: []InputContent{
								NewInputTextContent("Hello, world!"),
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing model",
			request: CreateRequest{
				Input: Input{Items: []InputItem{newAdapterTextItem("Hello")}},
			},
			wantErr: true,
			errMsg:  "`model`: is required",
		},
		{
			name: "invalid temperature",
			request: CreateRequest{
				Model:       "gpt-4",
				Input:       Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				Temperature: ptrFloat64(3.0),
			},
			wantErr: true,
			errMsg:  "`temperature`: must be between 0 and 2",
		},
		{
			name: "invalid top_p",
			request: CreateRequest{
				Model: "gpt-4",
				Input: Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				TopP:  ptrFloat64(1.5),
			},
			wantErr: true,
			errMsg:  "`top_p`: must be between 0 and 1",
		},
		{
			name: "invalid service tier",
			request: CreateRequest{
				Model:       "gpt-4",
				Input:       Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				ServiceTier: ptrString("invalid"),
			},
			wantErr: true,
			errMsg:  "`service_tier`: must be one of: auto, default, flex",
		},
		{
			name: "invalid truncation",
			request: CreateRequest{
				Model:      "gpt-4",
				Input:      Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				Truncation: ptrString("invalid"),
			},
			wantErr: true,
			errMsg:  "`truncation`: must be one of: auto, disabled",
		},
		{
			name: "max_tool_calls below minimum",
			request: CreateRequest{
				Model:        "openai/gpt-oss-20b",
				Input:        Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				MaxToolCalls: ptr.To(0),
			},
			wantErr: true,
			errMsg:  "`max_tool_calls`: must be between 1 and 100",
		},
		{
			name: "max_tool_calls negative",
			request: CreateRequest{
				Model:        "openai/gpt-oss-20b",
				Input:        Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				MaxToolCalls: ptr.To(-1),
			},
			wantErr: true,
			errMsg:  "`max_tool_calls`: must be between 1 and 100",
		},
		{
			name: "max_tool_calls above maximum",
			request: CreateRequest{
				Model:        "openai/gpt-oss-20b",
				Input:        Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				MaxToolCalls: ptr.To(101),
			},
			wantErr: true,
			errMsg:  "`max_tool_calls`: must be between 1 and 100",
		},
		{
			name: "max_tool_calls valid minimum",
			request: CreateRequest{
				Model:        "openai/gpt-oss-20b",
				Input:        Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				MaxToolCalls: ptr.To(1),
			},
			wantErr: false,
		},
		{
			name: "max_tool_calls valid maximum",
			request: CreateRequest{
				Model:        "openai/gpt-oss-20b",
				Input:        Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				MaxToolCalls: ptr.To(100),
			},
			wantErr: false,
		},
		{
			name: "top_logprobs negative",
			request: CreateRequest{
				Model:       "openai/gpt-oss-20b",
				Input:       Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				TopLogprobs: ptr.To(-1),
			},
			wantErr: true,
			errMsg:  "`top_logprobs`: must be between 0 and 20",
		},
		{
			name: "top_logprobs above maximum",
			request: CreateRequest{
				Model:       "openai/gpt-oss-20b",
				Input:       Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				TopLogprobs: ptr.To(21),
			},
			wantErr: true,
			errMsg:  "`top_logprobs`: must be between 0 and 20",
		},
		{
			name: "top_logprobs valid minimum",
			request: CreateRequest{
				Model:       "openai/gpt-oss-20b",
				Input:       Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				TopLogprobs: ptr.To(0),
			},
			wantErr: false,
		},
		{
			name: "top_logprobs valid maximum",
			request: CreateRequest{
				Model:       "openai/gpt-oss-20b",
				Input:       Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				TopLogprobs: ptr.To(20),
			},
			wantErr: false,
		},
		{
			name: "metadata valid with 16 pairs",
			request: CreateRequest{
				Model: "llama-3.3-70b-versatile",
				Input: Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				Metadata: &Metadata{
					"k1":  "v1",
					"k2":  "v2",
					"k3":  "v3",
					"k4":  "v4",
					"k5":  "v5",
					"k6":  "v6",
					"k7":  "v7",
					"k8":  "v8",
					"k9":  "v9",
					"k10": "v10",
					"k11": "v11",
					"k12": "v12",
					"k13": "v13",
					"k14": "v14",
					"k15": "v15",
					"k16": "v16",
				},
			},
			wantErr: false,
		},
		{
			name: "metadata too many pairs",
			request: CreateRequest{
				Model: "llama-3.3-70b-versatile",
				Input: Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				Metadata: &Metadata{
					"k1":  "v1",
					"k2":  "v2",
					"k3":  "v3",
					"k4":  "v4",
					"k5":  "v5",
					"k6":  "v6",
					"k7":  "v7",
					"k8":  "v8",
					"k9":  "v9",
					"k10": "v10",
					"k11": "v11",
					"k12": "v12",
					"k13": "v13",
					"k14": "v14",
					"k15": "v15",
					"k16": "v16",
					"k17": "v17",
				},
			},
			wantErr: true,
			errMsg:  "`metadata`: must have at most 16 key-value pairs",
		},
		{
			name: "metadata key too long",
			request: CreateRequest{
				Model: "llama-3.3-70b-versatile",
				Input: Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				Metadata: &Metadata{
					strings.Repeat("k", 65): "value",
				},
			},
			wantErr: true,
			errMsg:  "exceeds 64 character limit",
		},
		{
			name: "metadata value too long",
			request: CreateRequest{
				Model: "llama-3.3-70b-versatile",
				Input: Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				Metadata: &Metadata{
					"key": strings.Repeat("v", 513),
				},
			},
			wantErr: true,
			errMsg:  "exceeds 512 character limit",
		},
		{
			name: "metadata nil is valid",
			request: CreateRequest{
				Model:    "llama-3.3-70b-versatile",
				Input:    Input{Items: []InputItem{newAdapterTextItem("Hello")}},
				Metadata: nil,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestInput_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected Input
		wantErr  bool
	}{
		{
			name: "string input",
			json: `"Hello, world!"`,
			expected: Input{
				Items: []InputItem{newAdapterTextItem("Hello, world!")},
			},
			wantErr: false,
		},
		{
			name: "array input with message",
			json: `[{"role": "user", "content": "Hello"}]`,
			expected: Input{
				Items: []InputItem{
					{
						Type:           "", // EasyInputMessage has empty Type
						Role:           RoleUser,
						MessageContent: []InputContent{NewInputTextContent("Hello")},
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "invalid input",
			json:    `123`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				input Input
				err   = json.Unmarshal([]byte(tt.json), &input)
			)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, input)
			}
		})
	}
}

func TestInputItem_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected InputItem
		wantErr  bool
	}{
		{
			name: "easy input message",
			json: `{"role": "user", "content": "Hello"}`,
			expected: InputItem{
				Type: "", // EasyInputMessage has empty Type
				Role: RoleUser,
				MessageContent: []InputContent{
					NewInputTextContent("Hello"),
				},
			},
			wantErr: false,
		},
		{
			name: "easy input message with assistant role",
			json: `{"role": "assistant", "content": "I can help with that"}`,
			expected: InputItem{
				Type: "", // EasyInputMessage has empty Type
				Role: RoleAssistant,
				MessageContent: []InputContent{
					NewInputTextContent("I can help with that"),
				},
			},
			wantErr: false,
		},
		{
			name: "item reference",
			json: `{"type": "item_reference", "id": "item-123"}`,
			expected: InputItem{
				Type: ItemTypeReference,
				ID:   "item-123",
			},
			wantErr: false,
		},
		{
			name: "typed message",
			json: `{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}`,
			expected: InputItem{
				Type: ItemTypeMessage,
				Role: RoleUser,
				MessageContent: []InputContent{
					NewInputTextContent("Hello"),
				},
			},
			wantErr: false,
		},
		{
			name: "conversation history with output_text content",
			json: `{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Previous response", "annotations": []}]}`,
			expected: InputItem{
				Type: ItemTypeMessage,
				Role: RoleAssistant,
				MessageContent: []InputContent{
					{
						Type: ContentTypeOutputText,
						Data: &OutputContent{
							Type:        ContentTypeOutputText,
							Text:        "Previous response",
							Annotations: []Annotation{},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "conversation history with refusal content",
			json: `{"type": "message", "role": "assistant", "content": [{"type": "refusal", "refusal": "I cannot help with that"}]}`,
			expected: InputItem{
				Type: ItemTypeMessage,
				Role: RoleAssistant,
				MessageContent: []InputContent{
					{
						Type: ContentTypeRefusal,
						Data: &OutputContent{
							Type:    ContentTypeRefusal,
							Refusal: "I cannot help with that",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "unknown type",
			json:    `{"type": "unknown"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				item InputItem
				err  = json.Unmarshal([]byte(tt.json), &item)
			)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected.Type, item.Type)
				assert.Equal(t, tt.expected.Role, item.Role)
				assert.Equal(t, tt.expected.ID, item.ID)
				if len(tt.expected.MessageContent) > 0 {
					assert.Equal(t, tt.expected.MessageContent, item.MessageContent)
				}
			}
		})
	}
}

func TestOutputItem_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected OutputItem
		wantErr  bool
	}{
		{
			name: "output message",
			json: `{
				"type": "message",
				"id": "msg-123",
				"role": "assistant",
				"status": "completed",
				"content": [
					{
						"type": "output_text",
						"text": "Hello, world!",
						"annotations": []
					}
				]
			}`,
			expected: OutputItem{
				Type:   ItemTypeMessage,
				ID:     "msg-123",
				Role:   RoleAssistant,
				Status: ptrString(StatusCompleted),
				Content: []OutputContent{
					{
						Type:        ContentTypeOutputText,
						Text:        "Hello, world!",
						Annotations: []Annotation{},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "function call",
			json: `{
				"type": "function_call",
				"id": "call-123",
				"call_id": "call_abc",
				"name": "get_weather",
				"arguments": "{\"location\": \"SF\"}",
				"status": "completed"
			}`,
			expected: OutputItem{
				Type:   ItemTypeFunctionCall,
				ID:     "call-123",
				Status: ptrString(StatusCompleted),
			},
			wantErr: false,
		},
		{
			name:    "unknown type",
			json:    `{"type": "unknown", "id": "123"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				item OutputItem
				err  = json.Unmarshal([]byte(tt.json), &item)
			)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected.Type, item.Type)
				assert.Equal(t, tt.expected.ID, item.ID)
				assert.Equal(t, tt.expected.Role, item.Role)
				if len(tt.expected.Content) > 0 {
					assert.Equal(t, tt.expected.Content, item.Content)
				}
			}
		})
	}
}

func TestToolChoice_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected ToolChoice
		wantErr  bool
	}{
		{
			name: "string choice - auto",
			json: `"auto"`,
			expected: ToolChoice{
				Type: ToolChoiceAuto,
			},
			wantErr: false,
		},
		{
			name: "string choice - none",
			json: `"none"`,
			expected: ToolChoice{
				Type: ToolChoiceNone,
			},
			wantErr: false,
		},
		{
			name: "object choice",
			json: `{"type": "function", "name": "get_weather"}`,
			expected: ToolChoice{
				Type: "function",
				Name: "get_weather",
			},
			wantErr: false,
		},
		{
			name:    "invalid string choice",
			json:    `"invalid"`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				choice ToolChoice
				err    = json.Unmarshal([]byte(tt.json), &choice)
			)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, choice)
			}
		})
	}
}

func TestToolChoice_MarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		choice   ToolChoice
		expected string
	}{
		{
			name: "string choice",
			choice: ToolChoice{
				Type: ToolChoiceAuto,
			},
			expected: `"auto"`,
		},
		{
			name: "object choice",
			choice: ToolChoice{
				Type: "function",
				Name: "get_weather",
			},
			expected: `{"type":"function","name":"get_weather"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.choice)
			require.NoError(t, err)
			assert.JSONEq(t, tt.expected, string(data))
		})
	}
}

func TestTool_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tool    Tool
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid function tool",
			tool: &FunctionTool{
				ToolBase: ToolBase{Type: ToolTypeFunction},
				Name:     "get_weather",
			},
			wantErr: false,
		},
		{
			name: "valid code interpreter tool with string container",
			tool: &CodeInterpreterTool{
				ToolBase:  ToolBase{Type: ToolTypeCodeInterpreter},
				Container: "container-123",
			},
			wantErr: false,
		},
		{
			name: "valid code interpreter tool with container object",
			tool: &CodeInterpreterTool{
				ToolBase: ToolBase{Type: ToolTypeCodeInterpreter},
				Container: CodeInterpreterContainer{
					Type:    "auto",
					FileIDs: []string{"file-123", "file-456"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing type",
			tool: &FunctionTool{
				Name: "test",
			},
			wantErr: true,
			errMsg:  "`type`: is required",
		},
		{
			name: "invalid type",
			tool: &FunctionTool{
				ToolBase: ToolBase{Type: "invalid"},
				Name:     "test",
			},
			wantErr: true,
			errMsg:  "must be 'function'",
		},
		{
			name: "function tool missing name",
			tool: &FunctionTool{
				ToolBase: ToolBase{Type: ToolTypeFunction},
			},
			wantErr: true,
			errMsg:  "`name`: is required for function tools",
		},
		{
			name: "code interpreter tool without container",
			tool: &CodeInterpreterTool{
				ToolBase: ToolBase{Type: ToolTypeCodeInterpreter},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tool.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestResponse_GetText(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		expected string
	}{
		{
			name: "response with text content",
			response: Response{
				Output: []OutputItem{
					{
						Type: ItemTypeMessage,
						Role: RoleAssistant,
						Content: []OutputContent{
							{
								Type: ContentTypeOutputText,
								Text: "Hello, world!",
							},
						},
					},
				},
			},
			expected: "Hello, world!",
		},
		{
			name: "response with no text content",
			response: Response{
				Output: []OutputItem{
					{
						Type: ItemTypeFunctionCall,
					},
				},
			},
			expected: "",
		},
		{
			name: "response with refusal content",
			response: Response{
				Output: []OutputItem{
					{
						Type: ItemTypeMessage,
						Role: RoleAssistant,
						Content: []OutputContent{
							{
								Type:    ContentTypeRefusal,
								Refusal: "I cannot help with that",
							},
						},
					},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.response.GetText()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResponse_IsCompleted(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		expected bool
	}{
		{
			name: "completed response",
			response: Response{
				Status: StatusCompleted,
			},
			expected: true,
		},
		{
			name: "in progress response",
			response: Response{
				Status: StatusInProgress,
			},
			expected: false,
		},
		{
			name: "failed response",
			response: Response{
				Status: StatusFailed,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.response.IsCompleted()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResponse_HasError(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		expected bool
	}{
		{
			name: "response with error",
			response: Response{
				Error: &ResponseError{
					Code:    "invalid_request",
					Message: "Invalid request",
				},
			},
			expected: true,
		},
		{
			name:     "response without error",
			response: Response{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.response.HasError()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResponse_ProviderMetadataCompatibility(t *testing.T) {
	metadata := &ProviderResponseMetadata{
		Metrics: &ProviderTimingMetrics{
			PromptTime:     1.2,
			CompletionTime: 2.3,
			TotalTime:      3.5,
		},
	}

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "provider_metadata",
			input: `{"provider_metadata":{"metrics":{"prompt_time":1.2,"completion_time":2.3,"total_time":3.5,"queue_time":null}}}`,
		},
		{
			name:  "legacy groq",
			input: `{"groq":{"metrics":{"prompt_time":1.2,"completion_time":2.3,"total_time":3.5,"queue_time":null}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response Response
			require.NoError(t, json.Unmarshal([]byte(tt.input), &response))
			require.NotNil(t, response.ProviderMetadata)
			require.NotNil(t, response.Groq)
			assert.Equal(t, metadata, response.ProviderMetadata)
			assert.Equal(t, metadata, response.Groq)

			encoded, err := json.Marshal(response)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"provider_metadata"`)
			assert.Contains(t, string(encoded), `"groq"`)
		})
	}

	encoded, err := json.Marshal(Response{ProviderMetadata: metadata})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"provider_metadata"`)
	assert.Contains(t, string(encoded), `"groq"`)
}

func TestInput_IsItems(t *testing.T) {
	tests := []struct {
		name     string
		input    Input
		expected bool
	}{
		{
			name: "items input",
			input: Input{
				Items: []InputItem{{Role: RoleUser}},
			},
			expected: true,
		},
		{
			name:     "empty input",
			input:    Input{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.IsItems()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestOutputItem_GetFunctionToolCall(t *testing.T) {
	functionCall := FunctionToolCall{
		ID:        ptrString("call-123"),
		Type:      ItemTypeFunctionCall,
		CallID:    "call_abc",
		Name:      "get_weather",
		Arguments: `{"location": "SF"}`,
		Status:    ptrString(StatusCompleted),
	}

	tests := []struct {
		name     string
		item     OutputItem
		expected *FunctionToolCall
		found    bool
	}{
		{
			name: "function call item",
			item: OutputItem{
				Type:         ItemTypeFunctionCall,
				ToolCallData: functionCall,
			},
			expected: &functionCall,
			found:    true,
		},
		{
			name: "non-function call item",
			item: OutputItem{
				Type: ItemTypeMessage,
			},
			expected: nil,
			found:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, found := tt.item.GetFunctionToolCall()
			assert.Equal(t, tt.found, found)
			if tt.expected != nil {
				require.NotNil(t, result)
				assert.Equal(t, ptr.Deref(tt.expected), ptr.Deref(result))
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// Helper functions for tests
func ptrString(s string) *string {
	return &s
}

func ptrFloat64(f float64) *float64 {
	return &f
}

// to update test
// go test ./api/adapters/openai_responses -run TestJSONRoundtrip -update
// Test comprehensive JSON roundtrip
func TestJSONRoundtrip(t *testing.T) {
	request := CreateRequest{
		Model: "gpt-4",
		Input: Input{
			Items: []InputItem{
				{
					Role:           RoleUser,
					MessageContent: []InputContent{NewInputTextContent("Hello, world!")},
				},
			},
		},
		Temperature: ptrFloat64(0.7),
		TopP:        ptrFloat64(0.9),
		Tools: ToolSlice{
			&FunctionTool{
				ToolBase: ToolBase{Type: ToolTypeFunction},
				Name:     "get_weather",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
		ToolChoice: &ToolChoice{
			Type: ToolChoiceAuto,
		},
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(request, "", "  ")
	require.NoError(t, err)

	// Compare with golden file
	golden.Assert(
		t,
		string(data),
		"openai_responses__json_roundtrip.golden.json",
	)

	// Unmarshal back to verify roundtrip
	var unmarshaled CreateRequest
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	// Validate the unmarshaled request
	err = unmarshaled.Validate()
	require.NoError(t, err)

	// Basic sanity checks
	assert.Equal(t, request.Model, unmarshaled.Model)
	assert.Equal(t, request.Temperature, unmarshaled.Temperature)
	assert.Equal(t, request.TopP, unmarshaled.TopP)
	assert.Len(t, unmarshaled.Tools, 1)
	funcTool, ok := unmarshaled.Tools[0].(*FunctionTool)
	require.True(t, ok, "Expected FunctionTool type")
	assert.Equal(t, ToolTypeFunction, funcTool.Type)
	assert.Equal(t, "get_weather", funcTool.Name)
}

// Test error cases
func TestUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		json string
		dest any
	}{
		{
			name: "invalid input type",
			json: `{"model": "gpt-4", "input": 123}`,
			dest: &CreateRequest{},
		},
		{
			name: "invalid tool choice",
			json: `"invalid"`,
			dest: &ToolChoice{},
		},
		{
			name: "invalid input item type",
			json: `{"type": "unknown_type"}`,
			dest: &InputItem{},
		},
		{
			name: "invalid output item type",
			json: `{"type": "unknown_type", "id": "123"}`,
			dest: &OutputItem{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := json.Unmarshal([]byte(tt.json), tt.dest)
			assert.Error(t, err)
		})
	}
}
