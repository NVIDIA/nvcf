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
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const decisionsBody = `{"model":"test-model","input":"a photo","questions":[` +
	`{"type":"predicate","text":"is it red?"},` +
	`{"type":"choice","text":"which?","choices":["cat","dog"]},` +
	`{"type":"score","text":"how good?"}]}`

const systemOneBody = `{"model":"test-model","state":{"text":"hello"},"questions":{` +
	`"a":{"type":"noul"},"b":{"type":"choice","choices":["x","y"]},"c":{"type":"score"}}}`

var decisionHeaders = map[string]string{
	"TypeSafe-Danger-Zone-Images": "true",
	"X-Model":                     "test-model",
	"X-Input-Tokens":              "42",
	"X-Request-Id":                "req-abc",
	"X-Vendor-Test":               "vendor-value",
}

func TestDecisionsReturnsCannedAnswersInQuestionOrder(t *testing.T) {
	recorder := postJSON(t, "/v1/decisions", decisionsBody)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Model   string           `json:"model"`
		Answers []map[string]any `json:"answers"`
		Usage   map[string]int   `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, recorder.Body.String())
	}
	if response.Model != "test-model" {
		t.Fatalf("model = %q", response.Model)
	}
	if len(response.Answers) != 3 {
		t.Fatalf("answers = %v, want 3", response.Answers)
	}
	for i, key := range []string{"probability", "choice", "score"} {
		if _, ok := response.Answers[i][key]; !ok {
			t.Fatalf("answer %d = %v, want key %q", i, response.Answers[i], key)
		}
	}
	if response.Answers[1]["choice"] != "cat" {
		t.Fatalf("choice = %v, want first listed choice", response.Answers[1]["choice"])
	}
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if _, ok := response.Usage[key]; !ok {
			t.Fatalf("usage = %v, missing %q", response.Usage, key)
		}
	}
	if response.Usage["total_tokens"] != response.Usage["input_tokens"]+response.Usage["output_tokens"] {
		t.Fatalf("usage total mismatch: %v", response.Usage)
	}
}

func TestSystemOneReturnsCannedAnswersByName(t *testing.T) {
	recorder := postJSON(t, "/v1/systemone", systemOneBody)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Model   string                    `json:"model"`
		Answers map[string]map[string]any `json:"answers"`
		Usage   map[string]int            `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, recorder.Body.String())
	}
	if response.Model != "test-model" || len(response.Answers) != 3 {
		t.Fatalf("response = %s", recorder.Body.String())
	}
	if response.Answers["a"]["type"] != "noul" || response.Answers["a"]["noul"] != 0.99 {
		t.Fatalf("answer a = %v, want noul 0.99", response.Answers["a"])
	}
	if response.Answers["b"]["type"] != "choice" || response.Answers["b"]["choice"] != "x" {
		t.Fatalf("answer b = %v", response.Answers["b"])
	}
	if response.Answers["c"]["type"] != "score" {
		t.Fatalf("answer c = %v", response.Answers["c"])
	}
	if _, ok := response.Usage["total_tokens"]; !ok {
		t.Fatalf("usage = %v", response.Usage)
	}
}

func TestDecisionRoutesEchoRequestHeadersAndBodyLength(t *testing.T) {
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		t.Run(path, func(t *testing.T) {
			body := decisionsBody
			if path == "/v1/systemone" {
				body = systemOneBody
			}
			recorder := postJSONWithHeaders(t, path, body, decisionHeaders)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Echo map[string]any `json:"echo"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"typesafe-danger-zone-images": "true",
				"x-model":                     "test-model",
				"x-input-tokens":              "42",
				"x-request-id":                "req-abc",
				"x-vendor-test":               "vendor-value",
				"body_length":                 float64(len(body)),
			}
			for key, value := range want {
				if response.Echo[key] != value {
					t.Fatalf("echo[%q] = %v, want %v (echo %v)", key, response.Echo[key], value, response.Echo)
				}
			}
			if got := recorder.Header().Get("x-mock-echo-typesafe-danger-zone-images"); got != "true" {
				t.Fatalf("x-mock-echo-typesafe-danger-zone-images = %q, want true", got)
			}
		})
	}
}

func TestDecisionRoutesOmitEchoHeaderWhenRequestHasNone(t *testing.T) {
	recorder := postJSON(t, "/v1/decisions", decisionsBody)
	if got := recorder.Header().Get("x-mock-echo-typesafe-danger-zone-images"); got != "" {
		t.Fatalf("header = %q, want unset", got)
	}
}

func TestDecisionRoutesEchoRequestBodyWhenAsked(t *testing.T) {
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		t.Run(path, func(t *testing.T) {
			body := `{"model":"m","x_load_tester_echo":true,"extra":{"k":[1,2]}}`
			recorder := postJSON(t, path, body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				RequestBody json.RawMessage `json:"request_body"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if string(response.RequestBody) != body {
				t.Fatalf("request_body = %s, want %s", response.RequestBody, body)
			}
		})
	}
}

func TestDecisionRoutesAcceptEightMegabyteImageAtRealEncodedSize(t *testing.T) {
	image := base64.StdEncoding.EncodeToString(make([]byte, 8_000_000))
	if len(image) <= maxRequestBytes {
		t.Fatalf("test image encodes to %d, not above the 10 MiB general limit", len(image))
	}
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		t.Run(path, func(t *testing.T) {
			body := `{"model":"m","input":{"image":"` + image + `"}}`
			recorder := postJSON(t, path, body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %.200s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestDecisionRoutesRejectBodiesAbove16MiB(t *testing.T) {
	filler := strings.Repeat("a", decisionMaxRequestBytes)
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		t.Run(path, func(t *testing.T) {
			recorder := postJSON(t, path, `{"model":"m","input":"`+filler+`"}`)
			if recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", recorder.Code)
			}
			assertJSONError(t, recorder.Body.Bytes())
		})
	}
}

func TestExistingRoutesKeepTenMiBLimit(t *testing.T) {
	if decisionMaxRequestBytes != 16<<20 {
		t.Fatalf("decisionMaxRequestBytes = %d, want 16 MiB", decisionMaxRequestBytes)
	}
	if maxRequestBytes != 10<<20 {
		t.Fatalf("maxRequestBytes = %d, want 10 MiB", maxRequestBytes)
	}
	filler := strings.Repeat("a", 11<<20)
	recorder := postJSON(t, "/v1/embeddings", `{"model":"m","input":"`+filler+`"}`)
	if recorder.Code == http.StatusOK {
		t.Fatalf("embeddings accepted an 11 MiB body")
	}
	recorder = postJSON(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"`+filler+`"}]}`)
	if recorder.Code == http.StatusOK {
		t.Fatalf("chat accepted an 11 MiB body")
	}
}

func TestDecisionRoutesRejectInvalidBodies(t *testing.T) {
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		for name, body := range map[string]string{
			"not json":    `not json`,
			"array":       `[1,2]`,
			"string":      `"x"`,
			"two objects": `{} {}`,
			"empty":       ``,
		} {
			t.Run(path+" "+name, func(t *testing.T) {
				recorder := postJSON(t, path, body)
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
				}
				assertJSONError(t, recorder.Body.Bytes())
			})
		}
	}
}

func TestDecisionRoutesMockErrorTriggers(t *testing.T) {
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		t.Run(path+" 429", func(t *testing.T) {
			recorder := postJSON(t, path+"?mock_error=429", decisionsBody)
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", recorder.Code)
			}
			if got := recorder.Header().Get("Retry-After"); got != "7" {
				t.Fatalf("Retry-After = %q, want 7", got)
			}
			assertJSONError(t, recorder.Body.Bytes())
		})
	}
	t.Run("decisions 400 is OpenAI style", func(t *testing.T) {
		recorder := postJSON(t, "/v1/decisions?mock_error=400", decisionsBody)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		var body struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Error.Message == "" || body.Error.Type == "" {
			t.Fatalf("body = %s (err %v)", recorder.Body.String(), err)
		}
	})
	t.Run("systemone 400 is FastAPI style", func(t *testing.T) {
		recorder := postJSON(t, "/v1/systemone?mock_error=400", systemOneBody)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		var body struct {
			Detail []struct {
				Loc  []any  `json:"loc"`
				Msg  string `json:"msg"`
				Type string `json:"type"`
			} `json:"detail"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || len(body.Detail) == 0 || body.Detail[0].Msg == "" {
			t.Fatalf("body = %s (err %v)", recorder.Body.String(), err)
		}
	})
}

func TestDecisionRoutesRejectNonPost(t *testing.T) {
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		recorder := getPath(t, path)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s GET status = %d, want 405", path, recorder.Code)
		}
	}
}

func assertJSONError(t *testing.T, body []byte) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed) == 0 {
		t.Fatalf("error body is not a JSON object: %q (err %v)", body, err)
	}
}

func getPath(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	newRouter().ServeHTTP(recorder, request)
	return recorder
}
