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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/provider"
)

type UnaryResponseSender func(
	c *GatewayContext,
	response *models.ChatCompletionResponse,
	normalized *provider.NormalizedRequest,
) error

type StreamResponseSender func(
	c *GatewayContext,
	stream <-chan provider.StreamEvent,
	normalized *provider.NormalizedRequest,
) error

func (h *OpenAIChatHandlers) RegisterRoutes(group *echo.Group) {
	group.POST("/v1/chat/completions", h.CreateChatCompletion)
}

func (h *OpenAIChatHandlers) CreateChatCompletion(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)

	var request models.ChatCompletionRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return h.handleChatCompletionRequest(c, &request, nil, nil)
}

func (h *OpenAIChatHandlers) handleChatCompletionRequest(
	c *GatewayContext,
	request *models.ChatCompletionRequest,
	overrideUnaryResponseSender UnaryResponseSender,
	overrideStreamResponseSender StreamResponseSender,
) error {
	responseModel := request.Model
	normalized, err := h.handlers.normalizeChatRequest(c, request)
	if err != nil {
		return err
	}

	if ptr.Deref(request.Stream) {
		return h.streamChatCompletionWithSender(c, normalized, responseModel, overrideStreamResponseSender)
	}

	finalizeCtx := context.WithoutCancel(c.UserContext())
	var finalUsage *models.ChatCompletionUsage
	defer func() {
		h.handlers.finalizeTokenConsumption(finalizeCtx, normalized, finalUsage)
	}()

	response, err := h.handlers.provider.Complete(
		c.UserContext(),
		c.RequestContext(),
		normalized,
	)
	if err != nil {
		return providerHTTPError(err)
	}

	if usageHasTokenCounts(&response.Usage) {
		finalUsage = &response.Usage
	}
	if responseModel != "" {
		response.Model = responseModel
	}

	if overrideUnaryResponseSender != nil {
		return overrideUnaryResponseSender(c, response, normalized)
	}

	return c.JSON(http.StatusOK, response)
}

func (h *OpenAIChatHandlers) streamChatCompletion(
	c *GatewayContext,
	request *provider.NormalizedRequest,
) error {
	responseModel := ""
	if request != nil && request.ChatRequest != nil {
		responseModel = request.ChatRequest.Model
	}
	return h.streamChatCompletionWithSender(c, request, responseModel, nil)
}

func (h *OpenAIChatHandlers) streamChatCompletionWithSender(
	c *GatewayContext,
	request *provider.NormalizedRequest,
	responseModel string,
	overrideStreamResponseSender StreamResponseSender,
) error {
	finalizeCtx := context.WithoutCancel(c.UserContext())
	finalization := &streamFinalizationState{}
	defer func() {
		if finalization.IsFinalized() {
			return
		}
		h.handlers.releaseReservedTokenConsumption(finalizeCtx, request)
	}()

	stream, err := h.handlers.provider.Stream(
		c.UserContext(),
		c.RequestContext(),
		request,
	)
	if err != nil {
		return providerHTTPError(err)
	}

	wrappedStream := h.wrapStreamForFinalization(
		finalizeCtx,
		request,
		responseModel,
		stream,
		finalization,
	)
	if overrideStreamResponseSender != nil {
		return overrideStreamResponseSender(c, wrappedStream, request)
	}

	header := c.Response().Header()
	header.Set(echo.HeaderContentType, "text/event-stream")
	header.Set(echo.HeaderCacheControl, "no-cache")
	header.Set(echo.HeaderConnection, "keep-alive")
	c.Response().WriteHeader(http.StatusOK)

	for event := range wrappedStream {
		if event.Err != nil {
			if !errors.Is(event.Err, context.Canceled) {
				return echo.NewHTTPError(http.StatusBadGateway, event.Err.Error())
			}
			return nil
		}

		if event.Chunk == nil {
			continue
		}

		payload, err := json.Marshal(event.Chunk)
		if err != nil {
			return fmt.Errorf("marshal chat chunk: %w", err)
		}

		if _, err := c.Response().Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err := c.Response().Write(payload); err != nil {
			return err
		}
		if _, err := c.Response().Write([]byte("\n\n")); err != nil {
			return err
		}
		c.Response().Flush()
	}

	_, err = c.Response().Write([]byte("data: [DONE]\n\n"))
	if err == nil {
		c.Response().Flush()
	}

	return err
}

type streamFinalizationState struct {
	mu        sync.Mutex
	finalized bool
}

func (s *streamFinalizationState) MarkFinalized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.finalized {
		return false
	}

	s.finalized = true
	return true
}

func (s *streamFinalizationState) IsFinalized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.finalized
}

func (h *OpenAIChatHandlers) wrapStreamForFinalization(
	finalizeCtx context.Context,
	request *provider.NormalizedRequest,
	responseModel string,
	stream <-chan provider.StreamEvent,
	state *streamFinalizationState,
) <-chan provider.StreamEvent {
	if stream == nil {
		return nil
	}

	wrapped := make(chan provider.StreamEvent)
	go func() {
		defer close(wrapped)

		for event := range stream {
			if event.Chunk != nil && responseModel != "" {
				event.Chunk.Model = responseModel
			}
			if event.Chunk != nil && usageHasTokenCounts(event.Chunk.Usage) && state.MarkFinalized() {
				h.handlers.finalizeTokenConsumption(finalizeCtx, request, event.Chunk.Usage)
			}

			wrapped <- event
		}
	}()

	return wrapped
}
