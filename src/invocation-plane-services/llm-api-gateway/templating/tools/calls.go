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
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
	"unsafe"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
)

const (
	_parallelCallExample = `
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
`
	_parallelToolCallInstructions = `
- You can provide multiple tool_call objects at once.
- If a tool_call object depends on the result of a previous tool_call with {"id": "pending"} you should not include that tool_call object in the list.
- This means you can NEVER nest a tool_call object inside a tool_call object. If you do this, the system will break.`

	_singleCallExample = `
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
`

	_singleToolCallInstructions = `
- It is OK if we only answer the user's question partially. We're only trying to make progress towards our goal.
- You can only make A SINGLE tool_call. If multiple tool_calls are required, ONLY choose A SINGLE tool_call to make next.`
)

var (
	//go:embed embed/generic_tool_call_sysprompt.tmpl
	_genericToolCallSysPrompt     string
	_genericToolCallSysPromptTmpl = template.Must(
		template.New("toolCallingTemplate").Parse(_genericToolCallSysPrompt),
	)

	_callObjectSlicePool = pool.NewWithReleaser(
		func() *[]models.PromptToolCallObject {
			return ptr.To(make([]models.PromptToolCallObject, 0))
		},
		func(x *[]models.PromptToolCallObject) {
			*x = (*x)[:0]
		},
	)
	_bytesBufferPool = pool.NewWithReleaser(
		func() *bytes.Buffer {
			return bytes.NewBuffer(make([]byte, 0, 512))
		},
		func(x *bytes.Buffer) {
			x.Reset()
		},
	)
	_bufferedEncoderPool = json.NewBufferedEncoderPool(0)
)

func HandleGenericToolCalling(
	messages []models.ChatMessage,
	params Params,
	beginMarker string,
	endMarker string,
) ([]models.ChatMessage, error) {
	msgs, err := translateToolCalls(
		messages,
		params.ParallelToolCalls,
		beginMarker,
		endMarker,
	)
	switch {
	case err != nil:
		return nil, err
	case len(params.Tools) == 0:
		return msgs, nil
	default:
		prompt, err := genericToolPrompt(params, beginMarker, endMarker)
		if err != nil {
			return nil, err
		}

		return append(msgs, models.ChatMessage{
			Role:    models.ChatCompletionRoleSystem,
			Content: models.SingleTextContent(prompt),
		}), nil
	}
}

func genericToolPrompt(
	params Params,
	beginMarker string,
	endMarker string,
) (string, error) {
	if params.ParallelToolCalls {
		return genericToolPromptInternal(
			params,
			_parallelToolCallInstructions,
			beginMarker,
			endMarker,
			_parallelCallSchema,
			_parallelCallExample,
		)
	}
	return genericToolPromptInternal(
		params,
		_singleToolCallInstructions,
		beginMarker,
		endMarker,
		_singleCallSchema,
		_singleCallExample,
	)
}

func genericToolPromptInternal(
	params Params,
	instructions string,
	beginMarker string,
	endMarker string,
	schema string,
	example string,
) (string, error) {
	buf := _bytesBufferPool.Get()
	defer _bytesBufferPool.Put(buf)

	enc := _bufferedEncoderPool.Get()
	enc.SetIndent("", "\t")
	defer func() {
		enc.SetIndent("", "")
		_bufferedEncoderPool.Put(enc)
	}()

	// Should never error as this was validated early in request processing.
	if err := enc.Encode(params.Tools); err != nil {
		return "", fmt.Errorf("failed to marshal tools: %w", err)
	}

	if ptr.Deref(params.ToolChoice.String) == models.ChatToolSelectionNone {
		buf.WriteString("These tools were previously available:")
		buf.WriteString("\n```json\n")
		buf.Write(enc.TrimmedBytes())
		buf.WriteString("\n```\n\nProvide the answer to the user in text.")
		return buf.String(), nil
	}

	var (
		allowedTools   = "all"
		forcedGenerate bool
	)
	switch {
	case params.ToolChoice.ToolChoice != nil:
		allowedTools = params.ToolChoice.ToolChoice.Function.Name
		forcedGenerate = true
	case ptr.Deref(params.ToolChoice.String) == models.ChatToolSelectionRequired:
		forcedGenerate = true
	default:
		// nop
	}

	data := struct {
		ToolChoice                 string
		ToolsJSON                  string
		ToolUseBeginMarker         string
		ToolUseEndMarker           string
		ToolCallSchema             string
		ToolCallExample            string
		AllowedTools               string
		AdditionalToolInstructions string
		ForcedGenerate             bool
	}{
		ToolChoice:                 ptr.Deref(params.ToolChoice.String),
		ToolsJSON:                  enc.TrimmedString(),
		ToolUseBeginMarker:         beginMarker,
		ToolUseEndMarker:           endMarker,
		ToolCallSchema:             schema,
		ToolCallExample:            example,
		AllowedTools:               allowedTools,
		AdditionalToolInstructions: instructions,
		ForcedGenerate:             forcedGenerate,
	}

	if err := _genericToolCallSysPromptTmpl.Execute(buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// Compatibility layer for templates that do not support tool calling.
// Converts tool and function calls to user messages.
// Moves assistant message.tool_calls/function_calls to message.Content.
// This allows us to add tool calling support to templates that do not support it.
func translateToolCalls(
	messages []models.ChatMessage,
	parallel bool,
	beginMarker string,
	endMarker string,
) ([]models.ChatMessage, error) {
	enc := _bufferedEncoderPool.Get()
	defer _bufferedEncoderPool.Put(enc)

	translated := make([]models.ChatMessage, 0, len(messages))
	for i, in := range messages {
		enc.Buffer().Reset()

		out := models.ChatMessage{
			Role:    in.Role,
			Content: in.Content,
		}

		switch in.Role {
		case models.ChatCompletionRoleFunction:
			out.Role = models.ChatCompletionRoleUser
			out.Content = models.SingleTextContent(
				"I called the function, it yielded: " + in.Content.MustSingleText(),
			)
		case models.ChatCompletionRoleTool:
			out.Role = models.ChatCompletionRoleUser
			out.Content = models.SingleTextContent(
				fmt.Sprintf(
					`I called the tool for tool call id "%s" it yielded: %s`,
					ptr.Deref(in.ToolCallID),
					in.Content.MustSingleText(),
				),
			)
		case models.ChatCompletionRoleAssistant:
			src := ptr.Deref(in.ToolCalls)
			if numcalls := len(src); numcalls > 0 {
				var err error
				switch {
				case parallel:
					calls := _callObjectSlicePool.Get()
					for _, call := range src {
						*calls = append(*calls, toPromptToolCallObject(call))
					}
					err = enc.Encode(models.ToolCallsWithParameters{
						ToolCalls: *calls,
					})
					_callObjectSlicePool.Put(calls)
				case numcalls > 1:
					return nil, fmt.Errorf(
						"messages[%d]: parallel tool calling is disabled. "+
							"Please set `parallel_tool_calls` to true in your request",
						i,
					)
				default:
					err = enc.Encode(models.ToolCallWithParameters{
						ToolCall: toPromptToolCallObject(src[0]),
					})
				}
				if err != nil {
					return nil, fmt.Errorf(
						"failed to marshal tool calls: %w",
						err,
					)
				}
				out.Content = models.SingleTextContent(
					beginMarker + enc.TrimmedString() + endMarker,
				)
			} else if in.FunctionCall != nil {
				err := enc.Encode(models.FunctionCallWithParameters{
					Function: models.PromptFunctionObject{
						Name: in.FunctionCall.Name,
					},
					Parameters: unsafe.Slice(
						unsafe.StringData(in.FunctionCall.Arguments),
						len(in.FunctionCall.Arguments),
					),
				})
				if err != nil {
					return nil, fmt.Errorf(
						"failed to marshal function call: %w",
						err,
					)
				}

				out.Content = models.SingleTextContent(
					fmt.Sprintf(
						"Please call the following tool: %s%s%s",
						beginMarker,
						enc.TrimmedString(),
						endMarker,
					),
				)
			}
		}

		translated = append(translated, out)
	}

	return translated, nil
}

func toPromptToolCallObject(
	message models.ChatToolCall,
) models.PromptToolCallObject {
	return models.PromptToolCallObject{
		ID:   message.ID,
		Type: message.Type,
		Function: models.PromptFunctionObject{
			Name: message.Function.Name,
		},
		Parameters: []byte(message.Function.Arguments),
	}
}
