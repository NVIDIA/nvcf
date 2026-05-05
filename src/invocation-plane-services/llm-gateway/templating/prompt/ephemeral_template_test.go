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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

func TestNewEphemeralTemplateConfiguresJinjaParams(t *testing.T) {
	params := JinjaParams{
		RelaxedMessageOrdering:     true,
		PreserveSystemMessageOrder: true,
		SupportsSystemPrompts:      true,
		ToolParseConfig:            tools.NewDefaultParseConfig(),
	}

	templateString := `{% for message in messages %}{{ message.content }}{% endfor %}`
	template, err := NewEphemeralTemplate(templateString, params)
	require.NoError(t, err)
	require.NotNil(t, template)
	t.Cleanup(func() {
		require.NoError(t, template.Close())
	})

	jinja, ok := template.TextTemplate.(*Jinja)
	require.True(t, ok)

	jinjaParams := jinja.Params()
	require.True(t, jinjaParams.RelaxedMessageOrdering)
	require.True(t, jinjaParams.PreserveSystemMessageOrder)
	require.True(t, jinjaParams.SupportsSystemPrompts)
	require.Equal(t, HashTemplate(templateString), jinjaParams.Hash)

	out, err := template.RenderText([]models.ChatMessage{
		{
			Role:    models.ChatCompletionRoleUser,
			Content: models.SingleTextContent("Hello, world!"),
		},
	}, tools.Params{})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Len(t, out, 1)
	require.Equal(t, "Hello, world!", must.As[models.ContentPartText](out[0]).String())
}

func TestNewEphemeralTemplateInvalidSyntax(t *testing.T) {
	template, err := NewEphemeralTemplate(
		`{% for message in messages %}{{ message.content }`,
		JinjaParams{},
	)
	require.Error(t, err)
	require.Nil(t, template)
}

func TestEphemeralTemplateRenderText(t *testing.T) {
	template := &EphemeralTemplate{TextTemplate: Nop()}
	_, err := template.RenderText(nil, tools.Params{})
	require.NoError(t, err)
}
