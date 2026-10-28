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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// decisionMaxRequestBytes bounds the decision routes only. A System One image
// request can carry an 8 MB image, which is about 10.7 MB as base64 and does
// not fit the 10 MiB limit the other routes keep.
const decisionMaxRequestBytes = 16 << 20

const (
	decisionRoute     = "/v1/decisions"
	systemOneRoute    = "/v1/systemone"
	mockErrorQuery    = "mock_error"
	mockRetryAfter    = "7"
	mockEchoImagesHdr = "x-mock-echo-typesafe-danger-zone-images"
)

// decisionEchoHeaders are the request headers reported in the response "echo"
// object so a test can prove they reached the backend.
var decisionEchoHeaders = []string{
	"typesafe-danger-zone-images",
	"x-model",
	"x-input-tokens",
	"x-request-id",
	"x-vendor-test",
}

type decisionRequest struct {
	Model     string          `json:"model"`
	Questions json.RawMessage `json:"questions"`
	Echo      *bool           `json:"x_load_tester_echo"`
}

type decisionUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type decisionEcho map[string]any

type decisionsResponse struct {
	Model       string          `json:"model"`
	Answers     []any           `json:"answers"`
	Usage       decisionUsage   `json:"usage"`
	Echo        decisionEcho    `json:"echo"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
}

type systemOneResponse struct {
	Model       string          `json:"model"`
	Answers     map[string]any  `json:"answers"`
	Usage       decisionUsage   `json:"usage"`
	Echo        decisionEcho    `json:"echo"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
}

type decisionQuestion struct {
	Type    string   `json:"type"`
	Choices []string `json:"choices"`
}

func handleDecisions(w http.ResponseWriter, r *http.Request) {
	handleDecisionRoute(w, r, false)
}

func handleSystemOne(w http.ResponseWriter, r *http.Request) {
	handleDecisionRoute(w, r, true)
}

func handleDecisionRoute(w http.ResponseWriter, r *http.Request, systemOne bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeDecisionError(w, systemOne, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, decisionMaxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeDecisionError(w, systemOne, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		writeDecisionError(w, systemOne, http.StatusBadRequest, "could not read request body")
		return
	}
	request, err := parseDecisionRequest(raw)
	if err != nil {
		writeDecisionError(w, systemOne, http.StatusBadRequest, err.Error())
		return
	}

	switch r.URL.Query().Get(mockErrorQuery) {
	case "429":
		w.Header().Set("Retry-After", mockRetryAfter)
		writeDecisionError(w, systemOne, http.StatusTooManyRequests, "mock rate limit")
		return
	case "400":
		writeDecisionError(w, systemOne, http.StatusBadRequest, "mock validation error")
		return
	}

	echo := decisionEcho{"body_length": len(raw)}
	for _, name := range decisionEchoHeaders {
		if value := r.Header.Get(name); value != "" {
			echo[name] = value
		}
	}
	if value := r.Header.Get("typesafe-danger-zone-images"); value != "" {
		w.Header().Set(mockEchoImagesHdr, value)
	}
	var echoedBody json.RawMessage
	if request.Echo != nil && *request.Echo || strings.EqualFold(r.Header.Get(headerEcho), "true") {
		echoedBody = json.RawMessage(raw)
	}
	model := request.Model
	if model == "" {
		model = defaultModel
	}
	usage := decisionUsage{InputTokens: estimateDecisionInputTokens(r, raw), OutputTokens: 1}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens

	if systemOne {
		writeJSON(w, http.StatusOK, systemOneResponse{
			Model: model, Answers: systemOneAnswers(request.Questions),
			Usage: usage, Echo: echo, RequestBody: echoedBody,
		})
		return
	}
	writeJSON(w, http.StatusOK, decisionsResponse{
		Model: model, Answers: decisionAnswers(request.Questions),
		Usage: usage, Echo: echo, RequestBody: echoedBody,
	})
}

func parseDecisionRequest(raw []byte) (decisionRequest, error) {
	var request decisionRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("request body must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, errors.New("request body must contain one JSON object")
	}
	return request, nil
}

// estimateDecisionInputTokens prefers the gateway estimate when the caller
// sends one, so usage lines up with what the gateway metered.
func estimateDecisionInputTokens(r *http.Request, raw []byte) int {
	if value, err := strconv.Atoi(r.Header.Get("X-Input-Tokens")); err == nil && value >= 0 {
		return value
	}
	return len(raw)/4 + 1
}

func decisionAnswers(questions json.RawMessage) []any {
	var list []decisionQuestion
	if err := json.Unmarshal(questions, &list); err != nil || len(list) == 0 {
		return []any{predicateAnswer()}
	}
	answers := make([]any, 0, len(list))
	for _, question := range list {
		answers = append(answers, cannedAnswer(question, false))
	}
	return answers
}

func systemOneAnswers(questions json.RawMessage) map[string]any {
	var byName map[string]decisionQuestion
	if err := json.Unmarshal(questions, &byName); err != nil || len(byName) == 0 {
		return map[string]any{"question": cannedAnswer(decisionQuestion{Type: "noul"}, true)}
	}
	answers := make(map[string]any, len(byName))
	for name, question := range byName {
		answers[name] = cannedAnswer(question, true)
	}
	return answers
}

func predicateAnswer() map[string]any {
	return map[string]any{"type": "predicate", "probability": 0.99}
}

func cannedAnswer(question decisionQuestion, systemOne bool) map[string]any {
	switch question.Type {
	case "choice":
		choice := "A"
		if len(question.Choices) > 0 {
			choice = question.Choices[0]
		}
		return map[string]any{"type": "choice", "choice": choice}
	case "score":
		return map[string]any{"type": "score", "score": 0.5}
	}
	if systemOne {
		return map[string]any{"type": "noul", "noul": 0.99}
	}
	return predicateAnswer()
}

// writeDecisionError writes the error shape of the route: FastAPI style for
// System One, OpenAI style for Decisions.
func writeDecisionError(w http.ResponseWriter, systemOne bool, status int, message string) {
	if systemOne {
		writeJSON(w, status, map[string]any{"detail": []map[string]any{{
			"loc": []string{"body"}, "msg": message, "type": "value_error",
		}}})
		return
	}
	errorType := "invalid_request_error"
	if status == http.StatusTooManyRequests {
		errorType = "rate_limit_error"
	} else if status >= http.StatusInternalServerError {
		errorType = "api_error"
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"message": message, "type": errorType, "code": fmt.Sprint(status),
	}})
}
