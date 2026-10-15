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
	"net/http"

	echo "github.com/labstack/echo/v4"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

type Handlers struct {
	config        *config.Config
	provider      provider.InferenceProvider
	proxyProvider provider.OpenAIProxyProvider
	rateLimiter   ratelimit.RateLimiter
	limitResolver LimitResolver
	observability observabilityMetrics
}

type observabilityMetrics struct {
	llmTokens        otelmetric.Int64Counter
	providerTime     otelmetric.Float64Histogram
	streamFirstToken otelmetric.Float64Histogram
	streamDuration   otelmetric.Float64Histogram
}

func newObservabilityMetrics() observabilityMetrics {
	return observabilityMetrics{
		llmTokens:        telemetry.LLMTokens(),
		providerTime:     telemetry.ProviderTime(),
		streamFirstToken: telemetry.StreamFirstToken(),
		streamDuration:   telemetry.StreamDuration(),
	}
}

type HandlerOption func(*Handlers)

func WithLimitResolver(resolver LimitResolver) HandlerOption {
	return func(h *Handlers) {
		h.limitResolver = resolver
	}
}

type OpenAIChatHandlers struct {
	handlers *Handlers
}

type ResponsesHandlers struct {
	handlers *Handlers
}

type OpenAIProxyHandlers struct {
	handlers *Handlers
}

func NewHandlers(
	cfg *config.Config,
	p provider.InferenceProvider,
	limiter ratelimit.RateLimiter,
	opts ...HandlerOption,
) *Handlers {
	h := &Handlers{
		config:        cfg,
		provider:      p,
		rateLimiter:   limiter,
		limitResolver: CallerLimitResolver{},
		observability: newObservabilityMetrics(),
	}
	if proxyProvider, ok := any(p).(provider.OpenAIProxyProvider); ok {
		h.proxyProvider = proxyProvider
	}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return h
}

func (h *Handlers) AsOpenAIChatHandlers() *OpenAIChatHandlers {
	return &OpenAIChatHandlers{handlers: h}
}

func (h *Handlers) AsResponsesHandlers() *ResponsesHandlers {
	return &ResponsesHandlers{handlers: h}
}

func (h *Handlers) AsOpenAIProxyHandlers() *OpenAIProxyHandlers {
	return &OpenAIProxyHandlers{handlers: h}
}

func (h *Handlers) normalizeChatRequest(
	c *GatewayContext,
	request *models.ChatCompletionRequest,
) (*provider.NormalizedRequest, error) {
	reqCtx := c.RequestContext()
	if reqCtx == nil {
		return nil, echo.NewHTTPError(
			http.StatusBadRequest,
			"model prefix is required",
		)
	}

	if request.Messages == nil || len(*request.Messages) == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "messages is required")
	}

	routedModel, err := normalizeOpenAIRequestModel(reqCtx, request.Model)
	if err != nil {
		return nil, err
	}
	request.Model = routedModel
	reqCtx.Model = routedModel
	setRoutingMethodForModel(reqCtx, routedModel)

	if !request.ServiceTier.IsValid() {
		request.ServiceTier = h.config.DefaultServiceTier
	}

	estimatedInputTokens := estimatedInputTokensForNormalizedRequest(
		request.Model,
		request,
	)
	inputTokens := estimatedInputTokens
	maxOutputTokens := maxOutputTokensForRequest(request)
	checkRequest := ratelimit.ResourceRequest{
		Requests:     1,
		InputTokens:  int64(estimatedInputTokens),
		OutputTokens: int64(maxOutputTokens),
	}
	consumeRequest := ratelimit.ResourceRequest{
		Requests:     1,
		InputTokens:  int64(inputTokens),
		OutputTokens: int64(maxOutputTokens),
	}
	admissionPlan, err := NewAdmissionPlan(
		c,
		reqCtx,
		c.Request().URL.Path,
		h.limitResolver,
		h.rateLimiter,
		checkRequest,
		consumeRequest,
	)
	if err != nil {
		return nil, err
	}
	if admissionPlan != nil {
		defer admissionPlan.Close()
	}

	if admissionPlan != nil {
		if err := admissionPlan.CheckRequests(c.UserContext()); err != nil {
			return nil, err
		}
		if _, err := admissionPlan.CheckTokensAndFinalize(c.UserContext()); err != nil {
			return nil, err
		}
	}

	return &provider.NormalizedRequest{
		ChatRequest:     request,
		InputTokens:     inputTokens,
		MaxOutputTokens: maxOutputTokens,
		AdmissionPlan:   admissionPlan,
	}, nil
}

func (h *Handlers) finalizeTokenConsumption(
	ctx context.Context,
	request *provider.NormalizedRequest,
	usage *models.ChatCompletionUsage,
) {
	if request == nil || request.AdmissionPlan == nil {
		return
	}

	if !usageHasTokenCounts(usage) {
		h.releaseReservedTokenConsumption(ctx, request)
		return
	}

	inputTokens := int64(request.InputTokens)
	if usage.PromptTokens > 0 {
		inputTokens = int64(usage.PromptTokens)
	}
	resourceRequest := ratelimit.ResourceRequest{
		InputTokens:  inputTokens,
		OutputTokens: int64(usage.CompletionTokens),
	}

	if _, err := request.AdmissionPlan.FinalizeTokens(ctx, resourceRequest); err != nil {
		telemetry.Logger(ctx).
			Error().
			Err(err).
			Int64("input_tokens", resourceRequest.InputTokens).
			Int64("output_tokens", resourceRequest.OutputTokens).
			Msg("failed to finalize token consumption")
	}
}

func (h *Handlers) releaseReservedTokenConsumption(
	ctx context.Context,
	request *provider.NormalizedRequest,
) {
	if request == nil || request.AdmissionPlan == nil {
		return
	}

	if _, err := request.AdmissionPlan.ReleaseOutputReservation(ctx); err != nil {
		telemetry.Logger(ctx).
			Error().
			Msg("failed to release reserved output token consumption")
	}
}

func usageHasTokenCounts(usage *models.ChatCompletionUsage) bool {
	if usage == nil {
		return false
	}

	return usage.TotalTokens > 0 ||
		usage.PromptTokens > 0 ||
		usage.CompletionTokens > 0
}
