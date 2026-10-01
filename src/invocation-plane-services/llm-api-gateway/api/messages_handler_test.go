// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

const messagesTestBody = `{"model":"fn-alpha/company-name/model-name","max_tokens":64,"system":[{"type":"text","text":"Be concise","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"Hello"}]}],"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],"thinking":{"type":"enabled","budget_tokens":32},"extra-headers":{"private":"remove"},"backend_extension":{"keep":true}}`
const messagesTestSSE = "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":1,\"cache_read_input_tokens\":3}}}\r\n\r\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"think\"}}\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

type messagesCapturedRequest struct {
	body    []byte
	headers http.Header
	uri     string
}

func messagesTestAPI(t *testing.T, upstream string, cfg *config.Config, limiter *recordingRateLimiter, uris []string) (*echo.Echo, *stubInvocationAuthClient) {
	t.Helper()
	p, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream})
	require.NoError(t, err)
	auth := &stubInvocationAuthClient{authResponse: &nvcf.InvocationAuthResponse{
		RoutingKey: "fn-alpha", RateLimitKey: "org-test", AuthContext: map[string]string{"ncaId": "org-test"},
		ModelSpecs: map[string]nvcf.ModelSpec{"company-name/model-name": {URIs: uris, TokenRateLimit: "10000-M", RoutingMethod: "random"}},
	}}
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(auth))
	h := NewHandlers(cfg, p, nil)
	if limiter != nil {
		h.rateLimiter = limiter
	}
	RegisterRoutes(e, h)
	return e, auth
}

func messagesHTTPRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Add("anthropic-beta", "tools-test")
	req.Header.Add("anthropic-beta", "thinking-test")
	req.Header.Set(HeaderMultiTurnSessionID, "session-test")
	req.Header.Set("extra-headers", "private")
	req.Header.Set("X-API-Key", "private-key")
	req.Header.Set("X-Stargate-Retryable", "true")
	return req
}

func TestMessagesNativeProxyAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, response string
		status, wantTokens          int
	}{
		{"json", "application/json", `{"type":"message","content":[{"type":"tool_use","id":"tool-1","name":"get_weather","input":{}},{"type":"thinking","thinking":"think","signature":"sig"}],"usage":{"input_tokens":7,"output_tokens":5,"cache_read_input_tokens":3}}`, 200, 15},
		{"stream", "text/event-stream", messagesTestSSE, 200, 15},
		{"zero usage", "application/json", `{"type":"message","usage":{"input_tokens":0,"output_tokens":0}}`, 200, 0},
		{"missing usage", "application/json", `{"type":"message","content":[]}`, 200, -1},
		{"bad request", "application/json", `{"type":"error","error":{"type":"invalid_request_error","message":"bad tool"}}`, 400, -1},
		{"stream error", "text/event-stream", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n", 200, -1},
		{"error", "application/json", `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, 529, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captured := make(chan messagesCapturedRequest, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				captured <- messagesCapturedRequest{body, r.Header.Clone(), r.URL.RequestURI()}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Request-Id", "msg-request")
				w.Header().Set("X-Stargate-Private", "remove")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.response)
			}))
			defer backend.Close()
			limiter := &recordingRateLimiter{}
			cfg := config.Default()
			cfg.ModelURIAllowlistEnabled = true
			e, auth := messagesTestAPI(t, backend.URL, cfg, limiter, []string{messagesEndpointPath})
			rec := httptest.NewRecorder()
			body := messagesTestBody
			if tc.contentType == "text/event-stream" {
				body = strings.Replace(body, `"max_tokens":64`, `"stream":true,"max_tokens":64`, 1)
			}
			e.ServeHTTP(rec, messagesHTTPRequest(body))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, tc.response, rec.Body.String())
			require.Equal(t, "msg-request", rec.Header().Get("Request-Id"))
			require.Equal(t, "session-test", rec.Header().Get(HeaderMultiTurnSessionID))
			require.Empty(t, rec.Header().Get("X-Stargate-Private"))
			require.Equal(t, "caller-token", auth.authorizeToken)
			require.Equal(t, "fn-alpha", auth.authorizeRoutingKey)
			got := <-captured
			require.Equal(t, "/v1/messages?beta=test", got.uri)
			want := jsonObject(t, []byte(body))
			delete(want, "extra-headers")
			want["model"] = json.RawMessage(`"company-name/model-name"`)
			encoded, err := json.Marshal(want)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(got.body))
			require.Equal(t, "2023-06-01", got.headers.Get("anthropic-version"))
			require.Equal(t, []string{"tools-test", "thinking-test"}, got.headers.Values("anthropic-beta"))
			for _, h := range []string{"extra-headers", "X-API-Key", "X-Stargate-Retryable"} {
				require.Empty(t, got.headers.Get(h))
			}
			require.Equal(t, "random", got.headers.Get("X-Routing-Method"))
			require.NotEmpty(t, got.headers.Get("X-Cache-Affinity-Key"))
			input, err := strconv.Atoi(got.headers.Get("X-Input-Tokens"))
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(input+64), got.headers.Get("X-Token-Estimate"))
			var consumed []rateLimitCall
			for _, c := range limiter.Calls() {
				if c.mustConsume {
					consumed = append(consumed, c)
					require.NoError(t, c.contextErr)
				}
			}
			require.Len(t, consumed, 2)
			require.EqualValues(t, input+64, consumed[0].tokensRequested)
			if tc.wantTokens >= 0 {
				require.EqualValues(t, tc.wantTokens-input-64, consumed[1].tokensRequested)
			} else {
				require.EqualValues(t, -64, consumed[1].tokensRequested)
			}
		})
	}
}

func TestMessagesValidationAndURIAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		enforce    bool
		uris       []string
		status     int
	}{
		{"declared", messagesTestBody, true, []string{messagesEndpointPath}, 200},
		{"undeclared enforced", messagesTestBody, true, []string{chatCompletionsEndpointPath}, 400},
		{"undeclared log", messagesTestBody, false, []string{chatCompletionsEndpointPath}, 200},
		{"empty allowlist", messagesTestBody, true, nil, 200},
		{"zero max", `{"model":"fn-alpha/company-name/model-name","messages":[{}],"max_tokens":0}`, true, nil, 400},
		{"empty messages", `{"model":"fn-alpha/company-name/model-name","messages":[],"max_tokens":64}`, true, nil, 400},
		{"ambiguous max", `{"model":"fn-alpha/company-name/model-name","messages":[{}],"max_tokens":64,"MAX_TOKENS":1}`, true, nil, 400},
		{"overflow max", `{"model":"fn-alpha/company-name/model-name","messages":[{}],"max_tokens":9223372036854775807}`, true, nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := make(chan struct{}, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called <- struct{}{}
				io.WriteString(w, `{"type":"message"}`)
			}))
			defer backend.Close()
			cfg := config.Default()
			cfg.ModelURIAllowlistEnabled = tc.enforce
			e, _ := messagesTestAPI(t, backend.URL, cfg, nil, tc.uris)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, messagesHTTPRequest(tc.body))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, tc.status == 200, len(called) == 1)
		})
	}
}

func TestMessagesUsageObserverFragmentedAndBounded(t *testing.T) {
	o := &messagesUsageObserver{stream: true}
	for _, b := range []byte(messagesTestSSE) {
		_, err := o.Write([]byte{b})
		require.NoError(t, err)
	}
	require.EqualValues(t, 10, o.usage.chatUsage().PromptTokens)
	require.EqualValues(t, 5, o.usage.chatUsage().CompletionTokens)
	o = &messagesUsageObserver{stream: true}
	o.Write([]byte("data: " + strings.Repeat("x", messagesUsageBufferLimit+1) + "\n\n" + messagesTestSSE))
	require.EqualValues(t, 15, o.usage.chatUsage().TotalTokens)
	require.LessOrEqual(t, len(o.buffer)+len(o.data), 2*messagesUsageBufferLimit)
	for _, s := range []string{`{"usage":{"input_tokens":-1,"output_tokens":2}}`, `{"usage":{"input_tokens":4294967295,"output_tokens":1}}`, `{"usage":{"output_tokens":1}}`} {
		o = &messagesUsageObserver{}
		o.Write([]byte(s))
		o.finish()
		require.Nil(t, o.usage.chatUsage())
	}
}

func TestMessagesFlushesBeforeCompletionAndCancelsBackend(t *testing.T) {
	cancelled := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer backend.Close()
	limiter := &recordingRateLimiter{}
	e, _ := messagesTestAPI(t, backend.URL, config.Default(), limiter, nil)
	gateway := httptest.NewServer(e)
	defer gateway.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/messages", strings.NewReader(messagesTestBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer caller-token")
	resp, err := gateway.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "event: message_start\n", line)
	cancel()
	resp.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("backend did not receive cancellation")
	}
	require.Eventually(t, func() bool {
		for _, call := range limiter.Calls() {
			if call.mustConsume && call.tokensRequested == -64 {
				return call.contextErr == nil
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "output reservation must be released with an uncancelled context")
}

func TestMessagesTokenAdmissionRefusesBeforeBackend(t *testing.T) {
	hits := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- struct{}{}; w.WriteHeader(200) }))
	defer backend.Close()
	limiter := &recordingRateLimiter{currentValueFn: func(call rateLimitCall) int64 {
		if call.testOnly {
			return 0
		}
		return call.limit.Limit
	}}
	e, _ := messagesTestAPI(t, backend.URL, config.Default(), limiter, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, messagesHTTPRequest(messagesTestBody))
	require.Equal(t, 429, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Empty(t, hits)
	for _, call := range limiter.Calls() {
		require.False(t, call.mustConsume)
	}
}

func TestMessagesRequestSizeLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Server.MaxRequestBodyBytes = 32
	e, auth := messagesTestAPI(t, "http://127.0.0.1:1", cfg, nil, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, messagesHTTPRequest(messagesTestBody))
	require.Equal(t, 413, rec.Code)
	require.Zero(t, auth.authorizeCalls)
}

func TestMessagesUsageEventsAreCumulativeAndIgnoreContent(t *testing.T) {
	o := &messagesUsageObserver{stream: true}
	o.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n"))
	o.Write([]byte("data: {\"type\":\"content_block_delta\",\"usage\":{\"input_tokens\":999,\"output_tokens\":999}}\n\n"))
	require.EqualValues(t, 8, o.usage.chatUsage().TotalTokens)
	o.Write([]byte("data: {\"type\":\"message_delta\",\ndata: \"usage\":{\"output_tokens\":2}}\n\n"))
	o.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n"))
	require.EqualValues(t, 12, o.usage.chatUsage().TotalTokens)
	require.False(t, o.firstOutput.IsZero())
}

func TestMessagesHeaderPolicyStripsHopHeadersAndCredentials(t *testing.T) {
	headers := http.Header{
		"Connection":        {"anthropic-beta, X-Private"},
		"Anthropic-Beta":    {"tools-test"},
		"Anthropic-Version": {"2023-06-01"},
		"Authorization":     {"Bearer private"},
		"X-Private":         {"private"},
		"X-Routing-Key":     {"private"},
		"Extra-Headers":     {"private"},
	}
	got := messagesForwardedHeaders(headers)
	require.Len(t, got, 2)
	require.Equal(t, "2023-06-01", got.Get("Anthropic-Version"))
	require.Equal(t, "identity", got.Get("Accept-Encoding"))
}
