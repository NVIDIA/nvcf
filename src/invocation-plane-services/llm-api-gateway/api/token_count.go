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
	"encoding/json"
	"fmt"
	"math"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
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

func estimatedTokenCountForRequest(model string, request *models.ChatCompletionRequest) int {
	if request == nil {
		return 0
	}

	totalTokens := tokenExtraForModel[model]
	if request.Messages != nil {
		totalTokens += estimatedTokenCountForMessages(*request.Messages)
	}

	totalTokens += estimatedTokenCountForValue(request.Tools)
	totalTokens += estimatedTokenCountForValue(request.Functions)
	totalTokens += estimatedTokenCountForToolChoice(request.ToolChoice)
	totalTokens += estimatedTokenCountForFunctionChoice(request.FunctionChoice)
	totalTokens += estimatedTokenCountForValue(request.ResponseFormat)
	totalTokens += estimatedTokenCountForStop(request.Stop)
	totalTokens += estimatedTokenCountForStringPtr(request.User)
	totalTokens += estimatedTokenCountForStringPtr(request.ReasoningFormat)
	totalTokens += estimatedTokenCountForStringPtr(request.ReasoningEffort)
	totalTokens += estimatedTokenCountForValue(request.Metadata)

	return totalTokens
}

func estimatedTokenCountForMessages(messages []models.ChatMessage) int {
	totalTokens := 0
	for _, message := range messages {
		totalTokens += tokensPerMessage
		totalTokens += tokensPerRole

		totalTokens += estimatedTokenCountForMessageContent(message.Content)
		if message.Name != nil {
			totalTokens += estimatedTokenCountForStringPtr(message.Name)
			totalTokens += tokensPerName
		}
		totalTokens += estimatedTokenCountForStringPtr(message.ToolCallID)
		totalTokens += estimatedTokenCountForStringPtr(message.Reasoning)
		totalTokens += estimatedTokenCountForText(message.Channel)
		totalTokens += estimatedTokenCountForValue(message.ToolCalls)
		totalTokens += estimatedTokenCountForValue(message.FunctionCall)
	}

	return totalTokens
}

func estimatedTokenCountForMessageContent(content models.ChatMessageContent) int {
	totalTokens := 0
	for _, part := range content {
		if text, ok := part.(models.ContentPartText); ok {
			totalTokens += len(text.String()) / 4
			continue
		}
		totalTokens += estimatedTokenCountForValue(part)
	}

	return totalTokens
}

func estimatedTokenCountForToolChoice(choice models.ChatCompletionToolChoiceField) int {
	totalTokens := estimatedTokenCountForStringPtr(choice.String)
	totalTokens += estimatedTokenCountForValue(choice.ToolChoice)
	return totalTokens
}

func estimatedTokenCountForFunctionChoice(choice models.ChatCompletionFunctionChoiceField) int {
	totalTokens := estimatedTokenCountForStringPtr(choice.String)
	totalTokens += estimatedTokenCountForValue(choice.FunctionCall)
	return totalTokens
}

func estimatedTokenCountForStop(stop models.ChatCompletionStopField) int {
	totalTokens := 0
	for _, value := range stop {
		totalTokens += estimatedTokenCountForText(value)
	}
	return totalTokens
}

func estimatedTokenCountForStringPtr(value *string) int {
	if value == nil {
		return 0
	}
	return estimatedTokenCountForText(*value)
}

func estimatedTokenCountForValue(value any) int {
	if value == nil {
		return 0
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return estimatedTokenCountForText(fmt.Sprint(value))
	}
	if string(raw) == "null" {
		return 0
	}
	return estimatedTokenCountForText(string(raw))
}

func estimatedInputTokensForNormalizedRequest(
	model string,
	request *models.ChatCompletionRequest,
) int {
	return max(0, estimatedTokenCountForRequest(model, request))
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
