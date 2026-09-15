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

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/templating/testhelpers"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/templating/tools"
)

func TestParseTools(t *testing.T) {
	t.Parallel()

	const toolCall1Args = `{"location":"San Francisco"}`
	const toolCall1String = `{"id": "pending", "type": "function", "function": {"name": "weather_info"}, "parameters": ` + toolCall1Args + `}`
	const toolCall1Simple = `{"name": "weather_info", "arguments": ` + toolCall1Args + `}`
	const toolCall2Args = `{"symbol":"AAPL"}`
	const toolCall2String = `{"id": "unique-id-123", "type": "function", "function": {"name": "stock_prices"}, "parameters": ` + toolCall2Args + `}`
	const toolCall2Simple = `{"name": "stock_prices", "arguments": ` + toolCall2Args + `}`
	knownTools := []tools.Tool{
		testhelpers.NewWeatherInfoTool(),
		testhelpers.NewStockPricesTool(),
	}

	var (
		toolCall1 = tools.Call{
			Name:      "weather_info",
			Arguments: map[string]any{"location": "San Francisco"},
		}
		toolCall2 = tools.Call{
			Name:      "stock_prices",
			Arguments: map[string]any{"symbol": "AAPL"},
		}
	)

	t.Run("group", func(t *testing.T) {
		for _, testCase := range []struct {
			name                  string
			message               string
			toolsInfo             tools.Params
			parseConfig           tools.ParseConfig
			expectedToolCalls     []tools.Call
			expectedPrecedingText string
			expectedError         string
		}{
			{
				name:                  "no tools or functions",
				toolsInfo:             tools.Params{Tools: knownTools},
				parseConfig:           tools.NewDefaultParseConfig(),
				message:               "Hello, world!",
				expectedPrecedingText: "Hello, world!",
				expectedToolCalls:     nil,
			},
			{
				name:                  "single tool call no parse config",
				message:               `preamble<tool-use>{"tool_call": ` + toolCall1String + `}</tool-use> postamble`,
				parseConfig:           tools.NewDefaultParseConfig(),
				toolsInfo:             tools.Params{Tools: knownTools},
				expectedToolCalls:     []tools.Call{toolCall1},
				expectedPrecedingText: "preamble",
			},
			{
				name:        "parallel tool calls without parse config",
				message:     `preamble<tool-use>{"tool_calls": [` + toolCall1String + "," + toolCall2String + `]}</tool-use> postamble`,
				parseConfig: tools.NewDefaultParseConfig(),
				toolsInfo: tools.Params{
					Tools:             knownTools,
					ParallelToolCalls: true,
				},
				expectedToolCalls:     []tools.Call{toolCall1, toolCall2},
				expectedPrecedingText: "preamble",
			},
			{
				name:        "parallel tool call without end marker and braces in surrounding text",
				message:     `preamble { <tool-use>{"tool_calls": [` + toolCall1String + "," + toolCall2String + `]} This is not a tool call postamble } junk`,
				parseConfig: tools.NewDefaultParseConfig(),
				toolsInfo: tools.Params{
					Tools:             knownTools,
					ParallelToolCalls: true,
				},
				expectedToolCalls:     []tools.Call{toolCall1, toolCall2},
				expectedPrecedingText: "preamble { ",
			},
			{
				name:        "parallel tool calls in different xml tags junk in between",
				message:     `preamble <tool_call>` + toolCall1Simple + `</tool_call> filler that isn't a tool <tool_call>` + toolCall2Simple + `</tool-call> postamble`,
				parseConfig: tools.NewDeepSeekParseConfig(),
				toolsInfo: tools.Params{
					Tools:             knownTools,
					ParallelToolCalls: true,
				},
				expectedToolCalls:     []tools.Call{toolCall1, toolCall2},
				expectedPrecedingText: "preamble ",
			},
			{
				name:                  "mistral no tools",
				message:               "Hello, world!",
				toolsInfo:             tools.Params{Tools: knownTools},
				parseConfig:           tools.NewMistralParseConfig(),
				expectedToolCalls:     nil,
				expectedPrecedingText: "Hello, world!",
			},
			{
				name:                  "mistral one tool",
				message:               ` not json [{"name": "weather_info", "arguments": {"location": "San Francisco"}}] junk`,
				toolsInfo:             tools.Params{Tools: knownTools},
				parseConfig:           tools.NewMistralParseConfig(),
				expectedToolCalls:     []tools.Call{toolCall1},
				expectedPrecedingText: " not json ",
			},
			{
				name:                  "mistral two tools, not parallel",
				message:               ` [{"name": "weather_info", "arguments": {"location": "San Francisco"}},{"name": "stock_prices", "arguments": {"symbol": "AAPL"}}]`,
				toolsInfo:             tools.Params{Tools: knownTools},
				parseConfig:           tools.NewMistralParseConfig(),
				expectedToolCalls:     []tools.Call{toolCall1},
				expectedPrecedingText: " ",
			},
			{
				name:    "mistral two tools",
				message: ` [{"name": "weather_info", "arguments": {"location": "San Francisco"}},{"name": "stock_prices", "arguments": {"symbol": "AAPL"}}]`,
				toolsInfo: tools.Params{
					Tools:             knownTools,
					ParallelToolCalls: true,
				},
				parseConfig:           tools.NewMistralParseConfig(),
				expectedToolCalls:     []tools.Call{toolCall1, toolCall2},
				expectedPrecedingText: " ",
			},
			{
				name:                  "mistral with EOF",
				message:               `[{"name": "weather_info", "arguments": {"location": ["San Francisco"]`,
				toolsInfo:             tools.Params{Tools: knownTools},
				parseConfig:           tools.NewMistralParseConfig(),
				expectedError:         "need more data",
				expectedPrecedingText: "",
				expectedToolCalls:     nil,
			},
			{
				name:                  "llama3.1 no tools",
				message:               "hello, world",
				toolsInfo:             tools.Params{Tools: knownTools},
				parseConfig:           &tools.NativeParseConfig{},
				expectedToolCalls:     nil,
				expectedPrecedingText: "hello, world",
			},
			{
				name:                  "llama3.1 one tool",
				message:               "hello, world <function=weather_info>{\"location\": \"San Francisco\"}</function>",
				toolsInfo:             tools.Params{Tools: knownTools},
				parseConfig:           &tools.NativeParseConfig{},
				expectedToolCalls:     []tools.Call{toolCall1},
				expectedPrecedingText: "hello, world ",
			},
			{
				name:    "llama3.1 two tools, not parallel",
				message: "hello, world <function=weather_info>{\"location\": \"San Francisco\"}</function> filler junk<function=stock_prices>{\"symbol\": \"AAPL\"}</function> post",
				toolsInfo: tools.Params{
					Tools:             knownTools,
					ParallelToolCalls: false,
				},
				parseConfig:           &tools.NativeParseConfig{},
				expectedToolCalls:     []tools.Call{toolCall1},
				expectedPrecedingText: "hello, world ",
			},
			{
				name:    "llama3.1 two tools, parallel",
				message: "hello, world <function=weather_info>{\"location\": \"San Francisco\"}</function> filler junk<function=stock_prices>{\"symbol\": \"AAPL\"}</function> post",
				toolsInfo: tools.Params{
					Tools:             knownTools,
					ParallelToolCalls: true,
				},
				parseConfig:           &tools.NativeParseConfig{},
				expectedToolCalls:     []tools.Call{toolCall1, toolCall2},
				expectedPrecedingText: "hello, world ",
			},
			{
				name:              "llama3.1 slightly off",
				message:           "<function=weather_info>{\"location\": \"San Francisco\"}, </function>",
				toolsInfo:         tools.Params{Tools: knownTools},
				parseConfig:       &tools.NativeParseConfig{},
				expectedToolCalls: []tools.Call{toolCall1},
			},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				precedingText, toolCalls, err := testCase.parseConfig.ParseTools(
					testCase.toolsInfo,
					testCase.message,
				)
				if testCase.expectedError != "" {
					require.Error(t, err)
					require.ErrorContains(t, err, testCase.expectedError)
					assert.Empty(t, precedingText)
				} else {
					require.NoError(t, err)
					assert.Equal(t, testCase.expectedToolCalls, toolCalls)
					assert.Equal(t, testCase.expectedPrecedingText, precedingText)
				}
			})
		}
	})
}
