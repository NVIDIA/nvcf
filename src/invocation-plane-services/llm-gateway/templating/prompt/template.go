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
	"errors"

	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/output"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/token"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var (
	_ TextTemplate      = Nop()
	_ TokenizedTemplate = Nop()
)

type Template interface {
	ReasoningConfig() output.ReasoningConfig
	ToolParseConfig() tools.ParseConfig
	GetForcedToolUsePrefix(*models.ChatCompletionToolChoiceField) string
	DropTokens() []string
	Version() string
}

//go:generate go tool mockgen -package promptmock -destination promptmock/text_template.go . TextTemplate
type TextTemplate interface {
	Template

	RenderText([]models.ChatMessage, tools.Params) (Prompt, error)
}

//go:generate go tool mockgen -package promptmock -destination promptmock/tokenized_template.go . TokenizedTemplate
type TokenizedTemplate interface {
	Template

	RenderTokens([]models.ChatMessage, tools.Params) (token.Tokens, int, error)
	RenderMessages([]uint32) ([]models.ChatMessage, error)
}

// UniversalTemplate exists purely so that [Nop] can return it in order to
// satisfy either [TextTemplate] or [TokenizedTemplate].
type UniversalTemplate interface {
	Template

	RenderText([]models.ChatMessage, tools.Params) (Prompt, error)
	RenderTokens([]models.ChatMessage, tools.Params) (token.Tokens, int, error)
	RenderMessages([]uint32) ([]models.ChatMessage, error)
}

func Nop() UniversalTemplate {
	return nopTemplate{}
}

type nopTemplate struct{}

func (nopTemplate) RenderText(
	[]models.ChatMessage,
	tools.Params,
) (Prompt, error) {
	return nil, nil
}

func (nopTemplate) RenderTokens(
	[]models.ChatMessage,
	tools.Params,
) (token.Tokens, int, error) {
	return nil, 0, nil
}

func (nopTemplate) RenderMessages([]uint32) ([]models.ChatMessage, error) {
	return nil, nil
}

func (nopTemplate) ReasoningConfig() output.ReasoningConfig {
	return nil
}

func (nopTemplate) ToolParseConfig() tools.ParseConfig {
	return nil
}

func (nopTemplate) GetForcedToolUsePrefix(
	_ *models.ChatCompletionToolChoiceField,
) string {
	return ""
}

func (nopTemplate) DropTokens() []string {
	return nil
}

func (nopTemplate) Version() string {
	return "v1"
}

func (nopTemplate) ProcessTokens() error {
	return nil
}

func (nopTemplate) IncrementalParser(
	_ string, // role
) (IncrementalTokenParser, error) {
	return nil, errors.New("nopTemplate does not implement IncrementalParser")
}
