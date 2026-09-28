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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// These tests run requests through the registered routes and a real
// StargateProvider against a fake upstream, so every gateway transform on
// the path is exercised.

const passthroughChatUpstreamSSE = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"},"logprobs":{"content":[{"token":"thinking","logprob":-0.5,"bytes":null,"top_logprobs":[]}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"content":"ok"},"logprobs":{"content":[{"token":"ok","logprob":-0.1,"bytes":[111,107],"top_logprobs":[]}]},"finish_reason":"stop","stop_reason":null}],"nvext":{"worker_id":"w-1"}}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}

data: [DONE]

`

type passthroughUpstream struct {
	server      *httptest.Server
	requests    chan []byte
	inputTokens chan string
}

func newPassthroughUpstream(t *testing.T, contentType string, responseBody string) *passthroughUpstream {
	t.Helper()

	upstream := &passthroughUpstream{requests: make(chan []byte, 1), inputTokens: make(chan string, 1)}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
		}
		upstream.requests <- body
		upstream.inputTokens <- r.Header.Get("X-Input-Tokens")
		w.Header().Set(echo.HeaderContentType, contentType)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func newPassthroughAPI(t *testing.T, upstreamURL string) *echo.Echo {
	t.Helper()

	cfg := config.Default()
	stargate, err := provider.NewStargateProvider(config.StargateConfig{URL: upstreamURL})
	require.NoError(t, err)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	RegisterRoutes(e, NewHandlers(cfg, stargate, nil, nil))
	return e
}

func servePassthroughRequest(t *testing.T, e *echo.Echo, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func jsonObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()

	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &object), string(data))
	return object
}

func TestChatCompletionsForwardsExtensionFieldsUpstream(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{false, true} {
		name := "unary"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			upstream := newPassthroughUpstream(t, "text/event-stream", passthroughChatUpstreamSSE)
			e := newPassthroughAPI(t, upstream.server.URL)

			streamJSON := "false"
			if stream {
				streamJSON = "true"
			}
			clientBody := `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"Reply with the word ok."}],"max_tokens":256,"stream":` + streamJSON + `,"chat_template_kwargs":{"enable_thinking":false},"top_k":20,"nvext":{"priority":3}}`

			rec := servePassthroughRequest(t, e, "/v1/chat/completions", clientBody)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			outbound := jsonObject(t, <-upstream.requests)
			require.JSONEq(t, `{"enable_thinking":false}`, string(outbound["chat_template_kwargs"]))
			require.JSONEq(t, `20`, string(outbound["top_k"]))
			require.JSONEq(t, `{"priority":3}`, string(outbound["nvext"]))
			require.JSONEq(t, `256`, string(outbound["max_tokens"]))
			require.JSONEq(t, `"company-name/model-name"`, string(outbound["model"]))
			require.JSONEq(t, `true`, string(outbound["stream"]))
		})
	}
}

func TestChatCompletionsUnaryReturnsReasoningContentAndLogprobs(t *testing.T) {
	t.Parallel()

	upstream := newPassthroughUpstream(t, "text/event-stream", passthroughChatUpstreamSSE)
	e := newPassthroughAPI(t, upstream.server.URL)

	rec := servePassthroughRequest(
		t,
		e,
		"/v1/chat/completions",
		`{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hi"}],"logprobs":true}`,
	)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	<-upstream.requests

	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			Logprobs struct {
				Content []json.RawMessage `json:"content"`
			} `json:"logprobs"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, "fn-alpha/company-name/model-name", response.Model)
	require.Len(t, response.Choices, 1)
	require.Equal(t, "ok", response.Choices[0].Message.Content)
	require.Equal(t, "thinking", response.Choices[0].Message.ReasoningContent)
	require.Equal(t, "stop", response.Choices[0].FinishReason)
	require.Len(t, response.Choices[0].Logprobs.Content, 2)

	body := jsonObject(t, rec.Body.Bytes())
	require.JSONEq(t, `{"worker_id":"w-1"}`, string(body["nvext"]))
	var choices []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body["choices"], &choices))
	require.Contains(t, choices[0], "stop_reason")
}

func TestChatCompletionsAdmissionEstimateCountsUnmodeledPromptFields(t *testing.T) {
	t.Parallel()

	base := `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hi"}]}`
	padding := strings.Repeat("a", 4000)
	withExtensions := `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"assistant","content":"x","reasoning_content":"` + padding + `"},{"role":"user","content":"hi"}],"chat_template_kwargs":{"documents":["` + padding + `"]}}`

	inputTokens := func(body string) int {
		t.Helper()

		upstream := newPassthroughUpstream(t, "text/event-stream", passthroughChatUpstreamSSE)
		e := newPassthroughAPI(t, upstream.server.URL)
		rec := servePassthroughRequest(t, e, "/v1/chat/completions", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		<-upstream.requests
		tokens, err := strconv.Atoi(<-upstream.inputTokens)
		require.NoError(t, err)
		return tokens
	}

	baseTokens := inputTokens(base)
	extendedTokens := inputTokens(withExtensions)
	// Each 4000-byte field adds at least 1000 estimated tokens.
	require.GreaterOrEqual(t, extendedTokens-baseTokens, 2000)
}

func TestEstimatedTokenCountForUnmodeledChatFields(t *testing.T) {
	t.Parallel()

	require.Zero(t, estimatedTokenCountForUnmodeledChatFields(nil))
	require.Zero(t, estimatedTokenCountForUnmodeledChatFields(
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi","name":"n"}],"temperature":0.1}`),
	))
	require.Equal(
		t,
		estimatedTokenCountForText(`{"enable_thinking":false}`)+estimatedTokenCountForText(`"why"`),
		estimatedTokenCountForUnmodeledChatFields(
			[]byte(`{"model":"m","messages":[{"role":"assistant","content":"x","reasoning_content":"why"}],"chat_template_kwargs":{"enable_thinking":false}}`),
		),
	)
}

func TestChatCompletionsStreamRelaysUpstreamChunksVerbatim(t *testing.T) {
	t.Parallel()

	upstream := newPassthroughUpstream(t, "text/event-stream", passthroughChatUpstreamSSE)
	e := newPassthroughAPI(t, upstream.server.URL)

	rec := servePassthroughRequest(
		t,
		e,
		"/v1/chat/completions",
		`{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hi"}],"stream":true}`,
	)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	<-upstream.requests

	var upstreamChunks, clientChunks []string
	for _, line := range strings.Split(passthroughChatUpstreamSSE, "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok && payload != "[DONE]" {
			upstreamChunks = append(upstreamChunks, payload)
		}
	}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok && payload != "[DONE]" {
			clientChunks = append(clientChunks, payload)
		}
	}
	require.True(t, strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n"))
	require.Len(t, clientChunks, len(upstreamChunks))

	for i := range upstreamChunks {
		want := jsonObject(t, []byte(upstreamChunks[i]))
		want["model"] = json.RawMessage(`"fn-alpha/company-name/model-name"`)
		wantJSON, err := json.Marshal(want)
		require.NoError(t, err)
		require.JSONEq(t, string(wantJSON), clientChunks[i], "chunk %d", i)
	}
}

func TestResponsesForwardsExtensionFieldsAndReturnsUnknownOutputUnchanged(t *testing.T) {
	t.Parallel()

	terminal := `{"id":"resp_1","object":"response","status":"completed","created_at":1,"model":"company-name/model-name","output":[{"type":"reasoning","id":"rs_1","summary":[],"content":[{"type":"reasoning_text","text":"thinking"}]},{"type":"future_item","id":"fi_1","payload":{"k":"v"}},{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[],"logprobs":[]}]}],"usage":{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":10},"nvext":{"worker_id":"w-1"}}`
	upstreamSSE := "event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":0,"response":` + terminal + "}\n\n"

	upstream := newPassthroughUpstream(t, "text/event-stream", upstreamSSE)
	e := newPassthroughAPI(t, upstream.server.URL)

	clientBody := `{"model":"fn-alpha/company-name/model-name","input":"Reply with the word ok.","reasoning":{"effort":"none"},"chat_template_kwargs":{"enable_thinking":false},"top_k":20}`
	rec := servePassthroughRequest(t, e, "/v1/responses", clientBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	outbound := jsonObject(t, <-upstream.requests)
	require.JSONEq(t, `{"effort":"none"}`, string(outbound["reasoning"]))
	require.JSONEq(t, `{"enable_thinking":false}`, string(outbound["chat_template_kwargs"]))
	require.JSONEq(t, `20`, string(outbound["top_k"]))
	require.JSONEq(t, `"company-name/model-name"`, string(outbound["model"]))

	require.JSONEq(t, terminal, rec.Body.String())
}

func TestConsumeNativeResponsesSSEKeepsUsageForUnknownOutputItems(t *testing.T) {
	t.Parallel()

	terminal := `{"id":"resp_1","object":"response","status":"completed","created_at":1,"output":[{"type":"future_item","id":"fi_1"}],"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}`
	sse := "event: response.completed\n" +
		`data: {"type":"response.completed","response":` + terminal + "}\n\n"

	var relayed strings.Builder
	got, err := consumeNativeResponsesSSE(strings.NewReader(sse), &relayed)
	require.NoError(t, err)
	require.Equal(t, sse, relayed.String())
	require.NotNil(t, got)
	require.JSONEq(t, terminal, string(got.body))

	usage := chatUsageFromResponses(got.usage)
	require.NotNil(t, usage)
	require.Equal(t, uint32(4), usage.PromptTokens)
	require.Equal(t, uint32(5), usage.CompletionTokens)
	require.Equal(t, uint32(9), usage.TotalTokens)
}

func TestParseNativeResponsesSSEBlockIgnoresNonTerminalAndEmptyResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		block string
	}{
		{
			name:  "non terminal event",
			block: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n",
		},
		{
			name:  "null response",
			block: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":null}\n\n",
		},
		{
			name:  "missing response",
			block: "data: {\"type\":\"response.completed\"}\n\n",
		},
		{
			name:  "done marker",
			block: "data: [DONE]\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Nil(t, parseNativeResponsesSSEBlock([]byte(tt.block)))
		})
	}
}

func TestEmbeddingsForwardsExtensionFieldsAndResponseUnchanged(t *testing.T) {
	t.Parallel()

	upstreamResponse := `{"object":"list","data":[{"object":"embedding","index":0,"embedding":"AAAAAA=="}],"model":"company-name/model-name","usage":{"prompt_tokens":2,"total_tokens":2},"nvext":{"worker_id":"w-1"}}`
	upstream := newPassthroughUpstream(t, echo.MIMEApplicationJSON, upstreamResponse)
	e := newPassthroughAPI(t, upstream.server.URL)

	clientBody := `{"model":"fn-alpha/company-name/model-name","input":["hello","world"],"input_type":"query","truncate":"END","dimensions":256,"encoding_format":"base64","user":"u-1","nvext":{"priority":3}}`
	rec := servePassthroughRequest(t, e, "/v1/embeddings", clientBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	client := jsonObject(t, []byte(clientBody))
	outbound := jsonObject(t, <-upstream.requests)
	require.Len(t, outbound, len(client))
	for key, value := range client {
		if key == "model" {
			continue
		}
		require.Contains(t, outbound, key)
		require.JSONEq(t, string(value), string(outbound[key]), key)
	}
	require.JSONEq(t, `"company-name/model-name"`, string(outbound["model"]))

	require.Equal(t, upstreamResponse, rec.Body.String())
}

func TestEndpointsRejectFieldsSentWithDifferentLetterCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		body     string
		wantPath string
	}{
		{
			name:     "chat top level",
			path:     "/v1/chat/completions",
			body:     `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"long prompt"}],"MESSAGES":[{"role":"user","content":"hi"}]}`,
			wantPath: "messages",
		},
		{
			name:     "chat message member",
			path:     "/v1/chat/completions",
			body:     `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"long prompt","CONTENT":"hi"}]}`,
			wantPath: "messages[0].content",
		},
		{
			name:     "responses",
			path:     "/v1/responses",
			body:     `{"model":"fn-alpha/company-name/model-name","input":"long prompt","Input":"hi"}`,
			wantPath: "input",
		},
		{
			name:     "embeddings",
			path:     "/v1/embeddings",
			body:     `{"model":"fn-alpha/company-name/model-name","input":"long prompt","INPUT":"hi"}`,
			wantPath: "input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			upstream := newPassthroughUpstream(t, echo.MIMEApplicationJSON, `{}`)
			e := newPassthroughAPI(t, upstream.server.URL)

			rec := servePassthroughRequest(t, e, tt.path, tt.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tt.wantPath)
			require.Empty(t, upstream.requests, "request must not reach the backend")
		})
	}
}
