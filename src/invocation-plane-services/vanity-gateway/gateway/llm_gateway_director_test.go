/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gateway

import (
	config "ai-api-gateway-service/gateway_config"
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const llmHost = "llm.test"

type capturedRequest struct {
	path    string
	headers http.Header
	body    string
}

func captureServer(t *testing.T, requests chan<- capturedRequest, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		requests <- capturedRequest{path: r.URL.Path, headers: r.Header.Clone(), body: string(body)}
		if handler != nil {
			handler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

func llmGatewayMappings(entry config.LLMGatewayEntry) *config.GatewayConfig {
	mappings := &config.GatewayConfig{}
	mappings.LLMGateway = map[string]config.LLMGatewayEntry{"llm_example": entry}
	return mappings
}

func llmGatewayMux(t *testing.T, mappings *config.GatewayConfig, llmEndpoint string) http.Handler {
	t.Helper()
	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              "http://nvcf.invalid",
		LLMGatewayEndpoint:           llmEndpoint,
		PrivateModelNameRegexPattern: "^$",
	})
	require.NoError(t, err)
	return mux
}

func llmGatewayRequest(t *testing.T, path string, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Host = llmHost
	return req
}

func awaitRequest(t *testing.T, requests <-chan capturedRequest) capturedRequest {
	t.Helper()
	select {
	case received := <-requests:
		return received
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream request")
		return capturedRequest{}
	}
}

func TestNewLLMGatewayDirectorRejectsInvalidEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "llm-api-gateway:8080", "://bad", "/relative"} {
		t.Run(endpoint, func(t *testing.T) {
			director, err := NewLLMGatewayDirector(endpoint, http.DefaultTransport)
			require.Error(t, err)
			assert.Nil(t, director)
			assert.ErrorContains(t, err, "invalid LLM Gateway endpoint")
		})
	}
}

func TestBuildChiMux_LLMGatewayServesSupportedRoutes(t *testing.T) {
	requests := make(chan capturedRequest, 1)
	backend := captureServer(t, requests, nil)
	mux := llmGatewayMux(t, llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost}), backend.URL)

	for _, path := range llmGatewaySupportedPaths {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, llmGatewayRequest(t, path, `{"model":"func-id/meta/llama"}`))

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, path, awaitRequest(t, requests).path)
		})
	}
}

func TestBuildChiMux_LLMGatewayDoesNotServeUnsupportedRoutes(t *testing.T) {
	requests := make(chan capturedRequest, 1)
	backend := captureServer(t, requests, nil)
	mux := llmGatewayMux(t, llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost}), backend.URL)

	for _, path := range []string{"/v1/completions", "/v1/models", "/v1/images/generations"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, llmGatewayRequest(t, path, `{}`))

			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Empty(t, requests, "unsupported routes must not reach the LLM Gateway")
		})
	}
}

func TestBuildChiMux_LLMGatewayAndVanityCoexist(t *testing.T) {
	nvcfRequests := make(chan capturedRequest, 1)
	nvcfBackend := captureServer(t, nvcfRequests, nil)
	llmRequests := make(chan capturedRequest, 1)
	llmBackend := captureServer(t, llmRequests, nil)

	mappings := llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost})
	mappings.Vanity = map[string]config.VanityEntry{
		"example": {
			Host: "vanity.test",
			Paths: map[string]config.PathFunctionDetails{
				"infer": {Path: "/v1/example/infer", FunctionID: "vanity-func"},
			},
		},
	}

	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              nvcfBackend.URL,
		LLMGatewayEndpoint:           llmBackend.URL,
		PrivateModelNameRegexPattern: "^$",
	})
	require.NoError(t, err)

	chatRec := httptest.NewRecorder()
	mux.ServeHTTP(chatRec, llmGatewayRequest(t, "/v1/chat/completions", `{"model":"func-id/meta/llama"}`))
	require.Equal(t, http.StatusOK, chatRec.Code)

	llmReceived := awaitRequest(t, llmRequests)
	assert.Equal(t, "/v1/chat/completions", llmReceived.path)
	assert.Empty(t, llmReceived.headers.Get("function-id"))

	inferReq := httptest.NewRequest(http.MethodPost, "/v1/example/infer", bytes.NewBufferString(`{}`))
	inferReq.Host = "vanity.test"
	inferRec := httptest.NewRecorder()
	mux.ServeHTTP(inferRec, inferReq)
	require.Equal(t, http.StatusOK, inferRec.Code)

	nvcfReceived := awaitRequest(t, nvcfRequests)
	assert.Equal(t, "/v1/example/infer", nvcfReceived.path)
	assert.Equal(t, "vanity-func", nvcfReceived.headers.Get("function-id"))
	assert.Empty(t, llmRequests)
}

func TestBuildChiMux_LLMGatewayForwardsRequestUnchanged(t *testing.T) {
	requests := make(chan capturedRequest, 1)
	backend := captureServer(t, requests, nil)
	mux := llmGatewayMux(t, llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost}), backend.URL)

	body := `{"model":"func-id/meta/llama-3.3-70b","messages":[{"role":"user","content":"hi"}]}`
	req := llmGatewayRequest(t, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	received := awaitRequest(t, requests)
	assert.Equal(t, body, received.body, "request body must reach the LLM Gateway unmodified")
	assert.Equal(t, "Bearer caller-token", received.headers.Get("Authorization"))
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", received.headers.Get("traceparent"))
	assert.Empty(t, received.headers.Get("function-id"))
	assert.Empty(t, received.headers.Get("function-version-id"))
	assert.Empty(t, received.headers.Get("NVCF-POLL-SECONDS"))
}

func TestBuildChiMux_LLMGatewayAppliesCustomHeaders(t *testing.T) {
	requests := make(chan capturedRequest, 1)
	backend := captureServer(t, requests, nil)
	mappings := llmGatewayMappings(config.LLMGatewayEntry{
		Host:          llmHost,
		CustomHeaders: config.CustomHeaders{"X-Provider-Feature": "enabled"},
	})
	mux := llmGatewayMux(t, mappings, backend.URL)

	req := llmGatewayRequest(t, "/v1/embeddings", `{}`)
	req.Header.Set("X-Provider-Feature", "caller-value")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "enabled", awaitRequest(t, requests).headers.Get("X-Provider-Feature"))
}

func TestBuildChiMux_LLMGatewayRequiresEndpoint(t *testing.T) {
	_, err := buildChiMux(llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost}), Config{
		NvcfApiEndpoint:              "http://nvcf.invalid",
		PrivateModelNameRegexPattern: "^$",
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "LLM_GATEWAY_ENDPOINT is required")
}

func TestBuildChiMux_LLMGatewayEndpointNotRequiredWithoutSection(t *testing.T) {
	requests := make(chan capturedRequest, 1)
	backend := captureServer(t, requests, nil)

	mappings := &config.GatewayConfig{}
	mappings.Vanity = map[string]config.VanityEntry{
		"example": {
			Host:  "vanity.test",
			Paths: map[string]config.PathFunctionDetails{"infer": {Path: "/v1/example/infer", FunctionID: "vanity-func"}},
		},
	}

	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              backend.URL,
		PrivateModelNameRegexPattern: "^$",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/example/infer", bytes.NewBufferString(`{}`))
	req.Host = "vanity.test"
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestLLMGatewayDirectorOfflineMessageReturns503(t *testing.T) {
	requests := make(chan capturedRequest, 1)
	backend := captureServer(t, requests, nil)
	mappings := llmGatewayMappings(config.LLMGatewayEntry{
		Host:           llmHost,
		OfflineMessage: "temporarily offline",
	})
	mux := llmGatewayMux(t, mappings, backend.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, llmGatewayRequest(t, "/v1/chat/completions", `{}`))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "temporarily offline")
	assert.Empty(t, requests, "offline hosts must not reach the LLM Gateway")
}

func TestLLMGatewayDirectorEOLHandling(t *testing.T) {
	tests := []struct {
		name           string
		eol            time.Time
		wantStatus     int
		wantDeprecated bool
	}{
		{name: "future EOL adds Deprecation header", eol: time.Now().Add(24 * time.Hour), wantStatus: http.StatusOK, wantDeprecated: true},
		{name: "expired EOL returns 410", eol: time.Now().Add(-24 * time.Hour), wantStatus: http.StatusGone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			backend := captureServer(t, requests, nil)
			mux := llmGatewayMux(t, llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost, EOL: tc.eol}), backend.URL)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, llmGatewayRequest(t, "/v1/chat/completions", `{}`))

			assert.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantDeprecated {
				assert.Equal(t, tc.eol.Format(time.RFC3339), rec.Header().Get("Deprecation"))
			} else {
				assert.Empty(t, rec.Header().Get("Deprecation"))
			}
		})
	}
}

func TestLLMGatewayDirectorUpstreamFailureReturns502(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := backend.URL
	backend.Close()

	mux := llmGatewayMux(t, llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost}), endpoint)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, llmGatewayRequest(t, "/v1/chat/completions", `{}`))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
}

func TestLLMGatewayDirectorStreamsResponseIncrementally(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		_, _ = w.Write([]byte("data: first\n\n"))
		flusher.Flush()
		<-release
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	t.Cleanup(backend.Close)

	mux := llmGatewayMux(t, llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost}), backend.URL)
	proxy := httptest.NewServer(mux)
	t.Cleanup(proxy.Close)

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", bytes.NewBufferString(`{"stream":true}`))
	require.NoError(t, err)
	req.Host = llmHost

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: first\n", line, "first chunk must arrive before the upstream finishes")

	close(release)
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Contains(t, string(rest), "data: [DONE]")
}

// The openai section and the llmGateway section both serve
// /v1/chat/completions. They never compete, because routing selects a host
// first and each host owns a separate route table.
func TestBuildChiMux_SamePathOnOpenAIAndLLMGatewayHosts(t *testing.T) {
	nvcfRequests := make(chan capturedRequest, 1)
	nvcfBackend := captureServer(t, nvcfRequests, nil)
	llmRequests := make(chan capturedRequest, 1)
	llmBackend := captureServer(t, llmRequests, nil)

	mappings := llmGatewayMappings(config.LLMGatewayEntry{Host: llmHost})
	mappings.OpenAI.Host = "openai.test"
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"llama": {ModelName: "meta/llama-3.3-70b", FunctionID: "openai-func"},
	}

	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              nvcfBackend.URL,
		LLMGatewayEndpoint:           llmBackend.URL,
		PrivateModelNameRegexPattern: "^$",
	})
	require.NoError(t, err)

	openAIReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"meta/llama-3.3-70b"}`))
	openAIReq.Host = "openai.test"
	openAIRec := httptest.NewRecorder()
	mux.ServeHTTP(openAIRec, openAIReq)
	require.Equal(t, http.StatusOK, openAIRec.Code)

	openAIReceived := awaitRequest(t, nvcfRequests)
	assert.Equal(t, "openai-func", openAIReceived.headers.Get("function-id"))
	assert.Empty(t, llmRequests, "the openai host must not reach the LLM Gateway")

	llmRec := httptest.NewRecorder()
	mux.ServeHTTP(llmRec, llmGatewayRequest(t, "/v1/chat/completions", `{"model":"func-id/meta/llama-3.3-70b"}`))
	require.Equal(t, http.StatusOK, llmRec.Code)

	llmReceived := awaitRequest(t, llmRequests)
	assert.Equal(t, `{"model":"func-id/meta/llama-3.3-70b"}`, llmReceived.body)
	assert.Empty(t, llmReceived.headers.Get("function-id"))
	assert.Empty(t, nvcfRequests, "the llmGateway host must not reach the invocation service")
}

func TestBuildChiMux_LLMGatewayRejectsSelfProxyingHost(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		endpoint string
		wantErr  bool
	}{
		{name: "host matches endpoint with port", host: "llm.test", endpoint: "http://llm.test:8080", wantErr: true},
		{name: "host matches endpoint without port", host: "llm.test", endpoint: "http://llm.test", wantErr: true},
		{name: "host carries a port too", host: "llm.test:443", endpoint: "http://llm.test:8080", wantErr: true},
		{name: "distinct host and endpoint", host: "llm.test", endpoint: "http://llm-api-gateway.nvcf.svc.cluster.local:8080", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildChiMux(llmGatewayMappings(config.LLMGatewayEntry{Host: tc.host}), Config{
				NvcfApiEndpoint:              "http://nvcf.invalid",
				LLMGatewayEndpoint:           tc.endpoint,
				PrivateModelNameRegexPattern: "^$",
			})

			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, "the gateway would proxy to itself")
		})
	}
}
