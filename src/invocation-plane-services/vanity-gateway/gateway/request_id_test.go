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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	xRequestIDHeader        = "X-Request-Id"
	messagesRequestIDHeader = "Request-Id"
)

func requestIDMappings() *config.GatewayConfig {
	mappings := llmMappings(llmModelEntry())
	mappings.OpenAI.ChatCompletions["nvcf"] = config.ModelFunctionDetails{ModelName: "nvcf/model", FunctionID: "nvcf-func"}
	mappings.AnthropicMessages = map[string]config.ModelFunctionDetails{"native": llmModelEntry()}
	return mappings
}

// requireOneRequestID checks the response carries exactly one X-Request-Id and,
// on Messages, one request-id with the same value. It returns the ID.
func requireOneRequestID(t *testing.T, header http.Header, messages bool) string {
	t.Helper()
	ids := header.Values(xRequestIDHeader)
	require.Len(t, ids, 1, "response must carry exactly one X-Request-Id")
	require.NotEmpty(t, ids[0])
	if messages {
		require.Equal(t, ids, header.Values(messagesRequestIDHeader), "Messages request-id must equal X-Request-Id")
	}
	return ids[0]
}

func TestGatewayGeneratedErrorsCarryRequestID(t *testing.T) {
	tooLarge := `{"model":"` + strings.Repeat("a", maxRequestSize) + `"}`
	tests := []struct {
		name   string
		path   string
		body   string
		header map[string]string
		status int
	}{
		{"messages malformed json", "/v1/messages", `{`, nil, http.StatusBadRequest},
		{"messages missing model", "/v1/messages", `{"messages":[]}`, nil, http.StatusBadRequest},
		{"messages unknown model", "/v1/messages", `{"model":"unknown"}`, nil, http.StatusNotFound},
		{"messages body too large", "/v1/messages", tooLarge, nil, http.StatusRequestEntityTooLarge},
		{"messages reserved shadow header", "/v1/messages", `{"model":"` + publicModel + `"}`, map[string]string{shadowHeader: "true"}, http.StatusBadRequest},
		{"chat malformed json", "/v1/chat/completions", `{`, nil, http.StatusBadRequest},
		{"chat unknown model", "/v1/chat/completions", `{"model":"unknown"}`, nil, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := openAIMux(t, requestIDMappings(), "http://nvcf.invalid", "http://llm.invalid")
			req := openAIRequest(t, tc.path, tc.body)
			req.Header.Set("Content-Type", "application/json")
			for name, value := range tc.header {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			requireOneRequestID(t, rec.Header(), tc.path == "/v1/messages")
		})
	}
}

func TestProxiedResponsesCarryOneRequestIDSentUpstream(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		model     string
		inboundID string
		stream    bool
	}{
		{"messages generates id", "/v1/messages", publicModel, "", false},
		{"messages keeps caller id", "/v1/messages", publicModel, "caller-id-1", false},
		{"messages stream", "/v1/messages", publicModel, "", true},
		{"chat through invocation service", "/v1/chat/completions", "nvcf/model", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			upstream := captureServer(t, requests, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(xRequestIDHeader, "upstream-x-request-id")
				w.Header().Set(messagesRequestIDHeader, "upstream-request-id")
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "event: message_start\ndata: {}\n\n")
					w.(http.Flusher).Flush()
					io.WriteString(w, "event: message_stop\ndata: {}\n\n")
					return
				}
				io.WriteString(w, `{}`)
			})
			mux := openAIMux(t, requestIDMappings(), upstream.URL, upstream.URL)
			body := `{"model":"` + tc.model + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]`
			if tc.stream {
				body += `,"stream":true`
			}
			req := openAIRequest(t, tc.path, body+`}`)
			req.Header.Set("Content-Type", "application/json")
			if tc.inboundID != "" {
				req.Header.Set(xRequestIDHeader, tc.inboundID)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			id := requireOneRequestID(t, rec.Header(), tc.path == "/v1/messages")
			if tc.inboundID != "" {
				require.Equal(t, tc.inboundID, id, "a caller-supplied X-Request-Id is kept")
			}
			received := awaitRequest(t, requests)
			require.Equal(t, []string{id}, received.headers.Values(xRequestIDHeader), "upstream must see the same id")
		})
	}
}

func TestInvalidCallerRequestIDsAreRegenerated(t *testing.T) {
	tests := []struct {
		name      string
		inboundID string
		wantKept  bool
	}{
		{"maximum length and allowed characters", strings.Repeat("a", 122) + "._:-Z9", true},
		{"too long", strings.Repeat("a", 129), false},
		{"space", "caller id", false},
		{"disallowed character", "caller+id", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			upstream := captureServer(t, requests, func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, `{}`)
			})
			mux := openAIMux(t, requestIDMappings(), upstream.URL, upstream.URL)
			req := openAIRequest(t, "/v1/chat/completions", `{"model":"nvcf/model","messages":[]}`)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(xRequestIDHeader, tc.inboundID)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			id := requireOneRequestID(t, rec.Header(), false)
			if tc.wantKept {
				require.Equal(t, tc.inboundID, id)
			} else {
				require.NotEqual(t, tc.inboundID, id)
				require.Len(t, id, 36, "regenerated request ID should be a UUID")
			}
			received := awaitRequest(t, requests)
			require.Equal(t, []string{id}, received.headers.Values(xRequestIDHeader))
		})
	}
}

// Each shadow copy is a separate upstream request, so it gets its own ID. The
// trace still links it to the primary.
func TestShadowCopyGetsItsOwnRequestID(t *testing.T) {
	type received struct {
		id     []string
		shadow bool
	}
	requests := make(chan received, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests <- received{id: r.Header.Values(xRequestIDHeader), shadow: r.Header.Get(shadowHeader) == shadowHeaderValue}
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(upstream.Close)

	primary := llmModelEntry()
	primary.ShadowModelName = "native-shadow"
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.AnthropicMessages = map[string]config.ModelFunctionDetails{
		"primary": primary,
		"shadow":  {ModelName: "native-shadow", FunctionID: "shadow-func", FunctionType: config.FunctionTypeLLM},
	}
	require.NoError(t, mappings.Validate())
	mux := openAIMux(t, mappings, "http://nvcf.invalid", upstream.URL)

	rec := httptest.NewRecorder()
	req := openAIRequest(t, "/v1/messages", `{"model":"`+publicModel+`","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	req.Header.Set(xRequestIDHeader, "caller-id-1")
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	got := map[bool][]string{}
	for range 2 {
		r := <-requests
		got[r.shadow] = r.id
	}
	require.Equal(t, []string{"caller-id-1"}, got[false])
	require.Len(t, got[true], 1)
	require.NotEmpty(t, got[true][0])
	require.NotEqual(t, "caller-id-1", got[true][0], "shadow copy must not reuse the primary id")
}

// Pass-through vanity hosts keep their pre-existing header behavior: the
// gateway neither creates nor replaces request IDs there, so a function that
// mints its own ID still reaches the client.
func TestVanityHostPassesRequestIDsThrough(t *testing.T) {
	const vanityHost = "vanity.test"
	tests := []struct {
		name      string
		path      string
		inboundID string
	}{
		{"no caller id", "/v1/example/infer", ""},
		{"caller id", "/v1/example/infer", "caller-id-1"},
		{"messages path no caller id", "/v1/messages", ""},
		{"messages path caller id", "/v1/messages", "caller-id-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			upstream := captureServer(t, requests, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(xRequestIDHeader, "function-x-request-id")
				w.Header().Set(messagesRequestIDHeader, "function-request-id")
				io.WriteString(w, `{}`)
			})
			mappings := requestIDMappings()
			mappings.Vanity = map[string]config.VanityEntry{
				"example": {
					Host: vanityHost,
					Paths: map[string]config.PathFunctionDetails{
						"infer":    {Path: "/v1/example/infer", FunctionID: "vanity-func"},
						"messages": {Path: "/v1/messages", FunctionID: "vanity-messages-func"},
					},
				},
			}
			mux := openAIMux(t, mappings, upstream.URL, upstream.URL)

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			req.Host = vanityHost
			if tc.inboundID != "" {
				req.Header.Set(xRequestIDHeader, tc.inboundID)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			require.Equal(t, []string{"function-x-request-id"}, rec.Header().Values(xRequestIDHeader), "the function's X-Request-Id must reach the client")
			require.Equal(t, []string{"function-request-id"}, rec.Header().Values(messagesRequestIDHeader), "the function's Request-Id must reach the client")

			received := awaitRequest(t, requests)
			if tc.inboundID == "" {
				require.Empty(t, received.headers.Values(xRequestIDHeader), "the gateway must not add an X-Request-Id on vanity hosts")
			} else {
				require.Equal(t, []string{tc.inboundID}, received.headers.Values(xRequestIDHeader), "the caller's X-Request-Id is forwarded unchanged")
			}
		})
	}
}

// Requests to a host the gateway does not route keep the pre-existing
// behavior too.
func TestUnroutedHostGetsNoRequestID(t *testing.T) {
	mux := openAIMux(t, requestIDMappings(), "http://nvcf.invalid", "http://llm.invalid")
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	req.Host = "other.test"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Values(xRequestIDHeader))
}
