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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

const chatStreamPartialChunk = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}` + "\n\n"

// An upstream chat stream that stops before [DONE] (Pylon output timeout,
// worker hang) or reports an error mid-stream must reach the client as a
// failure, never as a finished answer.
func TestChatCompletionsFailsWhenUpstreamStreamEndsEarly(t *testing.T) {
	t.Parallel()

	const partial = chatStreamPartialChunk
	// n=2: choice 1 finished, choice 0 is still decoding when the stream stops.
	const siblingFinished = partial +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":1,"delta":{"role":"assistant","content":"HEADS"},"finish_reason":"stop"}]}` + "\n\n"
	// vLLM and LiteLLM report a failure after the 200 as an error event, then send [DONE].
	// vLLM's error object has a numeric code, which the OpenAI error schema does not allow.
	upstreamError := func(member string) string {
		return partial + `data: {"error":` + member + "}\n\n" + "data: [DONE]\n\n"
	}
	const vllmError = `{"message":"engine failed","type":"InternalServerError","param":null,"code":500}`
	// OpenRouter puts the error next to choices, with finish_reason "error".
	const errorWithChoices = partial +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","error":{"code":502,"message":"engine failed"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	tests := []struct {
		name         string
		stream       bool
		upstreamBody string
		// dropMidway aborts the upstream connection, which is what the gateway
		// sees once the tunnel propagates the Pylon reset.
		dropMidway bool
		// streamMessage is the message of the streaming error event; empty means msgUpstreamStreamIncomplete.
		streamMessage string
	}{
		{name: "stream clean end after output", stream: true, upstreamBody: partial},
		{name: "stream clean end before output", stream: true},
		{name: "stream clean end n=2 one choice finished", stream: true, upstreamBody: siblingFinished},
		{name: "stream connection drops after output", stream: true, upstreamBody: partial, dropMidway: true},
		{
			name: "stream upstream error event after output", stream: true,
			upstreamBody: upstreamError(vllmError), streamMessage: "engine failed",
		},
		{
			name: "stream upstream error string after output", stream: true,
			upstreamBody: upstreamError(`"engine failed"`), streamMessage: "engine failed",
		},
		{name: "stream upstream error event without message", stream: true, upstreamBody: upstreamError(`{"code":500}`)},
		{name: "stream upstream error with choices", stream: true, upstreamBody: errorWithChoices, streamMessage: "engine failed"},
		{name: "unary clean end after output", upstreamBody: partial},
		{name: "unary clean end n=2 one choice finished", upstreamBody: siblingFinished},
		{name: "unary connection drops after output", upstreamBody: partial, dropMidway: true},
		{name: "unary upstream error event after output", upstreamBody: upstreamError(vllmError)},
		{name: "unary upstream error with choices", upstreamBody: errorWithChoices},
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

			streamJSON := "false"
			if tt.stream {
				streamJSON = "true"
			}
			clientBody := `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hi"}],"stream":` + streamJSON + `}`
			rec := servePassthroughRequest(t, e, "/v1/chat/completions", clientBody)
			body := rec.Body.String()
			require.NotContains(t, body, "InternalServerError", "the upstream error event is not relayed as is")

			if !tt.stream {
				require.Equal(t, http.StatusBadGateway, rec.Code, body)
				var errBody openAIErrorResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errBody), body)
				require.Equal(t, "server_error", errBody.Error.Type)
				require.Equal(t, msgUpstreamStreamIncomplete, errBody.Error.Message)
				return
			}
			// The client keeps its 200; logs, metrics and traces record a 502.
			require.Equal(t, http.StatusOK, rec.Code)
			var httpErr *echo.HTTPError
			require.ErrorAs(t, *handlerErr, &httpErr)
			require.Equal(t, http.StatusBadGateway, httpErr.Code)

			require.NotContains(t, body, "data: [DONE]", "a truncated stream must not end like a finished one")
			events := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n")
			last, isData := strings.CutPrefix(events[len(events)-1], "data: ")
			require.True(t, isData, body)
			var event openAIErrorResponse
			require.NoError(t, json.Unmarshal([]byte(last), &event), "last event must be an error event: %q", body)
			require.Equal(t, "server_error", event.Error.Type)
			wantMessage := tt.streamMessage
			if wantMessage == "" {
				wantMessage = msgUpstreamStreamIncomplete
			}
			require.Equal(t, wantMessage, event.Error.Message)
		})
	}
}

func TestChatCompletionsStreamSendsNoErrorEventWhenClientGoesAway(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// The upstream holds the stream open until the gateway cancels it, which
	// happens when the client goes away after the first relayed chunk.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set(echo.HeaderContentType, "text/event-stream")
		_, _ = io.WriteString(w, chatStreamPartialChunk)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	e := newPassthroughAPI(t, upstream.URL)
	handlerErr := captureHandlerError(e)

	clientBody := `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", strings.NewReader(clientBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := &cancelOnWriteRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"content":"partial"`)
	require.NotContains(t, body, `"error"`)
	require.NotContains(t, body, "data: [DONE]")
	var httpErr *echo.HTTPError
	require.False(t, errors.As(*handlerErr, &httpErr), "client disconnect recorded as %v", *handlerErr)
}

// feedStreamProvider streams the events the test sends, so the test sees when
// nobody reads them any more.
type feedStreamProvider struct {
	*stubStreamProvider
	events chan provider.StreamEvent
}

func (p *feedStreamProvider) Stream(
	context.Context,
	*requestctx.RequestContext,
	*provider.NormalizedRequest,
) (<-chan provider.StreamEvent, error) {
	return p.events, nil
}

// A handler that stops early, here because the client write failed, must not
// strand the goroutines that relay the upstream stream.
func TestChatCompletionsStreamDrainsUpstreamAfterClientWriteFails(t *testing.T) {
	t.Parallel()

	events := make(chan provider.StreamEvent)
	handlers := NewHandlers(nil, &feedStreamProvider{stubStreamProvider: &stubStreamProvider{}, events: events}, nil, nil)
	gc, _ := newRateLimitGatewayContext()
	gc.Response().Writer = &failingWriteRecorder{ResponseRecorder: httptest.NewRecorder()}
	request := &provider.NormalizedRequest{ChatRequest: &models.ChatCompletionRequest{
		Model:    "fn-chat/company-name/model-name",
		Messages: &[]models.ChatMessage{{Role: models.ChatCompletionRoleUser, Content: models.SingleTextContent("hi")}},
	}}
	chunk := provider.StreamEvent{Chunk: &models.ChatCompletionChunk{
		Choices: []models.ChatCompletionChunkChoice{{Delta: models.ChatCompletionChunkDelta{Content: ptr.To("hi")}}},
	}}

	handlerErr := make(chan error, 1)
	go func() { handlerErr <- handlers.AsOpenAIChatHandlers().streamChatCompletion(gc, request) }()
	events <- chunk
	require.ErrorIs(t, <-handlerErr, errClientGone)

	for range 3 {
		select {
		case events <- chunk:
		case <-time.After(time.Second):
			t.Fatal("the upstream stream is not read after the handler returned")
		}
	}
	close(events)
}

// The drain keeps reading after the handler releases the output reservation, so a late
// usage chunk must not finalize the tokens again and charge the input twice.
func TestChatCompletionsStreamDoesNotFinalizeTokensAfterRelease(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	plan := ratelimit.NewAdmissionPlan(
		limiter,
		ratelimit.ResourceLimit{
			SubjectKey:      rateLimitSubjectKey("nca-456", "", "fn-chat"),
			SubjectRepr:     "org `nca-456` function `fn-chat`",
			Level:           ratelimit.LevelOrg,
			TokensPerMinute: 100,
		},
		"req-release",
	)
	_, err := plan.Commit(context.Background(), ratelimit.ResourceRequest{InputTokens: 4, OutputTokens: 6})
	require.NoError(t, err)

	events := make(chan provider.StreamEvent)
	handlers := NewHandlers(nil, &feedStreamProvider{stubStreamProvider: &stubStreamProvider{}, events: events}, limiter, nil)
	gc, _ := newRateLimitGatewayContext()
	gc.Response().Writer = &failingWriteRecorder{ResponseRecorder: httptest.NewRecorder()}
	request := &provider.NormalizedRequest{
		ChatRequest:     &models.ChatCompletionRequest{Model: "fn-chat/company-name/model-name"},
		InputTokens:     4,
		MaxOutputTokens: 6,
		AdmissionPlan:   plan,
	}
	chunk := provider.StreamEvent{Chunk: &models.ChatCompletionChunk{
		Choices: []models.ChatCompletionChunkChoice{{Delta: models.ChatCompletionChunkDelta{Content: ptr.To("hi")}}},
	}}
	usageChunk := provider.StreamEvent{Chunk: &models.ChatCompletionChunk{
		Usage: &models.ChatCompletionUsage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5},
	}}

	handlerErr := make(chan error, 1)
	go func() { handlerErr <- handlers.AsOpenAIChatHandlers().streamChatCompletion(gc, request) }()
	events <- chunk
	require.ErrorIs(t, <-handlerErr, errClientGone)

	// The relay handles an event before it reads the next one, so the second send
	// returns only after the usage chunk is handled.
	events <- usageChunk
	events <- chunk
	close(events)

	calls := limiter.Calls()
	require.Len(t, calls, 2, "want only the commit and the release, got %+v", calls)
	require.Equal(t, int64(-6), calls[1].tokensRequested)
}
