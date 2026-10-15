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

package api

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
)

func TestEstimatedInputTokensIncludesForwardedToolPayloads(t *testing.T) {
	t.Parallel()

	description := strings.Repeat("lookup field ", 32)
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": description,
			},
		},
	}
	request := &models.ChatCompletionRequest{
		Model: "test-model",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello"),
			},
		},
	}
	baseline := estimatedInputTokensForNormalizedRequest(request.Model, request)

	request.Tools = &[]models.ChatTool{
		{
			Type: models.ToolTypeFunction,
			Function: models.ChatFunctionSpec{
				Name:        "lookup",
				Description: &description,
				Parameters:  &parameters,
			},
		},
	}

	got := estimatedInputTokensForNormalizedRequest(request.Model, request)
	if got <= baseline {
		t.Fatalf("estimated input tokens = %d, want > baseline %d", got, baseline)
	}
}

func TestEstimatedInputTokensIncludesForwardedMultimodalPayloads(t *testing.T) {
	t.Parallel()

	request := &models.ChatCompletionRequest{
		Model: "test-model",
		Messages: &[]models.ChatMessage{
			{
				Role: models.ChatCompletionRoleUser,
				Content: []models.ContentPart{
					models.ContentPartText("describe this"),
				},
			},
		},
	}
	baseline := estimatedInputTokensForNormalizedRequest(request.Model, request)

	(*request.Messages)[0].Content = append(
		(*request.Messages)[0].Content,
		&models.ContentPartImageURL{
			URL:    "data:image/png;base64," + strings.Repeat("a", 512),
			Detail: "high",
		},
		models.ContentPartDocument{
			Data: map[string]any{
				"title": "specification",
				"body":  strings.Repeat("document content ", 64),
			},
		},
	)

	got := estimatedInputTokensForNormalizedRequest(request.Model, request)
	if got <= baseline {
		t.Fatalf("estimated input tokens = %d, want > baseline %d", got, baseline)
	}
}

func TestEstimatedTokenCountForValueFallsBackWhenJSONMarshalFails(t *testing.T) {
	t.Parallel()

	got := estimatedTokenCountForValue(make(chan int))
	if got == 0 {
		t.Fatal("estimated token count = 0, want fallback estimate")
	}
}
