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
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResponsesReturnsStrictCompatibleResponse(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", `{"model":"test-model","input":"hello world"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	body := responseMap(t, recorder)
	for _, field := range []string{
		"id",
		"object",
		"created_at",
		"completed_at",
		"parallel_tool_calls",
		"tool_choice",
		"tools",
		"usage",
	} {
		if _, ok := body[field]; !ok {
			t.Fatalf("response missing %q: %s", field, recorder.Body.String())
		}
	}
	if got := body["object"]; got != "response" {
		t.Fatalf("object = %#v, want response", got)
	}
	if body["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v, want true", body["parallel_tool_calls"])
	}

	usage := body["usage"].(map[string]any)
	for _, field := range []string{"input_tokens_details", "output_tokens_details"} {
		if _, ok := usage[field]; !ok {
			t.Fatalf("usage missing %q: %s", field, recorder.Body.String())
		}
	}

	output := body["output"].([]any)
	item := output[0].(map[string]any)
	content := item["content"].([]any)
	if got := content[0].(map[string]any)["text"]; got != defaultChunk {
		t.Fatalf("output text = %#v, want %s", got, defaultChunk)
	}
}

func TestResponsesStreamUsesHeaderControls(t *testing.T) {
	started := time.Now()
	recorder := postJSONWithHeaders(t, "/v1/responses", `{
		"model":"test-model",
		"input":"hello",
		"stream":true,
		"repeats":99,
		"delay":99,
		"size":999
	}`, map[string]string{
		headerTTFT:         "20",
		headerITL:          "10",
		headerChunk:        "header",
		headerOutputChunks: "3",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if elapsed := time.Since(started); elapsed < 35*time.Millisecond {
		t.Fatalf("stream elapsed = %s, want at least 35ms", elapsed)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
	}
	body := recorder.Body.String()
	if got := strings.Count(body, `"delta":"header"`); got != 3 {
		t.Fatalf("body chunk count = %d, want 3: %s", got, body)
	}
	if !strings.Contains(body, `"text":"headerheaderheader"`) {
		t.Fatalf("stream did not complete with repeated header chunks: %s", body)
	}

	lastIndex := -1
	for _, event := range []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	} {
		index := strings.Index(body, "event: "+event)
		if index == -1 {
			t.Fatalf("stream missing %q: %s", event, body)
		}
		if index <= lastIndex {
			t.Fatalf("event %q arrived out of order: %s", event, body)
		}
		lastIndex = index
	}
}

func TestResponsesStreamPreservesSSESemanticsAndFlushes(t *testing.T) {
	chunk := "quote\" slash\\ newline\n"
	recorder := newFlushCountingRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(headerChunk, chunk)
	request.Header.Set(headerOutputChunks, "3")
	newRouter().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	events := parseResponsesSSEEvents(t, recorder.Body.String())
	wantNames := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(events) != len(wantNames) {
		t.Fatalf("event count = %d, want %d: %s", len(events), len(wantNames), recorder.Body.String())
	}
	if recorder.flushes != len(events) {
		t.Fatalf("flushes = %d, want %d", recorder.flushes, len(events))
	}

	var deltaText strings.Builder
	for index, event := range events {
		if event.name != wantNames[index] {
			t.Fatalf("event %d = %q, want %q", index, event.name, wantNames[index])
		}
		if got := sseSequence(t, event); got != index {
			t.Fatalf("event %d sequence = %d, want %d", index, got, index)
		}
		if event.name == "response.output_text.delta" {
			delta, ok := event.data["delta"].(string)
			if !ok {
				t.Fatalf("delta = %#v, want string", event.data["delta"])
			}
			if delta != chunk {
				t.Fatalf("delta = %q, want %q", delta, chunk)
			}
			deltaText.WriteString(delta)
		}
	}

	wantText := strings.Repeat(chunk, 3)
	if got := eventText(t, events[7], "text"); got != wantText {
		t.Fatalf("output_text.done text = %q, want %q", got, wantText)
	}
	if got := nestedEventText(t, events[8], "part"); got != wantText {
		t.Fatalf("content_part.done text = %q, want %q", got, wantText)
	}
	completed := events[10].data["response"].(map[string]any)
	if got := nestedResponseText(t, completed); got != wantText {
		t.Fatalf("completed response text = %q, want %q", got, wantText)
	}
	if got := completed["status"]; got != "completed" {
		t.Fatalf("completed status = %#v, want completed", got)
	}
	usage := completed["usage"].(map[string]any)
	if got := int(usage["output_tokens"].(float64)); got != countTokens(deltaText.String()) {
		t.Fatalf("output tokens = %d, want %d", got, countTokens(deltaText.String()))
	}
}

func TestResponsesStreamRandomChunksMatchCompletedText(t *testing.T) {
	recorder := postJSONWithHeaders(t, "/v1/responses", `{"model":"test-model","input":"hello","stream":true}`, map[string]string{
		headerChunkBytes:   "7",
		headerOutputChunks: "3",
	})
	events := parseResponsesSSEEvents(t, recorder.Body.String())
	var deltaText strings.Builder
	for _, event := range events {
		if event.name != "response.output_text.delta" {
			continue
		}
		delta := event.data["delta"].(string)
		if len(delta) != 7 {
			t.Fatalf("random chunk length = %d, want 7", len(delta))
		}
		deltaText.WriteString(delta)
	}
	if got := eventText(t, events[len(events)-4], "text"); got != deltaText.String() {
		t.Fatalf("output_text.done text = %q, want %q", got, deltaText.String())
	}
	completed := events[len(events)-1].data["response"].(map[string]any)
	if got := nestedResponseText(t, completed); got != deltaText.String() {
		t.Fatalf("completed response text = %q, want %q", got, deltaText.String())
	}
}

func TestResponsesStreamCancellationDoesNotComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := newFlushCountingRecorder()
	recorder.onWrite = func(frame []byte) {
		if bytes.Contains(frame, []byte("event: response.output_text.delta\n")) {
			cancel()
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		streamResponses(ctx, recorder, newResponsesResponse("test-model", ""), benchmarkTuning{
			ITL:                 time.Hour,
			Chunk:               defaultChunk,
			OutputChunks:        2,
			StreamErrorAfter:    -1,
			StreamTruncateAfter: -1,
		})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after context cancellation")
	}
	if strings.Contains(recorder.Body.String(), "event: response.completed") {
		t.Fatalf("cancelled stream completed: %s", recorder.Body.String())
	}
}

func TestResponsesStreamCancellationDuringFinalDeltaDoesNotComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := newFlushCountingRecorder()
	recorder.onWrite = func(frame []byte) {
		if bytes.Contains(frame, []byte("event: response.output_text.delta\n")) {
			cancel()
		}
	}

	streamResponses(ctx, recorder, newResponsesResponse("test-model", ""), benchmarkTuning{
		Chunk:               defaultChunk,
		OutputChunks:        1,
		StreamErrorAfter:    -1,
		StreamTruncateAfter: -1,
	})

	if strings.Contains(recorder.Body.String(), "event: response.completed") {
		t.Fatalf("cancelled final delta completed: %s", recorder.Body.String())
	}
}

func TestResponsesStreamWaitsAfterSlowDeltaWrite(t *testing.T) {
	const itl = 25 * time.Millisecond
	recorder := &delayedDeltaRecorder{
		flushCountingRecorder: newFlushCountingRecorder(),
		delay:                 50 * time.Millisecond,
	}

	streamResponses(context.Background(), recorder, newResponsesResponse("test-model", ""), benchmarkTuning{
		ITL:                 itl,
		Chunk:               defaultChunk,
		OutputChunks:        3,
		StreamErrorAfter:    -1,
		StreamTruncateAfter: -1,
	})

	if len(recorder.deltaWrites) != 3 {
		t.Fatalf("delta writes = %d, want 3", len(recorder.deltaWrites))
	}
	if elapsed := recorder.deltaWrites[2].Sub(recorder.deltaWrites[1]); elapsed < itl-5*time.Millisecond {
		t.Fatalf("post-write ITL = %s, want at least %s", elapsed, itl-5*time.Millisecond)
	}
}

func TestLoadServerConfig(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{name: "unset defaults", want: defaultMaxOutputChunks},
		{name: "valid override", value: "12000", want: 12000},
		{name: "non numeric rejected", value: "many", wantErr: true},
		{name: "zero rejected", value: "0", wantErr: true},
		{name: "above hard maximum rejected", value: "60001", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := loadServerConfig(func(name string) string {
				if name == maxOutputChunksEnv {
					return test.value
				}
				return ""
			})
			if test.wantErr {
				if err == nil {
					t.Fatal("loadServerConfig() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadServerConfig() error = %v", err)
			}
			if config.maxOutputChunks != test.want {
				t.Fatalf("max output chunks = %d, want %d", config.maxOutputChunks, test.want)
			}
		})
	}
}

func TestOutputChunksLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "default maximum accepted", value: "6000", want: true},
		{name: "above default maximum rejected", value: "6001", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request.Header.Set(headerOutputChunks, test.value)
			tuning, err := resolveBenchmarkTuning(request, defaultMaxOutputChunks)
			if test.want {
				if err != nil {
					t.Fatalf("resolveBenchmarkTuning() error = %v", err)
				}
				if tuning.OutputChunks != defaultMaxOutputChunks {
					t.Fatalf("output chunks = %d, want %d", tuning.OutputChunks, defaultMaxOutputChunks)
				}
				return
			}
			if err == nil {
				t.Fatal("resolveBenchmarkTuning() error = nil, want error")
			}
		})
	}
}

func TestConfiguredOutputChunksLimit(t *testing.T) {
	config, err := loadServerConfig(func(name string) string {
		if name == maxOutputChunksEnv {
			return "12000"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("loadServerConfig() error = %v", err)
	}

	for _, test := range []struct {
		name       string
		output     string
		wantStatus int
	}{
		{name: "configured maximum accepted", output: "12000", wantStatus: http.StatusOK},
		{name: "above configured maximum rejected", output: "12001", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(headerOutputChunks, test.output)
			recorder := httptest.NewRecorder()
			newRouterWithConfig(config).ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestWriteSSEFrameHandlesPartialWrites(t *testing.T) {
	writer := &partialResponseWriter{header: make(http.Header), maxWrite: 3}
	if err := writeSSEFrame(writer, []byte("event: test\ndata: {}\n\n")); err != nil {
		t.Fatalf("writeSSEFrame() error = %v", err)
	}
	if got, want := writer.body.String(), "event: test\ndata: {}\n\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if writer.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", writer.flushes)
	}
}

func BenchmarkResponsesFixedChunkStream(b *testing.B) {
	tuning := benchmarkTuning{
		Chunk:               defaultChunk,
		OutputChunks:        1000,
		StreamErrorAfter:    -1,
		StreamTruncateAfter: -1,
	}
	writer := &discardResponseWriter{header: make(http.Header)}
	b.SetBytes(int64(tuning.OutputChunks * len(tuning.Chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		streamResponses(context.Background(), writer, newResponsesResponse("test-model", ""), tuning)
	}
}

func TestOpenAITextEndpointsIgnoreBodyTuning(t *testing.T) {
	for _, test := range []struct {
		path string
		body string
	}{
		{
			path: "/v1/responses",
			body: `{"model":"test-model","input":null,"repeats":99,"delay":99,"size":999}`,
		},
		{
			path: "/v1/chat/completions",
			body: `{"model":"test-model","messages":[],"repeats":99,"delay":99,"size":999}`,
		},
		{
			path: "/v1/completions",
			body: `{"model":"test-model","prompt":"ignored","repeats":99,"delay":99,"size":999}`,
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := postJSON(t, test.path, test.body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), defaultChunk) {
				t.Fatalf("response does not contain default chunk: %s", recorder.Body.String())
			}
		})
	}
}

func TestChunkBytesControlsOutput(t *testing.T) {
	recorder := postJSONWithHeaders(t, "/v1/chat/completions", `{
		"model":"test-model",
		"messages":[{"role":"user","content":"hello"}]
	}`, map[string]string{
		headerChunkBytes:   "7",
		headerOutputChunks: "2",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body := responseMap(t, recorder)
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if got := len(message["content"].(string)); got != 14 {
		t.Fatalf("content length = %d, want 14", got)
	}
}

func TestRejectsInvalidLoadTesterHeaders(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"negative itl": {
			headerITL: "-1",
		},
		"empty chunk": {
			headerChunk: "",
		},
		"zero chunks": {
			headerOutputChunks: "0",
		},
		"success status": {
			headerStatusCode: "200",
		},
		"combined chunk values": {
			headerChunk:      "text",
			headerChunkBytes: "4",
		},
		"output over cap": {
			headerChunkBytes:   strconv.Itoa(maxOutputBytes),
			headerOutputChunks: "2",
		},
		"competing stream stops": {
			headerStreamErrorAfter:    "0",
			headerStreamTruncateAfter: "0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := postJSONWithHeaders(t, "/v1/responses", `{"model":"test-model","input":"hello"}`, headers)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestInjectedStatusAndStreamFailures(t *testing.T) {
	recorder := postJSONWithHeaders(t, "/v1/responses", `{}`, map[string]string{
		headerStatusCode: "503",
	})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if got := responseMap(t, recorder)["error"].(map[string]any)["type"]; got != "server_error" {
		t.Fatalf("error type = %#v, want server_error", got)
	}

	recorder = postJSONWithHeaders(t, "/v1/responses", `{"model":"test-model","input":"hello","stream":true}`, map[string]string{
		headerOutputChunks:     "2",
		headerStreamErrorAfter: "1",
	})
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("stream does not contain error event: %s", body)
	}
	if strings.Contains(body, "event: response.completed") {
		t.Fatalf("failed stream unexpectedly completed: %s", body)
	}

	recorder = postJSONWithHeaders(t, "/v1/responses", `{"model":"test-model","input":"hello","stream":true}`, map[string]string{
		headerOutputChunks:        "2",
		headerStreamTruncateAfter: "1",
	})
	body = recorder.Body.String()
	if strings.Contains(body, "event: error") || strings.Contains(body, "event: response.completed") {
		t.Fatalf("truncated Responses stream unexpectedly terminated: %s", body)
	}

	recorder = postJSONWithHeaders(t, "/v1/chat/completions", `{"model":"test-model","messages":[],"stream":true}`, map[string]string{
		headerOutputChunks:        "2",
		headerStreamTruncateAfter: "1",
	})
	body = recorder.Body.String()
	if strings.Contains(body, "data: [DONE]") || strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("truncated stream unexpectedly completed: %s", body)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	activeRequests.Store(0)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	requestFinished := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-requestFinished:
		case <-time.After(time.Second):
		}
		activeRequests.Store(0)
	})
	router := newRouter()
	firstRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model"}`))
	firstRequest.Header.Set("Content-Type", "application/json")
	firstRequest.Header.Set(headerMaxConcurrency, "1")
	firstRequest.Header.Set(headerTTFT, "100")
	go func() {
		defer close(requestFinished)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, firstRequest)
		firstDone <- recorder
	}()

	deadline := time.Now().Add(time.Second)
	for activeRequests.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if activeRequests.Load() != 1 {
		t.Fatal("first request did not acquire the concurrency slot")
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model"}`))
	secondRequest.Header.Set("Content-Type", "application/json")
	secondRequest.Header.Set(headerMaxConcurrency, "1")
	secondRecorder := httptest.NewRecorder()
	router.ServeHTTP(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", secondRecorder.Code, http.StatusTooManyRequests, secondRecorder.Body.String())
	}
	if firstRecorder := <-firstDone; firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d: %s", firstRecorder.Code, http.StatusOK, firstRecorder.Body.String())
	}
	if activeRequests.Load() != 0 {
		t.Fatalf("active requests = %d, want 0", activeRequests.Load())
	}
}

func TestChatCompletionsSupportsJSONAndSSE(t *testing.T) {
	recorder := postJSON(t, "/v1/chat/completions", `{
		"model":"test-model",
		"messages":[{"role":"user","content":"hello"}]
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body := responseMap(t, recorder)
	if got := body["object"]; got != "chat.completion" {
		t.Fatalf("object = %#v, want chat.completion", got)
	}
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if got := message["content"]; got != defaultChunk {
		t.Fatalf("content = %#v, want %s", got, defaultChunk)
	}

	recorder = postJSONWithHeaders(t, "/v1/chat/completions", `{
		"model":"test-model",
		"messages":[{"role":"user","content":"hello"}],
		"stream":true,
		"stream_options":{"include_usage":true}
	}`, map[string]string{
		headerOutputChunks: "2",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	stream := recorder.Body.String()
	for _, expected := range []string{`"role":"assistant"`, `"content":"xxxx"`, `"finish_reason":"stop"`, `"usage":`, "data: [DONE]"} {
		if !strings.Contains(stream, expected) {
			t.Fatalf("stream missing %q: %s", expected, stream)
		}
	}
}

func TestLegacyStreamsFlushOncePerFrame(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: `{"model":"test-model","messages":[],"stream":true}`,
		},
		{
			name: "completions",
			path: "/v1/completions",
			body: `{"model":"test-model","prompt":"hello","stream":true}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newFlushCountingRecorder()
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(headerOutputChunks, "2")
			newRouter().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if frames := strings.Count(recorder.Body.String(), "\n\n"); recorder.flushes != frames {
				t.Fatalf("flushes = %d, want %d frames", recorder.flushes, frames)
			}
		})
	}
}

func TestCompletionsAndModels(t *testing.T) {
	recorder := postJSON(t, "/v1/completions", `{"model":"test-model","prompt":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("completion status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body := responseMap(t, recorder)
	if got := body["object"]; got != "text_completion" {
		t.Fatalf("object = %#v, want text_completion", got)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	recorder = httptest.NewRecorder()
	newRouter().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("models status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	data := responseMap(t, recorder)["data"].([]any)
	if got := data[0].(map[string]any)["id"]; got != defaultModel {
		t.Fatalf("model id = %#v, want %s", got, defaultModel)
	}
}

func TestEmbeddingsSupportFloatAndBase64(t *testing.T) {
	recorder := postJSON(t, "/v1/embeddings", `{
		"model":"test-model",
		"input":["one","two"],
		"encoding_format":"float"
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("float status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body := responseMap(t, recorder)
	data := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("embedding count = %d, want 2", len(data))
	}
	vector := data[0].(map[string]any)["embedding"].([]any)
	if len(vector) != 3 {
		t.Fatalf("vector length = %d, want 3", len(vector))
	}

	recorder = postJSON(t, "/v1/embeddings", `{
		"model":"test-model",
		"input":"one",
		"encoding_format":"base64"
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("base64 status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body = responseMap(t, recorder)
	encoded := body["data"].([]any)[0].(map[string]any)["embedding"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 embedding decode failed: %v", err)
	}
	if len(decoded) != 12 {
		t.Fatalf("base64 embedding length = %d, want 12", len(decoded))
	}
}

func TestEmbeddingsRejectEmptyInput(t *testing.T) {
	recorder := postJSON(t, "/v1/embeddings", `{"model":"test-model","input":[]}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func postJSON(t *testing.T, path, body string) *httptest.ResponseRecorder {
	return postJSONWithHeaders(t, path, body, nil)
}

func postJSONWithHeaders(t *testing.T, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	newRouter().ServeHTTP(recorder, request)
	return recorder
}

func responseMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON unmarshal failed: %v: %s", err, recorder.Body.String())
	}
	return response
}

type sseEvent struct {
	name string
	data map[string]any
}

type flushCountingRecorder struct {
	*httptest.ResponseRecorder
	flushes int
	onWrite func([]byte)
}

func newFlushCountingRecorder() *flushCountingRecorder {
	return &flushCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (recorder *flushCountingRecorder) Write(frame []byte) (int, error) {
	if recorder.onWrite != nil {
		recorder.onWrite(frame)
	}
	return recorder.ResponseRecorder.Write(frame)
}

func (recorder *flushCountingRecorder) Flush() {
	recorder.flushes++
}

type delayedDeltaRecorder struct {
	*flushCountingRecorder
	delay       time.Duration
	deltaWrites []time.Time
}

func (recorder *delayedDeltaRecorder) Write(frame []byte) (int, error) {
	isDelta := bytes.Contains(frame, []byte("event: response.output_text.delta\n"))
	if isDelta && len(recorder.deltaWrites) == 1 {
		time.Sleep(recorder.delay)
	}
	written, err := recorder.flushCountingRecorder.Write(frame)
	if isDelta {
		recorder.deltaWrites = append(recorder.deltaWrites, time.Now())
	}
	return written, err
}

type partialResponseWriter struct {
	header   http.Header
	body     bytes.Buffer
	maxWrite int
	flushes  int
}

func (writer *partialResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *partialResponseWriter) Write(frame []byte) (int, error) {
	written := len(frame)
	if writer.maxWrite > 0 && written > writer.maxWrite {
		written = writer.maxWrite
	}
	_, _ = writer.body.Write(frame[:written])
	return written, nil
}

func (writer *partialResponseWriter) WriteHeader(int) {}

func (writer *partialResponseWriter) Flush() {
	writer.flushes++
}

type discardResponseWriter struct {
	header http.Header
}

func (writer *discardResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *discardResponseWriter) Write(frame []byte) (int, error) {
	return len(frame), nil
}

func (writer *discardResponseWriter) WriteHeader(int) {}

func (writer *discardResponseWriter) Flush() {}

func parseResponsesSSEEvents(t *testing.T, stream string) []sseEvent {
	t.Helper()
	frames := strings.Split(strings.TrimSuffix(stream, "\n\n"), "\n\n")
	events := make([]sseEvent, 0, len(frames))
	for _, frame := range frames {
		var event sseEvent
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event.data); err != nil {
					t.Fatalf("SSE data JSON unmarshal failed: %v: %s", err, line)
				}
			}
		}
		if event.name == "" || event.data == nil {
			t.Fatalf("invalid SSE frame: %q", frame)
		}
		events = append(events, event)
	}
	return events
}

func sseSequence(t *testing.T, event sseEvent) int {
	t.Helper()
	sequence, ok := event.data["sequence_number"].(float64)
	if !ok {
		t.Fatalf("sequence_number = %#v, want number", event.data["sequence_number"])
	}
	return int(sequence)
}

func eventText(t *testing.T, event sseEvent, field string) string {
	t.Helper()
	text, ok := event.data[field].(string)
	if !ok {
		t.Fatalf("%s = %#v, want string", field, event.data[field])
	}
	return text
}

func nestedEventText(t *testing.T, event sseEvent, field string) string {
	t.Helper()
	part, ok := event.data[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want object", field, event.data[field])
	}
	text, ok := part["text"].(string)
	if !ok {
		t.Fatalf("%s.text = %#v, want string", field, part["text"])
	}
	return text
}

func nestedResponseText(t *testing.T, response map[string]any) string {
	t.Helper()
	output, ok := response["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("output = %#v, want one item", response["output"])
	}
	item, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("output item = %#v, want object", output[0])
	}
	content, ok := item["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one item", item["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content part = %#v, want object", content[0])
	}
	text, ok := part["text"].(string)
	if !ok {
		t.Fatalf("content text = %#v, want string", part["text"])
	}
	return text
}
