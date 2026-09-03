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

const (
	openAIHost    = "api.test"
	publicModel   = "meta/llama-3.3-70b"
	llmFunctionID = "func-id-1"
)

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

// llmMappings routes one public model to the LLM Gateway on the openai host.
func llmMappings(entry config.ModelFunctionDetails) *config.GatewayConfig {
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{"llm": entry}
	return mappings
}

func llmModelEntry() config.ModelFunctionDetails {
	return config.ModelFunctionDetails{
		ModelName:    publicModel,
		FunctionID:   llmFunctionID,
		FunctionType: config.FunctionTypeLLM,
	}
}

func openAIMux(t *testing.T, mappings *config.GatewayConfig, nvcfEndpoint, llmEndpoint string) http.Handler {
	t.Helper()
	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              nvcfEndpoint,
		LLMGatewayEndpoint:           llmEndpoint,
		PrivateModelNameRegexPattern: "^$",
	})
	require.NoError(t, err)
	return mux
}

func openAIRequest(t *testing.T, path, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Host = openAIHost
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

func TestNewLLMGatewayDirectorRejectsNonHTTPScheme(t *testing.T) {
	for _, endpoint := range []string{"ftp://llm.test", "gopher://llm.test:70", "ws://llm.test"} {
		t.Run(endpoint, func(t *testing.T) {
			director, err := NewLLMGatewayDirector(endpoint, http.DefaultTransport)
			require.Error(t, err)
			assert.Nil(t, director)
			assert.ErrorContains(t, err, "must use http or https")
		})
	}

	for _, endpoint := range []string{"http://llm.test:8080", "https://llm.test"} {
		t.Run(endpoint, func(t *testing.T) {
			director, err := NewLLMGatewayDirector(endpoint, http.DefaultTransport)
			require.NoError(t, err)
			assert.NotNil(t, director)
		})
	}
}

func TestNewLLMGatewayDirectorRejectsEndpointPath(t *testing.T) {
	for _, endpoint := range []string{"http://llm.test/llm", "http://llm.test:8080/v1"} {
		t.Run(endpoint, func(t *testing.T) {
			director, err := NewLLMGatewayDirector(endpoint, http.DefaultTransport)
			require.Error(t, err)
			assert.Nil(t, director)
			assert.ErrorContains(t, err, "must not set a path")
		})
	}

	director, err := NewLLMGatewayDirector("http://llm.test:8080/", http.DefaultTransport)
	require.NoError(t, err, "a bare trailing slash is not a path prefix")
	assert.NotNil(t, director)
}

// The caller sends the public model name; the gateway adds the function ID so
// the LLM Gateway can route it. Callers never see the function ID.
func TestOpenAIDirector_LLMModelRewritesModelAndRoutesToLLMGateway(t *testing.T) {
	nvcfRequests := make(chan capturedRequest, 1)
	nvcfBackend := captureServer(t, nvcfRequests, nil)
	llmRequests := make(chan capturedRequest, 1)
	llmBackend := captureServer(t, llmRequests, nil)

	mux := openAIMux(t, llmMappings(llmModelEntry()), nvcfBackend.URL, llmBackend.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, openAIRequest(t, "/v1/chat/completions",
		`{"model":"`+publicModel+`","messages":[{"role":"user","content":"hi"}]}`))
	require.Equal(t, http.StatusOK, rec.Code)

	received := awaitRequest(t, llmRequests)
	assert.Equal(t, "/v1/chat/completions", received.path)
	assert.Contains(t, received.body, `"model":"`+llmFunctionID+"/"+publicModel+`"`,
		"model must be rewritten to functionID/modelName")
	assert.Contains(t, received.body, `"messages"`, "the rest of the body must survive the rewrite")
	assert.Empty(t, received.headers.Get("function-id"), "function selection is carried in the model, not headers")
	assert.Empty(t, received.headers.Get("NVCF-POLL-SECONDS"))
	assert.Empty(t, nvcfRequests, "an LLM model must not reach the invocation service")
}

func TestOpenAIDirector_DefaultModelStillRoutesToInvocationService(t *testing.T) {
	nvcfRequests := make(chan capturedRequest, 1)
	nvcfBackend := captureServer(t, nvcfRequests, nil)
	llmRequests := make(chan capturedRequest, 1)
	llmBackend := captureServer(t, llmRequests, nil)

	mappings := llmMappings(llmModelEntry())
	mappings.OpenAI.ChatCompletions["plain"] = config.ModelFunctionDetails{
		ModelName:  "microsoft/phi-2",
		FunctionID: "plain-func",
	}
	mux := openAIMux(t, mappings, nvcfBackend.URL, llmBackend.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, openAIRequest(t, "/v1/chat/completions",
		`{"model":"microsoft/phi-2","messages":[{"role":"user","content":"hi"}]}`))
	require.Equal(t, http.StatusOK, rec.Code)

	received := awaitRequest(t, nvcfRequests)
	assert.Equal(t, "plain-func", received.headers.Get("function-id"))
	assert.Contains(t, received.body, `"model":"microsoft/phi-2"`, "a default model is not rewritten")
	assert.Empty(t, llmRequests, "a default model must not reach the LLM Gateway")
}

func TestOpenAIDirector_LLMModelAppliesCustomHeaders(t *testing.T) {
	requests := make(chan capturedRequest, 1)
	backend := captureServer(t, requests, nil)

	entry := llmModelEntry()
	entry.CustomHeaders = config.CustomHeaders{"X-Provider-Feature": "enabled"}
	mux := openAIMux(t, llmMappings(entry), "http://nvcf.invalid", backend.URL)

	req := openAIRequest(t, "/v1/chat/completions", `{"model":"`+publicModel+`"}`)
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set("X-Provider-Feature", "caller-value")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	received := awaitRequest(t, requests)
	assert.Equal(t, "enabled", received.headers.Get("X-Provider-Feature"))
	assert.Equal(t, "Bearer caller-token", received.headers.Get("Authorization"))
}

func TestOpenAIDirector_UnknownModelStill404s(t *testing.T) {
	mux := openAIMux(t, llmMappings(llmModelEntry()), "http://nvcf.invalid", "http://llm.invalid")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, openAIRequest(t, "/v1/chat/completions", `{"model":"nope/not-configured"}`))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBuildChiMux_LLMModelRequiresEndpoint(t *testing.T) {
	_, err := buildChiMux(llmMappings(llmModelEntry()), Config{
		NvcfApiEndpoint:              "http://nvcf.invalid",
		PrivateModelNameRegexPattern: "^$",
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "LLM_GATEWAY_ENDPOINT is required")
}

func TestOpenAIDirector_LLMModelOfflineAndEOL(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(e *config.ModelFunctionDetails)
		wantStatus int
	}{
		{"offline", func(e *config.ModelFunctionDetails) { e.OfflineMessage = "temporarily offline" }, http.StatusServiceUnavailable},
		{"expired eol", func(e *config.ModelFunctionDetails) { e.EOL = time.Now().Add(-24 * time.Hour) }, http.StatusGone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			backend := captureServer(t, requests, nil)
			entry := llmModelEntry()
			tc.mutate(&entry)
			mux := openAIMux(t, llmMappings(entry), "http://nvcf.invalid", backend.URL)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, openAIRequest(t, "/v1/chat/completions", `{"model":"`+publicModel+`"}`))
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Empty(t, requests, "the request must not reach the LLM Gateway")
		})
	}
}

func TestOpenAIDirector_LLMModelStreamsResponseIncrementally(t *testing.T) {
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

	mux := openAIMux(t, llmMappings(llmModelEntry()), "http://nvcf.invalid", backend.URL)
	proxy := httptest.NewServer(mux)
	t.Cleanup(proxy.Close)

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"`+publicModel+`","stream":true}`))
	require.NoError(t, err)
	req.Host = openAIHost

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

// The invocation service serves /health; the LLM Gateway serves /healthz.
func TestBuildChiMux_HealthProbesUseUpstreamSpecificPaths(t *testing.T) {
	nvcfPaths := make(chan string, 4)
	nvcfBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nvcfPaths <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(nvcfBackend.Close)

	llmPaths := make(chan string, 4)
	llmBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmPaths <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(llmBackend.Close)

	mux := openAIMux(t, llmMappings(llmModelEntry()), nvcfBackend.URL, llmBackend.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Host = openAIHost
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	assertPath := func(t *testing.T, paths <-chan string, want string) {
		t.Helper()
		select {
		case got := <-paths:
			assert.Equal(t, want, got)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for a probe of %q", want)
		}
	}
	assertPath(t, nvcfPaths, "/health")
	assertPath(t, llmPaths, "/healthz")
}

func TestOpenAIDirector_LLMModelShadowsToLLMGateway(t *testing.T) {
	const shadowModel = "meta/llama-3.3-70b-next"

	llmRequests := make(chan capturedRequest, 2)
	llmBackend := captureServer(t, llmRequests, nil)
	nvcfRequests := make(chan capturedRequest, 1)
	nvcfBackend := captureServer(t, nvcfRequests, nil)

	primary := llmModelEntry()
	primary.ShadowModelNames = []string{shadowModel}
	shadow := llmModelEntry()
	shadow.ModelName = shadowModel

	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"llm":        primary,
		"llm-shadow": shadow,
	}
	require.NoError(t, mappings.Validate(), "shadow traffic must be valid on LLM models")

	mux := openAIMux(t, mappings, nvcfBackend.URL, llmBackend.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, openAIRequest(t, "/v1/chat/completions",
		`{"model":"`+publicModel+`","messages":[{"role":"user","content":"hi"}]}`))
	require.Equal(t, http.StatusOK, rec.Code)

	// Both the primary and the shadow must reach the LLM Gateway, each rewritten
	// to its own functionID/modelName. Order between them is not guaranteed.
	got := []string{awaitRequest(t, llmRequests).body, awaitRequest(t, llmRequests).body}
	assert.Contains(t, got[0]+got[1], `"model":"`+llmFunctionID+"/"+publicModel+`"`)
	assert.Contains(t, got[0]+got[1], `"model":"`+llmFunctionID+"/"+shadowModel+`"`)
	assert.Empty(t, nvcfRequests, "neither request may reach the invocation service")
}

// A caller must not be able to aim NVCF routing headers at the LLM Gateway. The
// invocation path always sets or deletes these; this path deletes them because
// the LLM Gateway resolves the function from the model.
func TestOpenAIDirector_LLMModelStripsCallerRoutingHeaders(t *testing.T) {
	llmRequests := make(chan capturedRequest, 1)
	llmBackend := captureServer(t, llmRequests, nil)

	mux := openAIMux(t, llmMappings(llmModelEntry()), "http://nvcf.invalid", llmBackend.URL)

	req := openAIRequest(t, "/v1/chat/completions", `{"model":"`+publicModel+`"}`)
	req.Header.Set("function-id", "caller-supplied")
	req.Header.Set("function-version-id", "caller-supplied")
	req.Header.Set("nvcf-function-id", "caller-supplied")
	req.Header.Set("NVCF-POLL-SECONDS", "999")
	req.Header.Set("Authorization", "Bearer caller-token")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	received := awaitRequest(t, llmRequests)
	for _, name := range []string{"function-id", "function-version-id", "nvcf-function-id"} {
		assert.Empty(t, received.headers.Get(name), "%s must not reach the LLM Gateway", name)
	}
	assert.Equal(t, "Bearer caller-token", received.headers.Get("Authorization"),
		"the caller principal must still be forwarded")
	assert.Equal(t, "999", received.headers.Get("NVCF-POLL-SECONDS"),
		"poll seconds is a caller knob the invocation path honors too, not a routing header")
}

// /health is wired to both the readiness and liveness probes, so a gating LLM
// check would restart every pod whenever the LLM Gateway is down, taking
// invocation-service routing with it. The nvcf api is a dependency of every
// request, so that one stays gating.
func TestBuildChiMux_LLMGatewayOutageDoesNotFailHealth(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(up.Close)

	probe := func(nvcfEndpoint, llmEndpoint string) (int, string) {
		mux := openAIMux(t, llmMappings(llmModelEntry()), nvcfEndpoint, llmEndpoint)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host = openAIHost
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	code, body := probe(up.URL, down.URL)
	assert.Equal(t, http.StatusOK, code, "an LLM Gateway outage must not fail the probes")
	assert.Contains(t, body, "llm api gateway", "the failure must still be reported in the body")

	code, _ = probe(down.URL, up.URL)
	assert.Equal(t, http.StatusServiceUnavailable, code, "an nvcf api outage must still fail the probes")

	code, _ = probe(up.URL, up.URL)
	assert.Equal(t, http.StatusOK, code, "both upstreams healthy is OK")
}
