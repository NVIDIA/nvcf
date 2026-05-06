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

package server

import (
	"github.com/NVIDIA/nvcf/llm-api-gateway/api"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating"
	templatingprompt "github.com/NVIDIA/nvcf/llm-api-gateway/templating/prompt"
	templatingtools "github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var _ api.TemplateEngine = (*templateEngineAdapter)(nil)
var _ api.TextTemplate = (*textTemplateAdapter)(nil)

type templateEngineAdapter struct {
	engine *templating.Engine
}

func newTemplateEngineAdapter(engine *templating.Engine) *templateEngineAdapter {
	return &templateEngineAdapter{engine: engine}
}

func (a *templateEngineAdapter) GetTextTemplate(name string) (api.TextTemplate, error) {
	template, err := a.engine.GetTextTemplate(name)
	if err != nil {
		return nil, err
	}

	return &textTemplateAdapter{template: template}, nil
}

type textTemplateAdapter struct {
	template templatingprompt.TextTemplate
}

func (a *textTemplateAdapter) RenderText(
	messages []models.ChatMessage,
	params templatingtools.Params,
) (models.ChatMessageContent, error) {
	rendered, err := a.template.RenderText(messages, params)
	if err != nil {
		return nil, err
	}

	return models.ChatMessageContent(rendered), nil
}
