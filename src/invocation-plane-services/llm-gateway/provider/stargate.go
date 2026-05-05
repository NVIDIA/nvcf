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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/servicetier"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
)

const (
	stargateChatCompletionsPath = "/v1/chat/completions"

	headerAuthorization = "Authorization"
	headerContentType   = "Content-Type"
	headerAccept        = "Accept"
	headerRequestID     = "X-Request-Id"
	headerTargetRegion  = "X-Groq-Region"
	headerRoutingKey    = "X-Routing-Key"
	headerModel         = "X-Model"
	headerInputTokens   = "X-Input-Tokens"
	headerTokenEstimate = "X-Token-Estimate"

	contentTypeJSON = "application/json"
	contentTypeSSE  = "text/event-stream"

	sseMaxToken = 4 * 1024 * 1024
)

type StargateProvider struct {
	baseURL        *url.URL
	client         *http.Client
	requestTimeout time.Duration
}

func NewStargateProvider(cfg config.StargateConfig) (*StargateProvider, error) {
	baseURL := cfg.URL
	if baseURL == "" {
		return nil, errors.New("stargate url is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse stargate url: %w", err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("stargate url must include scheme and host: %q", baseURL)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ConnectTimeout > 0 {
		dialer := &net.Dialer{
			Timeout:   cfg.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}
		transport.DialContext = dialer.DialContext
	}

	return &StargateProvider{
		baseURL:        parsed,
		client:         &http.Client{Transport: transport},
		requestTimeout: cfg.RequestTimeout,
	}, nil
}

func (p *StargateProvider) Complete(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
) (*models.ChatCompletionResponse, error) {
	outbound, err := p.newOutboundRequest(reqCtx, request, false)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := p.requestContext(ctx)
	defer cancel()

	resp, err := p.client.Do(outbound.WithContext(requestCtx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkHTTPError(resp); err != nil {
		return nil, err
	}

	var response models.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode stargate completion response: %w", err)
	}

	return &response, nil
}

func (p *StargateProvider) Stream(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
) (<-chan StreamEvent, error) {
	outbound, err := p.newOutboundRequest(reqCtx, request, true)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := p.requestContext(ctx)
	resp, err := p.client.Do(outbound.WithContext(requestCtx))
	if err != nil {
		cancel()
		return nil, err
	}

	if err := checkHTTPError(resp); err != nil {
		cancel()
		resp.Body.Close()
		return nil, err
	}

	events := make(chan StreamEvent, 8)
	go p.readStream(requestCtx, cancel, resp.Body, events)

	return events, nil
}

func (p *StargateProvider) requestContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	if p.requestTimeout > 0 {
		return context.WithTimeout(ctx, p.requestTimeout)
	}
	return ctx, func() {}
}

func (p *StargateProvider) newOutboundRequest(
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
	stream bool,
) (*http.Request, error) {
	if reqCtx == nil {
		return nil, errors.New("request context is required")
	}
	if request == nil || request.ChatRequest == nil {
		return nil, errors.New("normalized chat request is required")
	}

	payload := outboundChatRequest(reqCtx, request, stream)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal stargate request: %w", err)
	}

	target := p.baseURL.ResolveReference(&url.URL{Path: stargateChatCompletionsPath})
	req, err := http.NewRequest(http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build stargate request: %w", err)
	}

	req.Header.Set(headerContentType, contentTypeJSON)
	req.Header.Set(headerAccept, contentTypeJSON)
	if stream {
		req.Header.Set(headerAccept, contentTypeSSE)
	}

	if reqCtx.RequestID != "" {
		req.Header.Set(headerRequestID, reqCtx.RequestID)
	}
	if reqCtx.TargetRegion != "" {
		req.Header.Set(headerTargetRegion, reqCtx.TargetRegion)
	}
	if reqCtx.RoutingKey != "" {
		req.Header.Set(headerRoutingKey, reqCtx.RoutingKey)
	}
	if bearerToken := reqCtx.BearerToken; bearerToken != "" {
		req.Header.Set(headerAuthorization, "Bearer "+bearerToken)
	}

	model := effectiveModel(reqCtx, request)
	if model != "" {
		req.Header.Set(headerModel, model)
	}

	inputTokens := routingTokenEstimate(request)
	req.Header.Set(headerInputTokens, strconv.Itoa(inputTokens))
	req.Header.Set(headerTokenEstimate, strconv.Itoa(inputTokens))

	return req, nil
}

func (p *StargateProvider) Proxy(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	request *ProxyRequest,
) (*ProxyResponse, error) {
	if request == nil {
		return nil, errors.New("proxy request is required")
	}
	if request.Path == "" {
		return nil, errors.New("proxy path is required")
	}

	requestCtx, cancel := p.requestContext(ctx)
	target := p.baseURL.ResolveReference(&url.URL{
		Path:     request.Path,
		RawQuery: request.RawQuery,
	})
	outbound, err := http.NewRequestWithContext(
		requestCtx,
		request.Method,
		target.String(),
		request.Body,
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build stargate proxy request: %w", err)
	}

	if request.Header != nil {
		outbound.Header = request.Header.Clone()
	}

	if reqCtx != nil {
		if reqCtx.RequestID != "" {
			outbound.Header.Set(headerRequestID, reqCtx.RequestID)
		}
		if reqCtx.TargetRegion != "" {
			outbound.Header.Set(headerTargetRegion, reqCtx.TargetRegion)
		}
		if reqCtx.RoutingKey != "" {
			outbound.Header.Set(headerRoutingKey, reqCtx.RoutingKey)
		}
		if reqCtx.Model != "" {
			outbound.Header.Set(headerModel, reqCtx.Model)
		}
		if bearerToken := reqCtx.BearerToken; bearerToken != "" {
			outbound.Header.Set(headerAuthorization, "Bearer "+bearerToken)
		}
	}

	resp, err := p.client.Do(outbound)
	if err != nil {
		cancel()
		return nil, err
	}

	return &ProxyResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       &proxyResponseBody{ReadCloser: resp.Body, cancel: cancel},
	}, nil
}

type proxyResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *proxyResponseBody) Close() error {
	if b == nil {
		return nil
	}
	if b.cancel != nil {
		defer b.cancel()
	}
	if b.ReadCloser == nil {
		return nil
	}
	return b.ReadCloser.Close()
}

func outboundChatRequest(
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
	stream bool,
) *models.ChatCompletionRequest {
	outbound := *request.ChatRequest
	outbound.Stream = ptr.To(stream)
	if !outbound.ServiceTier.IsValid() {
		outbound.ServiceTier = servicetier.Auto
	}

	renderedPrompt := ptr.Deref(request.RenderedPrompt)
	if renderedPrompt == "" {
		return &outbound
	}

	outbound.Messages = &[]models.ChatMessage{
		{
			Role:    models.ChatCompletionRoleUser,
			Content: models.SingleTextContent(renderedPrompt),
		},
	}
	outbound.Tools = nil
	outbound.Functions = nil
	outbound.ToolChoice = models.ChatCompletionToolChoiceField{}
	outbound.FunctionChoice = models.ChatCompletionFunctionChoiceField{}
	outbound.ParallelToolCalls = nil

	return &outbound
}

func routingTokenEstimate(request *NormalizedRequest) int {
	if request == nil {
		return 0
	}

	estimate := request.InputTokens
	renderedPrompt := ptr.Deref(request.RenderedPrompt)
	if renderedPrompt == "" {
		return max(0, estimate)
	}

	// A rendered prompt is sent as a single chat message downstream; keep the
	// routing hint conservative so Stargate does not under-estimate prefill work.
	renderedEstimate := 5 + int(math.Ceil(float64(len(renderedPrompt))/4.0))
	return max(estimate, renderedEstimate)
}

func checkHTTPError(resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	message := string(body)

	var errorResponse models.ErrorResponse
	if err := json.Unmarshal(body, &errorResponse); err == nil && errorResponse.Error.Message != "" {
		message = errorResponse.Error.Message
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}

	return echo.NewHTTPError(resp.StatusCode, message)
}

func (p *StargateProvider) readStream(
	ctx context.Context,
	cancel context.CancelFunc,
	body io.ReadCloser,
	events chan<- StreamEvent,
) {
	defer close(events)
	defer body.Close()
	defer cancel()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), sseMaxToken)

	var dataLines []string
	emit := func() bool {
		if len(dataLines) == 0 {
			return true
		}

		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]

		if payload == "[DONE]" {
			return false
		}

		var chunk models.ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			events <- StreamEvent{
				Err: fmt.Errorf("decode stargate stream chunk: %w", err),
			}
			return false
		}

		select {
		case <-ctx.Done():
			return false
		case events <- StreamEvent{Chunk: &chunk}:
			return true
		}
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if !emit() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimLeft(line[len("data:"):], " "))
		}
	}

	if len(dataLines) > 0 {
		_ = emit()
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(ctx.Err(), context.Canceled) {
		events <- StreamEvent{
			Err: fmt.Errorf("read stargate stream: %w", err),
		}
	}
}
