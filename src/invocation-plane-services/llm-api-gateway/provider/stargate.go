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
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/servicetier"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	stargateChatCompletionsPath = "/v1/chat/completions"
	stargateModelsPath          = "/v1/models"
	listModelsTimeout           = 2 * time.Second
	maxListModelsBodyBytes      = 4 << 20

	headerAuthorization      = "Authorization"
	headerContentType        = "Content-Type"
	headerAccept             = "Accept"
	headerRequestID          = "X-Request-Id"
	headerTargetRegion       = "X-NVCF-Target-Region"
	headerLegacyTargetRegion = "X-Groq-Region"
	headerRoutingKey         = "X-Routing-Key"
	headerRoutingMethod      = "X-Routing-Method"
	headerModel              = "X-Model"
	headerCacheAffinityKey   = "X-Cache-Affinity-Key"
	headerPriority           = "X-Priority"
	headerInputTokens        = "X-Input-Tokens"
	headerTokenEstimate      = "X-Token-Estimate"
	headerStargateErrorCode  = "X-Stargate-Error-Code"
	overloadErrorCode        = "overloaded_error"
	statusOverloaded         = 529

	contentTypeJSON = "application/json"
	contentTypeSSE  = "text/event-stream"

	upstreamName = "llm-request-router"

	sseMaxToken = 4 * 1024 * 1024
)

type StargateProvider struct {
	baseURL                 *url.URL
	client                  *http.Client
	requestTimeout          time.Duration
	upstreamRequestsTotal   otelmetric.Int64Counter
	upstreamRequestDuration otelmetric.Float64Histogram
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
		baseURL: parsed,
		client: &http.Client{Transport: otelhttp.NewTransport(
			transport,
			otelhttp.WithSpanNameFormatter(func(_ string, _ *http.Request) string {
				return "llm-api-gateway.upstream." + upstreamName
			}),
		)},
		requestTimeout:          cfg.RequestTimeout,
		upstreamRequestsTotal:   telemetry.UpstreamRequestsTotal(),
		upstreamRequestDuration: telemetry.UpstreamRequestDuration(),
	}, nil
}

func (p *StargateProvider) Complete(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	request *NormalizedRequest,
) (*models.ChatCompletionResponse, error) {
	outbound, err := p.newOutboundRequest(reqCtx, request, true)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := p.requestContext(ctx)
	start := time.Now()
	resp, err := p.client.Do(outbound.WithContext(requestCtx))
	if err != nil {
		cancel()
		p.recordUpstreamRequest(ctx, reqCtx, start, 0, err)
		return nil, err
	}
	recordRoutedModel(reqCtx, outbound, resp)

	if err := checkHTTPError(resp); err != nil {
		cancel()
		resp.Body.Close()
		p.recordUpstreamRequest(ctx, reqCtx, start, resp.StatusCode, err)
		return nil, err
	}

	events := make(chan StreamEvent, 8)
	go p.readStream(requestCtx, cancel, resp.Body, events)

	response, err := aggregateChatCompletionStream(events)
	if err != nil {
		cancel()
		p.recordUpstreamRequest(ctx, reqCtx, start, resp.StatusCode, err)
		return nil, err
	}

	p.recordUpstreamRequest(ctx, reqCtx, start, resp.StatusCode, nil)
	return response, nil
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
	start := time.Now()
	resp, err := p.client.Do(outbound.WithContext(requestCtx))
	if err != nil {
		cancel()
		p.recordUpstreamRequest(ctx, reqCtx, start, 0, err)
		return nil, err
	}
	recordRoutedModel(reqCtx, outbound, resp)

	if err := checkHTTPError(resp); err != nil {
		cancel()
		resp.Body.Close()
		p.recordUpstreamRequest(ctx, reqCtx, start, resp.StatusCode, err)
		return nil, err
	}

	p.recordUpstreamRequest(ctx, reqCtx, start, resp.StatusCode, nil)
	events := make(chan StreamEvent, 8)
	go p.readStream(requestCtx, cancel, resp.Body, events)

	return events, nil
}

// ListModels reads the router's model listing, bounded by a fixed timeout. A
// transport error, a non-2xx status, or an undecodable body fails the call.
// It records no upstream metrics, which describe inference calls only.
func (p *StargateProvider) ListModels(ctx context.Context) (*ModelListing, error) {
	ctx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()

	target := p.baseURL.ResolveReference(&url.URL{Path: stargateModelsPath})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build router model listing request: %w", err)
	}
	request.Header.Set(headerAccept, contentTypeJSON)

	resp, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call router model listing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("router model listing returned status %d", resp.StatusCode)
	}

	var listing ModelListing
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxListModelsBodyBytes)).Decode(&listing); err != nil {
		return nil, fmt.Errorf("decode router model listing: %w", err)
	}
	return &listing, nil
}

func (p *StargateProvider) requestContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	if p.requestTimeout > 0 {
		return context.WithTimeout(ctx, p.requestTimeout)
	}
	return ctx, func() {}
}

func (p *StargateProvider) recordUpstreamRequest(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	start time.Time,
	statusCode int,
	err error,
) {
	result := "success"
	if err != nil || statusCode >= http.StatusBadRequest || statusCode == 0 {
		result = "error"
	}

	status := "error"
	if statusCode > 0 {
		status = strconv.Itoa(statusCode)
	}

	routingKey := ""
	if reqCtx != nil {
		routingKey = reqCtx.RoutingKey
	}
	attrs := []attribute.KeyValue{
		attribute.String("upstream", upstreamName),
		attribute.String("result", result),
		attribute.String("status", status),
		telemetry.FunctionIDAttribute(routingKey),
	}
	telemetry.AddWithContext(ctx, p.upstreamRequestsTotal, 1, attrs...)
	telemetry.RecordWithContext(ctx, p.upstreamRequestDuration, time.Since(start).Seconds(), attrs...)

	logEvent := telemetry.Logger(ctx).Debug()
	if err != nil || statusCode >= http.StatusInternalServerError || statusCode == 0 {
		logEvent = telemetry.Logger(ctx).Warn()
	}

	log := logEvent.
		Str("upstream", upstreamName).
		Str("upstream_result", result).
		Str("upstream_status", status).
		Dur("duration", time.Since(start))
	if err != nil {
		log = log.Err(err)
	}
	if reqCtx != nil {
		log = log.
			Str("request_id", reqCtx.RequestID).
			Str("routing_key", reqCtx.RoutingKey).
			Str("target_region", reqCtx.TargetRegion).
			Str("project_id", reqCtx.ProjectID).
			Str("rate_limit_key", reqCtx.OrgID)
	}
	log.Msg("completed upstream request")
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

	body, err := outboundChatRequestBody(request, stream)
	if err != nil {
		return nil, err
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
		setTargetRegionHeaders(req.Header, reqCtx.TargetRegion)
	}
	if reqCtx.RoutingKey != "" {
		req.Header.Set(headerRoutingKey, reqCtx.RoutingKey)
	}
	if reqCtx.RoutingMethod != "" {
		req.Header.Set(headerRoutingMethod, reqCtx.RoutingMethod)
	}
	if reqCtx.CacheAffinityKey != "" {
		req.Header.Set(headerCacheAffinityKey, reqCtx.CacheAffinityKey)
	}
	if reqCtx.Priority != nil {
		req.Header.Set(headerPriority, strconv.FormatUint(uint64(*reqCtx.Priority), 10))
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

// recordRoutedModel keeps the X-Model value only when the router answered 2xx.
// Its unknown-model (404) and bad-header (400) answers must not create labels.
func recordRoutedModel(reqCtx *requestctx.RequestContext, outbound *http.Request, resp *http.Response) {
	if reqCtx != nil && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		reqCtx.RoutedModel = outbound.Header.Get(headerModel)
	}
}

func setTargetRegionHeaders(headers http.Header, targetRegion string) {
	headers.Set(headerTargetRegion, targetRegion)
	headers.Set(headerLegacyTargetRegion, targetRegion)
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
	// X-Priority is gateway-owned: a client-supplied value cloned from the
	// inbound request must never reach Stargate, even when no priority
	// resolves for this request.
	outbound.Header.Del(headerPriority)
	// An inbound X-Routing-Method request header must never reach the router.
	outbound.Header.Del(headerRoutingMethod)
	// Without a routing key (bare model names) the gateway authorized no
	// routing scope, so a caller-supplied one must not reach the router either.
	if reqCtx == nil || reqCtx.RoutingKey == "" {
		outbound.Header.Del(headerRoutingKey)
	}
	// Only a bearer token that NVCF auth validated reaches the router; it is
	// set again below.
	outbound.Header.Del(headerAuthorization)

	if reqCtx != nil {
		if reqCtx.RequestID != "" {
			outbound.Header.Set(headerRequestID, reqCtx.RequestID)
		}
		if reqCtx.TargetRegion != "" {
			setTargetRegionHeaders(outbound.Header, reqCtx.TargetRegion)
		}
		if reqCtx.RoutingKey != "" {
			outbound.Header.Set(headerRoutingKey, reqCtx.RoutingKey)
		}
		if reqCtx.RoutingMethod != "" {
			outbound.Header.Set(headerRoutingMethod, reqCtx.RoutingMethod)
		}
		if reqCtx.CacheAffinityKey != "" {
			outbound.Header.Set(headerCacheAffinityKey, reqCtx.CacheAffinityKey)
		}
		if reqCtx.Priority != nil {
			outbound.Header.Set(headerPriority, strconv.FormatUint(uint64(*reqCtx.Priority), 10))
		}
		if reqCtx.Model != "" {
			outbound.Header.Set(headerModel, reqCtx.Model)
		}
		if bearerToken := reqCtx.BearerToken; bearerToken != "" {
			outbound.Header.Set(headerAuthorization, "Bearer "+bearerToken)
		}
	}
	if request.InputTokens > 0 {
		outbound.Header.Set(headerInputTokens, strconv.Itoa(request.InputTokens))
	}
	if request.TokenEstimate > 0 {
		outbound.Header.Set(headerTokenEstimate, strconv.Itoa(request.TokenEstimate))
	}

	start := time.Now()
	resp, err := p.client.Do(outbound)
	if err != nil {
		cancel()
		p.recordUpstreamRequest(ctx, reqCtx, start, 0, err)
		return nil, err
	}
	recordRoutedModel(reqCtx, outbound, resp)

	p.recordUpstreamRequest(ctx, reqCtx, start, resp.StatusCode, nil)
	return &ProxyResponse{
		StatusCode: clientStatusCode(resp),
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
	request *NormalizedRequest,
	stream bool,
) *models.ChatCompletionRequest {
	outbound := *request.ChatRequest
	outbound.Stream = ptr.To(stream)
	if stream && !ptr.Deref(request.ChatRequest.Stream) {
		// Complete uses upstream streaming internally, including when the client omitted stream.
		// Request usage chunks so token finalization can use the aggregated response usage.
		streamOptions := models.ChatCompletionStreamOptions{}
		if request.ChatRequest.StreamOptions != nil {
			streamOptions = *request.ChatRequest.StreamOptions
		}
		streamOptions.IncludeUsage = ptr.To(true)
		outbound.StreamOptions = &streamOptions
	}
	if !outbound.ServiceTier.IsValid() {
		outbound.ServiceTier = servicetier.Auto
	}

	return &outbound
}

// gatewayOwnedChatFields are replaced or removed in the forwarded client body.
// Keys are matched case-insensitively because encoding/json binds them that way.
var gatewayOwnedChatFields = []string{"model", "stream", "stream_options", "service_tier", "debug"}

func outboundChatRequestBody(request *NormalizedRequest, stream bool) ([]byte, error) {
	outbound := outboundChatRequest(request, stream)
	if len(bytes.TrimSpace(request.RawBody)) == 0 {
		body, err := json.Marshal(outbound)
		if err != nil {
			return nil, fmt.Errorf("marshal stargate request: %w", err)
		}
		return body, nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(request.RawBody, &payload); err != nil {
		return nil, fmt.Errorf("decode chat request body: %w", err)
	}

	var clientStreamOptions json.RawMessage
	for key, value := range payload {
		for _, owned := range gatewayOwnedChatFields {
			if strings.EqualFold(key, owned) {
				if owned == "stream_options" {
					clientStreamOptions = value
				}
				delete(payload, key)
				break
			}
		}
	}

	streamOptions, err := outboundStreamOptions(clientStreamOptions, outbound.StreamOptions, request, stream)
	if err != nil {
		return nil, err
	}
	if streamOptions != nil {
		payload["stream_options"] = streamOptions
	}
	if err := setJSONField(payload, "model", outbound.Model); err != nil {
		return nil, err
	}
	if err := setJSONField(payload, "stream", outbound.Stream); err != nil {
		return nil, err
	}
	if err := setJSONField(payload, "service_tier", outbound.ServiceTier); err != nil {
		return nil, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal stargate request: %w", err)
	}
	return body, nil
}

// outboundStreamOptions keeps the client's stream_options object and only adds
// include_usage when Complete forces upstream streaming.
func outboundStreamOptions(
	client json.RawMessage,
	outbound *models.ChatCompletionStreamOptions,
	request *NormalizedRequest,
	stream bool,
) (json.RawMessage, error) {
	forcedUsage := stream && !ptr.Deref(request.ChatRequest.Stream) && outbound != nil
	if !forcedUsage {
		return client, nil
	}

	options := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(client); len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &options); err != nil {
			return nil, fmt.Errorf("decode stream_options: %w", err)
		}
	}
	for key := range options {
		if strings.EqualFold(key, "include_usage") {
			delete(options, key)
		}
	}
	if err := setJSONField(options, "include_usage", outbound.IncludeUsage); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(options)
	if err != nil {
		return nil, fmt.Errorf("marshal stream_options: %w", err)
	}
	return encoded, nil
}

func setJSONField(payload map[string]json.RawMessage, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	payload[key] = encoded
	return nil
}

func routingTokenEstimate(request *NormalizedRequest) int {
	if request == nil {
		return 0
	}

	return max(0, request.InputTokens)
}

func clientStatusCode(resp *http.Response) int {
	codes := resp.Header.Values(headerStargateErrorCode)
	if resp.StatusCode == http.StatusServiceUnavailable && len(codes) == 1 && codes[0] == overloadErrorCode {
		return statusOverloaded
	}
	return resp.StatusCode
}

func checkHTTPError(resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	statusCode := clientStatusCode(resp)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	message := string(body)

	var errorResponse models.ErrorResponse
	if err := json.Unmarshal(body, &errorResponse); err == nil && errorResponse.Error.Message != "" {
		if statusCode == statusOverloaded && errorResponse.Error.Code == overloadErrorCode {
			return echo.NewHTTPError(statusCode, errorResponse)
		}
		message = errorResponse.Error.Message
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}

	return echo.NewHTTPError(statusCode, message)
}

type chatCompletionChoiceAccumulator struct {
	index            uint32
	role             string
	content          strings.Builder
	reasoning        strings.Builder
	reasoningContent strings.Builder
	logprobs         *models.ChatCompletionLogprobs
	finishReason     string
	functionCall     *models.ChatCompletionFunctionCall
	toolCalls        map[uint32]*chatCompletionToolCallAccumulator
	extensions       map[string]json.RawMessage
	logprobsMembers  map[string]json.RawMessage
	deltaText        map[string]*strings.Builder
	deltaValues      map[string]json.RawMessage
}

type chatCompletionToolCallAccumulator struct {
	id        string
	typ       string
	name      string
	arguments strings.Builder
}

func aggregateChatCompletionStream(events <-chan StreamEvent) (*models.ChatCompletionResponse, error) {
	response := &models.ChatCompletionResponse{
		Object:  models.ObjectChatCompletion,
		Choices: []models.ChatCompletionChoice{},
	}
	choices := map[uint32]*chatCompletionChoiceAccumulator{}
	var choiceIndexes []uint32
	var usageRaw json.RawMessage
	seenChunk := false

	for event := range events {
		if event.Err != nil {
			return nil, event.Err
		}
		if event.Chunk == nil {
			continue
		}

		chunk := event.Chunk
		if !seenChunk {
			response.ID = chunk.ID
			response.CreatedAt = chunk.CreatedAt
			response.Model = chunk.Model
			response.SystemFingerprint = chunk.SystemFingerprint
			response.ServiceTier = chunk.ServiceTier
			seenChunk = true
		}
		if chunk.Usage != nil {
			response.Usage = *chunk.Usage
		}

		for _, choice := range chunk.Choices {
			acc, ok := choices[choice.Index]
			if !ok {
				acc = &chatCompletionChoiceAccumulator{
					index:     choice.Index,
					toolCalls: map[uint32]*chatCompletionToolCallAccumulator{},
				}
				choices[choice.Index] = acc
				choiceIndexes = append(choiceIndexes, choice.Index)
			}
			acc.merge(choice)
		}
		mergeUnmodeledChunkMembers(event.Raw, response, choices, &usageRaw)
	}
	response.Usage.Raw = usageRaw

	if !seenChunk {
		return nil, errors.New("stargate stream ended without chat completion chunks")
	}
	if len(choiceIndexes) == 0 {
		return nil, errors.New("stargate stream ended without chat completion choices")
	}

	sort.Slice(choiceIndexes, func(i, j int) bool {
		return choiceIndexes[i] < choiceIndexes[j]
	})
	for _, index := range choiceIndexes {
		response.Choices = append(response.Choices, choices[index].buildChoice())
	}

	return response, nil
}

// mergeUnmodeledChunkMembers carries upstream members the typed chunk structs
// do not declare (for example nvext, choices[].stop_reason, or delta.refusal)
// into the unary response, so unary clients see what streaming clients see.
// The last upstream usage object is kept whole.
func mergeUnmodeledChunkMembers(
	raw json.RawMessage,
	response *models.ChatCompletionResponse,
	choices map[uint32]*chatCompletionChoiceAccumulator,
	usageRaw *json.RawMessage,
) {
	if len(raw) == 0 {
		return
	}
	response.Extensions = mergeLatestMembers(
		response.Extensions,
		models.UnmodeledMembers(raw, models.ChatCompletionChunk{}),
	)

	var chunk struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   json.RawMessage   `json:"usage"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return
	}
	if usage := bytes.TrimSpace(chunk.Usage); len(usage) > 0 && usage[0] == '{' {
		*usageRaw = chunk.Usage
	}
	for _, rawChoice := range chunk.Choices {
		var choice struct {
			Index    uint32          `json:"index"`
			Delta    json.RawMessage `json:"delta"`
			Logprobs json.RawMessage `json:"logprobs"`
		}
		if err := json.Unmarshal(rawChoice, &choice); err != nil {
			continue
		}
		acc, ok := choices[choice.Index]
		if !ok {
			continue
		}
		acc.extensions = mergeLatestMembers(
			acc.extensions,
			models.UnmodeledMembers(rawChoice, models.ChatCompletionChunkChoice{}),
		)
		acc.logprobsMembers = mergeLatestMembers(
			acc.logprobsMembers,
			models.UnmodeledMembers(choice.Logprobs, models.ChatCompletionLogprobs{}),
		)
		acc.mergeDeltaMembers(models.UnmodeledMembers(choice.Delta, models.ChatCompletionChunkDelta{}))
	}
}

// mergeDeltaMembers applies the streaming merge rule to delta members the
// typed delta does not declare: string values are concatenated in arrival
// order like content, and any other value replaces the previous one. A null
// does not erase an earlier value.
func (a *chatCompletionChoiceAccumulator) mergeDeltaMembers(members map[string]json.RawMessage) {
	for key, value := range members {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			var text string
			if err := json.Unmarshal(trimmed, &text); err == nil {
				if a.deltaText == nil {
					a.deltaText = map[string]*strings.Builder{}
				}
				builder, ok := a.deltaText[key]
				if !ok {
					builder = &strings.Builder{}
					a.deltaText[key] = builder
				}
				builder.WriteString(text)
				delete(a.deltaValues, key)
				continue
			}
		}
		if string(trimmed) == "null" {
			if _, ok := a.deltaText[key]; ok {
				continue
			}
		}
		a.deltaValues = mergeLatestMembers(a.deltaValues, map[string]json.RawMessage{key: value})
		delete(a.deltaText, key)
	}
}

func (a *chatCompletionChoiceAccumulator) messageExtensions() map[string]json.RawMessage {
	if len(a.deltaText) == 0 && len(a.deltaValues) == 0 {
		return nil
	}
	extensions := make(map[string]json.RawMessage, len(a.deltaText)+len(a.deltaValues))
	for key, value := range a.deltaValues {
		extensions[key] = value
	}
	for key, builder := range a.deltaText {
		encoded, err := json.Marshal(builder.String())
		if err != nil {
			continue
		}
		extensions[key] = encoded
	}
	return extensions
}

// mergeLatestMembers keeps the latest value per member, except that a later
// null does not erase an earlier value.
func mergeLatestMembers(dst, src map[string]json.RawMessage) map[string]json.RawMessage {
	for key, value := range src {
		if _, ok := dst[key]; ok && string(bytes.TrimSpace(value)) == "null" {
			continue
		}
		if dst == nil {
			dst = make(map[string]json.RawMessage)
		}
		dst[key] = value
	}
	return dst
}

func (a *chatCompletionChoiceAccumulator) merge(choice models.ChatCompletionChunkChoice) {
	if choice.Delta.Role != nil && *choice.Delta.Role != "" {
		a.role = *choice.Delta.Role
	}
	if choice.Delta.Content != nil {
		a.content.WriteString(*choice.Delta.Content)
	}
	if choice.Delta.Reasoning != nil {
		a.reasoning.WriteString(*choice.Delta.Reasoning)
	}
	if choice.Delta.ReasoningContent != nil {
		a.reasoningContent.WriteString(*choice.Delta.ReasoningContent)
	}
	if choice.Logprobs != nil {
		if a.logprobs == nil {
			a.logprobs = &models.ChatCompletionLogprobs{}
		}
		a.logprobs.Content = append(a.logprobs.Content, choice.Logprobs.Content...)
		a.logprobs.Refusal = append(a.logprobs.Refusal, choice.Logprobs.Refusal...)
	}
	if choice.FinishReason != nil {
		a.finishReason = *choice.FinishReason
	}
	if choice.Delta.FunctionCall != nil {
		a.mergeFunctionCall(*choice.Delta.FunctionCall)
	}
	if choice.Delta.ToolCalls == nil {
		return
	}
	for _, toolCall := range *choice.Delta.ToolCalls {
		a.mergeToolCall(toolCall)
	}
}

func (a *chatCompletionChoiceAccumulator) mergeFunctionCall(functionCall models.ChatCompletionFunctionCall) {
	if a.functionCall == nil {
		a.functionCall = &models.ChatCompletionFunctionCall{}
	}
	if a.functionCall.Name == "" && functionCall.Name != "" {
		a.functionCall.Name = functionCall.Name
	}
	a.functionCall.Arguments += functionCall.Arguments
}

func (a *chatCompletionChoiceAccumulator) mergeToolCall(toolCall models.ChatCompletionToolCallChunk) {
	acc, ok := a.toolCalls[toolCall.Index]
	if !ok {
		acc = &chatCompletionToolCallAccumulator{}
		a.toolCalls[toolCall.Index] = acc
	}
	if acc.id == "" && toolCall.ID != "" {
		acc.id = toolCall.ID
	}
	if acc.typ == "" && toolCall.Type != "" {
		acc.typ = toolCall.Type
	}
	if acc.name == "" && toolCall.Function.Name != "" {
		acc.name = toolCall.Function.Name
	}
	acc.arguments.WriteString(toolCall.Function.Arguments)
}

func (a *chatCompletionChoiceAccumulator) buildChoice() models.ChatCompletionChoice {
	role := a.role
	if role == "" {
		role = models.ChatCompletionRoleAssistant
	}

	message := models.ChatCompletionMessage{
		Role:       role,
		Extensions: a.messageExtensions(),
	}
	if a.content.Len() > 0 {
		message.Content = ptr.To(a.content.String())
	}
	if a.reasoning.Len() > 0 {
		message.Reasoning = ptr.To(a.reasoning.String())
	}
	if a.reasoningContent.Len() > 0 {
		message.ReasoningContent = ptr.To(a.reasoningContent.String())
	}
	if a.functionCall != nil {
		message.FunctionCall = a.functionCall
	}
	if a.logprobs != nil {
		a.logprobs.Extensions = a.logprobsMembers
	}
	if len(a.toolCalls) > 0 {
		message.ToolCalls = ptr.To(a.buildToolCalls())
	}

	return models.ChatCompletionChoice{
		Index:        a.index,
		Message:      message,
		Logprobs:     a.logprobs,
		FinishReason: a.finishReason,
		Extensions:   a.extensions,
	}
}

func (a *chatCompletionChoiceAccumulator) buildToolCalls() []models.ChatCompletionToolCall {
	indexes := make([]uint32, 0, len(a.toolCalls))
	for index := range a.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	toolCalls := make([]models.ChatCompletionToolCall, 0, len(indexes))
	for _, index := range indexes {
		toolCall := a.toolCalls[index]
		toolCalls = append(toolCalls, models.ChatCompletionToolCall{
			ID:   toolCall.id,
			Type: toolCall.typ,
			Function: models.ChatCompletionFunctionCall{
				Name:      toolCall.name,
				Arguments: toolCall.arguments.String(),
			},
		})
	}

	return toolCalls
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
			select {
			case events <- StreamEvent{Err: ctx.Err()}:
			default:
			}
			return false
		case events <- StreamEvent{Chunk: &chunk, Raw: json.RawMessage(payload)}:
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

	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			select {
			case events <- StreamEvent{Err: ctxErr}:
			default:
			}
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		events <- StreamEvent{
			Err: fmt.Errorf("read stargate stream: %w", err),
		}
	}
}
