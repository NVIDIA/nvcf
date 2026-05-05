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
	"strconv"
	"strings"
	"unsafe"

	"github.com/kaptinlin/jsonrepair"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/container/set"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

// docs: https://huggingface.co/moonshotai/Kimi-K2-Instruct/blob/main/docs/tool_call_guidance.md#manually-parsing-tool-calls
type KimiParseConfig struct{}

func NewKimiParseConfig() ParseConfig {
	return &KimiParseConfig{}
}

const (
	_kimiToolSectionBeginMarker = "<|tool_calls_section_begin|>"
	_kimiToolSectionEndMarker   = "<|tool_calls_section_end|>"
)

var (
	_kimiToolSectionPattern = regexp.MustCompile(
		strings.ReplaceAll(_kimiToolSectionBeginMarker, "|", "\\|") +
			"(.*?)" +
			strings.ReplaceAll(_kimiToolSectionEndMarker, "|", "\\|"),
	)
	_kimiToolPattern = regexp.MustCompile(
		`<\|tool_call_begin\|>\s*([^\s]+?:\d+)\s*<\|tool_call_argument_begin\|>\s*(.*?)\s*<\|tool_call_end\|>`,
	)
)

var _ ParseConfig = (*KimiParseConfig)(nil)

func (c *KimiParseConfig) CanBeToolCall(value string) bool {
	if len(c.ToolUseBeginMarker()) > len(value) {
		return strings.HasPrefix(c.ToolUseBeginMarker(), value)
	}
	return strings.HasPrefix(value, c.ToolUseBeginMarker())
}

func (c *KimiParseConfig) GenerateToolCallID() string {
	// TODO: IDs should monotonically incrementing
	// but we don't have access to past IDs to do this.
	return NewMistralToolCallID()
}

func (c *KimiParseConfig) ToolUseBeginMarker() string {
	return _kimiToolSectionBeginMarker
}

func (c *KimiParseConfig) ToolUseEndMarker() string {
	return _kimiToolSectionEndMarker
}

// Kimi uses a specific tool call ID format: `functions.<function_name>:<function_call_index>`.
// The tool call ID is the only place where the function name is output, so if Kimi
// does not follow this format, we cannot determine which function was called.
//
// If the request contains tool calls in a different format, Kimi may try to copy
// the invalid format. To prevent this, we replace invalid ID in the request with IDs
// in the correct Kimi format.
//
// This function rewrites tool call IDs as necessary to enforce the expected format.
// This function modifies the input messages in place.
func ConvertToolIDsToKimiID(messages []models.ChatMessage) {
	var (
		toolIDs             = set.New[string]()
		invalidToolCalls    []*models.ChatToolCall
		invalidToolMessages []*models.ChatMessage
	)

	for i := range messages {
		message := &messages[i]
		switch message.Role {
		case models.ChatCompletionRoleAssistant:
			if message.ToolCalls != nil {
				for j := range *message.ToolCalls {
					tool := &(*message.ToolCalls)[j]
					if _, id, err := parseKimiNameAndID(tool.ID); err != nil {
						invalidToolCalls = append(invalidToolCalls, tool)
					} else {
						toolIDs.Insert(id)
					}
				}
			}
		case models.ChatCompletionRoleTool:
			if _, id, err := parseKimiNameAndID(ptr.Deref(message.ToolCallID)); err != nil {
				invalidToolMessages = append(invalidToolMessages, message)
			} else {
				toolIDs.Insert(id)
			}
		}
	}

	var (
		toolCallID int
		toolMap    = make(map[string]string, len(invalidToolCalls))
		newToolID  = func(functionName string) string {
			id := strconv.Itoa(toolCallID)
			for !toolIDs.Insert(id) {
				toolCallID++
				id = strconv.Itoa(toolCallID)
			}
			return fmt.Sprintf("functions.%s:%s", functionName, id)
		}
	)

	for _, toolCall := range invalidToolCalls {
		newID := newToolID(toolCall.Function.Name)
		toolMap[toolCall.ID] = newID
		toolCall.ID = newID
	}

	for _, message := range invalidToolMessages {
		newID, found := toolMap[ptr.Deref(message.ToolCallID)]
		if !found {
			newID = newToolID("unknown")
			toolMap[ptr.Deref(message.ToolCallID)] = newID
		}
		message.ToolCallID = ptr.To(newID)
	}
}

func parseKimiNameAndID(idName string) (string, string, error) {
	// format: functions.get_weather:0
	const functionPrefix = "functions."
	if !strings.HasPrefix(idName, functionPrefix) {
		return "", "", ErrToolParseFailed
	}
	idName = idName[len(functionPrefix):]

	idStart := strings.LastIndex(idName, ":")
	if idStart == -1 || idStart == 0 || idStart == len(idName)-1 {
		return "", "", ErrToolParseFailed
	}

	return idName[:idStart], idName[idStart+1:], nil
}

func (c *KimiParseConfig) ParseTools(
	params Params,
	message string,
) (string, []Call, error) {
	toolCallSectionIndices := _kimiToolSectionPattern.FindAllStringSubmatchIndex(message, -1)
	if len(toolCallSectionIndices) == 0 {
		return message, nil, nil
	}

	var precedingText string
	if len(toolCallSectionIndices) > 0 {
		precedingText = message[:toolCallSectionIndices[0][0]]
	}

	var calls []Call
outerLoop:
	for _, sectionIndices := range toolCallSectionIndices {
		sectionContent := message[sectionIndices[2]:sectionIndices[3]]
		tools := _kimiToolPattern.FindAllStringSubmatch(sectionContent, -1)
		for _, match := range tools {
			var (
				fullID       = match[1]
				name, _, err = parseKimiNameAndID(match[1])
				argstr       = match[2]
				args         map[string]any
			)

			if err != nil {
				break outerLoop
			}

			if argstr != "" && argstr != "null" {
				raw := unsafe.Slice(unsafe.StringData(argstr), len(argstr))
				if err := json.Unmarshal(raw, &args); err != nil {
					repaired, err := jsonrepair.JSONRepair(argstr)
					if err != nil {
						break outerLoop
					}
					if err := json.UnmarshalString(repaired, &args); err != nil {
						break outerLoop
					}
				}
			}

			calls = append(calls, Call{
				Name:      name,
				Arguments: args,
				ID:        fullID,
			})
			if !params.ParallelToolCalls {
				break outerLoop
			}
		}
	}

	if len(calls) == 0 {
		return "", nil, ErrToolParseFailed
	}
	return precedingText, calls, nil
}
