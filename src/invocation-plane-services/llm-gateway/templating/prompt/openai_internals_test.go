//go:build harmony

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

package prompt

import (
	"testing"

	"github.com/nvidia-lpu/harmony"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

func TestOrionToHarmonyMessages(t *testing.T) {
	cases := map[string]struct {
		msg  models.ChatMessage
		want []harmony.Message
	}{
		"single user": {
			msg: models.ChatMessage{
				Role:    harmony.RoleUser.String(),
				Content: models.SingleTextContent("hi"),
				Channel: "foo",
			},
			want: []harmony.Message{{
				Author: harmony.Author{
					Role: harmony.RoleUser,
				},
				Channel: "foo",
				Content: harmony.MultiContent{harmony.TextContent{
					Text: "hi",
				}},
			}},
		},
		"single system as developer": {
			msg: models.ChatMessage{
				Role:    harmony.RoleSystem.String(),
				Content: models.SingleTextContent("hi"),
				Channel: "does not matter developer messages have no channel",
			},
			want: []harmony.Message{{
				Author: harmony.Author{
					Role: harmony.RoleDeveloper,
				},
				Channel: "", // empty
				Content: harmony.MultiContent{harmony.DeveloperContent{
					Instructions: "hi",
				}},
			}},
		},
		"single developer": {
			msg: models.ChatMessage{
				Role:    harmony.RoleDeveloper.String(),
				Content: models.SingleTextContent("hi"),
				Channel: "does not matter developer messages have no channel",
			},
			want: []harmony.Message{{
				Author: harmony.Author{
					Role: harmony.RoleDeveloper,
				},
				Channel: "", // empty
				Content: harmony.MultiContent{harmony.DeveloperContent{
					Instructions: "hi",
				}},
			}},
		},
		"final response with reasoning": {
			msg: models.ChatMessage{
				Role:      harmony.RoleAssistant.String(),
				Content:   models.SingleTextContent("hi"),
				Channel:   harmony.ChannelFinal,
				Reasoning: ptr.To("hmmm"),
			},
			want: []harmony.Message{
				func() harmony.Message {
					msg := harmony.NewAssistantMessage("hmmm")
					msg.Channel = harmony.ChannelAnalysis
					return msg
				}(),
				func() harmony.Message {
					msg := harmony.NewAssistantMessage("hi")
					msg.Channel = harmony.ChannelFinal
					return msg
				}(),
			},
		},
		"assistant tool calls": {
			msg: models.ChatMessage{
				Role:      harmony.RoleAssistant.String(),
				Content:   models.SingleTextContent("hi"),
				Channel:   harmony.ChannelFinal,
				Reasoning: ptr.To("hmmm"),
				ToolCalls: ptr.To([]models.ChatToolCall{
					{
						ID:   "1",
						Type: "function",
						Function: models.ChatToolCallFunction{
							Name:      "function.foo",
							Arguments: `["bar","baz"]`,
						},
					},
					{
						ID:   "2",
						Type: "function",
						Function: models.ChatToolCallFunction{
							Name:      "function.bar",
							Arguments: `["baz","bat"]`,
						},
					},
				}),
			},
			want: []harmony.Message{
				func() harmony.Message {
					msg := harmony.NewAssistantMessage("hmmm")
					msg.Channel = harmony.ChannelAnalysis
					return msg
				}(),
				func() harmony.Message {
					msg := harmony.NewAssistantMessage(`["bar","baz"]`)
					msg.Recipient = "function.foo"
					msg.Channel = harmony.ChannelCommentary
					return msg
				}(),
				func() harmony.Message {
					msg := harmony.NewAssistantMessage(`["baz","bat"]`)
					msg.Recipient = "function.bar"
					msg.Channel = harmony.ChannelCommentary
					return msg
				}(),
			},
		},
		// assisstant tool calls + reasoning
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			msgs, err := orionToHarmonyMessages(
				tt.msg,
				nil, // no previous messages for these tests
				nil,
				false,
			)
			require.NoError(t, err)
			require.Equal(t, tt.want, msgs)
		})
	}
}

func TestOrionToHarmonyMessagesWithPreviousFunctionCall(t *testing.T) {
	// Previous message with a tool call
	prevMsg := models.ChatMessage{
		Role: models.ChatCompletionRoleAssistant,
		ToolCalls: &[]models.ChatToolCall{
			{
				ID:   "test-call-id",
				Type: models.ToolTypeFunction,
				Function: models.ChatToolCallFunction{
					Name:      "test_function",
					Arguments: `{"arg": "value"}`,
				},
			},
		},
	}

	// Tool message with empty name
	toolMsg := models.ChatMessage{
		Role:    models.ChatCompletionRoleTool,
		Content: models.SingleTextContent("Function result"),
		Channel: "analysis",
		Name:    nil, // Empty name should be filled from previous call
	}

	msgs, err := orionToHarmonyMessages(
		toolMsg,
		[]models.ChatMessage{prevMsg}, // Previous messages
		nil,                           // No user tools
		false,                         // Don't include tools
	)

	require.NoError(t, err)
	require.Len(t, msgs, 1)

	// Check that the tool name was automatically set from the previous function call
	expectedName := FixHarmonyRequestToolName("test_function")
	require.Equal(t, expectedName, msgs[0].Author.Name)
	require.Equal(t, harmony.RoleTool, msgs[0].Author.Role)
	require.Equal(t, "assistant", msgs[0].Recipient)
}

func TestFindPreviousFunctionCallName(t *testing.T) {
	t.Run("finds tool call name", func(t *testing.T) {
		msgs := []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("Call a function"),
			},
			{
				Role: models.ChatCompletionRoleAssistant,
				ToolCalls: &[]models.ChatToolCall{
					{
						Function: models.ChatToolCallFunction{
							Name: "my_function",
						},
					},
				},
			},
		}

		name := findPreviousFunctionCallName(msgs)
		require.Equal(t, "my_function", name)
	})

	t.Run("finds function call name", func(t *testing.T) {
		msgs := []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("Call a function"),
			},
			{
				Role: models.ChatCompletionRoleAssistant,
				FunctionCall: &models.ChatCompletionFunctionCall{
					Name: "legacy_function",
				},
			},
		}

		name := findPreviousFunctionCallName(msgs)
		require.Equal(t, "legacy_function", name)
	})

	t.Run("returns empty when no function calls found", func(t *testing.T) {
		msgs := []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("No functions here"),
			},
		}

		name := findPreviousFunctionCallName(msgs)
		require.Empty(t, name)
	})

	t.Run("finds most recent function call", func(t *testing.T) {
		msgs := []models.ChatMessage{
			{
				Role: models.ChatCompletionRoleAssistant,
				FunctionCall: &models.ChatCompletionFunctionCall{
					Name: "old_function",
				},
			},
			{
				Role: models.ChatCompletionRoleAssistant,
				ToolCalls: &[]models.ChatToolCall{
					{
						Function: models.ChatToolCallFunction{
							Name: "recent_function",
						},
					},
				},
			},
		}

		name := findPreviousFunctionCallName(msgs)
		require.Equal(t, "recent_function", name)
	})
}
