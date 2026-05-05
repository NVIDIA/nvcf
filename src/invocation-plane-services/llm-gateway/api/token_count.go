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
	"math"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

const (
	tokensPerMessage = 3
	tokensPerName    = 1
	tokensPerRole    = 2
)

var tokenExtraForModel = map[string]int{
	"llama2-70b-4096":    15,
	"mixtral-8x7b-32768": 1,
	"gemma-7b-it":        2,
}

func estimatedTokenCountForRequest(model string, messages *[]models.ChatMessage) int {
	if messages == nil {
		return 0
	}

	totalTokens := tokenExtraForModel[model]
	for _, message := range *messages {
		totalTokens += tokensPerMessage
		totalTokens += tokensPerRole

		for _, content := range message.Content {
			if text, ok := content.(models.ContentPartText); ok {
				totalTokens += len(text.String()) / 4
			}
		}

		if message.Name != nil {
			totalTokens += len(*message.Name) / 4
			totalTokens += tokensPerName
		}
	}

	return totalTokens
}

func estimatedInputTokensForNormalizedRequest(
	model string,
	messages *[]models.ChatMessage,
	renderedPrompt *string,
) int {
	estimate := estimatedTokenCountForRequest(model, messages)
	renderedText := ptr.Deref(renderedPrompt)
	if renderedText == "" {
		return max(0, estimate)
	}

	return max(estimate, estimatedTokenCountForText(renderedText))
}

func estimatedTokenCountForText(text string) int {
	if text == "" {
		return 0
	}

	return 5 + int(math.Ceil(float64(len(text))/4.0))
}

func maxOutputTokensForRequest(request *models.ChatCompletionRequest) int {
	if request == nil {
		return 0
	}
	if request.MaxCompletionTokens != nil {
		return int(*request.MaxCompletionTokens)
	}
	if request.MaxTokens != nil {
		return int(*request.MaxTokens)
	}
	return 1
}
