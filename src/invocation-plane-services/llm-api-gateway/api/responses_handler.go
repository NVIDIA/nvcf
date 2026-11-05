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

	"github.com/google/uuid"
	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"

	openairesponses "github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	headerResponsesInput        = "X-Input-Tokens"
	headerResponsesEstimate     = "X-Token-Estimate"
	headerResponsesRequest      = "X-Request-Id"
	headerResponsesRegion       = "X-NVCF-Target-Region"
	headerResponsesLegacyRegion = "X-Groq-Region"
	headerResponsesRouting      = "X-Routing-Key"
	headerResponsesMethod       = "X-Routing-Method"
	headerResponsesModel        = "X-Model"
	headerResponsesAffinity     = "X-Cache-Affinity-Key"
	headerResponsesPriority     = "X-Priority"

	msgNativeResponsesNoTerminal = "responses proxy stream ended without a terminal response event"
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
	if err := rejectAmbiguousMembers(body, openairesponses.CreateRequest{}); err != nil {
		return err
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

	h.handlers.setResponsesLastClusterKeys(c.RequestContext(), &request, body)

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
	if resp.StatusCode >= http.StatusBadRequest {
		copyProxyHeaders(c.Response().Header(), resp.Header)
		setMultiTurnSessionResponseHeader(c)
		if err := proxyUpstreamError(resp); err != nil {
			h.finalizeNativeResponsesUsage(c, normalized, nil, clientWantsStream)
			return err
		}
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

	if err := h.handlers.requireModelURIAllowlist(c, routedModel, responsesEndpointPath, true); err != nil {
		return nil, nil, err
	}

	countRequest := ConvertToChatCompletionRequest(request)
	estimatedInputTokens := estimatedInputTokensForNormalizedRequest(
		countRequest.Model,
		countRequest,
	)
	estimatedInputTokens += estimatedTokenCountForText(customToolFormatText(request.Tools))
	estimatedInputTokens += estimatedTokenCountForRawTools(request.Tools)
	for _, value := range request.UndecodedMembers {
		estimatedInputTokens += estimatedTokenCountForRawJSON(value)
	}
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
	// The gateway parses the upstream stream, so it must not arrive compressed.
	headers.Set(echo.HeaderAcceptEncoding, "identity")
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
			ContentLength: int64(len(body)),
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
	headers.Del(headerResponsesMethod)
	if reqCtx.RoutingMethod != "" {
		headers.Set(headerResponsesMethod, reqCtx.RoutingMethod)
	}
	if reqCtx.Model != "" {
		headers.Set(headerResponsesModel, reqCtx.Model)
	}
	if reqCtx.CacheAffinityKey != "" {
		headers.Set(headerResponsesAffinity, reqCtx.CacheAffinityKey)
	}
	// X-Priority is gateway-owned: strip any client-supplied value from the
	// cloned inbound headers, then set it only when a priority resolved.
	headers.Del(headerResponsesPriority)
	if reqCtx.Priority != nil {
		headers.Set(headerResponsesPriority, strconv.FormatUint(uint64(*reqCtx.Priority), 10))
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
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	start := time.Now()
	stream, err := consumeNativeResponsesSSE(
		resp.Body,
		c.Response().Writer,
	)
	if stream.terminal != nil {
		h.recordNativeResponsesProviderTime(c, start, true)
	}
	h.finalizeNativeResponsesUsage(c, request, stream.terminal, true)
	// A client that went away is not an upstream failure. A failed write can
	// come before net/http cancels the request context, so check both.
	if stream.terminal == nil && !stream.clientWriteFailed && c.Request().Context().Err() == nil {
		return failNativeResponsesStream(c, stream, err)
	}
	return err
}

// failNativeResponsesStream ends a relayed stream that stopped without a
// terminal event. The 200 is already sent, so the failure goes to the client
// as response.failed, and the returned 502 marks the request as failed in
// logs, metrics and traces.
func failNativeResponsesStream(c *GatewayContext, stream nativeResponsesStream, cause error) error {
	failure := echo.NewHTTPError(http.StatusBadGateway, msgNativeResponsesNoTerminal)
	message := msgNativeResponsesNoTerminal
	if stream.upstreamError != nil && stream.upstreamError.Message != "" {
		message = stream.upstreamError.Message
	}
	cause = stream.failureCause(cause)

	sequenceNumber := 0
	if stream.sequenceNumber != nil {
		sequenceNumber = *stream.sequenceNumber + 1
	}
	var events bytes.Buffer
	appendEvent := func(eventType string, response map[string]any) error {
		data, err := json.Marshal(struct {
			Type           string         `json:"type"`
			SequenceNumber int            `json:"sequence_number"`
			Response       map[string]any `json:"response"`
		}{Type: eventType, SequenceNumber: sequenceNumber, Response: response})
		if err != nil {
			return fmt.Errorf("marshal %s event: %w", eventType, err)
		}
		fmt.Fprintf(&events, "event: %s\ndata: %s\n\n", eventType, data)
		sequenceNumber++
		return nil
	}

	// Keep the upstream response.created fields as sent so the failed
	// response matches the one the client already saw.
	var created map[string]json.RawMessage
	if err := json.Unmarshal(stream.createdResponse, &created); err != nil || created["id"] == nil {
		created = nil
	}
	response := make(map[string]any, len(created)+2)
	for key, value := range created {
		response[key] = value
	}
	if created == nil {
		response["id"] = "resp_" + uuid.NewString()
		response["object"] = "response"
		response["created_at"] = time.Now().Unix()
		response["output"] = []any{}
		if reqCtx := c.RequestContext(); reqCtx != nil {
			response["model"] = reqCtx.Model
		}
		// Clients such as the OpenAI SDK reject a stream that does not start
		// with response.created.
		response["status"] = "in_progress"
		if err := appendEvent(openairesponses.EventTypeResponseCreated, response); err != nil {
			return failure.SetInternal(errors.Join(cause, err))
		}
	}
	response["status"] = "failed"
	response["error"] = openairesponses.ResponseError{
		Code:    "server_error",
		Message: message,
	}
	if err := appendEvent(openairesponses.EventTypeResponseFailed, response); err != nil {
		return failure.SetInternal(errors.Join(cause, err))
	}

	writer := c.Response().Writer
	_, writeErr := writer.Write(events.Bytes())
	if flusher, ok := writer.(http.Flusher); ok && writeErr == nil {
		flusher.Flush()
	}
	if internal := errors.Join(cause, writeErr); internal != nil {
		return failure.SetInternal(internal)
	}
	return failure
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
	// A read error after the terminal event does not change the response; one
	// before it fails the request like a stream that ends without one.
	stream, err := consumeNativeResponsesSSE(resp.Body, nil)
	terminalResponse := stream.terminal
	if terminalResponse == nil {
		h.finalizeNativeResponsesUsage(c, request, nil, false)
		return echo.NewHTTPError(http.StatusBadGateway, msgNativeResponsesNoTerminal).SetInternal(stream.failureCause(err))
	}

	h.recordNativeResponsesProviderTime(c, start, false)
	h.finalizeNativeResponsesUsage(c, request, terminalResponse, false)
	setMultiTurnSessionResponseHeader(c)
	return c.JSONBlob(http.StatusOK, terminalResponse.body)
}

// nativeResponsesTerminal is the response object carried by a terminal
// Responses stream event. The body is kept as upstream sent it so unary
// clients see the same fields as streaming clients.
type nativeResponsesTerminal struct {
	body  json.RawMessage
	usage *openairesponses.ResponseUsage
}

// nativeResponsesStream is what the gateway keeps from a relayed Responses
// stream: its terminal response, and the last response.created payload and
// sequence_number so a failed stream can be ended with a matching event.
type nativeResponsesStream struct {
	terminal          *nativeResponsesTerminal
	createdResponse   json.RawMessage
	sequenceNumber    *int
	clientWriteFailed bool
	// upstreamError is an error event that came before any terminal event. It
	// ends the stream, and its message goes into the gateway's response.failed.
	upstreamError *provider.UpstreamStreamError
}

// failureCause joins a read error with the upstream error event, if any, for the request log.
func (s *nativeResponsesStream) failureCause(readErr error) error {
	if s.upstreamError == nil {
		return readErr
	}
	return errors.Join(readErr, s.upstreamError)
}

func (s *nativeResponsesStream) observe(block []byte) {
	eventType, event, ok := decodeNativeResponsesSSEBlock(block)
	if ok {
		s.observeEvent(eventType, event)
	}
}

// captureUpstreamError records block as upstreamError when it is an error event
// before any terminal event. eventType is empty when the block is not JSON.
func (s *nativeResponsesStream) captureUpstreamError(eventType string, block []byte) bool {
	if eventType == "" {
		// An error event can carry plain text, which only the event field names.
		eventType, _, _ = splitNativeResponsesSSEBlock(block)
	}
	if eventType != openairesponses.EventTypeError || s.terminal != nil {
		return false
	}
	_, data, _ := splitNativeResponsesSSEBlock(block)
	s.upstreamError = &provider.UpstreamStreamError{Message: provider.UpstreamErrorMessage([]byte(data))}
	return true
}

func (s *nativeResponsesStream) observeEvent(eventType string, event nativeResponsesEvent) {
	if sequenceNumber, err := strconv.Atoi(string(event.SequenceNumber)); err == nil {
		s.sequenceNumber = &sequenceNumber
	}
	if eventType == openairesponses.EventTypeResponseCreated && len(event.Response) > 0 {
		s.createdResponse = event.Response
	}
	if terminal := nativeResponsesTerminalFromEvent(eventType, event); terminal != nil {
		s.terminal = terminal
	}
}

func consumeNativeResponsesSSE(
	reader io.Reader,
	writer io.Writer,
) (nativeResponsesStream, error) {
	var stream nativeResponsesStream
	if reader == nil {
		return stream, nil
	}

	lineReader := bufio.NewReader(reader)
	var eventBlock bytes.Buffer

	// Whole events only go to the client, so an event cut off by an upstream
	// failure never reaches a client parser ahead of response.failed.
	for {
		line, err := lineReader.ReadBytes('\n')
		eventBlock.Write(line)
		if len(line) > 0 && isSSEBlankLine(line) {
			eventType, event, ok := decodeNativeResponsesSSEBlock(eventBlock.Bytes())
			// An upstream error event ends the stream unrelayed: the caller reports
			// its message in the gateway's response.failed.
			if stream.captureUpstreamError(eventType, eventBlock.Bytes()) {
				return stream, nil
			}
			if writeErr := stream.relay(writer, eventBlock.Bytes()); writeErr != nil {
				return stream, writeErr
			}
			if ok {
				stream.observeEvent(eventType, event)
			}
			eventBlock.Reset()
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return stream, err
	}

	// A client drops an event that the stream ends before its blank line, so
	// a terminal event cut off at EOF is completed, an error event is captured,
	// and anything else is dropped.
	if eventBlock.Len() > 0 && stream.terminal == nil {
		eventType, _, _ := decodeNativeResponsesSSEBlock(eventBlock.Bytes())
		if stream.captureUpstreamError(eventType, eventBlock.Bytes()) {
			return stream, nil
		}
		stream.observe(eventBlock.Bytes())
		if stream.terminal != nil {
			if !bytes.HasSuffix(eventBlock.Bytes(), []byte("\n")) {
				eventBlock.WriteByte('\n')
			}
			eventBlock.WriteByte('\n')
			return stream, stream.relay(writer, eventBlock.Bytes())
		}
	}
	return stream, nil
}

func (s *nativeResponsesStream) relay(writer io.Writer, block []byte) error {
	if writer == nil {
		return nil
	}
	if _, err := writer.Write(block); err != nil {
		s.clientWriteFailed = true
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func isSSEBlankLine(line []byte) bool {
	return len(bytes.TrimSpace(line)) == 0
}

// nativeResponsesEvent is the part of a Responses stream event the gateway
// reads. SequenceNumber stays raw so an unexpected value cannot hide a
// terminal event.
type nativeResponsesEvent struct {
	Type           string          `json:"type"`
	SequenceNumber json.RawMessage `json:"sequence_number"`
	Response       json.RawMessage `json:"response"`
}

// splitNativeResponsesSSEBlock returns the event field and the joined data
// lines of one SSE block. ok is false when the block has no data line.
func splitNativeResponsesSSEBlock(block []byte) (eventType, data string, ok bool) {
	var dataLines []string
	for _, rawLine := range strings.Split(string(block), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			value := strings.TrimPrefix(line, "data:")
			dataLines = append(dataLines, strings.TrimPrefix(value, " "))
		}
	}
	return eventType, strings.Join(dataLines, "\n"), len(dataLines) > 0
}

// decodeNativeResponsesSSEBlock returns the event type and data of one SSE
// block, or false when the block carries no JSON event.
func decodeNativeResponsesSSEBlock(block []byte) (string, nativeResponsesEvent, bool) {
	eventType, data, ok := splitNativeResponsesSSEBlock(block)
	if !ok || data == "[DONE]" {
		return "", nativeResponsesEvent{}, false
	}

	var event nativeResponsesEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", nativeResponsesEvent{}, false
	}
	if eventType == "" {
		eventType = event.Type
	}
	return eventType, event, true
}

func nativeResponsesTerminalFromEvent(eventType string, event nativeResponsesEvent) *nativeResponsesTerminal {
	switch eventType {
	case openairesponses.EventTypeResponseCompleted,
		openairesponses.EventTypeResponseFailed,
		openairesponses.EventTypeResponseIncomplete:
	default:
		return nil
	}
	if trimmed := bytes.TrimSpace(event.Response); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}

	// Decode only usage so output item types the adapter does not know
	// cannot hide the terminal response.
	var response struct {
		Usage *openairesponses.ResponseUsage `json:"usage"`
	}
	terminal := &nativeResponsesTerminal{body: event.Response}
	if err := json.Unmarshal(event.Response, &response); err == nil {
		terminal.usage = response.Usage
	}
	return terminal
}

func (h *ResponsesHandlers) finalizeNativeResponsesUsage(
	c *GatewayContext,
	request *provider.NormalizedRequest,
	response *nativeResponsesTerminal,
	stream bool,
) {
	ctx := c.UserContext()
	var usage *models.ChatCompletionUsage
	if response != nil {
		usage = chatUsageFromResponses(response.usage)
	}
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

func chatUsageFromResponses(usage *openairesponses.ResponseUsage) *models.ChatCompletionUsage {
	if usage == nil {
		return nil
	}

	return &models.ChatCompletionUsage{
		PromptTokens:     responsesUsageTokenCount(usage.InputTokens),
		CompletionTokens: responsesUsageTokenCount(usage.OutputTokens),
		TotalTokens:      responsesUsageTokenCount(usage.TotalTokens),
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
