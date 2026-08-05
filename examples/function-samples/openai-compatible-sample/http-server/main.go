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
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxRequestBytes        = 10 << 20
	maxOutputBytes         = 1 << 20
	maxOutputChunks        = 4096
	maxEmbeddingItems      = 2048
	maxControlMilliseconds = 5 * 60 * 1000
	maxConcurrencyLimit    = 100000
	defaultChunk           = "xxxx"
	defaultModel           = "test-model"

	headerQueueDelay          = "X-Load-Tester-Queue-Delay-Ms"
	headerTTFT                = "X-Load-Tester-TTFT-Ms"
	headerTTFTJitter          = "X-Load-Tester-TTFT-Jitter-Ms"
	headerITL                 = "X-Load-Tester-ITL-Ms"
	headerITLJitter           = "X-Load-Tester-ITL-Jitter-Ms"
	headerChunk               = "X-Load-Tester-Chunk"
	headerChunkBytes          = "X-Load-Tester-Chunk-Bytes"
	headerOutputChunks        = "X-Load-Tester-Output-Chunks"
	headerStatusCode          = "X-Load-Tester-Status-Code"
	headerStreamErrorAfter    = "X-Load-Tester-Stream-Error-After-Chunks"
	headerStreamTruncateAfter = "X-Load-Tester-Stream-Truncate-After-Chunks"
	headerMaxConcurrency      = "X-Load-Tester-Max-Concurrency"
)

var (
	responseSequence atomic.Uint64
	activeRequests   atomic.Int64
	randomSource     = rand.New(rand.NewSource(time.Now().UnixNano()))
	randomSourceMu   sync.Mutex
)

type benchmarkTuning struct {
	QueueDelay          time.Duration
	TTFT                time.Duration
	TTFTJitter          time.Duration
	ITL                 time.Duration
	ITLJitter           time.Duration
	Chunk               string
	ChunkBytes          int
	OutputChunks        int
	StatusCode          int
	StreamErrorAfter    int
	StreamTruncateAfter int
	MaxConcurrency      int
}

type responsesRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

type chatCompletionsRequest struct {
	Model         string             `json:"model"`
	Stream        bool               `json:"stream"`
	StreamOptions *chatStreamOptions `json:"stream_options"`
}

type completionsRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type embeddingsRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format"`
}

type responsesResponse struct {
	ID                string               `json:"id"`
	Object            string               `json:"object"`
	CreatedAt         int64                `json:"created_at"`
	CompletedAt       *int64               `json:"completed_at,omitempty"`
	Status            string               `json:"status"`
	Model             string               `json:"model"`
	Output            []responseOutputItem `json:"output"`
	ParallelToolCalls bool                 `json:"parallel_tool_calls"`
	ToolChoice        string               `json:"tool_choice"`
	Tools             []any                `json:"tools"`
	Usage             *responsesUsage      `json:"usage"`
}

type responseOutputItem struct {
	ID      string               `json:"id"`
	Type    string               `json:"type"`
	Status  string               `json:"status"`
	Role    string               `json:"role"`
	Content []responseOutputText `json:"content"`
}

type responseOutputText struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type responsesUsage struct {
	InputTokens        int                       `json:"input_tokens"`
	InputTokensDetails responseInputTokenDetails `json:"input_tokens_details"`
	OutputTokens       int                       `json:"output_tokens"`
	OutputTokensDetail responseOutputTokenDetail `json:"output_tokens_details"`
	TotalTokens        int                       `json:"total_tokens"`
}

type responseInputTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responseOutputTokenDetail struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []chatChoice    `json:"choices"`
	Usage   completionUsage `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *completionUsage  `json:"usage,omitempty"`
}

type chatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Role    *string `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

type completionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   completionUsage    `json:"usage"`
}

type completionChoice struct {
	Text         string `json:"text"`
	Index        int    `json:"index"`
	Logprobs     any    `json:"logprobs"`
	FinishReason string `json:"finish_reason"`
}

type completionChunk struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Model   string                  `json:"model"`
	Choices []completionChunkChoice `json:"choices"`
}

type completionChunkChoice struct {
	Text         string  `json:"text"`
	Index        int     `json:"index"`
	Logprobs     any     `json:"logprobs"`
	FinishReason *string `json:"finish_reason"`
}

type embeddingsResponse struct {
	Object string            `json:"object"`
	Data   []embeddingResult `json:"data"`
	Model  string            `json:"model"`
	Usage  embeddingUsage    `json:"usage"`
}

type embeddingResult struct {
	Object    string `json:"object"`
	Embedding any    `json:"embedding"`
	Index     int    `json:"index"`
}

type embeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    any    `json:"code"`
}

type apiErrorResponse struct {
	Error apiError `json:"error"`
}

func main() {
	server := &http.Server{
		Addr:              ":8000",
		Handler:           newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func newRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/v1/models/", handleModel)
	mux.HandleFunc("/v1/responses", handleResponses)
	mux.HandleFunc("/v1/chat/completions", handleChatCompletions)
	mux.HandleFunc("/v1/completions", handleCompletions)
	mux.HandleFunc("/v1/embeddings", handleEmbeddings)
	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, modelsResponse{
		Object: "list",
		Data:   []modelInfo{newModelInfo(defaultModel)},
	})
}

func handleModel(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	model := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	if model == "" {
		writeAPIError(w, http.StatusNotFound, "model not found", "")
		return
	}
	writeJSON(w, http.StatusOK, newModelInfo(model))
}

func handleResponses(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	tuning, release, ok := startBenchmark(w, r)
	if !ok {
		return
	}
	defer release()

	var request responsesRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "")
		return
	}
	if request.Model == "" {
		writeAPIError(w, http.StatusBadRequest, "model is required", "model")
		return
	}
	chunks, err := outputChunks(tuning)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	response := newResponsesResponse(request.Model, strings.Join(chunks, ""))
	if request.Stream {
		streamResponses(r.Context(), w, response, chunks, tuning)
		return
	}
	if !waitFor(r.Context(), tuning.TTFT, tuning.TTFTJitter) {
		return
	}
	setResponsesCompleted(&response)
	writeJSON(w, http.StatusOK, response)
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	tuning, release, ok := startBenchmark(w, r)
	if !ok {
		return
	}
	defer release()

	var request chatCompletionsRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "")
		return
	}
	if request.Model == "" {
		writeAPIError(w, http.StatusBadRequest, "model is required", "model")
		return
	}
	chunks, err := outputChunks(tuning)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	response := newChatCompletionResponse(request.Model, strings.Join(chunks, ""))
	if request.Stream {
		includeUsage := request.StreamOptions != nil && request.StreamOptions.IncludeUsage
		streamChatCompletion(r.Context(), w, response, chunks, tuning, includeUsage)
		return
	}
	if !waitFor(r.Context(), tuning.TTFT, tuning.TTFTJitter) {
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func handleCompletions(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	tuning, release, ok := startBenchmark(w, r)
	if !ok {
		return
	}
	defer release()

	var request completionsRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "")
		return
	}
	if request.Model == "" {
		writeAPIError(w, http.StatusBadRequest, "model is required", "model")
		return
	}
	chunks, err := outputChunks(tuning)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	response := newCompletionResponse(request.Model, strings.Join(chunks, ""))
	if request.Stream {
		streamCompletion(r.Context(), w, response, chunks, tuning)
		return
	}
	if !waitFor(r.Context(), tuning.TTFT, tuning.TTFTJitter) {
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	tuning, release, ok := startBenchmark(w, r)
	if !ok {
		return
	}
	defer release()

	var request embeddingsRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "")
		return
	}
	if request.Model == "" {
		writeAPIError(w, http.StatusBadRequest, "model is required", "model")
		return
	}
	inputs, err := embeddingInputs(request.Input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "input")
		return
	}
	if request.EncodingFormat == "" {
		request.EncodingFormat = "float"
	}
	if request.EncodingFormat != "float" && request.EncodingFormat != "base64" {
		writeAPIError(w, http.StatusBadRequest, "encoding_format must be float or base64", "encoding_format")
		return
	}
	if !waitFor(r.Context(), tuning.TTFT, tuning.TTFTJitter) {
		return
	}

	data := make([]embeddingResult, len(inputs))
	for index := range inputs {
		vector := embeddingVector(index)
		var embedding any = vector
		if request.EncodingFormat == "base64" {
			embedding = base64Embedding(vector)
		}
		data[index] = embeddingResult{
			Object:    "embedding",
			Embedding: embedding,
			Index:     index,
		}
	}

	tokens := countTokens(strings.Join(inputs, " "))
	writeJSON(w, http.StatusOK, embeddingsResponse{
		Object: "list",
		Data:   data,
		Model:  request.Model,
		Usage: embeddingUsage{
			PromptTokens: tokens,
			TotalTokens:  tokens,
		},
	})
}

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	w.Header().Set("Allow", http.MethodPost)
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "")
	return false
}

func requireGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "")
	return false
}

func startBenchmark(w http.ResponseWriter, r *http.Request) (benchmarkTuning, func(), bool) {
	tuning, err := resolveBenchmarkTuning(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "")
		return benchmarkTuning{}, nil, false
	}

	active := activeRequests.Add(1)
	release := func() {
		activeRequests.Add(-1)
	}
	if tuning.MaxConcurrency > 0 && active > int64(tuning.MaxConcurrency) {
		release()
		writeAPIError(w, http.StatusTooManyRequests, "injected concurrency limit reached", "")
		return benchmarkTuning{}, nil, false
	}
	if !waitFor(r.Context(), tuning.QueueDelay, 0) {
		release()
		return benchmarkTuning{}, nil, false
	}
	if tuning.StatusCode != 0 {
		writeAPIErrorWithCode(w, tuning.StatusCode, "injected status response", "", "injected_status")
		release()
		return benchmarkTuning{}, nil, false
	}
	return tuning, release, true
}

func resolveBenchmarkTuning(r *http.Request) (benchmarkTuning, error) {
	tuning := benchmarkTuning{
		Chunk:               defaultChunk,
		OutputChunks:        1,
		StreamErrorAfter:    -1,
		StreamTruncateAfter: -1,
	}
	var err error
	for _, setting := range []struct {
		name  string
		value *time.Duration
	}{
		{name: headerQueueDelay, value: &tuning.QueueDelay},
		{name: headerTTFT, value: &tuning.TTFT},
		{name: headerTTFTJitter, value: &tuning.TTFTJitter},
		{name: headerITL, value: &tuning.ITL},
		{name: headerITLJitter, value: &tuning.ITLJitter},
	} {
		*setting.value, err = durationHeader(r, setting.name)
		if err != nil {
			return benchmarkTuning{}, err
		}
	}

	chunk, hasChunk, err := oneHeader(r, headerChunk)
	if err != nil {
		return benchmarkTuning{}, err
	}
	if hasChunk {
		if chunk == "" {
			return benchmarkTuning{}, fmt.Errorf("%s must not be empty", headerChunk)
		}
		tuning.Chunk = chunk
	}
	if tuning.ChunkBytes, err = integerHeader(r, headerChunkBytes, 0, 0, maxOutputBytes); err != nil {
		return benchmarkTuning{}, err
	}
	if tuning.ChunkBytes > 0 && hasChunk {
		return benchmarkTuning{}, fmt.Errorf("%s and %s cannot be combined", headerChunk, headerChunkBytes)
	}
	if tuning.OutputChunks, err = integerHeader(r, headerOutputChunks, 1, 1, maxOutputChunks); err != nil {
		return benchmarkTuning{}, err
	}
	if tuning.StatusCode, err = integerHeader(r, headerStatusCode, 0, 0, 599); err != nil {
		return benchmarkTuning{}, err
	}
	if tuning.StatusCode != 0 && tuning.StatusCode < http.StatusBadRequest {
		return benchmarkTuning{}, fmt.Errorf("%s must be an HTTP error status", headerStatusCode)
	}
	if tuning.StreamErrorAfter, err = integerHeader(r, headerStreamErrorAfter, -1, -1, tuning.OutputChunks); err != nil {
		return benchmarkTuning{}, err
	}
	if tuning.StreamTruncateAfter, err = integerHeader(r, headerStreamTruncateAfter, -1, -1, tuning.OutputChunks); err != nil {
		return benchmarkTuning{}, err
	}
	if tuning.StreamErrorAfter >= 0 && tuning.StreamTruncateAfter >= 0 {
		return benchmarkTuning{}, fmt.Errorf("%s and %s cannot be combined", headerStreamErrorAfter, headerStreamTruncateAfter)
	}
	if tuning.MaxConcurrency, err = integerHeader(r, headerMaxConcurrency, 0, 0, maxConcurrencyLimit); err != nil {
		return benchmarkTuning{}, err
	}

	chunkBytes := len(tuning.Chunk)
	if tuning.ChunkBytes > 0 {
		chunkBytes = tuning.ChunkBytes
	}
	if int64(chunkBytes)*int64(tuning.OutputChunks) > maxOutputBytes {
		return benchmarkTuning{}, fmt.Errorf("configured output exceeds %d bytes", maxOutputBytes)
	}
	return tuning, nil
}

func oneHeader(r *http.Request, name string) (string, bool, error) {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", false, fmt.Errorf("%s must have one value", name)
	}
	return values[0], true, nil
}

func durationHeader(r *http.Request, name string) (time.Duration, error) {
	value, present, err := oneHeader(r, name)
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds < 0 || milliseconds > maxControlMilliseconds {
		return 0, fmt.Errorf("%s must be an integer from 0 to %d milliseconds", name, maxControlMilliseconds)
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func integerHeader(r *http.Request, name string, defaultValue, minimum, maximum int) (int, error) {
	value, present, err := oneHeader(r, name)
	if err != nil {
		return 0, err
	}
	if !present {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum)
	}
	return parsed, nil
}

func outputChunks(tuning benchmarkTuning) ([]string, error) {
	chunks := make([]string, tuning.OutputChunks)
	for index := range chunks {
		chunk := tuning.Chunk
		if tuning.ChunkBytes > 0 {
			chunk = randomText(tuning.ChunkBytes)
		}
		chunks[index] = chunk
	}
	return chunks, nil
}

func randomText(size int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bytes := make([]byte, size)
	randomSourceMu.Lock()
	defer randomSourceMu.Unlock()
	for index := range bytes {
		bytes[index] = alphabet[randomSource.Intn(len(alphabet))]
	}
	return string(bytes)
}

func waitFor(ctx context.Context, delay, jitter time.Duration) bool {
	delay += randomDuration(jitter)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func randomDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	randomSourceMu.Lock()
	defer randomSourceMu.Unlock()
	return time.Duration(randomSource.Int63n(int64(max) + 1))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("response JSON write failed: %v", err)
	}
}

func writeAPIError(w http.ResponseWriter, status int, message, param string) {
	writeAPIErrorWithCode(w, status, message, param, "")
}

func writeAPIErrorWithCode(w http.ResponseWriter, status int, message, param, code string) {
	var parameter any
	if param != "" {
		parameter = param
	}
	var errorCode any
	if code != "" {
		errorCode = code
	}
	writeJSON(w, status, apiErrorResponse{Error: apiError{
		Message: message,
		Type:    apiErrorType(status),
		Param:   parameter,
		Code:    errorCode,
	}})
}

func apiErrorType(status int) string {
	if status == http.StatusTooManyRequests {
		return "rate_limit_error"
	}
	if status >= http.StatusInternalServerError {
		return "server_error"
	}
	return "invalid_request_error"
}

func embeddingInputs(input json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("input is required")
	}

	var one string
	if err := json.Unmarshal(trimmed, &one); err == nil {
		return validateEmbeddingInputs([]string{one})
	}

	var many []string
	if err := json.Unmarshal(trimmed, &many); err != nil {
		return nil, errors.New("input must be a string or array of strings")
	}
	return validateEmbeddingInputs(many)
}

func validateEmbeddingInputs(inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return nil, errors.New("input must include at least one string")
	}
	if len(inputs) > maxEmbeddingItems {
		return nil, fmt.Errorf("input supports at most %d strings", maxEmbeddingItems)
	}
	for _, input := range inputs {
		if input == "" {
			return nil, errors.New("input strings must not be empty")
		}
	}
	return inputs, nil
}

func newResponsesResponse(model, text string) responsesResponse {
	sequence := responseSequence.Add(1)
	now := time.Now().Unix()
	outputTokens := countTokens(text)
	return responsesResponse{
		ID:                fmt.Sprintf("resp_%d", sequence),
		Object:            "response",
		CreatedAt:         now,
		Status:            "completed",
		Model:             model,
		ParallelToolCalls: true,
		ToolChoice:        "auto",
		Tools:             []any{},
		Output: []responseOutputItem{{
			ID:     fmt.Sprintf("msg_%d", sequence),
			Type:   "message",
			Status: "completed",
			Role:   "assistant",
			Content: []responseOutputText{{
				Type:        "output_text",
				Text:        text,
				Annotations: []any{},
			}},
		}},
		Usage: &responsesUsage{
			InputTokens:        0,
			InputTokensDetails: responseInputTokenDetails{CachedTokens: 0},
			OutputTokens:       outputTokens,
			OutputTokensDetail: responseOutputTokenDetail{ReasoningTokens: 0},
			TotalTokens:        outputTokens,
		},
	}
}

func setResponsesCompleted(response *responsesResponse) {
	completedAt := time.Now().Unix()
	response.CompletedAt = &completedAt
}

func newChatCompletionResponse(model, text string) chatCompletionResponse {
	sequence := responseSequence.Add(1)
	outputTokens := countTokens(text)
	return chatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl_%d", sequence),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []chatChoice{{
			Index: 0,
			Message: chatMessage{
				Role:    "assistant",
				Content: text,
			},
			FinishReason: "stop",
		}},
		Usage: completionUsage{
			PromptTokens:     0,
			CompletionTokens: outputTokens,
			TotalTokens:      outputTokens,
		},
	}
}

func newCompletionResponse(model, text string) completionResponse {
	sequence := responseSequence.Add(1)
	outputTokens := countTokens(text)
	return completionResponse{
		ID:      fmt.Sprintf("cmpl_%d", sequence),
		Object:  "text_completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []completionChoice{{
			Text:         text,
			Index:        0,
			Logprobs:     nil,
			FinishReason: "stop",
		}},
		Usage: completionUsage{
			PromptTokens:     0,
			CompletionTokens: outputTokens,
			TotalTokens:      outputTokens,
		},
	}
}

func newModelInfo(model string) modelInfo {
	return modelInfo{
		ID:      model,
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: "nvidia",
	}
}

func streamResponses(ctx context.Context, w http.ResponseWriter, response responsesResponse, chunks []string, tuning benchmarkTuning) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	if !waitFor(ctx, tuning.TTFT, tuning.TTFTJitter) {
		return
	}

	inProgress := response
	inProgress.Status = "in_progress"
	inProgress.CompletedAt = nil
	inProgress.Output = []responseOutputItem{}
	inProgress.Usage = nil

	item := response.Output[0]
	itemInProgress := item
	itemInProgress.Status = "in_progress"
	itemInProgress.Content = []responseOutputText{}
	part := item.Content[0]
	emptyPart := part
	emptyPart.Text = ""

	events := []struct {
		name string
		data any
	}{
		{"response.created", map[string]any{"type": "response.created", "sequence_number": 0, "response": inProgress}},
		{"response.in_progress", map[string]any{"type": "response.in_progress", "sequence_number": 1, "response": inProgress}},
		{"response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": 2, "output_index": 0, "item": itemInProgress}},
		{"response.content_part.added", map[string]any{"type": "response.content_part.added", "sequence_number": 3, "item_id": item.ID, "output_index": 0, "content_index": 0, "part": emptyPart}},
	}
	for _, event := range events {
		if err := writeSSEJSON(w, event.name, event.data); err != nil {
			log.Printf("Responses event write failed: %v", err)
			return
		}
	}

	if terminate, truncated := streamTermination(tuning, 0); terminate {
		if !truncated {
			writeResponsesStreamError(w, 4)
		}
		return
	}
	for index, chunk := range chunks {
		if index > 0 && !waitFor(ctx, tuning.ITL, tuning.ITLJitter) {
			return
		}
		if err := writeSSEJSON(w, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "sequence_number": 4 + index,
			"item_id": item.ID, "output_index": 0, "content_index": 0,
			"delta": chunk, "logprobs": []any{},
		}); err != nil {
			log.Printf("Responses event write failed: %v", err)
			return
		}
		if terminate, truncated := streamTermination(tuning, index+1); terminate {
			if !truncated {
				writeResponsesStreamError(w, 5+index)
			}
			return
		}
	}

	setResponsesCompleted(&response)
	sequence := 4 + len(chunks)
	events = []struct {
		name string
		data any
	}{
		{"response.output_text.done", map[string]any{"type": "response.output_text.done", "sequence_number": sequence, "item_id": item.ID, "output_index": 0, "content_index": 0, "text": part.Text, "logprobs": []any{}}},
		{"response.content_part.done", map[string]any{"type": "response.content_part.done", "sequence_number": sequence + 1, "item_id": item.ID, "output_index": 0, "content_index": 0, "part": part}},
		{"response.output_item.done", map[string]any{"type": "response.output_item.done", "sequence_number": sequence + 2, "output_index": 0, "item": item}},
		{"response.completed", map[string]any{"type": "response.completed", "sequence_number": sequence + 3, "response": response}},
	}
	for _, event := range events {
		if err := writeSSEJSON(w, event.name, event.data); err != nil {
			log.Printf("Responses event write failed: %v", err)
			return
		}
	}
}

func streamChatCompletion(ctx context.Context, w http.ResponseWriter, response chatCompletionResponse, chunks []string, tuning benchmarkTuning, includeUsage bool) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	if !waitFor(ctx, tuning.TTFT, tuning.TTFTJitter) {
		return
	}

	role := "assistant"
	if err := writeSSEJSON(w, "", chatCompletionChunk{
		ID:      response.ID,
		Object:  "chat.completion.chunk",
		Created: response.Created,
		Model:   response.Model,
		Choices: []chatChunkChoice{{Index: 0, Delta: chatDelta{Role: &role}}},
	}); err != nil {
		log.Printf("Chat role event write failed: %v", err)
		return
	}

	if terminate, truncated := streamTermination(tuning, 0); terminate {
		if !truncated {
			writeLegacyStreamError(w)
		}
		return
	}
	for index, text := range chunks {
		if index > 0 && !waitFor(ctx, tuning.ITL, tuning.ITLJitter) {
			return
		}
		if err := writeSSEJSON(w, "", chatCompletionChunk{
			ID:      response.ID,
			Object:  "chat.completion.chunk",
			Created: response.Created,
			Model:   response.Model,
			Choices: []chatChunkChoice{{Index: 0, Delta: chatDelta{Content: &text}}},
		}); err != nil {
			log.Printf("Chat content event write failed: %v", err)
			return
		}
		if terminate, truncated := streamTermination(tuning, index+1); terminate {
			if !truncated {
				writeLegacyStreamError(w)
			}
			return
		}
	}

	stop := "stop"
	if err := writeSSEJSON(w, "", chatCompletionChunk{
		ID:      response.ID,
		Object:  "chat.completion.chunk",
		Created: response.Created,
		Model:   response.Model,
		Choices: []chatChunkChoice{{Index: 0, Delta: chatDelta{}, FinishReason: &stop}},
	}); err != nil {
		log.Printf("Chat stop event write failed: %v", err)
		return
	}

	if includeUsage {
		usage := response.Usage
		if err := writeSSEJSON(w, "", chatCompletionChunk{
			ID:      response.ID,
			Object:  "chat.completion.chunk",
			Created: response.Created,
			Model:   response.Model,
			Choices: []chatChunkChoice{},
			Usage:   &usage,
		}); err != nil {
			log.Printf("Chat usage event write failed: %v", err)
			return
		}
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		log.Printf("Chat completion event write failed: %v", err)
		return
	}
	flush(w)
}

func streamCompletion(ctx context.Context, w http.ResponseWriter, response completionResponse, chunks []string, tuning benchmarkTuning) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	if !waitFor(ctx, tuning.TTFT, tuning.TTFTJitter) {
		return
	}

	if terminate, truncated := streamTermination(tuning, 0); terminate {
		if !truncated {
			writeLegacyStreamError(w)
		}
		return
	}
	for index, text := range chunks {
		if index > 0 && !waitFor(ctx, tuning.ITL, tuning.ITLJitter) {
			return
		}
		if err := writeSSEJSON(w, "", completionChunk{
			ID:      response.ID,
			Object:  "text_completion",
			Created: response.Created,
			Model:   response.Model,
			Choices: []completionChunkChoice{{Text: text, Index: 0, Logprobs: nil}},
		}); err != nil {
			log.Printf("Completion event write failed: %v", err)
			return
		}
		if terminate, truncated := streamTermination(tuning, index+1); terminate {
			if !truncated {
				writeLegacyStreamError(w)
			}
			return
		}
	}

	stop := "stop"
	if err := writeSSEJSON(w, "", completionChunk{
		ID:      response.ID,
		Object:  "text_completion",
		Created: response.Created,
		Model:   response.Model,
		Choices: []completionChunkChoice{{Text: "", Index: 0, Logprobs: nil, FinishReason: &stop}},
	}); err != nil {
		log.Printf("Completion stop event write failed: %v", err)
		return
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		log.Printf("Completion event write failed: %v", err)
		return
	}
	flush(w)
}

func streamTermination(tuning benchmarkTuning, emitted int) (bool, bool) {
	if tuning.StreamErrorAfter >= 0 && emitted >= tuning.StreamErrorAfter {
		return true, false
	}
	if tuning.StreamTruncateAfter >= 0 && emitted >= tuning.StreamTruncateAfter {
		return true, true
	}
	return false, false
}

func writeResponsesStreamError(w http.ResponseWriter, sequence int) {
	if err := writeSSEJSON(w, "error", map[string]any{
		"type":            "error",
		"sequence_number": sequence,
		"error": apiError{
			Message: "injected streaming error",
			Type:    "server_error",
			Param:   nil,
			Code:    "injected_stream_error",
		},
	}); err != nil {
		log.Printf("Responses error event write failed: %v", err)
	}
}

func writeLegacyStreamError(w http.ResponseWriter) {
	if err := writeSSEJSON(w, "", apiErrorResponse{Error: apiError{
		Message: "injected streaming error",
		Type:    "server_error",
		Param:   nil,
		Code:    "injected_stream_error",
	}}); err != nil {
		log.Printf("Streaming error event write failed: %v", err)
	}
}

func writeSSEJSON(w http.ResponseWriter, name string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if name != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", name); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	flush(w)
	return nil
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func embeddingVector(index int) []float64 {
	value := float64(index)
	return []float64{value, value + 0.125, -value - 0.25}
}

func base64Embedding(vector []float64) string {
	bytes := make([]byte, 4*len(vector))
	for index, value := range vector {
		binary.LittleEndian.PutUint32(bytes[index*4:], math.Float32bits(float32(value)))
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func countTokens(text string) int {
	return len(strings.Fields(text))
}
