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
	"bytes"
	"strings"

	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

const _deepseekBufferCap = 512

var _ TextTemplate = (*DeepSeek)(nil)

type DeepSeek struct {
	tools.NativeParseConfig

	bufpool  *pool.Pool[*bytes.Buffer]
	encpool  *json.BufferedEncoderPool
	parsecfg tools.ParseConfig
}

func NewDeepSeek() *DeepSeek {
	return &DeepSeek{
		NativeParseConfig: tools.NativeParseConfig{
			ToolUseBeginToken:   "<tool_call>",
			ToolUseEndToken:     "</tool_call>",
			ToolNameBeginToken:  `{"name": "`,
			ToolNameEndToken:    `"}`,
			ToolNoun:            "function call",
			EndToolCallToken:    " ",
			ReasoningBeginToken: "<think>",
			ReasoningEndToken:   "</think>",
		},
		bufpool: pool.NewWithReleaser(
			func() *bytes.Buffer {
				return bytes.NewBuffer(make([]byte, 0, _deepseekBufferCap))
			},
			func(x *bytes.Buffer) {
				x.Reset()
			},
		),
		encpool:  json.NewBufferedEncoderPool(_deepseekBufferCap),
		parsecfg: tools.NewDeepSeekParseConfig(),
	}
}

func (d *DeepSeek) ToolParseConfig() tools.ParseConfig {
	return d.parsecfg
}

func (d *DeepSeek) GetForcedToolUsePrefix(
	choice *models.ChatCompletionToolChoiceField,
) string {
	switch {
	case d.ReasoningBeginToken != "":
		// If we prefill a tool call, we conflict with <think> tokens
		return ""
	case choice.ToolChoice != nil:
		return d.ToolUseBeginToken + d.ToolNameBeginToken +
			choice.ToolChoice.Function.Name + d.ToolNameEndToken
	case ptr.Eq(choice.String, models.ChatToolSelectionRequired):
		return d.ToolUseBeginToken
	default:
		return ""
	}
}

func (*DeepSeek) DropTokens() []string {
	return nil
}

func (d *DeepSeek) RenderText(
	msgs []models.ChatMessage,
	params tools.Params,
) (Prompt, error) {
	buf := d.bufpool.Get()
	defer d.bufpool.Put(buf)

	for _, msg := range msgs {
		if msg.Role == models.ChatCompletionRoleSystem {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(msg.Content.MustSingleText())
		}
	}

	if ptr.Ne(params.ToolChoice.String, models.ChatToolSelectionNone) {
		buf.WriteString(`
# Tools

You may call one`)
		if params.ParallelToolCalls {
			buf.WriteString(" or more functions")
		} else {
			buf.WriteString(" function")
		}
		buf.WriteString(` to assist with the user query.

You are provided with function signatures within <tools></tools> XML tags:
<tools>
`)

		// Since we have a variable number of tools, reuse an encoder to
		// minimize memory thrash.
		enc := d.encpool.Get()
		defer d.encpool.Put(enc)

		for _, tool := range params.Tools {
			if err := enc.Encode(tool.Function); err != nil {
				return nil, err
			}

			enc.Buffer().WriteTo(buf) //nolint:errcheck
			enc.Buffer().Reset()
			buf.WriteByte('\n')
		}

		buf.WriteString("</tools>\n")
		buf.WriteString(
			`For each function call, return a JSON object with function name and arguments within <tool_call></tool_call> XML tags:
</think>

<tool_call>{"name": <function-name>, "arguments": <args-json-object>}</tool_call>`,
		)

		switch {
		case ptr.Eq(params.ToolChoice.String, models.ChatToolSelectionRequired):
			buf.WriteString("You must call a function\n")
		case params.ToolChoice.ToolChoice != nil:
			buf.WriteString("You must call the function: ")
			buf.WriteString(params.ToolChoice.ToolChoice.Function.Name)
			buf.WriteByte('\n')
		default:
			// nop
		}
	}

	var writing bool
	for _, msg := range msgs {
		switch msg.Role {
		case models.ChatCompletionRoleTool:
		case models.ChatCompletionRoleFunction:
		default:
			if writing {
				buf.WriteString("<｜tool▁outputs▁end｜>")
				writing = false
			}
		}

		switch msg.Role {
		case models.ChatCompletionRoleUser:
			buf.WriteString("<｜User｜>")
			buf.WriteString(msg.Content.MustSingleText())
		case models.ChatCompletionRoleAssistant:
			buf.WriteString("<｜Assistant｜>")
			switch {
			case msg.ToolCalls != nil && len(*msg.ToolCalls) > 0:
				str, err := tools.GetToolCallJSONString(msg.ToolCalls)
				if err != nil {
					zlog.Error().Err(err).Msg("failed to marshal tool calls")
				}
				writeStrings(buf, d.ToolUseBeginToken, str, d.EndToolCallToken)
			case msg.FunctionCall != nil:
				str, err := tools.GetToolCallJSONStringFromFunctionCall(msg.FunctionCall)
				if err != nil {
					zlog.Error().Err(err).Msg("failed to marshal function call")
				}
				writeStrings(buf, d.ToolUseBeginToken, str, d.EndToolCallToken)
			default:
				str := msg.Content.MustSingleText()
				// The model does not want previous reasoning in the prompt
				if i := strings.LastIndex(str, d.ReasoningEndToken); i >= 0 {
					buf.WriteString(str[i+len(d.ReasoningEndToken):])
				} else if i := strings.Index(str, d.ReasoningBeginToken); i == -1 {
					// No reasoning in the message
					buf.WriteString(str)
				} // else content is only reasoning
			}
			if msgs[len(msgs)-1].Role != models.ChatCompletionRoleAssistant {
				buf.WriteString("<｜end▁of▁sentence｜>")
			}
		case models.ChatCompletionRoleFunction, models.ChatCompletionRoleTool:
			if !writing {
				writing = true
				buf.WriteString("<｜tool▁outputs▁begin｜>")
			}
			buf.WriteString(msg.Content.MustSingleText())
		}
	}

	if writing {
		buf.WriteString("<｜tool▁outputs▁end｜>")
	}

	if msgs[len(msgs)-1].Role != models.ChatCompletionRoleAssistant {
		buf.WriteString("<｜Assistant｜>")
	}

	return WrapText(buf.String()), nil
}

func (d *DeepSeek) Version() string { return "v1" }
