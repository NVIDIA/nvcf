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
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	echo "github.com/labstack/echo/v4"

	openairesponses "github.com/NVIDIA/nvcf/llm-api-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/provider"
)

func (h *ResponsesHandlers) RegisterRoutes(group *echo.Group) {
	group.POST("/v1/responses", h.CreateResponse)
}

func (h *ResponsesHandlers) CreateResponse(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)

	var request openairesponses.CreateRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := request.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	chatRequest := ConvertToChatCompletionRequest(&request)
	return h.handlers.AsOpenAIChatHandlers().handleChatCompletionRequest(
		c,
		chatRequest,
		h.createUnaryResponseSender(&request),
		h.createStreamResponseSender(&request),
	)
}

func (h *ResponsesHandlers) createUnaryResponseSender(
	request *openairesponses.CreateRequest,
) UnaryResponseSender {
	return func(
		c *GatewayContext,
		chatResponse *models.ChatCompletionResponse,
		_ *provider.NormalizedRequest,
	) error {
		return c.JSON(http.StatusOK, responseFromChat(chatResponse, request))
	}
}

func (h *ResponsesHandlers) createStreamResponseSender(
	request *openairesponses.CreateRequest,
) StreamResponseSender {
	return func(
		c *GatewayContext,
		stream <-chan provider.StreamEvent,
		normalized *provider.NormalizedRequest,
	) error {
		return h.streamResponse(c, request, normalized, stream)
	}
}

func (h *ResponsesHandlers) streamResponse(
	c *GatewayContext,
	request *openairesponses.CreateRequest,
	_ *provider.NormalizedRequest,
	stream <-chan provider.StreamEvent,
) error {
	writer, err := openairesponses.NewSSEWriter(c.UserContext(), c.Response().Writer)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	defer writer.Close()

	response := &openairesponses.Response{
		ID:         "resp_" + uuid.NewString(),
		Object:     openairesponses.ObjectResponse,
		Status:     openairesponses.StatusInProgress,
		CreatedAt:  time.Now().Unix(),
		Model:      ptr.To(request.Model),
		Output:     []openairesponses.OutputItem{},
		Background: ptr.To(false),
		Store:      request.Store,
	}

	messageID := "msg_" + uuid.NewString()
	contentIndex := 0
	outputIndex := 0
	statusInProgress := openairesponses.StatusInProgress
	statusCompleted := openairesponses.StatusCompleted
	outputItem := openairesponses.OutputItem{
		Type:   openairesponses.ItemTypeMessage,
		ID:     messageID,
		Role:   openairesponses.RoleAssistant,
		Status: &statusInProgress,
		Content: []openairesponses.OutputContent{
			{
				Type: openairesponses.ContentTypeOutputText,
				Text: "",
			},
		},
	}

	if err := writer.SendResponseCreated(response); err != nil {
		return err
	}
	if err := writer.SendResponseInProgress(response); err != nil {
		return err
	}
	if err := writer.SendOutputItemAdded(outputIndex, outputItem); err != nil {
		return err
	}
	if err := writer.SendContentPartAdded(
		messageID,
		outputIndex,
		contentIndex,
		outputItem.Content[0],
	); err != nil {
		return err
	}

	var (
		textBuilder strings.Builder
	)

	for event := range stream {
		if event.Err != nil {
			_ = writer.SendError(nil, event.Err.Error(), nil)
			response.Status = openairesponses.StatusFailed
			_ = writer.SendResponseFailed(response)
			return nil
		}

		if event.Chunk == nil {
			continue
		}

		for _, choice := range event.Chunk.Choices {
			if choice.Delta.Content != nil {
				textBuilder.WriteString(*choice.Delta.Content)
				if err := writer.SendOutputTextDelta(
					messageID,
					outputIndex,
					contentIndex,
					*choice.Delta.Content,
				); err != nil {
					return err
				}
			}
		}

		if usageHasTokenCounts(event.Chunk.Usage) {
			usage := *event.Chunk.Usage
			response.Usage = responseUsageFromChat(usage)
		}
	}

	finalText := textBuilder.String()
	outputItem.Status = &statusCompleted
	outputItem.Content[0].Text = finalText
	response.Output = []openairesponses.OutputItem{outputItem}
	response.Status = openairesponses.StatusCompleted

	if err := writer.SendOutputTextDone(
		messageID,
		outputIndex,
		contentIndex,
		finalText,
	); err != nil {
		return err
	}
	if err := writer.SendContentPartDone(
		messageID,
		outputIndex,
		contentIndex,
		outputItem.Content[0],
	); err != nil {
		return err
	}
	if err := writer.SendOutputItemDone(outputIndex, outputItem); err != nil {
		return err
	}

	return writer.SendResponseCompleted(response)
}

func responseFromChat(
	chatResponse *models.ChatCompletionResponse,
	request *openairesponses.CreateRequest,
) *openairesponses.Response {
	response := &openairesponses.Response{
		ID:         "resp_" + uuid.NewString(),
		Object:     openairesponses.ObjectResponse,
		Status:     openairesponses.StatusCompleted,
		CreatedAt:  chatResponse.CreatedAt,
		Model:      ptr.To(chatResponse.Model),
		Output:     []openairesponses.OutputItem{},
		Background: ptr.To(false),
		Store:      request.Store,
		Tools:      request.Tools,
		ToolChoice: request.ToolChoice,
		Text:       request.Text,
		Usage:      responseUsageFromChat(chatResponse.Usage),
	}

	if len(chatResponse.Choices) == 0 {
		return response
	}

	choice := chatResponse.Choices[0]
	if choice.Message.ToolCalls != nil {
		for _, toolCall := range *choice.Message.ToolCalls {
			status := openairesponses.StatusCompleted
			response.Output = append(response.Output, openairesponses.OutputItem{
				Type:   openairesponses.ItemTypeFunctionCall,
				ID:     "fc_" + uuid.NewString(),
				Status: &status,
				ToolCallData: openairesponses.FunctionToolCall{
					Type:      openairesponses.ItemTypeFunctionCall,
					CallID:    toolCall.ID,
					Name:      toolCall.Function.Name,
					Arguments: toolCall.Function.Arguments,
					Status:    &status,
				},
			})
		}
	}

	if choice.Message.FunctionCall != nil {
		status := openairesponses.StatusCompleted
		response.Output = append(response.Output, openairesponses.OutputItem{
			Type:   openairesponses.ItemTypeFunctionCall,
			ID:     "fc_" + uuid.NewString(),
			Status: &status,
			ToolCallData: openairesponses.FunctionToolCall{
				Type:      openairesponses.ItemTypeFunctionCall,
				CallID:    "call_" + uuid.NewString(),
				Name:      choice.Message.FunctionCall.Name,
				Arguments: choice.Message.FunctionCall.Arguments,
				Status:    &status,
			},
		})
	}

	message := openairesponses.OutputItem{
		Type: openairesponses.ItemTypeMessage,
		ID:   "msg_" + uuid.NewString(),
		Role: openairesponses.RoleAssistant,
		Content: []openairesponses.OutputContent{
			{
				Type: openairesponses.ContentTypeOutputText,
				Text: ptr.Deref(choice.Message.Content),
			},
		},
	}
	response.Output = append(response.Output, message)

	return response
}

func responseUsageFromChat(
	usage models.ChatCompletionUsage,
) *openairesponses.ResponseUsage {
	var cachedTokens int
	if usage.PromptTokensDetails != nil {
		cachedTokens = int(usage.PromptTokensDetails.CachedTokens)
	}

	var reasoningTokens int
	if usage.CompletionTokensDetails != nil {
		reasoningTokens = int(usage.CompletionTokensDetails.ReasoningTokens)
	}

	return &openairesponses.ResponseUsage{
		InputTokens:  int(usage.PromptTokens),
		OutputTokens: int(usage.CompletionTokens),
		TotalTokens:  int(usage.TotalTokens),
		InputTokensDetails: openairesponses.InputTokensDetails{
			CachedTokens: cachedTokens,
		},
		OutputTokensDetails: openairesponses.OutputTokensDetails{
			ReasoningTokens: reasoningTokens,
		},
	}
}
