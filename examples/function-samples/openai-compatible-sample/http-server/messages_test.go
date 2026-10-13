// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const messagesBody = `{"model":"test-model","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]`

func TestMessagesReturnsAnthropicMessage(t *testing.T) {
	recorder := postJSONWithHeaders(t, "/v1/messages", messagesBody+`}`, map[string]string{
		headerChunk:        "token",
		headerOutputChunks: "3",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := recorder.Header().Get("request-id"); !strings.HasPrefix(got, "req_") {
		t.Fatalf("request-id = %q, want req_ prefix", got)
	}

	body := responseMap(t, recorder)
	if id, _ := body["id"].(string); !strings.HasPrefix(id, "msg_") {
		t.Fatalf("id = %#v, want msg_ prefix", body["id"])
	}
	for field, want := range map[string]any{
		"type":          "message",
		"role":          "assistant",
		"model":         "test-model",
		"stop_reason":   messagesStopEndTurn,
		"stop_sequence": nil,
	} {
		if got, ok := body[field]; !ok || got != want {
			t.Fatalf("%s = %#v, want %#v: %s", field, got, want, recorder.Body.String())
		}
	}
	content := body["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one block", content)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "tokentokentoken" {
		t.Fatalf("content block = %#v, want text tokentokentoken", block)
	}
	assertMessagesUsage(t, body["usage"], 3)
}

func TestMessagesStreamEventSequence(t *testing.T) {
	chunk := "quote\" slash\\ newline\n"
	recorder := newFlushCountingRecorder()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", strings.NewReader(messagesBody+`,"stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(headerChunk, chunk)
	request.Header.Set(headerOutputChunks, "3")
	newRouter().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	events := parseResponsesSSEEvents(t, recorder.Body.String())
	wantNames := []string{
		"message_start",
		"content_block_start",
		"ping",
		"content_block_delta",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	assertEventNames(t, events, wantNames, recorder.Body.String())
	if recorder.flushes != len(events) {
		t.Fatalf("flushes = %d, want %d", recorder.flushes, len(events))
	}

	start := events[0].data["message"].(map[string]any)
	if start["type"] != "message" || start["role"] != "assistant" || start["stop_reason"] != nil {
		t.Fatalf("message_start message = %#v", start)
	}
	if content := start["content"].([]any); len(content) != 0 {
		t.Fatalf("message_start content = %#v, want empty", content)
	}
	blockStart := events[1].data["content_block"].(map[string]any)
	if blockStart["type"] != "text" || blockStart["text"] != "" || events[1].data["index"] != float64(0) {
		t.Fatalf("content_block_start = %#v", events[1].data)
	}

	var text strings.Builder
	for _, event := range events[3:6] {
		delta := event.data["delta"].(map[string]any)
		if delta["type"] != "text_delta" || event.data["index"] != float64(0) {
			t.Fatalf("content_block_delta = %#v", event.data)
		}
		text.WriteString(delta["text"].(string))
	}
	if got, want := text.String(), strings.Repeat(chunk, 3); got != want {
		t.Fatalf("streamed text = %q, want %q", got, want)
	}

	messageDelta := events[7].data
	delta := messageDelta["delta"].(map[string]any)
	if delta["stop_reason"] != messagesStopEndTurn {
		t.Fatalf("stop_reason = %#v, want %s", delta["stop_reason"], messagesStopEndTurn)
	}
	if value, ok := delta["stop_sequence"]; !ok || value != nil {
		t.Fatalf("stop_sequence = %#v, want null", value)
	}
	assertMessagesUsage(t, messageDelta["usage"], 3)
}

func TestMessagesStreamUsesTimingControls(t *testing.T) {
	started := time.Now()
	recorder := postJSON(t, "/v1/messages", messagesBody+`,
		"stream":true,
		"x_load_tester_ttft_ms":40,
		"x_load_tester_itl_ms":20,
		"x_load_tester_chunk_bytes":8,
		"x_load_tester_output_chunks":3
	}`)
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond {
		t.Fatalf("elapsed = %s, want at least 80ms", elapsed)
	}
	events := parseResponsesSSEEvents(t, recorder.Body.String())
	deltas := 0
	for _, event := range events {
		if event.name == "content_block_delta" {
			deltas++
			if text := event.data["delta"].(map[string]any)["text"].(string); len(text) != 8 {
				t.Fatalf("delta text length = %d, want 8", len(text))
			}
		}
	}
	if deltas != 3 {
		t.Fatalf("delta count = %d, want 3: %s", deltas, recorder.Body.String())
	}
}

func TestMessagesMaxTokensCapsOutput(t *testing.T) {
	headers := map[string]string{headerOutputChunks: "5"}
	recorder := postJSONWithHeaders(t, "/v1/messages", `{"model":"test-model","max_tokens":2,"messages":[{"role":"user","content":"hi"}]}`, headers)
	body := responseMap(t, recorder)
	if body["stop_reason"] != messagesStopMaxTokens {
		t.Fatalf("stop_reason = %#v, want %s", body["stop_reason"], messagesStopMaxTokens)
	}
	if got := body["content"].([]any)[0].(map[string]any)["text"]; got != strings.Repeat(defaultChunk, 2) {
		t.Fatalf("text = %#v, want two chunks", got)
	}
	assertMessagesUsage(t, body["usage"], 2)

	recorder = postJSONWithHeaders(t, "/v1/messages", `{"model":"test-model","max_tokens":2,"messages":[{"role":"user","content":"hi"}],"stream":true}`, headers)
	events := parseResponsesSSEEvents(t, recorder.Body.String())
	messageDelta := events[len(events)-2].data
	if got := messageDelta["delta"].(map[string]any)["stop_reason"]; got != messagesStopMaxTokens {
		t.Fatalf("stream stop_reason = %#v, want %s", got, messagesStopMaxTokens)
	}
	assertMessagesUsage(t, messageDelta["usage"], 2)

	recorder = postJSON(t, "/v1/messages", `{"model":"test-model","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`)
	body = responseMap(t, recorder)
	if content := body["content"].([]any); len(content) != 0 || body["stop_reason"] != messagesStopMaxTokens {
		t.Fatalf("max_tokens 0 response = %s", recorder.Body.String())
	}

	recorder = postJSON(t, "/v1/messages", `{"model":"test-model","max_tokens":0,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	assertEventNames(t, parseResponsesSSEEvents(t, recorder.Body.String()), []string{"message_start", "ping", "message_delta", "message_stop"}, recorder.Body.String())
}

func TestMessagesErrorsUseAnthropicShape(t *testing.T) {
	for name, test := range map[string]struct {
		method  string
		body    string
		headers map[string]string
		status  int
		errType string
	}{
		"invalid JSON":        {body: `{`, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"missing model":       {body: `{"max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"missing max_tokens":  {body: `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"negative max_tokens": {body: `{"model":"test-model","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"string max_tokens":   {body: `{"model":"test-model","max_tokens":"1","messages":[{"role":"user","content":"hi"}]}`, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"empty messages":      {body: `{"model":"test-model","max_tokens":1,"messages":[]}`, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"missing messages":    {body: `{"model":"test-model","max_tokens":1}`, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"invalid control":     {body: messagesBody + `}`, headers: map[string]string{headerTTFT: "-1"}, status: http.StatusBadRequest, errType: "invalid_request_error"},
		"wrong method":        {method: http.MethodGet, status: http.StatusMethodNotAllowed, errType: "invalid_request_error"},
		"injected 401":        {body: messagesBody + `}`, headers: map[string]string{headerStatusCode: "401"}, status: http.StatusUnauthorized, errType: "authentication_error"},
		"injected 429":        {body: messagesBody + `}`, headers: map[string]string{headerStatusCode: "429"}, status: http.StatusTooManyRequests, errType: "rate_limit_error"},
		"injected 500":        {body: messagesBody + `}`, headers: map[string]string{headerStatusCode: "500"}, status: http.StatusInternalServerError, errType: "api_error"},
		"injected 529":        {body: messagesBody + `,"x_load_tester_status_code":529}`, status: 529, errType: "overloaded_error"},
		"oversized body":      {body: `{"model":"` + strings.Repeat("x", maxRequestBytes) + `"}`, status: http.StatusRequestEntityTooLarge, errType: "request_too_large"},
	} {
		t.Run(name, func(t *testing.T) {
			method := test.method
			if method == "" {
				method = http.MethodPost
			}
			request := httptest.NewRequestWithContext(context.Background(), method, "/v1/messages", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			for header, value := range test.headers {
				request.Header.Set(header, value)
			}
			recorder := httptest.NewRecorder()
			newRouter().ServeHTTP(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
			assertMessagesError(t, recorder, test.errType)
		})
	}
}

func TestMessagesConcurrencyLimit(t *testing.T) {
	activeRequests.Store(1)
	t.Cleanup(func() { activeRequests.Store(0) })
	recorder := postJSONWithHeaders(t, "/v1/messages", messagesBody+`}`, map[string]string{headerMaxConcurrency: "1"})
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusTooManyRequests, recorder.Body.String())
	}
	assertMessagesError(t, recorder, "rate_limit_error")
	if got := activeRequests.Load(); got != 1 {
		t.Fatalf("active requests = %d, want 1", got)
	}
}

func TestMessagesStreamFailures(t *testing.T) {
	for name, test := range map[string]struct {
		body      string
		headers   map[string]string
		wantError bool
		wantDelta int
	}{
		"header error":         {body: messagesBody + `,"stream":true}`, headers: map[string]string{headerOutputChunks: "2", headerStreamErrorAfter: "1"}, wantError: true, wantDelta: 1},
		"header error at zero": {body: messagesBody + `,"stream":true}`, headers: map[string]string{headerOutputChunks: "2", headerStreamErrorAfter: "0"}, wantError: true},
		"header truncate":      {body: messagesBody + `,"stream":true}`, headers: map[string]string{headerOutputChunks: "2", headerStreamTruncateAfter: "1"}, wantDelta: 1},
		"body error":           {body: messagesBody + `,"stream":true,"x_load_tester_output_chunks":2,"x_load_tester_stream_error_after_chunks":1}`, wantError: true, wantDelta: 1},
		"body truncate":        {body: messagesBody + `,"stream":true,"x_load_tester_output_chunks":2,"x_load_tester_stream_truncate_after_chunks":2}`, wantDelta: 2},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := postJSONWithHeaders(t, "/v1/messages", test.body, test.headers)
			events := parseResponsesSSEEvents(t, recorder.Body.String())
			deltas := 0
			for _, event := range events {
				switch event.name {
				case "content_block_delta":
					deltas++
				case "content_block_stop", "message_delta", "message_stop":
					t.Fatalf("failed stream emitted %s: %s", event.name, recorder.Body.String())
				}
			}
			if deltas != test.wantDelta {
				t.Fatalf("delta count = %d, want %d", deltas, test.wantDelta)
			}
			last := events[len(events)-1]
			if test.wantError {
				errorBody := last.data["error"].(map[string]any)
				if last.name != "error" || last.data["type"] != "error" || errorBody["type"] != "api_error" || errorBody["message"] == "" {
					t.Fatalf("last event = %#v, want Messages error event", last)
				}
			} else if last.name == "error" {
				t.Fatalf("truncated stream emitted error: %s", recorder.Body.String())
			}
		})
	}
}

func TestMessagesStreamCancellationDoesNotComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := newFlushCountingRecorder()
	recorder.onWrite = func(frame []byte) {
		if bytes.Contains(frame, []byte("event: content_block_delta\n")) {
			cancel()
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		streamMessages(ctx, recorder, newMessagesResponse("test-model"), benchmarkTuning{
			ITL:                 time.Hour,
			Chunk:               defaultChunk,
			OutputChunks:        2,
			StreamErrorAfter:    -1,
			StreamTruncateAfter: -1,
		}, messagesStopEndTurn)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after context cancellation")
	}
	if strings.Contains(recorder.Body.String(), "event: message_stop") {
		t.Fatalf("cancelled stream completed: %s", recorder.Body.String())
	}
}

func assertEventNames(t *testing.T, events []sseEvent, want []string, stream string) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d: %s", len(events), len(want), stream)
	}
	for index, event := range events {
		if event.name != want[index] || event.data["type"] != want[index] {
			t.Fatalf("event %d = %s/%#v, want %s: %s", index, event.name, event.data["type"], want[index], stream)
		}
	}
}

func assertMessagesUsage(t *testing.T, value any, outputTokens int) {
	t.Helper()
	usage, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v, want object", value)
	}
	for field, want := range map[string]float64{
		"input_tokens":                0,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     0,
		"output_tokens":               float64(outputTokens),
	} {
		if got := usage[field]; got != want {
			t.Fatalf("usage.%s = %#v, want %v", field, got, want)
		}
	}
}

func assertMessagesError(t *testing.T, recorder *httptest.ResponseRecorder, errType string) {
	t.Helper()
	body := responseMap(t, recorder)
	errorBody, ok := body["error"].(map[string]any)
	if !ok || body["type"] != "error" {
		t.Fatalf("error response = %s, want Messages error shape", recorder.Body.String())
	}
	if errorBody["type"] != errType {
		t.Fatalf("error.type = %#v, want %s", errorBody["type"], errType)
	}
	if message, _ := errorBody["message"].(string); message == "" {
		t.Fatalf("error.message = %#v, want text", errorBody["message"])
	}
	if requestID := recorder.Header().Get("request-id"); requestID == "" || body["request_id"] != requestID {
		t.Fatalf("request_id = %#v, header = %q", body["request_id"], requestID)
	}
}
