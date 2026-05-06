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

package prompt_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nvidia-lpu/harmony"
	"github.com/stretchr/testify/require"
	"go.mway.dev/chrono/clock"
	"gotest.tools/v3/golden"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/prompt"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

// to update:
// go test ./templating/prompt --run TestFire_Smoke -update
func TestFire_Smoke(t *testing.T) {
	cases := map[string]func(clock.Clock) *prompt.Fire{
		"raw ffi": prompt.NewFireWithClockRawFFI,
		"uniffi":  prompt.NewFireWithClockUniFFI,
	}
	for name, newFire := range cases {
		t.Run(name, func(t *testing.T) {
			var (
				clk      = clock.NewFakeClock()
				fire     = newFire(clk)
				messages = []models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleSystem,
						Content: models.SingleTextContent("You are a helpful AI assistant."),
					},
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("Marco?"),
					},
				}
				params tools.Params
			)

			// n.b. We need to mock time in order to have a consistent date be rendered
			//      as part of the prompt.
			clk.SetTime(time.Unix(1483938000, 0))

			tokens, n, err := fire.RenderTokens(messages, params)
			require.NoError(t, err)
			require.Positive(t, n)
			require.NotEmpty(t, tokens)

			golden.Assert(t, prompt.HarmonyDecodeTokens(tokens), "gpt-oss-smoke.txt")

			// For sanity, render again with a different clock reading and expect that
			// the result is different than what we had before.
			clk.SetTime(time.Unix(1504242000, 0))

			updated, _, err := fire.RenderTokens(messages, params)
			require.NoError(t, err)
			require.NotEqual(t, tokens, updated)

			// Then change the clock back again, and expect the original tokens.
			clk.SetTime(time.Unix(1483938000, 0))
			updated, _, err = fire.RenderTokens(messages, params)
			require.NoError(t, err)
			require.Equal(t, tokens, updated)
		})
	}
}

// to update:
// go test ./templating/prompt --run TestFire_FunctionCall -update
func TestFire_FunctionCall(t *testing.T) {
	var (
		clk  = clock.NewFakeClock()
		fire = prompt.NewFireWithClock(clk)

		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("You are a helpful AI assistant."),
			},
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.SingleTextContent(
					"What is the weather in Boston and San Francisco?",
				),
			},
		}
		params = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "function",
					Function: tools.ChatFunctionSpec{
						Name: "get_current_weather",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"location": map[string]any{
									"type":        "string",
									"description": "The city and state, e.g. San Francisco, CA",
								},
								"unit": map[string]any{
									"type": "string",
									"enum": []string{"celsius", "fahrenheit"},
								},
							},
							"required": []string{"location"},
						},
						Description: ptr.To("Get the current weather in a given location"),
					},
				},
			},
		}
	)

	clk.SetTime(time.Date(2025, 8, 3, 0, 0, 0, 0, time.UTC))

	tokens, _, err := fire.RenderTokens(messages, params)
	require.NoError(t, err)
	require.NotEmpty(t, tokens)

	golden.Assert(t, prompt.HarmonyDecodeTokens(tokens), "gpt-oss-function-call.txt")
}

// to update:
// go test ./templating/prompt --run TestFire_Browser -update
func TestFire_Browser(t *testing.T) {
	var (
		clk  = clock.NewFakeClock()
		fire = prompt.NewFireWithClock(clk)

		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("You are a helpful AI assistant."),
			},
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.SingleTextContent(
					"What is the weather in Boston and San Francisco?",
				),
			},
		}
		params = tools.Params{
			Tools: []tools.Tool{{Type: models.ToolTypeBrowserSearch}},
		}
	)

	clk.SetTime(time.Date(2025, 8, 3, 0, 0, 0, 0, time.UTC))

	tokens, _, err := fire.RenderTokens(messages, params)
	require.NoError(t, err)
	require.NotEmpty(t, tokens)

	golden.Assert(t, prompt.HarmonyDecodeTokens(tokens), "gpt-oss-browser.txt")
}

// to update:
// go test ./templating/prompt --run TestFire_Python -update
func TestFire_Python(t *testing.T) {
	var (
		clk  = clock.NewFakeClock()
		fire = prompt.NewFireWithClock(clk)

		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("You are a helpful AI assistant."),
			},
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.SingleTextContent(
					"What is the weather in Boston and San Francisco?",
				),
			},
		}
		params = tools.Params{
			Tools: []tools.Tool{{Type: "code_interpreter"}},
		}
	)

	clk.SetTime(time.Date(2025, 8, 3, 0, 0, 0, 0, time.UTC))

	tokens, _, err := fire.RenderTokens(messages, params)
	require.NoError(t, err)
	require.NotEmpty(t, tokens)

	golden.Assert(t, prompt.HarmonyDecodeTokens(tokens), "gpt-oss-python.txt")
}

// to update:
// go test ./templating/prompt --run TestFire_AssistantReasoningToolCall -update
func TestFire_AssistantReasoningToolCall(t *testing.T) {
	var (
		clk  = clock.NewFakeClock()
		fire = prompt.NewFireWithClock(clk)

		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("You are a helpful AI assistant."),
			},
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.SingleTextContent(
					"What is the weather in Boston and San Francisco?",
				),
			},
			{
				Role:      models.ChatCompletionRoleAssistant,
				Channel:   harmony.ChannelAnalysis,
				Reasoning: ptr.To("I am reasoning"),
			},
			{
				Role:      models.ChatCompletionRoleAssistant,
				Channel:   harmony.ChannelCommentary,
				Reasoning: ptr.To("Using tool to find the weather in Boston and San Francisco."),
			},
			{
				Role:    models.ChatCompletionRoleAssistant,
				Channel: harmony.ChannelCommentary,
				ToolCalls: &[]models.ChatToolCall{
					{
						Type: models.ToolTypeFunction,
						Function: models.ChatToolCallFunction{
							Name:      "functions.get_current_weather",
							Arguments: `{"location":"Boston"}`,
						},
					},
				},
			},
			{
				Role:    models.ChatCompletionRoleTool,
				Name:    ptr.To("get_current_weather"),
				Content: models.SingleTextContent(`{"weather":"sunny"}`),
			},
			{
				Role:      models.ChatCompletionRoleAssistant,
				Channel:   harmony.ChannelFinal,
				Reasoning: ptr.To("the weather in Boston is sunny. telling the user."),
				Content:   models.SingleTextContent("The weather in Boston is sunny."),
			},
		}
		params = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "function",
					Function: tools.ChatFunctionSpec{
						Name: "get_current_weather",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"location": map[string]any{
									"type":        "string",
									"description": "The city and state, e.g. San Francisco, CA",
								},
								"unit": map[string]any{
									"type": "string",
									"enum": []string{"celsius", "fahrenheit"},
								},
							},
							"required": []string{"location"},
						},
						Description: ptr.To("Get the current weather in a given location"),
					},
				},
			},
		}
	)

	clk.SetTime(time.Date(2025, 8, 3, 0, 0, 0, 0, time.UTC))

	tokens, _, err := fire.RenderTokens(messages, params)
	require.NoError(t, err)
	require.NotEmpty(t, tokens)

	golden.Assert(
		t,
		prompt.HarmonyDecodeTokens(tokens),
		"gpt-oss-assistant-reasoning-tool-call.txt",
	)
}

func TestFire_InvalidUTF8(t *testing.T) {
	var (
		fire        = prompt.NewFire()
		invalidUTF8 = string([]byte{
			0x48,
			0x65,
			0x6c,
			0x6c,
			0x6f,
			0xff,
			0x20,
			0x77,
			0x6f,
			0x72,
			0x6c,
			0x64,
		})
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("OK"),
			},
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent(invalidUTF8),
			},
		}
		params tools.Params
	)

	_, _, err := fire.RenderTokens(messages, params)
	require.ErrorIs(t, err, prompt.ErrInvalidUTF8)
	require.ErrorContains(t, err, "message 2")
}

func TestFire_IsInvalidUTF8(t *testing.T) {
	cases := map[string]struct {
		give string
		want bool
	}{
		"nominal variant a": {
			give: "Failed to convert arg 'messages': incomplete utf-8",
			want: true,
		},
		"prefixed variant a": {
			give: "foobar Failed to convert arg 'messages': incomplete utf-8",
			want: true,
		},
		"suffixed variant a": {
			give: "Failed to convert arg 'messages': incomplete utf-8 foobar",
			want: true,
		},
		"nominal variant b": {
			give: "Failed to convert arg 'messages': invalid utf-8",
			want: true,
		},
		"prefixed variant b": {
			give: "foobar Failed to convert arg 'messages': invalid utf-8",
			want: true,
		},
		"suffixed variant b": {
			give: "Failed to convert arg 'messages': invalid utf-8 foobar",
			want: true,
		},
		"partial": {
			give: "Failed to convert arg 'messages'",
			want: false,
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			err := errors.New(tt.give)
			require.Equal(t, tt.want, prompt.IsInvalidUTF8Error(err))
		})
	}
}

func TestFire_LongRunning(t *testing.T) {
	t.Skip("skipping debugging test")

	var (
		fire     = prompt.NewFire()
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("You are a helpful AI assistant."),
			},
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.SingleTextContent(
					"What is the weather in Boston and San Francisco?",
				),
			},
			{
				Role:      models.ChatCompletionRoleAssistant,
				Channel:   harmony.ChannelAnalysis,
				Reasoning: ptr.To("I am reasoning"),
			},
			{
				Role:      models.ChatCompletionRoleAssistant,
				Channel:   harmony.ChannelCommentary,
				Reasoning: ptr.To("Using tool to find the weather in Boston and San Francisco."),
			},
			{
				Role:    models.ChatCompletionRoleAssistant,
				Channel: harmony.ChannelCommentary,
				ToolCalls: &[]models.ChatToolCall{
					{
						Type: models.ToolTypeFunction,
						Function: models.ChatToolCallFunction{
							Name:      "functions.get_current_weather",
							Arguments: `{"location":"Boston"}`,
						},
					},
				},
			},
			{
				Role:    models.ChatCompletionRoleTool,
				Name:    ptr.To("get_current_weather"),
				Content: models.SingleTextContent("The weather in Boston is sunny."),
			},
		}
		params = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "function",
					Function: tools.ChatFunctionSpec{
						Name: "get_current_weather",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"location": map[string]any{
									"type":        "string",
									"description": "The city and state, e.g. San Francisco, CA",
								},
								"unit": map[string]any{
									"type": "string",
									"enum": []string{"celsius", "fahrenheit"},
								},
							},
							"required": []string{"location"},
						},
						Description: ptr.To("Get the current weather in a given location"),
					},
				},
			},
		}
	)

	t.Log("starting")

	// go func() {
	// 	log.Println(http.ListenAndServe("localhost:6060", nil))
	// }()

	ticker := time.NewTicker(100 * time.Microsecond)
	defer ticker.Stop()
	for {
		<-ticker.C

		tokens, _, err := fire.RenderTokens(messages, params)
		require.NoError(t, err)
		require.NotEmpty(t, tokens)
		_ = prompt.HarmonyDecodeTokens(tokens)
	}
}
