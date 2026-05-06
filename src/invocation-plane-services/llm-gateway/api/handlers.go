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

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/provider"
	"github.com/NVIDIA/nvcf/llm-api-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
	"github.com/NVIDIA/nvcf/llm-api-gateway/telemetry"
	templatingtools "github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

type Handlers struct {
	config        *config.Config
	provider      provider.InferenceProvider
	proxyProvider provider.OpenAIProxyProvider
	rateLimiter   ratelimit.RateLimiter
	templater     TemplateEngine
	tokenCounter  TokenCounter
	tokenizers    TokenizerProvider
	limitResolver LimitResolver
}

type TemplateEngine interface {
	GetTextTemplate(string) (TextTemplate, error)
}

type TextTemplate interface {
	RenderText([]models.ChatMessage, templatingtools.Params) (models.ChatMessageContent, error)
}

type TokenCounter interface {
	TokenCountForText(model string, text string) (int, error)
	TokenCountForRequest(model string, messages *[]models.ChatMessage) (int, error)
}

type TokenizerProvider interface {
	TokenizerForModel(model string) (Tokenizer, error)
}

type Tokenizer interface {
	Encode(text string) ([]uint32, error)
	Decode(ids []uint32, skipSpecial bool) (string, error)
}

type HandlerOption func(*Handlers)

func WithTokenCounter(counter TokenCounter) HandlerOption {
	return func(h *Handlers) {
		h.tokenCounter = counter
		if provider, ok := counter.(TokenizerProvider); ok {
			h.tokenizers = provider
		}
	}
}

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
	templater TemplateEngine,
	opts ...HandlerOption,
) *Handlers {
	h := &Handlers{
		config:      cfg,
		provider:    p,
		rateLimiter: limiter,
		templater:   templater,
		limitResolver: CallerLimitResolver{},
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

	if !request.ServiceTier.IsValid() {
		request.ServiceTier = h.config.DefaultServiceTier
	}

	renderedPrompt, err := h.renderPromptFromContext(reqCtx, request)
	if err != nil {
		return nil, err
	}

	estimatedInputTokens := estimatedInputTokensForNormalizedRequest(
		request.Model,
		request.Messages,
		renderedPrompt,
	)
	inputTokens := h.countInputTokensForNormalizedRequest(
		c.UserContext(),
		reqCtx,
		request,
		renderedPrompt,
		estimatedInputTokens,
	)
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
		RenderedPrompt:  renderedPrompt,
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

func (h *Handlers) renderPromptFromContext(
	reqCtx *requestctx.RequestContext,
	request *models.ChatCompletionRequest,
) (*string, error) {
	templateStr := ""
	if reqCtx != nil && h.config != nil && h.config.ModelTemplates != nil {
		templateStr = h.config.ModelTemplates[reqCtx.Model]
	}
	if h.templater == nil || reqCtx == nil || templateStr == "" || request.Messages == nil {
		return nil, nil
	}

	template, err := h.templater.GetTextTemplate(templateStr)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	rendered, err := template.RenderText(
		*request.Messages,
		templatingtools.Params{
			ToolChoice:        request.ToolChoice,
			Tools:             toTemplatingTools(request.Tools),
			ParallelToolCalls: ptr.Deref(request.ParallelToolCalls),
			EnableThinking:    request.IncludeReasoning,
			ReasoningEffort:   ptr.Deref(request.ReasoningEffort),
		},
	)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	content := models.ChatMessageContent(rendered)
	text, err := content.TrySingleText()
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return ptr.To(text), nil
}

func (h *Handlers) countInputTokensForNormalizedRequest(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	request *models.ChatCompletionRequest,
	renderedPrompt *string,
	fallback int,
) int {
	if h == nil || h.tokenCounter == nil || reqCtx == nil {
		return fallback
	}

	var model string
	if spec, ok := reqCtx.ModelSpecs[request.Model]; ok && spec.Tokenizer != "" {
		model = spec.Tokenizer
	}
	if model == "" && h.config != nil {
		model = h.config.DefaultTokenizer
	}
	if model == "" {
		model = request.Model
	}
	if model == "" {
		return fallback
	}

	renderedText := ptr.Deref(renderedPrompt)
	if renderedText != "" {
		count, err := h.tokenCounter.TokenCountForText(model, renderedText)
		if err == nil {
			return count
		}
		telemetry.Logger(ctx).
			Warn().
			Err(err).
			Str("tokenizer", model).
			Msg("failed to tokenize rendered prompt, using estimated input tokens")
		return fallback
	}

	count, err := h.tokenCounter.TokenCountForRequest(model, request.Messages)
	if err == nil {
		return count
	}
	telemetry.Logger(ctx).
		Warn().
		Err(err).
		Str("tokenizer", model).
		Msg("failed to tokenize chat messages, using estimated input tokens")
	return fallback
}

func toTemplatingTools(chatTools *[]models.ChatTool) []templatingtools.Tool {
	if chatTools == nil {
		return nil
	}

	tools := make([]templatingtools.Tool, 0, len(*chatTools))
	for _, tool := range *chatTools {
		templatingTool := templatingtools.Tool{
			Type: tool.Type,
			Function: templatingtools.ChatFunctionSpec{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Allowed:     true,
			},
		}
		if tool.Function.Parameters != nil {
			templatingTool.Function.Parameters = *tool.Function.Parameters
		}
		tools = append(tools, templatingTool)
	}

	return tools
}
