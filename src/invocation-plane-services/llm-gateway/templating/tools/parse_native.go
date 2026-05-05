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
	"io"
	"regexp"
	"strings"
	"unsafe"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/output"
)

var (
	// This format is exclusively used with the llama family of models.
	// Example: <function=tool_name>{"arg_name": "arg_value"}</function>
	_llamaFunctionRegex = LlamaFunctionRegex()

	_nativeParseConfigBufferPool = pool.NewWithReleaser(
		func() *bytes.Buffer {
			return bytes.NewBuffer(make([]byte, 0, 128))
		},
		func(x *bytes.Buffer) {
			x.Reset()
		},
	)

	_ ParseConfig = (*NativeParseConfig)(nil)
)

func LlamaFunctionRegex() *regexp.Regexp {
	return regexp.MustCompile(`<function=([^>]+)>((?:.|\n)*?)</function>`)
}

// NativeToolUseConfigFamily represents the family of tool use configuration.
type NativeToolUseFamily int

// TODO(mway): Remove MistralToolUseFamily if it remains unused
const (
	InvalidToolUseFamily NativeToolUseFamily = iota
	Llama31ToolUseFamily
	Llama4ToolUseFamily
	MistralToolUseFamily
)

type NativeParseConfig struct {
	NativeToolUseFamily NativeToolUseFamily
	HeaderBeginToken    string
	HeaderEndToken      string
	EndOfTurnToken      string

	ToolNoun           string
	ToolUseBeginToken  string
	ToolNameBeginToken string
	ToolNameEndToken   string
	ToolUseEndToken    string
	EndToolCallToken   string

	ReasoningBeginToken string
	ReasoningPrefill    string
	ReasoningEndToken   string
}

func (c *NativeParseConfig) ReasoningConfig() output.ReasoningConfig {
	if c.ReasoningBeginToken == "" {
		return nil
	}
	return output.NewReasoningConfigImpl(
		c.ReasoningBeginToken,
		c.ReasoningEndToken,
		c.ReasoningPrefill,
	)
}

func (c *NativeParseConfig) ToolParseConfig() ParseConfig {
	return c
}

func (*NativeParseConfig) ParseTools(
	params Params,
	message string,
) (string, []Call, error) {
	matches := _llamaFunctionRegex.FindAllStringSubmatchIndex(message, -1)
	if len(matches) == 0 {
		return message, nil, nil
	}

	calls := make([]Call, 0, len(matches))
	for _, match := range matches {
		if len(match) != 6 {
			continue
		}

		var (
			fn     = message[match[2]:match[3]]
			argstr = message[match[4]:match[5]]
			args   map[string]any
		)

		if argstr != "" && argstr != "null" {
			raw := unsafe.Slice(unsafe.StringData(argstr), len(argstr))
			dec := json.NewDecoder(bytes.NewReader(raw))
			if err := dec.Decode(&args); err != nil {
				return "", nil, ErrToolParseFailed
			}
		}

		calls = append(calls, Call{
			Name:      fn,
			Arguments: args,
		})

		if !params.ParallelToolCalls {
			break
		}
	}
	return message[:matches[0][0]], calls, nil
}

func (*NativeParseConfig) GenerateToolCallID() string {
	return NewShortCallID()
}

func (c *NativeParseConfig) ToolUseBeginMarker() string {
	return c.ToolUseBeginToken
}

func (c *NativeParseConfig) ToolUseEndMarker() string {
	return c.ToolUseEndToken
}

func (c *NativeParseConfig) ToolNameBeginMarker() string {
	return c.ToolNameBeginToken
}

func (c *NativeParseConfig) ToolNameEndMarker() string {
	return c.ToolNameEndToken
}

func (c *NativeParseConfig) CanBeToolCall(value string) bool {
	if len(c.ToolUseBeginToken) > len(value) {
		return strings.HasPrefix(c.ToolUseBeginToken, value)
	}
	return strings.HasPrefix(value, c.ToolUseBeginToken)
}

func (c *NativeParseConfig) WriteRoleHeader(
	buf io.StringWriter,
	role string,
) {
	buf.WriteString(c.HeaderBeginToken) //nolint:errcheck
	buf.WriteString(role)               //nolint:errcheck
	buf.WriteString(c.HeaderEndToken)   //nolint:errcheck
}

func (c *NativeParseConfig) GenerateSysPrompt(
	extractedSystemPrompt string,
	systemPrompt string,
) string {
	extractedSystemPrompt = strings.TrimSpace(extractedSystemPrompt)
	if systemPrompt == "" && extractedSystemPrompt == "" {
		return ""
	}

	buf := _nativeParseConfigBufferPool.Get()
	defer _nativeParseConfigBufferPool.Put(buf)

	c.WriteRoleHeader(buf, "system")
	buf.WriteString(systemPrompt)
	buf.WriteString(extractedSystemPrompt)
	buf.WriteString(c.EndOfTurnToken)

	return buf.String()
}

func (c *NativeParseConfig) GetForcedUseInstruction(
	choice models.ChatCompletionToolChoiceField,
) (string, bool) {
	buf := _nativeParseConfigBufferPool.Get()
	defer _nativeParseConfigBufferPool.Put(buf)

	var disabled bool
	switch {
	case choice.ToolChoice != nil:
		// n.b. Historically, this branch was not mutually exclusive with the
		//      following two branches, meaning that an instruction could be
		//      comprised of conflicting instructions, e.g.:
		//
		//        You must use the noun funcname to answer the query.For this
		//        specific request, you are NOT allowed to make any funcname
		//        calls. [...]
		buf.WriteString("You must use the ")
		buf.WriteString(c.ToolNoun)
		buf.WriteByte(' ')
		buf.WriteString(choice.ToolChoice.Function.Name)
		buf.WriteString(" to answer the user query.")
	case ptr.Eq(choice.String, models.ChatToolSelectionRequired):
		buf.WriteString("You must use at least one ")
		buf.WriteString(c.ToolNoun)
		buf.WriteString(" to answer the user query.")
	case ptr.Eq(choice.String, models.ChatToolSelectionNone):
		buf.WriteString("For this specific request, you are NOT allowed to make any ")
		buf.WriteString(c.ToolNoun)
		buf.WriteString(" calls. Simply answer the user query directly, without using any ")
		buf.WriteString(c.ToolNoun)
		buf.WriteString("s.")
		disabled = true
	}

	return buf.String(), !disabled
}
