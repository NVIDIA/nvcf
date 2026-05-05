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

package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
)

type EchoProvider struct{}

func NewEchoProvider() *EchoProvider {
	return &EchoProvider{}
}

func (p *EchoProvider) Complete(
	_ context.Context,
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
) (*models.ChatCompletionResponse, error) {
	now := time.Now().Unix()
	messageText := responseText(reqCtx, request)
	usage := newUsage(request.InputTokens, len(messageText)/4)

	return &models.ChatCompletionResponse{
		ID:        "chatcmpl_" + uuid.NewString(),
		Object:    models.ObjectChatCompletion,
		CreatedAt: now,
		Model:     effectiveModel(reqCtx, request),
		Choices: []models.ChatCompletionChoice{
			{
				Index: 0,
				Message: models.ChatCompletionMessage{
					Role:    models.ChatCompletionRoleAssistant,
					Content: ptr.To(messageText),
				},
				FinishReason: models.FinishReasonStop,
			},
		},
		Usage:       usage,
		ServiceTier: request.ChatRequest.ServiceTier,
	}, nil
}

func (p *EchoProvider) Stream(
	_ context.Context,
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
) (<-chan StreamEvent, error) {
	events := make(chan StreamEvent, 8)

	go func() {
		defer close(events)

		var (
			now        = time.Now().Unix()
			model      = effectiveModel(reqCtx, request)
			responseID = "chatcmpl_" + uuid.NewString()
			role       = models.ChatCompletionRoleAssistant
			text       = responseText(reqCtx, request)
			words      = strings.Fields(text)
		)

		if len(words) == 0 {
			words = []string{text}
		}

		for i, word := range words {
			delta := word
			if i < len(words)-1 {
				delta += " "
			}

			events <- StreamEvent{
				Chunk: &models.ChatCompletionChunk{
					ID:        responseID,
					Object:    models.ObjectChatCompletionChunk,
					CreatedAt: now,
					Model:     model,
					Choices: []models.ChatCompletionChunkChoice{
						{
							Index: 0,
							Delta: models.ChatCompletionChunkDelta{
								Role:    ptr.To(role),
								Content: ptr.To(delta),
							},
						},
					},
					ServiceTier: request.ChatRequest.ServiceTier,
				},
			}
		}

		finishReason := models.FinishReasonStop
		events <- StreamEvent{
			Chunk: &models.ChatCompletionChunk{
				ID:        responseID,
				Object:    models.ObjectChatCompletionChunk,
				CreatedAt: now,
				Model:     model,
				Choices: []models.ChatCompletionChunkChoice{
					{
						Index:        0,
						Delta:        models.ChatCompletionChunkDelta{},
						FinishReason: ptr.To(finishReason),
					},
				},
				Usage:       ptr.To(newUsage(request.InputTokens, len(text)/4)),
				ServiceTier: request.ChatRequest.ServiceTier,
			},
		}
	}()

	return events, nil
}

func effectiveModel(
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
) string {
	if request != nil && request.ChatRequest != nil && request.ChatRequest.Model != "" {
		return request.ChatRequest.Model
	}
	return ""
}

func responseText(
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
) string {
	switch {
	case request != nil && request.RenderedPrompt != nil && *request.RenderedPrompt != "":
		return "rendered prompt for routing key " + reqCtx.RoutingKey + ": " + *request.RenderedPrompt
	case request != nil && request.ChatRequest != nil && request.ChatRequest.Messages != nil:
		for i := len(*request.ChatRequest.Messages) - 1; i >= 0; i-- {
			msg := (*request.ChatRequest.Messages)[i]
			if msg.Role != models.ChatCompletionRoleUser {
				continue
			}

			text, err := msg.Content.TrySingleText()
			if err == nil && text != "" {
				return fmt.Sprintf("routing key %s received: %s", reqCtx.RoutingKey, text)
			}
		}
	}

	return "routing key " + reqCtx.RoutingKey + " completed"
}

func newUsage(inputTokens int, outputTokens int) models.ChatCompletionUsage {
	return models.ChatCompletionUsage{
		PromptTokens:     uint32(max(0, inputTokens)),
		CompletionTokens: uint32(max(0, outputTokens)),
		TotalTokens:      uint32(max(0, inputTokens+outputTokens)),
	}
}

func (p *EchoProvider) Proxy(
	_ context.Context,
	_ *requestctx.RequestContext,
	_ *ProxyRequest,
) (*ProxyResponse, error) {
	return nil, ErrProxyNotSupported
}
