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
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"

	openairesponses "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	responsesEndpointPath       = "/v1/responses"
	headerResponsesInput        = "X-Input-Tokens"
	headerResponsesEstimate     = "X-Token-Estimate"
	headerResponsesRequest      = "X-Request-Id"
	headerResponsesRegion       = "X-NVCF-Target-Region"
	headerResponsesLegacyRegion = "X-Groq-Region"
	headerResponsesRouting      = "X-Routing-Key"
	headerResponsesMethod       = "X-Routing-Method"
	headerResponsesModel        = "X-Model"
	headerResponsesAffinity     = "X-Cache-Affinity-Key"
)

func (h *ResponsesHandlers) RegisterRoutes(group *echo.Group) {
	group.POST("/v1/responses", h.CreateResponse)
}

func (h *ResponsesHandlers) CreateResponse(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)

	body, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var request openairesponses.CreateRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := request.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := applyResponsesSessionAffinity(c, &request); err != nil {
		return err
	}

	clientWantsStream := ptr.Deref(request.Stream)
	normalized, outboundBody, err := h.prepareNativeResponsesRequest(c, &request, body)
	if err != nil {
		return err
	}

	resp, err := h.dispatchNativeResponsesRequest(
		c,
		normalized,
		outboundBody,
	)
	if err != nil {
		return err
	}
	if resp == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "proxy provider returned no response")
	}

	if clientWantsStream {
		return h.relayNativeResponsesStream(c, normalized, resp)
	}
	return h.aggregateNativeResponsesStream(c, normalized, resp)
}

func (h *ResponsesHandlers) prepareNativeResponsesRequest(
	c *GatewayContext,
	request *openairesponses.CreateRequest,
	body []byte,
) (*provider.NormalizedRequest, []byte, error) {
	reqCtx := c.RequestContext()
	if reqCtx == nil {
		return nil, nil, echo.NewHTTPError(
			http.StatusBadRequest,
			"model prefix is required",
		)
	}

	routedModel, err := normalizeOpenAIRequestModel(reqCtx, request.Model)
	if err != nil {
		return nil, nil, err
	}
	request.Model = routedModel
	reqCtx.Model = routedModel
	setRoutingMethodForModel(reqCtx, routedModel)

	if err := requireResponsesURI(reqCtx, routedModel); err != nil {
		return nil, nil, err
	}

	countRequest := ConvertToChatCompletionRequest(request)
	estimatedInputTokens := estimatedInputTokensForNormalizedRequest(
		countRequest.Model,
		countRequest,
	)
	inputTokens := estimatedInputTokens
	maxOutputTokens := maxOutputTokensForRequest(countRequest)
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
		h.handlers.limitResolver,
		h.handlers.rateLimiter,
		checkRequest,
		consumeRequest,
	)
	if err != nil {
		return nil, nil, err
	}
	if admissionPlan != nil {
		defer admissionPlan.Close()
		if err := admissionPlan.CheckRequests(c.UserContext()); err != nil {
			return nil, nil, err
		}
		if _, err := admissionPlan.CheckTokensAndFinalize(c.UserContext()); err != nil {
			return nil, nil, err
		}
	}

	outboundBody, err := rewriteResponsesProxyBody(body, routedModel, true)
	if err != nil {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return &provider.NormalizedRequest{
		ChatRequest:     countRequest,
		InputTokens:     inputTokens,
		MaxOutputTokens: maxOutputTokens,
		AdmissionPlan:   admissionPlan,
	}, outboundBody, nil
}

func requireResponsesURI(reqCtx *requestctx.RequestContext, model string) error {
	if reqCtx == nil || reqCtx.ModelSpecs == nil {
		return nil
	}

	spec, ok := reqCtx.ModelSpecs[model]
	if !ok || len(spec.URIs) == 0 {
		return nil
	}

	for _, uri := range spec.URIs {
		if normalizeModelURI(uri) == responsesEndpointPath {
			return nil
		}
	}

	return echo.NewHTTPError(
		http.StatusBadRequest,
		fmt.Sprintf("model %q does not support %s", model, responsesEndpointPath),
	)
}

func normalizeModelURI(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	return "/" + strings.TrimPrefix(uri, "/")
}

func rewriteResponsesProxyBody(body []byte, model string, stream bool) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal responses request: %w", err)
	}

	encodedModel, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("marshal responses model: %w", err)
	}
	encodedStream, err := json.Marshal(stream)
	if err != nil {
		return nil, fmt.Errorf("marshal responses stream flag: %w", err)
	}

	payload["model"] = encodedModel
	payload["stream"] = encodedStream
	return json.Marshal(payload)
}

func (h *ResponsesHandlers) dispatchNativeResponsesRequest(
	c *GatewayContext,
	request *provider.NormalizedRequest,
	body []byte,
) (*provider.ProxyResponse, error) {
	if h.handlers.proxyProvider == nil {
		return nil, echo.NewHTTPError(http.StatusNotImplemented, "proxy endpoint is not configured")
	}

	headers := c.Request().Header.Clone()
	headers.Set(echo.HeaderContentLength, strconv.Itoa(len(body)))
	headers.Set(headerResponsesInput, strconv.Itoa(request.InputTokens))
	headers.Set(
		headerResponsesEstimate,
		strconv.Itoa(request.InputTokens+request.MaxOutputTokens),
	)
	setResponsesProxyContextHeaders(headers, c.RequestContext())

	resp, err := h.handlers.proxyProvider.Proxy(
		c.UserContext(),
		c.RequestContext(),
		&provider.ProxyRequest{
			Method:        c.Request().Method,
			Path:          stargatePath(c.Request().URL.Path),
			RawQuery:      c.Request().URL.RawQuery,
			Header:        headers,
			Body:          io.NopCloser(bytes.NewReader(body)),
			InputTokens:   request.InputTokens,
			TokenEstimate: request.InputTokens + request.MaxOutputTokens,
		},
	)
	if err != nil {
		if errors.Is(err, provider.ErrProxyNotSupported) {
			return nil, echo.NewHTTPError(http.StatusNotImplemented, err.Error())
		}
		return nil, providerHTTPError(err)
	}
	return resp, nil
}

func setResponsesProxyContextHeaders(headers http.Header, reqCtx *requestctx.RequestContext) {
	if headers == nil || reqCtx == nil {
		return
	}
	if reqCtx.RequestID != "" {
		headers.Set(headerResponsesRequest, reqCtx.RequestID)
	}
	if reqCtx.TargetRegion != "" {
		headers.Set(headerResponsesRegion, reqCtx.TargetRegion)
		headers.Set(headerResponsesLegacyRegion, reqCtx.TargetRegion)
	}
	if reqCtx.RoutingKey != "" {
		headers.Set(headerResponsesRouting, reqCtx.RoutingKey)
	}
	if reqCtx.RoutingMethod != "" {
		headers.Set(headerResponsesMethod, reqCtx.RoutingMethod)
	}
	if reqCtx.Model != "" {
		headers.Set(headerResponsesModel, reqCtx.Model)
	}
	if reqCtx.CacheAffinityKey != "" {
		headers.Set(headerResponsesAffinity, reqCtx.CacheAffinityKey)
	}
}

func (h *ResponsesHandlers) relayNativeResponsesStream(
	c *GatewayContext,
	request *provider.NormalizedRequest,
	resp *provider.ProxyResponse,
) error {
	copyProxyHeaders(c.Response().Header(), resp.Header)
	setMultiTurnSessionResponseHeader(c)
	c.Response().WriteHeader(resp.StatusCode)
	if resp.Body == nil {
		h.finalizeNativeResponsesUsage(c, request, nil, true)
		return nil
	}
	defer resp.Body.Close()

	start := time.Now()
	terminalResponse, err := consumeNativeResponsesSSE(
		resp.Body,
		c.Response().Writer,
	)
	if terminalResponse != nil {
		h.recordNativeResponsesProviderTime(c, start, true)
	}
	h.finalizeNativeResponsesUsage(c, request, terminalResponse, true)
	return err
}

func (h *ResponsesHandlers) aggregateNativeResponsesStream(
	c *GatewayContext,
	request *provider.NormalizedRequest,
	resp *provider.ProxyResponse,
) error {
	if resp.Body == nil {
		h.finalizeNativeResponsesUsage(c, request, nil, false)
		return echo.NewHTTPError(http.StatusBadGateway, "responses proxy returned no body")
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		copyProxyHeaders(c.Response().Header(), resp.Header)
		setMultiTurnSessionResponseHeader(c)
		c.Response().WriteHeader(resp.StatusCode)
		_, err := io.Copy(c.Response().Writer, resp.Body)
		h.finalizeNativeResponsesUsage(c, request, nil, false)
		return err
	}

	start := time.Now()
	terminalResponse, err := consumeNativeResponsesSSE(resp.Body, nil)
	if err != nil {
		h.finalizeNativeResponsesUsage(c, request, nil, false)
		return err
	}
	if terminalResponse == nil {
		h.finalizeNativeResponsesUsage(c, request, nil, false)
		return echo.NewHTTPError(
			http.StatusBadGateway,
			"responses proxy stream ended without a terminal response event",
		)
	}

	h.recordNativeResponsesProviderTime(c, start, false)
	h.finalizeNativeResponsesUsage(c, request, terminalResponse, false)
	setMultiTurnSessionResponseHeader(c)
	return c.JSON(http.StatusOK, terminalResponse)
}

func consumeNativeResponsesSSE(
	reader io.Reader,
	writer io.Writer,
) (*openairesponses.Response, error) {
	if reader == nil {
		return nil, nil
	}

	lineReader := bufio.NewReader(reader)
	var (
		eventBlock       bytes.Buffer
		terminalResponse *openairesponses.Response
	)

	for {
		line, err := lineReader.ReadBytes('\n')
		if len(line) > 0 {
			if writer != nil {
				if _, writeErr := writer.Write(line); writeErr != nil {
					return terminalResponse, writeErr
				}
			}
			eventBlock.Write(line)
			if isSSEBlankLine(line) {
				if response := parseNativeResponsesSSEBlock(eventBlock.Bytes()); response != nil {
					terminalResponse = response
				}
				eventBlock.Reset()
				if flusher, ok := writer.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return terminalResponse, err
	}

	if eventBlock.Len() > 0 {
		if response := parseNativeResponsesSSEBlock(eventBlock.Bytes()); response != nil {
			terminalResponse = response
		}
	}
	return terminalResponse, nil
}

func isSSEBlankLine(line []byte) bool {
	return len(bytes.TrimSpace(line)) == 0
}

func parseNativeResponsesSSEBlock(block []byte) *openairesponses.Response {
	var (
		eventType string
		dataLines []string
	)

	for _, rawLine := range strings.Split(string(block), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			dataLines = append(dataLines, data)
		}
	}
	if len(dataLines) == 0 {
		return nil
	}

	data := strings.Join(dataLines, "\n")
	if data == "[DONE]" {
		return nil
	}

	var event struct {
		Type     string                    `json:"type"`
		Response *openairesponses.Response `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil
	}
	if eventType == "" {
		eventType = event.Type
	}

	switch eventType {
	case openairesponses.EventTypeResponseCompleted,
		openairesponses.EventTypeResponseFailed,
		openairesponses.EventTypeResponseIncomplete:
		return event.Response
	default:
		return nil
	}
}

func (h *ResponsesHandlers) finalizeNativeResponsesUsage(
	c *GatewayContext,
	request *provider.NormalizedRequest,
	response *openairesponses.Response,
	stream bool,
) {
	ctx := c.UserContext()
	usage := chatUsageFromResponses(response)
	if usageHasTokenCounts(usage) {
		h.handlers.observability.recordLLMUsage(
			ctx,
			responsesEndpointPath,
			requestFunctionID(c),
			usage,
			stream,
		)
	}
	h.handlers.finalizeTokenConsumption(ctx, request, usage)
}

func (h *ResponsesHandlers) recordNativeResponsesProviderTime(
	c *GatewayContext,
	start time.Time,
	stream bool,
) {
	ctx := c.UserContext()
	telemetry.RecordWithContext(
		ctx,
		h.handlers.observability.providerTime,
		time.Since(start).Seconds(),
		attribute.String("endpoint", responsesEndpointPath),
		attribute.String("phase", "total"),
		attribute.String("stream", boolLabel(stream)),
		telemetry.FunctionIDAttribute(requestFunctionID(c)),
	)
}

func chatUsageFromResponses(response *openairesponses.Response) *models.ChatCompletionUsage {
	if response == nil || response.Usage == nil {
		return nil
	}

	return &models.ChatCompletionUsage{
		PromptTokens:     responsesUsageTokenCount(response.Usage.InputTokens),
		CompletionTokens: responsesUsageTokenCount(response.Usage.OutputTokens),
		TotalTokens:      responsesUsageTokenCount(response.Usage.TotalTokens),
	}
}

func responsesUsageTokenCount(value int) uint32 {
	if value <= 0 {
		return 0
	}
	if uint64(value) > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(value)
}
