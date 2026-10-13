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
	"sync"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
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
			for _, h := range []string{"extra-headers", "Authorization", "X-API-Key", "X-Stargate-Retryable"} {
				require.Empty(t, got.headers.Get(h))
			}
			require.Equal(t, "random", got.headers.Get("X-Routing-Method"))
			require.NotEmpty(t, got.headers.Get("X-Cache-Affinity-Key"))
			input, err := strconv.Atoi(got.headers.Get("X-Input-Tokens"))
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(input), got.headers.Get("X-Token-Estimate"))
			var consumed []rateLimitCall
			for _, c := range limiter.Calls() {
				if c.mustConsume {
					consumed = append(consumed, c)
					require.NoError(t, c.contextErr)
				}
			}
			require.NotEmpty(t, consumed)
			require.EqualValues(t, input+64, consumed[0].tokensRequested)
			if tc.wantTokens >= 0 {
				require.Len(t, consumed, 2)
				require.EqualValues(t, tc.wantTokens-input-64, consumed[1].tokensRequested)
			} else if tc.status >= 400 {
				require.Len(t, consumed, 2)
				require.EqualValues(t, -64, consumed[1].tokensRequested)
			} else {
				require.Len(t, consumed, 1)
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
		{"noncanonical max", `{"model":"fn-alpha/company-name/model-name","messages":[{}],"MAX_TOKENS":1}`, true, nil, 400},
		{"noncanonical stream", `{"model":"fn-alpha/company-name/model-name","messages":[{}],"max_tokens":64,"Stream":true}`, true, nil, 400},
		{"malformed JSON", `{`, true, nil, 400},
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
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"generated content before cancellation\"}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer backend.Close()
	limiter := &recordingRateLimiter{}
	e, _ := messagesTestAPI(t, backend.URL, config.Default(), limiter, nil)
	handled := make(chan struct{})
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { defer close(handled); return next(c) }
	})
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
	// Wait for deferred reconciliation to complete, then check it did not
	// credit the reservation for generation whose final usage is unknown.
	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not finish reconciliation")
	}
	var tokens int64
	for _, call := range limiter.Calls() {
		if call.mustConsume {
			tokens += call.tokensRequested
			require.NoError(t, call.contextErr)
		}
	}
	require.EqualValues(t, 7+64, tokens, "initial stream usage must not be treated as final output usage")
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
	require.Contains(t, rec.Body.String(), `"type":"rate_limit_error"`)
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
	require.Contains(t, rec.Body.String(), `"type":"request_too_large"`)
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

func TestMessagesUsageFromLargeJSONAndIdentity(t *testing.T) {
	for _, position := range []string{"first", "last"} {
		t.Run(position, func(t *testing.T) {
			usage := `"usage":{"input_tokens":7,"output_tokens":5}`
			content := `"content":[{"type":"text","text":"` + strings.Repeat(`abc\"xyz`, messagesUsageBufferLimit/3) + `"}]`
			response := `{` + usage + `,` + content + `}`
			if position == "last" {
				response = `{` + content + `,` + usage + `}`
			}
			o := &messagesUsageObserver{}
			for i := 0; i < len(response); i += 17 {
				end := i + 17
				if end > len(response) {
					end = len(response)
				}
				o.Write([]byte(response[i:end]))
				require.LessOrEqual(t, len(o.jsonScanner.text)+len(o.jsonScanner.raw), messagesUsageBufferLimit+1024)
			}
			o.finish()
			require.Equal(t, "complete", o.accountingStatus(true))
			require.EqualValues(t, 12, o.usage.chatUsage().TotalTokens)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", "identity")
				io.WriteString(w, response)
			}))
			defer backend.Close()
			limiter := &recordingRateLimiter{}
			e, _ := messagesTestAPI(t, backend.URL, config.Default(), limiter, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, messagesHTTPRequest(messagesTestBody))
			require.Equal(t, response, rec.Body.String())
			var total int64
			for _, call := range limiter.Calls() {
				if call.mustConsume {
					total += call.tokensRequested
				}
			}
			require.EqualValues(t, 12, total)
		})
	}
}

func TestMessagesJSONObserverRejectsInvalidStructure(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"input_tokens":1,"output_tokens":2},"content":["unterminated`,
		`{"usage":{"input_tokens":1,"output_tokens":2}} trailing`,
		`{"usage":{"input_tokens":1,"output_tokens":2},"usage":{"input_tokens":0,"output_tokens":0}}`,
		`{"content":[{"usage":{"input_tokens":1,"output_tokens":2}}]}`,
		`{"usage":{"input_tokens":1,"output_tokens":2},"content":[1,]}`,
	} {
		o := &messagesUsageObserver{}
		o.Write([]byte(body))
		o.finish()
		require.NotEqual(t, "complete", o.accountingStatus(true), body)
	}
}

func TestMessagesSSECompletionAndCR(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		o := &messagesUsageObserver{stream: true}
		wire := strings.ReplaceAll(strings.ReplaceAll(messagesTestSSE, "\r\n", "\n"), "\n", newline)
		for _, b := range []byte(wire) {
			o.Write([]byte{b})
		}
		require.Equal(t, "complete", o.accountingStatus(true))
		require.EqualValues(t, 15, o.usage.chatUsage().TotalTokens)
	}
	o := &messagesUsageObserver{stream: true}
	o.Write([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":7,"output_tokens":1}}}` + "\n\n"))
	require.Equal(t, "partial", o.accountingStatus(true))
	o.Write([]byte(messagesTestSSE + "event: error\ndata: {\"type\":\"error\"}\n\n"))
	require.Equal(t, "error", o.accountingStatus(true))
}

func TestMessagesStripMetadataVariants(t *testing.T) {
	body, err := rewriteMessagesBody([]byte(`{"model":"old","extra-headers":{},"Extra-Headers":{},"extra_headers":{},"EXTRA_HEADERS":{},"metadata":{"user_id":"keep"}}`), "new")
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"new","metadata":{"user_id":"keep"}}`, string(body))
}

func TestMessagesClaudeSessionStableAcrossTurns(t *testing.T) {
	keys := make(chan string, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys <- r.Header.Get("X-Cache-Affinity-Key")
		require.Empty(t, r.Header.Get("x-claude-code-session-id"))
		io.WriteString(w, `{"usage":{"input_tokens":0,"output_tokens":0}}`)
	}))
	defer backend.Close()
	e, _ := messagesTestAPI(t, backend.URL, config.Default(), nil, nil)
	for _, body := range []string{messagesTestBody, strings.Replace(messagesTestBody, `"Hello"`, `"follow-up"`, 1)} {
		req := messagesHTTPRequest(body)
		req.Header.Del(HeaderMultiTurnSessionID)
		req.Header.Set("x-claude-code-session-id", "claude-session")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code)
		require.Equal(t, "claude-session", rec.Header().Get(HeaderMultiTurnSessionID))
	}
	require.Equal(t, <-keys, <-keys)
}

func TestMessagesNativeGatewayErrors(t *testing.T) {
	e, _ := messagesTestAPI(t, "http://127.0.0.1:1", config.Default(), nil, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, messagesHTTPRequest(`{`))
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"error"`)
	require.Contains(t, rec.Body.String(), `"type":"invalid_request_error"`)
	require.NotContains(t, rec.Body.String(), "model must be prefixed")
}

func TestMessagesInBandErrorsAndUsageObservability(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldMeter := otel.GetMeterProvider()
	otel.SetMeterProvider(meter)
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldTracer := telemetry.Tracer
	telemetry.Tracer = sync.OnceValue(func() trace.Tracer { return tracerProvider.Tracer("test") })
	defer func() {
		otel.SetMeterProvider(oldMeter)
		telemetry.Tracer = oldTracer
		meter.Shutdown(context.Background())
		tracerProvider.Shutdown(context.Background())
	}()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, messagesTestSSE+"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n")
	}))
	defer backend.Close()
	e, _ := messagesTestAPI(t, backend.URL, config.Default(), nil, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, messagesHTTPRequest(strings.Replace(messagesTestBody, `"max_tokens":64`, `"max_tokens":64,"stream":true`, 1)))
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "event: error")
	metrics := collectMetrics(t, reader)
	assertMetricHasAttributes(t, metrics, "llm_api_gateway_stream_duration_seconds", map[string]string{"status": "error", "function_id": "fn-alpha"})
	assertMetricHasAttributes(t, metrics, "llm_api_gateway_messages_usage_observations_total", map[string]string{"status": "error", "stream": "true", "function_id": "fn-alpha"})
	found := false
	for _, span := range recorder.Ended() {
		if span.Name() == "llm-api-gateway.stream" {
			found = true
			require.Equal(t, codes.Error, span.Status().Code)
			assertHasAttribute(t, span.Attributes(), "nvcf.function.id", "fn-alpha")
		}
		if span.Name() == "POST /v1/messages" {
			assertHasAttribute(t, span.Attributes(), "nvcf.function.id", "fn-alpha")
		}
	}
	require.True(t, found)
}

func FuzzMessagesJSONUsageScanner(f *testing.F) {
	for _, seed := range []string{
		`{"usage":{"input_tokens":7,"output_tokens":5},"content":[{"text":"a\\b\"c"}]}`,
		`{"content":[null,true,false,-1.25e+2,{},[]],"usage":{"input_tokens":0,"output_tokens":0}}`,
		`{"usage":{"input_tokens":1,"output_tokens":2},"content":[1,]}`,
		`{"usage":{"input_tokens":1,"output_tokens":2},"usage":null}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 2*messagesUsageBufferLimit {
			t.Skip()
		}
		o := &messagesUsageObserver{}
		for i := 0; i < len(body); i += 7 {
			end := i + 7
			if end > len(body) {
				end = len(body)
			}
			o.Write([]byte(body[i:end]))
		}
		o.finish()
		if o.accountingStatus(true) != "complete" {
			return
		}
		require.True(t, json.Valid([]byte(body)), "observer accepted malformed JSON")
		var parsed struct {
			Usage messagesUsage `json:"usage"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &parsed))
		require.Equal(t, parsed.Usage.chatUsage(), o.usage.chatUsage())
	})
}

func TestMessagesUnknownDispatchOutcomeRetainsReservation(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The backend received the request but failed before sending headers.
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		conn.Close()
	}))
	defer backend.Close()
	limiter := &recordingRateLimiter{}
	e, _ := messagesTestAPI(t, backend.URL, config.Default(), limiter, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, messagesHTTPRequest(messagesTestBody))
	require.GreaterOrEqual(t, rec.Code, 500)
	var charged int64
	for _, call := range limiter.Calls() {
		if call.mustConsume {
			charged += call.tokensRequested
			require.NoError(t, call.contextErr)
		}
	}
	input := estimatedTokenCountForText(string(jsonObject(t, []byte(messagesTestBody))["messages"])) + estimatedTokenCountForText(string(jsonObject(t, []byte(messagesTestBody))["system"])) + estimatedTokenCountForText(string(jsonObject(t, []byte(messagesTestBody))["tools"]))
	require.EqualValues(t, input+64, charged)
}
