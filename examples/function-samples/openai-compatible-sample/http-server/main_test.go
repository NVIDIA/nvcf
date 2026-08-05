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
	router := newRouter()
	firstRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model"}`))
	firstRequest.Header.Set("Content-Type", "application/json")
	firstRequest.Header.Set(headerMaxConcurrency, "1")
	firstRequest.Header.Set(headerTTFT, "100")
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
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
