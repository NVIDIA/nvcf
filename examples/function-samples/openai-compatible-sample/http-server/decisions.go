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

type decisionsUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int `json:"total_tokens"`
}

type systemOneUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type decisionEcho map[string]any

type decisionsResponse struct {
	Model       string          `json:"model"`
	Answers     []any           `json:"answers"`
	Usage       decisionsUsage  `json:"usage"`
	Echo        decisionEcho    `json:"echo"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
}

type systemOneResponse struct {
	Model       string          `json:"model"`
	Answers     map[string]any  `json:"answers"`
	Usage       systemOneUsage  `json:"usage"`
	Echo        decisionEcho    `json:"echo"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
}

// decisionQuestion reads only what the canned answers need. Everything else is
// kept raw so an unexpected field never fails a request.
type decisionQuestion struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Choices  json.RawMessage `json:"choices"`
	Levels   json.RawMessage `json:"levels"`
	Criteria json.RawMessage `json:"criteria"`
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
	inputTokens := estimateDecisionInputTokens(r, raw)

	if systemOne {
		writeJSON(w, http.StatusOK, systemOneResponse{
			Model: model, Answers: systemOneAnswers(request.Questions),
			Usage: systemOneUsage{InputTokens: inputTokens, OutputTokens: 1}, Echo: echo, RequestBody: echoedBody,
		})
		return
	}
	usage := decisionsUsage{InputTokens: inputTokens, OutputTokens: 1, TotalTokens: inputTokens + 1}
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
	var list []json.RawMessage
	if err := json.Unmarshal(questions, &list); err != nil || len(list) == 0 {
		return []any{predicateAnswer("")}
	}
	answers := make([]any, 0, len(list))
	for _, item := range list {
		var question decisionQuestion
		_ = json.Unmarshal(item, &question)
		answers = append(answers, cannedAnswer(question, false))
	}
	return answers
}

func systemOneAnswers(questions json.RawMessage) map[string]any {
	var byName map[string]json.RawMessage
	if err := json.Unmarshal(questions, &byName); err != nil || len(byName) == 0 {
		return map[string]any{"question": cannedAnswer(decisionQuestion{Type: "noul"}, true)}
	}
	answers := make(map[string]any, len(byName))
	for name, item := range byName {
		var question decisionQuestion
		_ = json.Unmarshal(item, &question)
		answers[name] = cannedAnswer(question, true)
	}
	return answers
}

func predicateAnswer(name string) map[string]any {
	return withName(map[string]any{"type": "predicate", "probability": 0.99}, name)
}

func withName(answer map[string]any, name string) map[string]any {
	if name != "" {
		answer["name"] = name
	}
	return answer
}

// distribution puts 0.7 on the first of n options and spreads the rest evenly.
func distribution(n int) []float64 {
	probabilities := make([]float64, n)
	for i := range probabilities {
		switch {
		case n == 1:
			probabilities[i] = 1
		case i == 0:
			probabilities[i] = 0.7
		default:
			probabilities[i] = 0.3 / float64(n-1)
		}
	}
	return probabilities
}

// listItems returns the elements of a JSON array, or nil.
func listItems(raw json.RawMessage) []json.RawMessage {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	return list
}

// field returns the member of a JSON object item, or the item itself when it
// is not an object, so plain values and objects are both accepted.
func field(item json.RawMessage, key string) json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(item, &object) == nil {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return item
}

// objectKeys returns the keys of a JSON object in document order.
func objectKeys(raw json.RawMessage) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return keys
		}
		keys = append(keys, name)
		var skip json.RawMessage
		if decoder.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

func labelOf(item json.RawMessage, key string) string {
	var label string
	if json.Unmarshal(field(item, key), &label) == nil {
		return label
	}
	return strings.Trim(string(field(item, key)), `"`)
}

func weightedIndex(probabilities []float64) float64 {
	var score float64
	for i, probability := range probabilities {
		score += float64(i) * probability
	}
	return score
}

func cannedAnswer(question decisionQuestion, systemOne bool) map[string]any {
	switch question.Type {
	case "choice":
		if systemOne {
			return systemOneChoice(question)
		}
		return decisionsChoice(question)
	case "score":
		if systemOne {
			return systemOneScore(question)
		}
		return decisionsScore(question)
	case "box":
		answer := map[string]any{
			"type": "box", "box": map[string]any{"top": 0.1, "bottom": 0.9, "left": 0.1, "right": 0.9}, "confidence": 0.9,
		}
		if systemOne {
			return answer
		}
		return withName(answer, question.Name)
	}
	if systemOne {
		return map[string]any{"type": "noul", "noul": 0.99}
	}
	return predicateAnswer(question.Name)
}

func decisionsChoice(question decisionQuestion) map[string]any {
	values := []json.RawMessage{}
	for _, item := range listItems(question.Choices) {
		values = append(values, field(item, "value"))
	}
	if len(values) == 0 {
		values = []json.RawMessage{json.RawMessage(`"A"`)}
	}
	weights := distribution(len(values))
	probabilities := make([]map[string]any, len(values))
	for i, value := range values {
		probabilities[i] = map[string]any{"value": value, "probability": weights[i]}
	}
	return withName(map[string]any{"type": "choice", "choice": values[0], "probabilities": probabilities, "confidence": 0.9}, question.Name)
}

func decisionsScore(question decisionQuestion) map[string]any {
	levels := listItems(question.Levels)
	labels := []string{"0", "1"}
	if len(levels) > 0 {
		labels = labels[:0]
		for _, level := range levels {
			labels = append(labels, labelOf(level, "label"))
		}
	}
	weights := distribution(len(labels))
	probabilities := make([]map[string]any, len(labels))
	for i, label := range labels {
		probabilities[i] = map[string]any{"value": i, "label": label, "probability": weights[i]}
	}
	return withName(map[string]any{"type": "score", "score": weightedIndex(weights), "probabilities": probabilities, "confidence": 0.9}, question.Name)
}

func systemOneChoice(question decisionQuestion) map[string]any {
	labels := objectKeys(question.Criteria)
	if len(labels) == 0 {
		for _, item := range listItems(question.Choices) {
			labels = append(labels, labelOf(item, "value"))
		}
	}
	if len(labels) == 0 {
		labels = []string{"A"}
	}
	weights := distribution(len(labels))
	probabilities := make(map[string]float64, len(labels))
	for i, label := range labels {
		probabilities[label] = weights[i]
	}
	return map[string]any{"type": "choice", "choice": labels[0], "confidence": 0.9, "probabilities": probabilities}
}

func systemOneScore(question decisionQuestion) map[string]any {
	descriptions := listItems(question.Criteria)
	legend := map[string]string{}
	for i, description := range descriptions {
		legend[strconv.Itoa(i)] = labelOf(description, "")
	}
	count := len(descriptions)
	if count == 0 {
		count = 2
		legend = map[string]string{"0": "0", "1": "1"}
	}
	weights := distribution(count)
	probabilities := make(map[string]float64, count)
	for i, weight := range weights {
		probabilities[strconv.Itoa(i)] = weight
	}
	return map[string]any{"type": "score", "score": weightedIndex(weights), "confidence": 0.9, "legend": legend, "probabilities": probabilities}
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
