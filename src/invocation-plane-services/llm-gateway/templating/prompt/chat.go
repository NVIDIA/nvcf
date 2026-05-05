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

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/output"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var _ TextTemplate = (*GenericChat)(nil)

type GenericChat struct {
	SystemTag             string
	UserTag               string
	AssistantTag          string
	BeginMessageTag       string
	EndMessageTag         string
	PadMessages           bool
	SupportsSystemMessage bool

	bufpool  *pool.Pool[*bytes.Buffer]
	parsecfg tools.ParseConfig
}

func NewGemma7BIt() *GenericChat {
	// Gemma does not support system messages; instead, we treat them as
	// user messages.
	//
	// Ref:
	// https://ai.google.dev/gemma/docs/formatting
	return applyGenericChatDefaults(&GenericChat{
		AssistantTag:          "model\n",
		BeginMessageTag:       "<start_of_turn>",
		EndMessageTag:         "<end_of_turn>\n",
		SupportsSystemMessage: false,
		UserTag:               "user\n",
	})
}

func NewLlama3() *GenericChat {
	// Ref:
	// https://llama.meta.com/docs/model-cards-and-prompt-formats/meta-llama-3/
	return applyGenericChatDefaults(&GenericChat{
		SystemTag:             "<|start_header_id|>system<|end_header_id|>\n\n",
		UserTag:               "<|start_header_id|>user<|end_header_id|>\n\n",
		AssistantTag:          "<|start_header_id|>assistant<|end_header_id|>\n\n",
		BeginMessageTag:       "",
		EndMessageTag:         "<|eot_id|>",
		SupportsSystemMessage: true,
	})
}

func (*GenericChat) ReasoningConfig() output.ReasoningConfig {
	return nil
}

func (c *GenericChat) ToolParseConfig() tools.ParseConfig {
	return c.parsecfg
}

func (c *GenericChat) GetForcedToolUsePrefix(
	_ *models.ChatCompletionToolChoiceField,
) string {
	return ""
}

func (*GenericChat) DropTokens() []string {
	return nil
}

func (c *GenericChat) RenderText(
	messages []models.ChatMessage,
	params tools.Params,
) (Prompt, error) {
	var err error
	messages, err = tools.HandleGenericToolCalling(
		messages,
		params,
		c.parsecfg.ToolUseBeginMarker(),
		c.parsecfg.ToolUseEndMarker(),
	)
	if err != nil {
		return nil, err
	}

	buf := c.bufpool.Get()
	defer c.bufpool.Put(buf)
	const noPreserveOrder = false

	normalizedMessages := NormalizeMessages(
		messages,
		c.PadMessages,
		c.SupportsSystemMessage,
		models.ChatCompletionRoleAssistant,
		noPreserveOrder,
	)
	for i, m := range normalizedMessages {
		buf.WriteString(c.BeginMessageTag)
		switch m.Role {
		case models.ChatCompletionRoleUser:
			buf.WriteString(c.UserTag)
		case models.ChatCompletionRoleAssistant:
			buf.WriteString(c.AssistantTag)
		case models.ChatCompletionRoleSystem:
			buf.WriteString(c.SystemTag)
		}
		buf.WriteString(m.Content.MustSingleText())

		// Leave final message "open" so model can continue it
		if i != len(normalizedMessages)-1 {
			buf.WriteString(c.EndMessageTag)
		}
	}

	return WrapText(buf.String()), nil
}

func applyGenericChatDefaults(dst *GenericChat) *GenericChat {
	dst.PadMessages = true
	dst.parsecfg = tools.NewDefaultParseConfig()
	dst.bufpool = pool.NewWithReleaser(
		func() *bytes.Buffer {
			return bytes.NewBuffer(make([]byte, 0, 512))
		},
		func(x *bytes.Buffer) {
			x.Reset()
		},
	)
	return dst
}

func (c *GenericChat) Version() string { return "v1" }
