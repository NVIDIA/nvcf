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
	"fmt"

	"github.com/nvidia-lpu/minijinja"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

type EphemeralTemplate struct {
	TextTemplate

	env *minijinja.Environment
}

var _ TextTemplate = (*EphemeralTemplate)(nil)

// Close releases the minijinja environment resources.
// This must be called explicitly when the template is no longer needed.
func (t *EphemeralTemplate) Close() error {
	if t.env != nil {
		return t.env.Close()
	}
	return nil
}

func (t *EphemeralTemplate) RenderText(
	msgs []models.ChatMessage,
	params tools.Params,
) (Prompt, error) {
	return t.TextTemplate.RenderText(msgs, params)
}

func NewEphemeralTemplate(
	templateString string,
	params JinjaParams,
) (*EphemeralTemplate, error) {
	const ephemeralTemplateName = "custom"
	params.Hash = HashTemplate(templateString)

	// TODO(mway): Pool environments for reuse
	env := minijinja.NewEnvironment(
		minijinja.WithLstripBlocks(true),
		minijinja.WithTrimBlocks(true),
	)

	if err := env.AddTemplate(ephemeralTemplateName, templateString); err != nil {
		_ = env.Close()
		return nil, fmt.Errorf("failed to register custom template: %w", err)
	}

	// This should not fail because we just successfully added the template.
	tmpl := must.True(env.Template(ephemeralTemplateName))
	jinjaTemplate, err := NewJinja(
		tmpl,
		nil,
		params,
	)
	if err != nil {
		_ = env.Close()
		return nil, fmt.Errorf("failed to build jinja template: %w", err)
	}

	return &EphemeralTemplate{
		TextTemplate: jinjaTemplate,
		env:          env,
	}, nil
}
