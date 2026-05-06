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

package templating_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/prompt"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

func TestCompareGenericVsNativeToolSupport(t *testing.T) {
	t.Parallel()

	// Test tool with description
	testTool := tools.Tool{
		Type: "function",
		Function: tools.ChatFunctionSpec{
			Name:        "test_function",
			Description: ptr.To("This is a test function for comparison."),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "string",
						"description": "The input parameter.",
					},
				},
				"required": []any{"input"},
			},
		},
	}

	messages := []models.ChatMessage{
		{
			Role:    models.ChatCompletionRoleUser,
			Content: models.SingleTextContent("Test message"),
		},
	}

	toolParams := tools.Params{
		Tools: []tools.Tool{testTool},
		ToolChoice: models.ChatCompletionToolChoiceField{
			String: ptr.To(models.ChatToolSelectionAuto),
		},
	}

	t.Run("generic_system_includes_descriptions", func(t *testing.T) {
		modifiedMessages, err := tools.HandleGenericToolCalling(
			messages,
			toolParams,
			"<tool-use>",
			"</tool-use>",
		)
		require.NoError(t, err)

		systemContent := modifiedMessages[len(modifiedMessages)-1].Content.MustSingleText()

		assert.Contains(
			t,
			systemContent,
			"This is a test function for comparison.",
			"Generic system should include function description",
		)
		assert.Contains(
			t,
			systemContent,
			"The input parameter.",
			"Generic system should include parameter description",
		)
	})

	// Test multiple templates to see which ones properly include descriptions
	t.Run("survey_native_template_support", func(t *testing.T) {
		engine := templating.NewEngine()
		defer engine.Close()

		require.NoError(t, engine.RegisterHFTemplates("../lib/tokenizers/vendor"))
		require.NoError(t, engine.RegisterCustomJinjaTemplates())
		require.NoError(t, engine.RegisterCustomTemplates())

		var (
			templatesWithDescriptions    = []string{}
			templatesWithoutDescriptions = []string{}
		)

		for name, template := range engine.TemplatesIter() {
			textTemplate, ok := template.(prompt.TextTemplate)
			if !ok {
				continue
			}

			if parseConfig := textTemplate.ToolParseConfig(); parseConfig != nil {
				rendered, err := textTemplate.RenderText(messages, toolParams)
				if err != nil {
					continue // Skip templates that fail
				}

				// Extract text from the prompt
				var promptText string
				for _, part := range rendered {
					if textPart, ok := part.(models.ContentPartText); ok {
						promptText += textPart.String()
					}
				}

				// Check if description is included
				if assert.Contains(t, promptText, "This is a test function for comparison.", "") {
					templatesWithDescriptions = append(templatesWithDescriptions, name)
				} else {
					templatesWithoutDescriptions = append(templatesWithoutDescriptions, name)
				}
			}
		}

		t.Logf("✅ Templates that include function descriptions: %v", templatesWithDescriptions)
		t.Logf(
			"❌ Templates that DON'T include function descriptions: %v",
			templatesWithoutDescriptions,
		)

		if len(templatesWithoutDescriptions) > 0 {
			t.Logf("⚠️  Some native templates are not including function descriptions!")
		}
	})
}
