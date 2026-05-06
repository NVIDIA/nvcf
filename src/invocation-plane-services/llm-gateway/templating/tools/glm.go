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

// GLMParseConfig provides parsing for GLM-4.6's tool call format.
type GLMParseConfig struct{}

func NewGLMParseConfig() ParseConfig { //nolint:ireturn
	return &GLMParseConfig{}
}

const (
	_glmToolSectionBeginMarker = "<tool_call>"
	_glmToolSectionEndMarker   = "</tool_call>"
)

var (
	_glmToolCallPattern = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`)
	_glmArgPattern      = regexp.MustCompile(
		`(?s)<arg_key>(.*?)</arg_key>\s*<arg_value>(.*?)</arg_value>`,
	)
)

var _ ParseConfig = (*GLMParseConfig)(nil)

// ParseTools parses GLM-4.6 tool call format:
// <tool_call>function_name
// <arg_key>key</arg_key>
// <arg_value>value</arg_value>
// </tool_call>
// https://github.com/zai-org/GLM-4.5/blob/main/resources/glm_4.6_tir_guide.md#manual-parsing-of-reasoning-and-tool-calls
func (g *GLMParseConfig) ParseTools(
	params Params,
	message string,
) (string, []Call, error) {
	remaining := message

	// Find all tool call blocks
	matchIndexes := _glmToolCallPattern.FindAllStringSubmatchIndex(remaining, -1)

	if len(matchIndexes) == 0 {
		return message, nil, nil
	}

	// Pre-allocate calls slice based on number of matches
	calls := make([]Call, 0, len(matchIndexes))

	// Preceding text is only the text before the first tool call
	precedingText := strings.TrimSpace(remaining[:matchIndexes[0][0]])
	if !params.ParallelToolCalls {
		matchIndexes = matchIndexes[:1]
	}

	// Parse each tool call block using indexes
	for i, indexes := range matchIndexes {
		if len(indexes) < 4 {
			if i == 0 {
				return "", nil, fmt.Errorf("%w: malformed tool call block", ErrToolParseFailed)
			}
			break
		}

		// Extract content between <tool_call> and </tool_call> using indexes
		// indexes[2] and indexes[3] are the start/end of the first capture group
		content := strings.TrimSpace(remaining[indexes[2]:indexes[3]])

		// Extract function name from first line
		lines := strings.Split(content, "\n")
		if len(lines) == 0 {
			if i == 0 {
				return "", nil, fmt.Errorf("%w: empty tool call content", ErrToolParseFailed)
			}
			break
		}

		funcName := strings.TrimSpace(lines[0])
		if funcName == "" || strings.Contains(funcName, "<arg_key>") {
			if i == 0 {
				return "", nil, fmt.Errorf("%w: empty or invalid function name", ErrToolParseFailed)
			}
			break
		}

		// Parse arguments using pre-compiled regex
		argMatches := _glmArgPattern.FindAllStringSubmatch(content, -1)

		arguments := make(map[string]any)
		for _, argMatch := range argMatches {
			if len(argMatch) >= 3 {
				key := strings.TrimSpace(argMatch[1])
				value := strings.TrimSpace(argMatch[2])

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
			ID:        g.GenerateToolCallID(),
		}
		calls = append(calls, call)
	}

	return precedingText, calls, nil
}

func (*GLMParseConfig) GenerateToolCallID() string {
	return NewShortCallID()
}

func (*GLMParseConfig) ToolUseBeginMarker() string {
	return _glmToolSectionBeginMarker
}

func (*GLMParseConfig) ToolUseEndMarker() string {
	return _glmToolSectionEndMarker
}

func (g *GLMParseConfig) CanBeToolCall(value string) bool {
	if len(g.ToolUseBeginMarker()) > len(value) {
		return strings.HasPrefix(g.ToolUseBeginMarker(), value)
	}
	return strings.HasPrefix(value, g.ToolUseBeginMarker())
}
