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
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Unusual spacing, key order, and an escaped character make any re-encoding
// visible. The extension fields and audio part are the ones a gateway has
// dropped, added, or rejected before.
const echoChatBody = `{"messages":[{"role":"user","content":[{"type":"text","text":"caf\u00e9"},` +
	`{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}],  "model":"test-model",` +
	`"chat_template_kwargs":{"enable_thinking":false},"ignore_eos":true%s}`

const echoResponsesBody = `{"input":"caf\u00e9",  "model":"test-model","metadata":{"k":"v"}%s}`

func TestEchoReturnsRequestBodyVerbatim(t *testing.T) {
	for name, test := range map[string]struct {
		path    string
		body    string
		headers map[string]string
		text    func(*testing.T, string) string
	}{
		"chat json body control": {
			path: "/v1/chat/completions",
			body: echoBody(echoChatBody, `,"x_load_tester_echo":true`),
			text: chatMessageText,
		},
		"chat json header control": {
			path:    "/v1/chat/completions",
			body:    echoBody(echoChatBody, ""),
			headers: map[string]string{headerEcho: "true"},
			text:    chatMessageText,
		},
		"chat stream": {
			path: "/v1/chat/completions",
			body: echoBody(echoChatBody, `,"stream":true,"x_load_tester_echo":true`),
			text: chatStreamText,
		},
		"responses json": {
			path: "/v1/responses",
			body: echoBody(echoResponsesBody, `,"x_load_tester_echo":true`),
			text: func(t *testing.T, body string) string {
				var response map[string]any
				if err := json.Unmarshal([]byte(body), &response); err != nil {
					t.Fatalf("response JSON unmarshal failed: %v: %s", err, body)
				}
				return nestedResponseText(t, response)
			},
		},
		"responses stream": {
			path: "/v1/responses",
			body: echoBody(echoResponsesBody, `,"stream":true,"x_load_tester_echo":true`),
			text: func(t *testing.T, body string) string {
				events := parseResponsesSSEEvents(t, body)
				if got := events[len(events)-1].name; got != "response.completed" {
					t.Fatalf("last event = %q, want response.completed", got)
				}
				var text strings.Builder
				for _, event := range events {
					if event.name == "response.output_text.delta" {
						text.WriteString(eventText(t, event, "delta"))
					}
				}
				return text.String()
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := postJSONWithHeaders(t, test.path, test.body, test.headers)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if got := test.text(t, recorder.Body.String()); got != test.body {
				t.Fatalf("echoed text = %q, want request body %q", got, test.body)
			}
		})
	}
}

func TestEchoFalseKeepsGeneratedOutput(t *testing.T) {
	recorder := postJSON(t, "/v1/chat/completions", echoBody(echoChatBody, `,"x_load_tester_echo":false`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := chatMessageText(t, recorder.Body.String()); got != defaultChunk {
		t.Fatalf("content = %q, want %q", got, defaultChunk)
	}
}

func TestRejectsInvalidEchoControls(t *testing.T) {
	for name, test := range map[string]struct {
		path    string
		body    string
		headers map[string]string
	}{
		"non-boolean body value": {
			path: "/v1/chat/completions",
			body: echoBody(echoChatBody, `,"x_load_tester_echo":"yes"`),
		},
		"non-boolean header value": {
			path:    "/v1/chat/completions",
			body:    echoBody(echoChatBody, ""),
			headers: map[string]string{headerEcho: "maybe"},
		},
		"combined with chunk": {
			path: "/v1/chat/completions",
			body: echoBody(echoChatBody, `,"x_load_tester_echo":true,"x_load_tester_chunk":"text"`),
		},
		"combined with chunk bytes": {
			path: "/v1/chat/completions",
			body: echoBody(echoChatBody, `,"x_load_tester_echo":true,"x_load_tester_chunk_bytes":4`),
		},
		"combined with output chunks": {
			path: "/v1/chat/completions",
			body: echoBody(echoChatBody, `,"x_load_tester_echo":true,"x_load_tester_output_chunks":2`),
		},
		"unsupported on embeddings": {
			path: "/v1/embeddings",
			body: `{"model":"test-model","input":"hello","x_load_tester_echo":true}`,
		},
		"unsupported on messages": {
			path: "/v1/messages",
			body: messagesBody + `,"x_load_tester_echo":true}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := postJSONWithHeaders(t, test.path, test.body, test.headers)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func echoBody(format, extra string) string {
	return strings.Replace(format, "%s", extra, 1)
}

func chatMessageText(t *testing.T, body string) string {
	t.Helper()
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil || len(response.Choices) != 1 {
		t.Fatalf("chat response = %s, want one choice (err %v)", body, err)
	}
	return response.Choices[0].Message.Content
}

func chatStreamText(t *testing.T, body string) string {
	t.Helper()
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream does not end with [DONE]: %s", body)
	}
	var text strings.Builder
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("stream chunk JSON unmarshal failed: %v: %s", err, data)
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
		}
	}
	return text.String()
}
