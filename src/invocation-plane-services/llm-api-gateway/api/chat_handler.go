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
	"time"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
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
	if err := applyChatSessionAffinity(c, request); err != nil {
		return err
	}
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
		h.handlers.observability.recordLLMUsage(c.UserContext(), endpointLabel(c), finalUsage, false)
	}
	if responseModel != "" {
		response.Model = responseModel
	}
	setMultiTurnSessionResponseHeader(c)

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
	setMultiTurnSessionResponseHeader(c)

	wrappedStream := h.wrapStreamForFinalization(
		finalizeCtx,
		c.UserContext(),
		endpointLabel(c),
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
	streamCtx context.Context,
	endpoint string,
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
		ctx, span := telemetry.Tracer().Start(streamCtx, "llm-api-gateway.stream")
		defer span.End()
		span.SetAttributes(attribute.String("endpoint", endpoint))

		streamStart := time.Now()
		firstTokenRecorded := false
		streamStatus := "completed"
		observation := streamObservation{
			ctx:                context.WithoutCancel(ctx),
			finalizeCtx:        finalizeCtx,
			endpoint:           endpoint,
			metrics:            h.handlers.observability,
			request:            request,
			firstTokenRecorded: &firstTokenRecorded,
			streamStart:        streamStart,
			state:              state,
			span:               span,
		}
		defer func() {
			telemetry.RecordWithContext(
				context.WithoutCancel(ctx),
				observation.metrics.streamDuration,
				time.Since(streamStart).Seconds(),
				attribute.String("endpoint", endpoint),
				attribute.String("status", streamStatus),
			)
		}()

		for event := range stream {
			setChunkResponseModel(event.Chunk, responseModel)
			if status, ok := h.observeStreamEvent(observation, event); ok {
				streamStatus = status
			}

			wrapped <- event
		}
	}()

	return wrapped
}

type streamObservation struct {
	ctx                context.Context
	finalizeCtx        context.Context
	endpoint           string
	metrics            observabilityMetrics
	request            *provider.NormalizedRequest
	firstTokenRecorded *bool
	streamStart        time.Time
	state              *streamFinalizationState
	span               trace.Span
}

func (h *OpenAIChatHandlers) observeStreamEvent(obs streamObservation, event provider.StreamEvent) (string, bool) {
	streamStatus, hasStatus := streamEventStatus(event.Err, obs.span)
	recordFirstTokenIfNeeded(obs.ctx, obs.endpoint, obs.metrics, event.Chunk, obs.firstTokenRecorded, obs.streamStart)
	h.finalizeStreamUsageIfNeeded(obs.ctx, obs.finalizeCtx, obs.endpoint, obs.request, event.Chunk, obs.state)
	return streamStatus, hasStatus
}

func setChunkResponseModel(chunk *models.ChatCompletionChunk, responseModel string) {
	if chunk != nil && responseModel != "" {
		chunk.Model = responseModel
	}
}

func streamEventStatus(err error, span trace.Span) (string, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", true
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, "stream failed")
	return "error", true
}

func recordFirstTokenIfNeeded(
	ctx context.Context,
	endpoint string,
	metrics observabilityMetrics,
	chunk *models.ChatCompletionChunk,
	firstTokenRecorded *bool,
	streamStart time.Time,
) {
	if chunk == nil || *firstTokenRecorded || !chunkHasContent(chunk) {
		return
	}
	*firstTokenRecorded = true
	telemetry.RecordWithContext(
		ctx,
		metrics.streamFirstToken,
		time.Since(streamStart).Seconds(),
		attribute.String("endpoint", endpoint),
	)
}

func (h *OpenAIChatHandlers) finalizeStreamUsageIfNeeded(
	ctx context.Context,
	finalizeCtx context.Context,
	endpoint string,
	request *provider.NormalizedRequest,
	chunk *models.ChatCompletionChunk,
	state *streamFinalizationState,
) {
	if chunk == nil || !usageHasTokenCounts(chunk.Usage) || !state.MarkFinalized() {
		return
	}
	h.handlers.observability.recordLLMUsage(ctx, endpoint, chunk.Usage, true)
	h.handlers.finalizeTokenConsumption(finalizeCtx, request, chunk.Usage)
}

func chunkHasContent(chunk *models.ChatCompletionChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			return true
		}
	}
	return false
}
