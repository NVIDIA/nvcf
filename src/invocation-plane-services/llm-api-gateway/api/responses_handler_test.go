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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	echo "github.com/labstack/echo/v4"

	openairesponses "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

func TestCreateResponseAggregatesNativeResponsesThroughProxy(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	handlers := NewHandlers(
		cfg,
		&stubResponsesProvider{
			completeResponse: &models.ChatCompletionResponse{
				ID:        "chatcmpl-123",
				Object:    models.ObjectChatCompletion,
				CreatedAt: 123,
				Model:     "gateway-model",
				Choices: []models.ChatCompletionChoice{
					{
						Index: 0,
						Message: models.ChatCompletionMessage{
							Role:    models.ChatCompletionRoleAssistant,
							Content: ptr.To("hello from gateway"),
						},
						FinishReason: models.FinishReasonStop,
					},
				},
				Usage: models.ChatCompletionUsage{
					PromptTokens:     5,
					CompletionTokens: 3,
					TotalTokens:      8,
				},
			},
		},
		nil,
		nil,
	)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"object":"response"`) {
		t.Fatalf("response body missing responses object: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model":"company-name/model-name"`) {
		t.Fatalf("response body missing model: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hello from gateway"`) {
		t.Fatalf("response body missing assistant text: %s", rec.Body.String())
	}
}

func TestCreateResponseReturnsBodySessionIDAndUsesItForAffinity(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	provider := &stubResponsesProvider{
		completeResponse: &models.ChatCompletionResponse{
			ID:        "chatcmpl-session",
			Object:    models.ObjectChatCompletion,
			CreatedAt: 123,
			Model:     "gateway-model",
		},
	}
	handlers := NewHandlers(cfg, provider, nil)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello","prompt_cache_key":"body-session"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(HeaderMultiTurnSessionID, "header-session")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get(HeaderMultiTurnSessionID); got != "body-session" {
		t.Fatalf("%s = %q, want body-session", HeaderMultiTurnSessionID, got)
	}

	reqCtx := provider.lastRequestContext()
	if reqCtx == nil {
		t.Fatal("provider did not receive request context")
	}
	if reqCtx.SessionID != "body-session" {
		t.Fatalf("SessionID = %q, want body-session", reqCtx.SessionID)
	}
	if reqCtx.CacheAffinityKey == "" {
		t.Fatal("CacheAffinityKey is empty")
	}
	if strings.Contains(reqCtx.CacheAffinityKey, "body-session") {
		t.Fatalf("CacheAffinityKey leaks raw session ID: %q", reqCtx.CacheAffinityKey)
	}
}

func TestSetResponsesProxyContextHeadersSetsTargetRegionCompatibilityHeaders(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	setResponsesProxyContextHeaders(headers, &requestctx.RequestContext{
		TargetRegion: "us-west",
	})

	if got := headers.Get(headerResponsesRegion); got != "us-west" {
		t.Fatalf("%s = %q, want us-west", headerResponsesRegion, got)
	}
	if got := headers.Get(headerResponsesLegacyRegion); got != "us-west" {
		t.Fatalf("%s = %q, want us-west", headerResponsesLegacyRegion, got)
	}
}

func TestSetResponsesProxyContextHeadersForwardsResolvedPriority(t *testing.T) {
	t.Parallel()

	priority := uint32(3)
	headers := http.Header{}
	setResponsesProxyContextHeaders(headers, &requestctx.RequestContext{
		Priority: &priority,
	})

	if got := headers.Get(headerResponsesPriority); got != "3" {
		t.Fatalf("%s = %q, want 3", headerResponsesPriority, got)
	}
}

func TestSetResponsesProxyContextHeadersOmitsUnsetPriority(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	setResponsesProxyContextHeaders(headers, &requestctx.RequestContext{})

	if got := headers.Get(headerResponsesPriority); got != "" {
		t.Fatalf("%s = %q, want empty", headerResponsesPriority, got)
	}
}

func TestSetResponsesProxyContextHeadersStripsClientSuppliedPriorityWhenUnset(t *testing.T) {
	t.Parallel()

	// The responses path clones inbound client headers before applying
	// context headers; a client-supplied X-Priority must be stripped when no
	// priority resolved.
	headers := http.Header{}
	headers.Set(headerResponsesPriority, "9")
	setResponsesProxyContextHeaders(headers, &requestctx.RequestContext{})

	if got := headers.Get(headerResponsesPriority); got != "" {
		t.Fatalf("%s = %q, want empty", headerResponsesPriority, got)
	}
}

func TestCreateResponseReusesReturnedSessionIDForAffinity(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	provider := &stubResponsesProvider{
		completeResponse: &models.ChatCompletionResponse{
			ID:        "chatcmpl-session",
			Object:    models.ObjectChatCompletion,
			CreatedAt: 123,
			Model:     "gateway-model",
		},
	}
	handlers := NewHandlers(cfg, provider, nil)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	firstReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello","prompt_cache_key":"body-session"}`),
	)
	firstReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	firstRec := httptest.NewRecorder()

	e.ServeHTTP(firstRec, firstReq)

	if firstRec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d: %s", firstRec.Code, http.StatusOK, firstRec.Body.String())
	}
	returnedSessionID := firstRec.Header().Get(HeaderMultiTurnSessionID)
	if returnedSessionID != "body-session" {
		t.Fatalf("first %s = %q, want body-session", HeaderMultiTurnSessionID, returnedSessionID)
	}

	secondReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"follow-up"}`),
	)
	secondReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	secondReq.Header.Set(HeaderMultiTurnSessionID, returnedSessionID)
	secondRec := httptest.NewRecorder()

	e.ServeHTTP(secondRec, secondReq)

	if secondRec.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d: %s", secondRec.Code, http.StatusOK, secondRec.Body.String())
	}
	if got := secondRec.Header().Get(HeaderMultiTurnSessionID); got != returnedSessionID {
		t.Fatalf("second %s = %q, want %q", HeaderMultiTurnSessionID, got, returnedSessionID)
	}

	reqCtxs := provider.requestContexts()
	if len(reqCtxs) != 2 {
		t.Fatalf("provider recorded %d request contexts, want 2", len(reqCtxs))
	}
	if reqCtxs[0].CacheAffinityKey == "" {
		t.Fatal("first CacheAffinityKey is empty")
	}
	if reqCtxs[1].CacheAffinityKey != reqCtxs[0].CacheAffinityKey {
		t.Fatalf("second CacheAffinityKey = %q, want %q", reqCtxs[1].CacheAffinityKey, reqCtxs[0].CacheAffinityKey)
	}
}

func TestCreateResponseReturnsHeaderSessionIDWhenNoBodyID(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	handlers := NewHandlers(
		cfg,
		&stubResponsesProvider{
			completeResponse: &models.ChatCompletionResponse{
				ID:        "chatcmpl-session",
				Object:    models.ObjectChatCompletion,
				CreatedAt: 123,
				Model:     "gateway-model",
			},
		},
		nil,
		nil,
	)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(HeaderMultiTurnSessionID, "header-session")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get(HeaderMultiTurnSessionID); got != "header-session" {
		t.Fatalf("%s = %q, want header-session", HeaderMultiTurnSessionID, got)
	}
}

func TestCreateResponseReturnsGeneratedSessionIDForPayloadFallback(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	handlers := NewHandlers(
		cfg,
		&stubResponsesProvider{
			completeResponse: &models.ChatCompletionResponse{
				ID:        "chatcmpl-session",
				Object:    models.ObjectChatCompletion,
				CreatedAt: 123,
				Model:     "gateway-model",
			},
		},
		nil,
		nil,
	)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := rec.Header().Get(HeaderMultiTurnSessionID)
	if !strings.HasPrefix(got, "mt:v1:payload:") {
		t.Fatalf("%s = %q, want generated payload session ID", HeaderMultiTurnSessionID, got)
	}
}

func TestCreateResponseRejectsInvalidSessionHeader(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	handlers := NewHandlers(
		cfg,
		&stubResponsesProvider{
			completeResponse: &models.ChatCompletionResponse{
				ID:        "chatcmpl-session",
				Object:    models.ObjectChatCompletion,
				CreatedAt: 123,
				Model:     "gateway-model",
			},
		},
		nil,
		nil,
	)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(HeaderMultiTurnSessionID, "bad\nsession")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestCreateResponseRelaysNativeResponsesStreamThroughProxy(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	handlers := NewHandlers(
		cfg,
		&stubResponsesProvider{
			streamEvents: []provider.StreamEvent{
				{
					Chunk: &models.ChatCompletionChunk{
						Choices: []models.ChatCompletionChunkChoice{
							{
								Delta: models.ChatCompletionChunkDelta{
									Content: ptr.To("hello "),
								},
							},
						},
					},
				},
				{
					Chunk: &models.ChatCompletionChunk{
						Choices: []models.ChatCompletionChunkChoice{
							{
								Delta: models.ChatCompletionChunkDelta{
									Content: ptr.To("world"),
								},
							},
						},
						Usage: &models.ChatCompletionUsage{
							PromptTokens:     5,
							CompletionTokens: 2,
							TotalTokens:      7,
						},
					},
				},
			},
		},
		nil,
		nil,
	)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello","stream":true}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get(HeaderMultiTurnSessionID); got == "" {
		t.Fatalf("%s response header is empty", HeaderMultiTurnSessionID)
	}
	if !strings.Contains(rec.Body.String(), "event: "+openairesponses.EventTypeResponseCreated) {
		t.Fatalf("stream body missing response.created: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: "+openairesponses.EventTypeResponseCompleted) {
		t.Fatalf("stream body missing response.completed: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model":"company-name/model-name"`) {
		t.Fatalf("stream body missing model: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"text":"hello world"`) {
		t.Fatalf("stream body missing completed assistant text: %s", rec.Body.String())
	}
}

func TestCreateResponseStreamsNativeResponsesThroughProxy(t *testing.T) {
	t.Parallel()

	provider := &stubResponsesProvider{
		proxyHeader: http.Header{
			echo.HeaderContentType: []string{"text/event-stream"},
		},
		proxyBody: strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_native","object":"response","status":"in_progress","created_at":123,"model":"company-name/model-name","output":[]}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_native","output_index":0,"content_index":0,"delta":"native hello","logprobs":[]}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_native","object":"response","status":"completed","created_at":123,"model":"company-name/model-name","output":[{"type":"message","id":"msg_native","role":"assistant","status":"completed","content":[{"type":"output_text","text":"native hello","annotations":[]}]}],"usage":{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":10}}}`,
			"",
		}, "\n"),
	}
	handlers := NewHandlers(config.Default(), provider, nil)

	e := echo.New()
	e.Use(NewContextMiddleware(config.Default()))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello","stream":true}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "native hello") {
		t.Fatalf("stream body missing native upstream event: %s", rec.Body.String())
	}

	calls := provider.proxyRequestCalls()
	if len(calls) != 1 {
		t.Fatalf("proxy calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Path != "/v1/responses" {
		t.Fatalf("proxy path = %q, want /v1/responses", call.Path)
	}
	if got := call.Header.Get("X-Routing-Key"); got != "fn-chat" {
		t.Fatalf("X-Routing-Key = %q, want fn-chat", got)
	}
	if got := call.Header.Get("X-Model"); got != "company-name/model-name" {
		t.Fatalf("X-Model = %q, want company-name/model-name", got)
	}
	if got := call.Header.Get("X-Input-Tokens"); got == "" {
		t.Fatal("X-Input-Tokens header is empty")
	}
	if got := call.Header.Get("X-Token-Estimate"); got == "" {
		t.Fatal("X-Token-Estimate header is empty")
	}

	var outbound map[string]any
	if err := json.Unmarshal(call.Body, &outbound); err != nil {
		t.Fatalf("unmarshal outbound body: %v", err)
	}
	if outbound["model"] != "company-name/model-name" {
		t.Fatalf("outbound model = %v, want routed model", outbound["model"])
	}
	if outbound["stream"] != true {
		t.Fatalf("outbound stream = %v, want true", outbound["stream"])
	}
}

func TestCreateResponseAggregatesNativeStreamForNonStreamingClient(t *testing.T) {
	t.Parallel()

	provider := &stubResponsesProvider{
		completeResponse: &models.ChatCompletionResponse{
			ID:        "chatcmpl-legacy",
			Object:    models.ObjectChatCompletion,
			CreatedAt: 123,
			Model:     "company-name/model-name",
		},
		proxyHeader: http.Header{
			echo.HeaderContentType: []string{"text/event-stream"},
		},
		proxyBody: strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_native","object":"response","status":"in_progress","created_at":123,"model":"company-name/model-name","output":[]}}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_native","object":"response","status":"completed","created_at":123,"model":"company-name/model-name","output":[{"type":"message","id":"msg_native","role":"assistant","status":"completed","content":[{"type":"output_text","text":"native hello","annotations":[]}]}],"usage":{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":10}}}`,
			"",
		}, "\n"),
	}
	handlers := NewHandlers(config.Default(), provider, nil)

	e := echo.New()
	e.Use(NewContextMiddleware(config.Default()))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello","stream":false}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "event: response.") {
		t.Fatalf("non-streaming response returned SSE body: %s", rec.Body.String())
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response["id"] != "resp_native" {
		t.Fatalf("response id = %v, want resp_native", response["id"])
	}
	if response["status"] != "completed" {
		t.Fatalf("response status = %v, want completed", response["status"])
	}

	calls := provider.proxyRequestCalls()
	if len(calls) != 1 {
		t.Fatalf("proxy calls = %d, want 1", len(calls))
	}
	var outbound map[string]any
	if err := json.Unmarshal(calls[0].Body, &outbound); err != nil {
		t.Fatalf("unmarshal outbound body: %v", err)
	}
	if outbound["stream"] != true {
		t.Fatalf("outbound stream = %v, want true", outbound["stream"])
	}
}

func TestCreateResponseRejectsModelWithoutResponsesURI(t *testing.T) {
	t.Parallel()

	provider := &stubResponsesProvider{
		completeResponse: &models.ChatCompletionResponse{
			ID:        "chatcmpl-legacy",
			Object:    models.ObjectChatCompletion,
			CreatedAt: 123,
			Model:     "company-name/model-name",
		},
	}
	handlers := NewHandlers(config.Default(), provider, nil)

	e := echo.New()
	e.Use(NewContextMiddleware(config.Default()))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			if gc, ok := ec.(*GatewayContext); ok && gc.RequestContext() != nil {
				gc.RequestContext().ModelSpecs = map[string]nvcf.ModelSpec{
					"company-name/model-name": {
						URIs: []string{"/v1/chat/completions"},
					},
				}
			}
			return next(ec)
		}
	})
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello","stream":true}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/v1/responses") {
		t.Fatalf("response body missing endpoint: %s", rec.Body.String())
	}
}

func TestChatUsageFromResponsesClampsInvalidCounts(t *testing.T) {
	t.Parallel()

	usage := chatUsageFromResponses(&openairesponses.Response{
		Usage: &openairesponses.ResponseUsage{
			InputTokens:  -1,
			OutputTokens: 3,
			TotalTokens:  int(^uint32(0)) + 1,
		},
	})

	if usage == nil {
		t.Fatal("usage is nil")
	}
	if usage.PromptTokens != 0 {
		t.Fatalf("prompt tokens = %d, want 0", usage.PromptTokens)
	}
	if usage.CompletionTokens != 3 {
		t.Fatalf("completion tokens = %d, want 3", usage.CompletionTokens)
	}
	if usage.TotalTokens != ^uint32(0) {
		t.Fatalf("total tokens = %d, want max uint32", usage.TotalTokens)
	}
}

type stubResponsesProvider struct {
	completeResponse *models.ChatCompletionResponse
	streamEvents     []provider.StreamEvent
	proxyStatusCode  int
	proxyHeader      http.Header
	proxyBody        string
	mu               sync.RWMutex
	reqCtx           *requestctx.RequestContext
	reqCtxs          []*requestctx.RequestContext
	proxyCalls       []stubProxyCall
}

func (s *stubResponsesProvider) Complete(
	_ context.Context,
	reqCtx *requestctx.RequestContext,
	_ *provider.NormalizedRequest,
) (*models.ChatCompletionResponse, error) {
	s.recordRequestContext(reqCtx)
	return s.completeResponse, nil
}

func (s *stubResponsesProvider) Stream(
	_ context.Context,
	reqCtx *requestctx.RequestContext,
	_ *provider.NormalizedRequest,
) (<-chan provider.StreamEvent, error) {
	s.recordRequestContext(reqCtx)
	ch := make(chan provider.StreamEvent, len(s.streamEvents))
	go func() {
		defer close(ch)
		for _, event := range s.streamEvents {
			ch <- event
		}
	}()
	return ch, nil
}

func (s *stubResponsesProvider) Proxy(
	_ context.Context,
	reqCtx *requestctx.RequestContext,
	request *provider.ProxyRequest,
) (*provider.ProxyResponse, error) {
	s.recordRequestContext(reqCtx)

	var body []byte
	if request != nil && request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}

	call := stubProxyCall{
		Body: body,
	}
	if request != nil {
		call.Method = request.Method
		call.Path = request.Path
		call.RawQuery = request.RawQuery
		call.Header = request.Header.Clone()
	}

	s.mu.Lock()
	s.proxyCalls = append(s.proxyCalls, call)
	statusCode := s.proxyStatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	header := s.proxyHeader.Clone()
	bodyText := s.proxyBody
	if bodyText == "" {
		bodyText = defaultResponsesProxyStream(reqCtx, s.completeResponse, s.streamEvents)
		if header == nil {
			header = make(http.Header)
		}
		header.Set(echo.HeaderContentType, "text/event-stream")
	}
	s.mu.Unlock()

	return &provider.ProxyResponse{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(bodyText)),
	}, nil
}

func defaultResponsesProxyStream(
	reqCtx *requestctx.RequestContext,
	completeResponse *models.ChatCompletionResponse,
	streamEvents []provider.StreamEvent,
) string {
	model := "company-name/model-name"
	if reqCtx != nil && reqCtx.Model != "" {
		model = reqCtx.Model
	}

	createdAt := int64(123)
	if completeResponse != nil && completeResponse.CreatedAt != 0 {
		createdAt = completeResponse.CreatedAt
	}

	text := ""
	if completeResponse != nil && len(completeResponse.Choices) > 0 {
		if completeResponse.Choices[0].Message.Content != nil {
			text = string(*completeResponse.Choices[0].Message.Content)
		}
	}
	var usage *models.ChatCompletionUsage
	if completeResponse != nil && usageHasTokenCounts(&completeResponse.Usage) {
		usage = &completeResponse.Usage
	}
	for _, event := range streamEvents {
		if event.Chunk == nil {
			continue
		}
		for _, choice := range event.Chunk.Choices {
			if choice.Delta.Content != nil {
				text += *choice.Delta.Content
			}
		}
		if usageHasTokenCounts(event.Chunk.Usage) {
			usage = event.Chunk.Usage
		}
	}
	if usage == nil {
		usage = &models.ChatCompletionUsage{
			PromptTokens:     5,
			CompletionTokens: 2,
			TotalTokens:      7,
		}
	}

	statusInProgress := openairesponses.StatusInProgress
	statusCompleted := openairesponses.StatusCompleted
	created := &openairesponses.Response{
		ID:        "resp_stub",
		Object:    openairesponses.ObjectResponse,
		Status:    statusInProgress,
		CreatedAt: createdAt,
		Model:     ptr.To(model),
		Output:    []openairesponses.OutputItem{},
	}
	completed := &openairesponses.Response{
		ID:        "resp_stub",
		Object:    openairesponses.ObjectResponse,
		Status:    statusCompleted,
		CreatedAt: createdAt,
		Model:     ptr.To(model),
		Output: []openairesponses.OutputItem{
			{
				Type:   openairesponses.ItemTypeMessage,
				ID:     "msg_stub",
				Role:   openairesponses.RoleAssistant,
				Status: &statusCompleted,
				Content: []openairesponses.OutputContent{
					{
						Type: openairesponses.ContentTypeOutputText,
						Text: text,
					},
				},
			},
		},
		Usage: responseUsageFromStubChat(*usage),
	}

	return strings.Join([]string{
		"event: " + openairesponses.EventTypeResponseCreated,
		"data: " + mustMarshalResponsesEvent(openairesponses.EventTypeResponseCreated, 0, created),
		"",
		"event: " + openairesponses.EventTypeResponseCompleted,
		"data: " + mustMarshalResponsesEvent(openairesponses.EventTypeResponseCompleted, 1, completed),
		"",
	}, "\n")
}

func responseUsageFromStubChat(
	usage models.ChatCompletionUsage,
) *openairesponses.ResponseUsage {
	return &openairesponses.ResponseUsage{
		InputTokens:  int(usage.PromptTokens),
		OutputTokens: int(usage.CompletionTokens),
		TotalTokens:  int(usage.TotalTokens),
	}
}

func mustMarshalResponsesEvent(
	eventType string,
	sequenceNumber int,
	response *openairesponses.Response,
) string {
	payload, err := json.Marshal(map[string]any{
		"type":            eventType,
		"sequence_number": sequenceNumber,
		"response":        response,
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func (s *stubResponsesProvider) lastRequestContext() *requestctx.RequestContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reqCtx
}

func (s *stubResponsesProvider) requestContexts() []*requestctx.RequestContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reqCtxs := make([]*requestctx.RequestContext, len(s.reqCtxs))
	copy(reqCtxs, s.reqCtxs)
	return reqCtxs
}

func (s *stubResponsesProvider) proxyRequestCalls() []stubProxyCall {
	s.mu.RLock()
	defer s.mu.RUnlock()
	calls := make([]stubProxyCall, len(s.proxyCalls))
	copy(calls, s.proxyCalls)
	return calls
}

func (s *stubResponsesProvider) recordRequestContext(reqCtx *requestctx.RequestContext) {
	reqCtx = cloneRequestContext(reqCtx)
	s.mu.Lock()
	s.reqCtx = reqCtx
	s.reqCtxs = append(s.reqCtxs, reqCtx)
	s.mu.Unlock()
}

func cloneRequestContext(reqCtx *requestctx.RequestContext) *requestctx.RequestContext {
	if reqCtx == nil {
		return nil
	}
	clone := *reqCtx
	return &clone
}

type stubProxyCall struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}
