// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// Only fields used for routing and admission are bound. Content blocks and
// backend extensions remain in the raw body and are never converted to OpenAI.
type messagesRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream"`
	Messages  json.RawMessage `json:"messages"`
	System    json.RawMessage `json:"system"`
	Tools     json.RawMessage `json:"tools"`
}

func (h *MessagesHandlers) RegisterRoutes(group *echo.Group) {
	group.POST(messagesEndpointPath, h.ServeMessages)
}

func (h *MessagesHandlers) ServeMessages(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)
	reqCtx, err := h.handlers.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}
	body, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := rejectAmbiguousMembers(body, messagesRequest{}); err != nil {
		return err
	}
	var request messagesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if request.MaxTokens <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "max_tokens must be positive")
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(request.Messages, &messages); err != nil || len(messages) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "messages must be a non-empty array")
	}
	routedModel, err := normalizeOpenAIRequestModel(reqCtx, request.Model)
	if err != nil {
		return err
	}
	reqCtx.Model = routedModel
	setRoutingMethodForModel(reqCtx, routedModel)
	if err := h.handlers.requireModelURIAllowlist(c, routedModel, messagesEndpointPath, h.handlers.modelURIAllowlistEnabled()); err != nil {
		return err
	}
	if reqCtx.SessionID == "" {
		sessionID, err := sessionIDFromHeader(c.Request().Header.Get(HeaderMultiTurnSessionID))
		if err != nil {
			return err
		}
		if sessionID != "" {
			if err := setSessionAffinity(reqCtx, sessionAffinitySourceHeader, sessionID); err != nil {
				return err
			}
		} else {
			setPayloadSessionAffinity(reqCtx, request.Messages)
		}
	}
	outboundBody, err := rewriteMessagesBody(body, routedModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	inputTokens := estimatedTokenCountForText(string(request.Messages)) + estimatedTokenCountForText(string(request.System)) + estimatedTokenCountForText(string(request.Tools))
	if request.MaxTokens > math.MaxInt-inputTokens {
		return echo.NewHTTPError(http.StatusBadRequest, "max_tokens is too large")
	}
	resource := ratelimit.ResourceRequest{Requests: 1, InputTokens: int64(inputTokens), OutputTokens: int64(request.MaxTokens)}
	plan, err := NewAdmissionPlan(c, reqCtx, messagesEndpointPath, h.handlers.limitResolver, h.handlers.rateLimiter, resource, resource)
	if err != nil {
		return err
	}
	if plan != nil {
		defer plan.Close()
		if err := plan.CheckRequests(c.UserContext()); err != nil {
			return err
		}
		if _, err := plan.CheckTokensAndFinalize(c.UserContext()); err != nil {
			return err
		}
	}
	normalized := &provider.NormalizedRequest{InputTokens: inputTokens, MaxOutputTokens: request.MaxTokens, AdmissionPlan: plan}
	observer := &messagesUsageObserver{}
	// Reconciliation must still run after client cancellation. Keep it bounded.
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.UserContext()), 5*time.Second)
		defer cancel()
		usage := observer.usage.chatUsage()
		if usage != nil {
			h.handlers.observability.recordLLMUsage(ctx, messagesEndpointPath, requestFunctionID(c), usage, request.Stream)
		}
		if usage == nil {
			h.handlers.releaseReservedTokenConsumption(ctx, normalized)
		} else if plan != nil {
			// Anthropic can report valid zero counts; do not replace them with estimates.
			if _, err := plan.FinalizeTokens(ctx, ratelimit.ResourceRequest{InputTokens: int64(usage.PromptTokens), OutputTokens: int64(usage.CompletionTokens)}); err != nil {
				telemetry.Logger(ctx).Error().Err(err).Msg("failed to finalize Messages token consumption")
			}
		}
	}()
	span := trace.SpanFromContext(c.UserContext())
	span.SetAttributes(attribute.String("gateway.endpoint", messagesEndpointPath), attribute.String("gen_ai.request.model", request.Model), attribute.Bool("gen_ai.request.stream", request.Stream))
	start := time.Now()
	defer func() {
		telemetry.RecordWithContext(c.UserContext(), h.handlers.observability.providerTime, time.Since(start).Seconds(), attribute.String("endpoint", messagesEndpointPath), attribute.String("phase", "total"), attribute.String("stream", boolLabel(request.Stream)), telemetry.FunctionIDAttribute(requestFunctionID(c)))
	}()
	headers := messagesForwardedHeaders(c.Request().Header)
	resp, err := h.handlers.dispatchEstimatedProxyRequest(c, reqCtx, headers, io.NopCloser(bytes.NewReader(outboundBody)), int64(len(outboundBody)), inputTokens, inputTokens+request.MaxTokens)
	if err != nil {
		return err
	}
	if resp == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "proxy provider returned no response")
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	copyProxyHeaders(c.Response().Header(), resp.Header)
	setMultiTurnSessionResponseHeader(c)
	c.Response().WriteHeader(resp.StatusCode)
	if resp.Body == nil {
		return nil
	}
	observer.stream = strings.HasPrefix(strings.ToLower(resp.Header.Get(echo.HeaderContentType)), "text/event-stream")
	reader := io.Reader(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.Header.Get("Content-Encoding") == "" {
		reader = io.TeeReader(reader, observer)
	}
	var writer io.Writer = c.Response().Writer
	if observer.stream {
		writer = messagesFlushWriter{c.Response().Writer}
	}
	_, err = io.Copy(writer, reader)
	observer.finish()
	if observer.stream {
		status := "success"
		if err != nil || resp.StatusCode >= http.StatusBadRequest {
			status = "error"
		}
		telemetry.RecordWithContext(c.UserContext(), h.handlers.observability.streamDuration, time.Since(start).Seconds(), attribute.String("endpoint", messagesEndpointPath), attribute.String("status", status), telemetry.FunctionIDAttribute(requestFunctionID(c)))
		if !observer.firstOutput.IsZero() {
			telemetry.RecordWithContext(c.UserContext(), h.handlers.observability.streamFirstToken, observer.firstOutput.Sub(start).Seconds(), attribute.String("endpoint", messagesEndpointPath), telemetry.FunctionIDAttribute(requestFunctionID(c)))
		}
	}
	return err
}

func rewriteMessagesBody(body []byte, model string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	payload["model"] = encoded
	delete(payload, "extra-headers")
	return json.Marshal(payload)
}

// NVCF authenticates with Authorization before proxying. Stargate adds its own
// routing headers; pylon consumes them before applying the backend policy.
func messagesForwardedHeaders(src http.Header) http.Header {
	clean := make(http.Header)
	copyProxyHeaders(clean, src)
	for name := range clean {
		switch strings.ToLower(name) {
		case "content-type", "accept", "anthropic-version", "anthropic-beta", "user-agent", "traceparent", "tracestate", "baggage", "x-request-id":
		default:
			clean.Del(name)
		}
	}
	clean.Set("Accept-Encoding", "identity")
	return clean
}

type messagesFlushWriter struct{ http.ResponseWriter }

func (w messagesFlushWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err == nil {
		err = http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}

type messagesUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

func (u *messagesUsage) merge(v messagesUsage) {
	if v.InputTokens != nil {
		u.InputTokens = v.InputTokens
	}
	if v.OutputTokens != nil {
		u.OutputTokens = v.OutputTokens
	}
	if v.CacheCreationInputTokens != nil {
		u.CacheCreationInputTokens = v.CacheCreationInputTokens
	}
	if v.CacheReadInputTokens != nil {
		u.CacheReadInputTokens = v.CacheReadInputTokens
	}
}
func (u messagesUsage) chatUsage() *models.ChatCompletionUsage {
	if u.InputTokens == nil || u.OutputTokens == nil {
		return nil
	}
	totalInput := 0
	for _, count := range []*int{u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens} {
		if count == nil {
			continue
		}
		if *count < 0 || *count > math.MaxUint32-totalInput {
			return nil
		}
		totalInput += *count
	}
	if *u.OutputTokens < 0 || *u.OutputTokens > math.MaxUint32-totalInput {
		return nil
	}
	return &models.ChatCompletionUsage{PromptTokens: uint32(totalInput), CompletionTokens: uint32(*u.OutputTokens), TotalTokens: uint32(totalInput + *u.OutputTokens)}
}

// Usage observation is best effort and bounded. Oversized or unknown events
// still pass through byte for byte; they never block delivery to the client.
const messagesUsageBufferLimit = 1 << 20

type messagesUsageObserver struct {
	stream         bool
	buffer         []byte
	data           []byte
	oversized      bool
	eventOversized bool
	usage          messagesUsage
	firstOutput    time.Time
}

func (o *messagesUsageObserver) Write(p []byte) (int, error) {
	if !o.stream {
		if !o.oversized && len(o.buffer)+len(p) <= messagesUsageBufferLimit {
			o.buffer = append(o.buffer, p...)
		} else {
			o.oversized = true
			o.buffer = nil
		}
		return len(p), nil
	}
	for _, b := range p {
		if b != '\n' {
			if !o.oversized {
				if len(o.buffer) < messagesUsageBufferLimit {
					o.buffer = append(o.buffer, b)
				} else {
					o.buffer = nil
					o.oversized = true
					o.eventOversized = true
				}
			}
			continue
		}
		line := bytes.TrimSuffix(o.buffer, []byte{'\r'})
		if !o.oversized && len(line) == 0 {
			if !o.eventOversized {
				o.observe(o.data)
			}
			o.data = nil
			o.eventOversized = false
		} else if !o.oversized && bytes.HasPrefix(line, []byte("data:")) && !o.eventOversized {
			value := bytes.TrimPrefix(line[5:], []byte{' '})
			if len(o.data)+len(value)+1 <= messagesUsageBufferLimit {
				o.data = append(o.data, value...)
				o.data = append(o.data, '\n')
			} else {
				o.data = nil
				o.eventOversized = true
			}
		}
		o.buffer = o.buffer[:0]
		o.oversized = false
	}
	return len(p), nil
}
func (o *messagesUsageObserver) observe(data []byte) {
	var event struct {
		Type    string        `json:"type"`
		Usage   messagesUsage `json:"usage"`
		Message struct {
			Usage messagesUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	if !o.stream {
		o.usage.merge(event.Usage)
		return
	}
	switch event.Type {
	case "content_block_delta":
		if o.firstOutput.IsZero() {
			o.firstOutput = time.Now()
		}
	case "message_start":
		o.usage.merge(event.Message.Usage)
	case "message_delta":
		o.usage.merge(event.Usage)
	}
}
func (o *messagesUsageObserver) finish() {
	if !o.stream && !o.oversized {
		o.observe(o.buffer)
	}
}
