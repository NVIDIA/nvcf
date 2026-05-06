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
	"fmt"
	"regexp"
	"strings"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
)

// MinimaxM2ParseConfig provides parsing for Minimax-M2's tool call format.
type MinimaxM2ParseConfig struct{}

func NewMinimaxM2ParseConfig() ParseConfig { //nolint:ireturn
	return &MinimaxM2ParseConfig{}
}

const (
	_minimaxToolSectionBeginMarker = "<minimax:tool_call>"
	_minimaxToolSectionEndMarker   = "</minimax:tool_call>"
)

var (
	_minimaxToolCallPattern = regexp.MustCompile(
		`(?s)<minimax:tool_call>(.*?)</minimax:tool_call>`,
	)
	_minimaxInvokePattern    = regexp.MustCompile(`(?s)<invoke name="([^"]+)">(.*?)</invoke>`)
	_minimaxParameterPattern = regexp.MustCompile(
		`(?s)<parameter name="([^"]+)">(.*?)</parameter>`,
	)
)

var _ ParseConfig = (*MinimaxM2ParseConfig)(nil)

// ParseTools parses Minimax-M2 tool call format:
// <minimax:tool_call>
// <invoke name="function_name">
// <parameter name="param_name">param_value</parameter>
// </invoke>
// </minimax:tool_call>
func (m *MinimaxM2ParseConfig) ParseTools(
	params Params,
	message string,
) (string, []Call, error) {
	remaining := message

	// Find all tool call blocks
	toolCallMatches := _minimaxToolCallPattern.FindAllStringSubmatchIndex(remaining, -1)

	if len(toolCallMatches) == 0 {
		return message, nil, nil
	}

	// Pre-allocate calls slice based on number of matches
	calls := make([]Call, 0, len(toolCallMatches))

	// Preceding text is only the text before the first tool call
	precedingText := strings.TrimSpace(remaining[:toolCallMatches[0][0]])

	if !params.ParallelToolCalls {
		toolCallMatches = toolCallMatches[:1]
	}

	// Parse each tool call block using indexes
	for i, indexes := range toolCallMatches {
		if len(indexes) < 4 {
			if i == 0 {
				return "", nil, fmt.Errorf("%w: malformed tool call block", ErrToolParseFailed)
			}
			break
		}

		// Extract content between <minimax:tool_call> and </minimax:tool_call> using indexes
		// indexes[2] and indexes[3] are the start/end of the first capture group
		content := strings.TrimSpace(remaining[indexes[2]:indexes[3]])

		// Find all invoke blocks within this tool call
		invokeMatches := _minimaxInvokePattern.FindAllStringSubmatch(content, -1)

		if len(invokeMatches) == 0 {
			if i == 0 {
				return "", nil, fmt.Errorf("%w: no invoke found in tool call", ErrToolParseFailed)
			}
			break
		}

		// Process each invoke (usually just one per tool_call block)
		for j, invokeMatch := range invokeMatches {
			if len(invokeMatch) < 3 {
				if i == 0 && j == 0 {
					return "", nil, fmt.Errorf("%w: malformed invoke block", ErrToolParseFailed)
				}
				break
			}

			funcName := strings.TrimSpace(invokeMatch[1])
			if funcName == "" {
				if i == 0 && j == 0 {
					return "", nil, fmt.Errorf("%w: empty function name", ErrToolParseFailed)
				}
				break
			}

			invokeContent := invokeMatch[2]

			// Parse parameters using pre-compiled regex
			paramMatches := _minimaxParameterPattern.FindAllStringSubmatch(invokeContent, -1)

			arguments := make(map[string]any)
			for _, paramMatch := range paramMatches {
				if len(paramMatch) >= 3 {
					key := strings.TrimSpace(paramMatch[1])
					value := paramMatch[2]

					// Remove leading and trailing newlines
					value = strings.TrimPrefix(value, "\n")
					value = strings.TrimSuffix(value, "\n")

					// Try to parse as JSON first, fall back to string
					var parsedValue any
					if err := json.UnmarshalString(value, &parsedValue); err != nil {
						// Not valid JSON, use as string
						parsedValue = value
					}
					arguments[key] = parsedValue
				}
			}

			call := Call{
				Name:      funcName,
				Arguments: arguments,
				ID:        m.GenerateToolCallID(),
			}
			calls = append(calls, call)
		}
	}

	return precedingText, calls, nil
}

func (*MinimaxM2ParseConfig) GenerateToolCallID() string {
	return NewShortCallID()
}

func (*MinimaxM2ParseConfig) ToolUseBeginMarker() string {
	return _minimaxToolSectionBeginMarker
}

func (*MinimaxM2ParseConfig) ToolUseEndMarker() string {
	return _minimaxToolSectionEndMarker
}

func (m *MinimaxM2ParseConfig) CanBeToolCall(value string) bool {
	if len(m.ToolUseBeginMarker()) > len(value) {
		return strings.HasPrefix(m.ToolUseBeginMarker(), value)
	}
	return strings.HasPrefix(value, m.ToolUseBeginMarker())
}
