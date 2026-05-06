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
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var _ TextTemplate = (*Classification)(nil)

type Classification struct{}

func NewClassification() Classification {
	return Classification{}
}

func (Classification) RenderText(
	msgs []models.ChatMessage,
	_ tools.Params,
) (Prompt, error) {
	if len(msgs) != 1 || msgs[0].Role != models.ChatCompletionRoleUser {
		return nil, errors.New(
			"messages must contains a single user message for text classification models",
		)
	}
	text, err := msgs[0].Content.TrySingleText()
	if err != nil {
		return nil, err
	}
	if len(text) == 0 {
		return nil, errors.New("user message must not be empty")
	}
	return WrapText(text), nil
}

func (Classification) GetForcedToolUsePrefix(
	*models.ChatCompletionToolChoiceField,
) string {
	return ""
}

func (Classification) DropTokens() []string {
	return nil
}

func (Classification) ReasoningConfig() output.ReasoningConfig { return nil }
func (Classification) ToolParseConfig() tools.ParseConfig      { return nil }
func (Classification) Version() string                         { return "v1" }
