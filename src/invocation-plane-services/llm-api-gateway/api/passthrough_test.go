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
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/provider"
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
	server       *httptest.Server
	requests     chan []byte
	inputTokens  chan string
	affinityKeys chan string
}

func newPassthroughUpstream(t *testing.T, contentType string, responseBody string) *passthroughUpstream {
	t.Helper()

	upstream := &passthroughUpstream{
		requests:     make(chan []byte, 1),
		inputTokens:  make(chan string, 1),
		affinityKeys: make(chan string, 1),
	}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
		}
		upstream.requests <- body
		upstream.inputTokens <- r.Header.Get("X-Input-Tokens")
		upstream.affinityKeys <- r.Header.Get("X-Cache-Affinity-Key")
		w.Header().Set(echo.HeaderContentType, contentType)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func newPassthroughAPI(t *testing.T, upstreamURL string) *echo.Echo {
	t.Helper()

	cfg := config.Default()
	cfg.ModelURIAllowlistEnabled = true
	stargate, err := provider.NewStargateProvider(config.StargateConfig{URL: upstreamURL})
	require.NoError(t, err)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	// company/model and company-name/model-name have no spec, so the chat,
	// responses, embeddings and messages cases exercise the no-spec
	// default-allow. company/restricted has no allowed route. The decision
	// routes have no default-allow, so their cases use company/decision-model,
	// which declares them.
	e.Use(modelSpecsMiddleware(map[string]nvcf.ModelSpec{
		"company/restricted":     {URIs: []string{"/v1/other"}},
		"company/decision-model": {URIs: []string{decisionsEndpointPath, systemOneEndpointPath}},
	}))
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

func TestMultimodalInputsReachUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, path, field, input string
	}{
		{"chat text", "/v1/chat/completions", "messages", `[{"role":"user","content":"Describe this."}]`},
		{"chat image", "/v1/chat/completions", "messages", `[{"role":"user","content":[{"type":"text","text":"Describe this."},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]`},
		{"chat audio", "/v1/chat/completions", "messages", `[{"role":"user","content":[{"type":"text","text":"Transcribe this."},{"type":"audio_url","audio_url":{"url":"data:audio/ogg;base64,T2dnUw==","extension":"preserve"}}]}]`},
		{"chat video", "/v1/chat/completions", "messages", `[{"role":"user","content":[{"type":"text","text":"Describe this."},{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAAA","fps":1}}]}]`},
		{"responses text", "/v1/responses", "input", `[{"role":"user","content":[{"type":"input_text","text":"Describe this."}]}]`},
		{"responses image", "/v1/responses", "input", `[{"role":"user","content":[{"type":"input_text","text":"Describe this."},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]`},
		{"responses audio", "/v1/responses", "input", `[{"role":"user","content":[{"type":"input_text","text":"Transcribe this."},{"type":"input_audio","input_audio":{"data":"T2dnUw==","format":"ogg"}}]}]`},
		{"responses typed audio", "/v1/responses", "input", `[{"type":"message","role":"user","content":[{"type":"input_text","text":"Transcribe this."},{"type":"input_audio","input_audio":{"data":"T2dnUw==","format":"ogg"}}]}]`},
	}
	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusInternalServerError} {
				t.Run(tt.name+"/stream="+strconv.FormatBool(stream)+"/backend="+strconv.Itoa(status), func(t *testing.T) {
					requests := make(chan []byte, 1)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Errorf("read upstream request: %v", err)
						}
						requests <- body
						if status != http.StatusOK {
							w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
							w.WriteHeader(status)
							_, _ = io.WriteString(w, `{"error":{"message":"backend rejected input","type":"backend_error"}}`)
							return
						}
						w.Header().Set(echo.HeaderContentType, "text/event-stream")
						if tt.path == "/v1/responses" {
							_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"created_at\":1,\"output\":[]}}\n\n")
						} else {
							_, _ = io.WriteString(w, passthroughChatUpstreamSSE)
						}
					}))
					defer upstream.Close()
					e := newPassthroughAPI(t, upstream.URL)
					body := `{"model":"fn-alpha/company-name/model-name","` + tt.field + `":` + tt.input + `,"stream":` + strconv.FormatBool(stream) + `}`
					rec := servePassthroughRequest(t, e, tt.path, body)
					require.Equal(t, status, rec.Code, rec.Body.String())
					require.Len(t, requests, 1, "request must reach upstream")
					outbound := jsonObject(t, <-requests)
					require.JSONEq(t, tt.input, string(outbound[tt.field]))
					if status != http.StatusOK {
						require.Contains(t, rec.Body.String(), "backend rejected input")
					}
				})
			}
		}
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

	require.Zero(t, estimatedTokenCountForUnmodeledChatFields(nil, nil))
	require.Zero(t, estimatedTokenCountForUnmodeledChatFields(
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi","name":"n"}],"temperature":0.1}`), nil,
	))
	require.Equal(
		t,
		estimatedTokenCountForText(`{"enable_thinking":false}`)+estimatedTokenCountForText(`"why"`),
		estimatedTokenCountForUnmodeledChatFields(
			[]byte(`{"model":"m","messages":[{"role":"assistant","content":"x","reasoning_content":"why"}],"chat_template_kwargs":{"enable_thinking":false}}`), nil,
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

func TestResponsesStreamEndsWithResponseFailedWhenUpstreamStreamFails(t *testing.T) {
	t.Parallel()

	created := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":4,"response":{"id":"resp_1","object":"response","created_at":1,"model":"company-name/model-name","status":"in_progress","output":[]}}` + "\n\n"
	createdNoSequence := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","created_at":1,"model":"company-name/model-name","status":"in_progress","output":[]}}` + "\n\n"
	unfinished := "event: response.in_progress\ndata: {\"type\":\"resp"
	// The upstream error event is not relayed; its message moves into the gateway's response.failed.
	upstreamError := "event: error\n" +
		`data: {"type":"error","code":"server_error","message":"engine failed","param":null,"sequence_number":5}` + "\n\n"
	upstreamErrorNoMessage := "event: error\n" + `data: {"type":"error","code":"server_error","sequence_number":5}` + "\n\n"

	tests := []struct {
		name         string
		upstreamBody string
		dropMidway   bool
		wantRelayed  string
		// wantResponseID is empty when no response.created came, so the
		// gateway sends its own before response.failed.
		wantResponseID string
		wantSequence   int
		// wantMessage is the response.failed error message; empty means msgNativeResponsesNoTerminal.
		wantMessage string
	}{
		{name: "empty stream", wantSequence: 1},
		{name: "ends after non terminal event", upstreamBody: created, wantRelayed: created, wantResponseID: "resp_1", wantSequence: 5},
		{name: "connection drops mid stream", upstreamBody: created, dropMidway: true, wantRelayed: created, wantResponseID: "resp_1", wantSequence: 5},
		{name: "no sequence numbers", upstreamBody: createdNoSequence, wantRelayed: createdNoSequence, wantResponseID: "resp_1", wantSequence: 0},
		{name: "connection drops mid event", upstreamBody: created + unfinished, dropMidway: true, wantRelayed: created, wantResponseID: "resp_1", wantSequence: 5},
		{name: "stream ends mid event", upstreamBody: created + unfinished, wantRelayed: created, wantResponseID: "resp_1", wantSequence: 5},
		{
			name: "upstream error event", upstreamBody: created + upstreamError, wantRelayed: created,
			wantResponseID: "resp_1", wantSequence: 5, wantMessage: "engine failed",
		},
		{
			name: "upstream error event cut off at EOF", upstreamBody: created + strings.TrimSuffix(upstreamError, "\n\n"),
			wantRelayed: created, wantResponseID: "resp_1", wantSequence: 5, wantMessage: "engine failed",
		},
		{
			name: "upstream error event without message", upstreamBody: created + upstreamErrorNoMessage,
			wantRelayed: created, wantResponseID: "resp_1", wantSequence: 5,
		},
		{
			name: "upstream error event with plain text data", upstreamBody: created + "event: error\ndata: engine failed\n\n",
			wantRelayed: created, wantResponseID: "resp_1", wantSequence: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set(echo.HeaderContentType, "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.upstreamBody)
				if tt.dropMidway {
					w.(http.Flusher).Flush()
					panic(http.ErrAbortHandler)
				}
			}))
			t.Cleanup(upstream.Close)
			e := newPassthroughAPI(t, upstream.URL)
			handlerErr := captureHandlerError(e)

			clientBody := `{"model":"fn-alpha/company-name/model-name","input":"hi","stream":true}`
			rec := servePassthroughRequest(t, e, "/v1/responses", clientBody)
			require.Equal(t, http.StatusOK, rec.Code)

			// The client keeps its 200; logs, metrics and traces record a 502.
			var httpErr *echo.HTTPError
			require.ErrorAs(t, *handlerErr, &httpErr)
			require.Equal(t, http.StatusBadGateway, httpErr.Code)

			body := rec.Body.String()
			require.True(t, strings.HasPrefix(body, tt.wantRelayed), body)
			require.True(t, strings.HasSuffix(body, "\n\n"), body)
			wantTypes := []string{"response.failed"}
			if tt.wantResponseID == "" {
				wantTypes = []string{"response.created", "response.failed"}
			}
			rawEvents := strings.Split(strings.TrimSuffix(strings.TrimPrefix(body, tt.wantRelayed), "\n\n"), "\n\n")
			require.Len(t, rawEvents, len(wantTypes), body)

			type gatewayEvent struct {
				Type           string `json:"type"`
				SequenceNumber int    `json:"sequence_number"`
				Response       struct {
					ID     string `json:"id"`
					Object string `json:"object"`
					Model  string `json:"model"`
					Status string `json:"status"`
					Error  struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				} `json:"response"`
			}
			events := make([]gatewayEvent, len(rawEvents))
			for i, raw := range rawEvents {
				prefix := "event: " + wantTypes[i] + "\ndata: "
				require.True(t, strings.HasPrefix(raw, prefix), body)
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(raw, prefix)), &events[i]), raw)
				require.Equal(t, wantTypes[i], events[i].Type)
				require.Equal(t, "response", events[i].Response.Object)
				require.Equal(t, "company-name/model-name", events[i].Response.Model)
			}

			event := events[len(events)-1]
			require.Equal(t, tt.wantSequence, event.SequenceNumber)
			require.Equal(t, "failed", event.Response.Status)
			require.Equal(t, "server_error", event.Response.Error.Code)
			wantMessage := tt.wantMessage
			if wantMessage == "" {
				wantMessage = msgNativeResponsesNoTerminal
			}
			require.Equal(t, wantMessage, event.Response.Error.Message)
			if tt.wantResponseID != "" {
				require.Equal(t, tt.wantResponseID, event.Response.ID)
			} else {
				require.True(t, strings.HasPrefix(event.Response.ID, "resp_"), event.Response.ID)
				require.Equal(t, event.Response.ID, events[0].Response.ID)
				require.Equal(t, tt.wantSequence-1, events[0].SequenceNumber)
				require.Equal(t, "in_progress", events[0].Response.Status)
			}
		})
	}
}

func TestResponsesUnaryReturns502WhenUpstreamStreamFails(t *testing.T) {
	t.Parallel()

	created := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":4,"response":{"id":"resp_1","object":"response","created_at":1,"model":"company-name/model-name","status":"in_progress","output":[]}}` + "\n\n"

	tests := []struct {
		name         string
		upstreamBody string
		dropMidway   bool
	}{
		{name: "ends after non terminal event", upstreamBody: created},
		{name: "connection drops mid stream", upstreamBody: created, dropMidway: true},
		{
			name: "upstream error event",
			upstreamBody: created + "event: error\n" +
				`data: {"type":"error","code":"server_error","message":"engine failed","param":null,"sequence_number":5}` + "\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set(echo.HeaderContentType, "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.upstreamBody)
				w.(http.Flusher).Flush()
				if tt.dropMidway {
					panic(http.ErrAbortHandler)
				}
			}))
			t.Cleanup(upstream.Close)
			e := newPassthroughAPI(t, upstream.URL)
			handlerErr := captureHandlerError(e)

			clientBody := `{"model":"fn-alpha/company-name/model-name","input":"hi","stream":false}`
			rec := servePassthroughRequest(t, e, "/v1/responses", clientBody)
			body := rec.Body.String()

			require.Equal(t, http.StatusBadGateway, rec.Code, body)
			var httpErr *echo.HTTPError
			require.ErrorAs(t, *handlerErr, &httpErr, "logs and metrics must record the 502")
			require.Equal(t, http.StatusBadGateway, httpErr.Code)
			var errBody openAIErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errBody), body)
			require.Equal(t, "server_error", errBody.Error.Type)
			require.Equal(t, msgNativeResponsesNoTerminal, errBody.Error.Message)
		})
	}
}

func TestResponsesStreamKeepsTerminalStreamWhenConnectionDropsAfterIt(t *testing.T) {
	t.Parallel()

	completed := "event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"completed","output":[]}}` + "\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set(echo.HeaderContentType, "text/event-stream")
		_, _ = io.WriteString(w, completed)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(upstream.Close)
	e := newPassthroughAPI(t, upstream.URL)

	clientBody := `{"model":"fn-alpha/company-name/model-name","input":"hi","stream":true}`
	rec := servePassthroughRequest(t, e, "/v1/responses", clientBody)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, completed, rec.Body.String())
}

func TestResponsesUnaryKeepsTerminalResponseWhenConnectionDropsAfterIt(t *testing.T) {
	t.Parallel()

	const response = `{"id":"resp_1","object":"response","status":"completed","output":[]}`
	completed := "event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":0,"response":` + response + `}` + "\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set(echo.HeaderContentType, "text/event-stream")
		_, _ = io.WriteString(w, completed)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(upstream.Close)
	e := newPassthroughAPI(t, upstream.URL)

	clientBody := `{"model":"fn-alpha/company-name/model-name","input":"hi","stream":false}`
	rec := servePassthroughRequest(t, e, "/v1/responses", clientBody)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, response, rec.Body.String())
}

func TestResponsesStreamReadsUncompressedStreamWhenClientAcceptsGzip(t *testing.T) {
	t.Parallel()

	completed := "event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"completed","output":[]}}` + "\n\n"
	// The upstream compresses whenever the request allows it.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set(echo.HeaderContentType, "text/event-stream")
		if !strings.Contains(r.Header.Get(echo.HeaderAcceptEncoding), "gzip") {
			_, _ = io.WriteString(w, completed)
			return
		}
		w.Header().Set(echo.HeaderContentEncoding, "gzip")
		gz := gzip.NewWriter(w)
		_, _ = io.WriteString(gz, completed)
		_ = gz.Close()
	}))
	t.Cleanup(upstream.Close)
	e := newPassthroughAPI(t, upstream.URL)
	handlerErr := captureHandlerError(e)

	clientBody := `{"model":"fn-alpha/company-name/model-name","input":"hi","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(clientBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAcceptEncoding, "gzip")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.NoError(t, *handlerErr)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get(echo.HeaderContentEncoding))
	require.Equal(t, completed, rec.Body.String())
}

func TestResponsesStreamSendsNoFailureEventWhenClientGoesAway(t *testing.T) {
	t.Parallel()

	created := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress"}}` + "\n\n"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// The upstream holds the stream open until the gateway cancels it, which
	// happens when the client goes away after the first relayed event.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set(echo.HeaderContentType, "text/event-stream")
		_, _ = io.WriteString(w, created)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	e := newPassthroughAPI(t, upstream.URL)

	clientBody := `{"model":"fn-alpha/company-name/model-name","input":"hi","stream":true}`
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", strings.NewReader(clientBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := &cancelOnWriteRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, created, rec.Body.String())
}

func TestResponsesStreamSendsNoFailureEventWhenClientWriteFails(t *testing.T) {
	t.Parallel()

	created := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress"}}` + "\n\n"
	upstream := newPassthroughUpstream(t, "text/event-stream", created)
	e := newPassthroughAPI(t, upstream.server.URL)
	handlerErr := captureHandlerError(e)

	clientBody := `{"model":"fn-alpha/company-name/model-name","input":"hi","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(clientBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := &failingWriteRecorder{ResponseRecorder: httptest.NewRecorder()}
	e.ServeHTTP(rec, req)

	// The request context is still live, as when net/http has not yet noticed
	// the closed connection; the failed write alone shows the client is gone.
	require.ErrorIs(t, *handlerErr, errClientGone)
	var httpErr *echo.HTTPError
	require.False(t, errors.As(*handlerErr, &httpErr), "client write failure recorded as %v", *handlerErr)
}

var errClientGone = errors.New("client connection closed")

// failingWriteRecorder fails every body write, as a closed client connection
// would.
type failingWriteRecorder struct {
	*httptest.ResponseRecorder
}

func (r *failingWriteRecorder) Write([]byte) (int, error) {
	return 0, errClientGone
}

// captureHandlerError records the error the route handler returns, which the
// gateway middleware turns into the status it logs and counts.
func captureHandlerError(e *echo.Echo) *error {
	var handlerErr error
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			handlerErr = next(c)
			return handlerErr
		}
	})
	return &handlerErr
}

// cancelOnWriteRecorder cancels the client request after the first body
// write, as a client that disconnects mid-stream would.
type cancelOnWriteRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (r *cancelOnWriteRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(b)
	r.cancel()
	return n, err
}

func TestConsumeNativeResponsesSSEKeepsUsageForUnknownOutputItems(t *testing.T) {
	t.Parallel()

	terminal := `{"id":"resp_1","object":"response","status":"completed","created_at":1,"output":[{"type":"future_item","id":"fi_1"}],"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}`
	sse := "event: response.completed\n" +
		`data: {"type":"response.completed","response":` + terminal + "}\n\n"

	var relayed strings.Builder
	stream, err := consumeNativeResponsesSSE(strings.NewReader(sse), &relayed)
	require.NoError(t, err)
	require.Equal(t, sse, relayed.String())
	got := stream.terminal
	require.NotNil(t, got)
	require.JSONEq(t, terminal, string(got.body))

	usage := chatUsageFromResponses(got.usage)
	require.NotNil(t, usage)
	require.Equal(t, uint32(4), usage.PromptTokens)
	require.Equal(t, uint32(5), usage.CompletionTokens)
	require.Equal(t, uint32(9), usage.TotalTokens)
}

func TestConsumeNativeResponsesSSECompletesTerminalEventCutOffAtEOF(t *testing.T) {
	t.Parallel()

	completed := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`

	// SSE clients drop an event that the stream ends before its blank line.
	tests := []struct {
		name        string
		upstream    string
		wantRelayed string
	}{
		{name: "ends after a newline", upstream: completed + "\n", wantRelayed: completed + "\n\n"},
		{name: "ends mid line", upstream: completed, wantRelayed: completed + "\n\n"},
		{name: "ends with crlf", upstream: completed + "\r\n", wantRelayed: completed + "\r\n\n"},
		{name: "partial event after terminal", upstream: completed + "\n\nevent: x\ndata: {", wantRelayed: completed + "\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var relayed strings.Builder
			stream, err := consumeNativeResponsesSSE(strings.NewReader(tt.upstream), &relayed)
			require.NoError(t, err)
			require.NotNil(t, stream.terminal)
			require.Equal(t, tt.wantRelayed, relayed.String())
		})
	}
}

func TestNativeResponsesStreamObserveFindsOnlyTerminalResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		block        string
		wantTerminal bool
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
		{
			name:         "non integer sequence number",
			block:        "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":5.0,\"response\":{\"id\":\"resp_1\"}}\n\n",
			wantTerminal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stream nativeResponsesStream
			stream.observe([]byte(tt.block))
			require.Equal(t, tt.wantTerminal, stream.terminal != nil)
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
