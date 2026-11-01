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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
)

const (
	messagesStopEndTurn   = "end_turn"
	messagesStopMaxTokens = "max_tokens"
)

type messagesRequest struct {
	Model     string          `json:"model"`
	MaxTokens json.RawMessage `json:"max_tokens"`
	Messages  json.RawMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	benchmarkBodyControls
}

type messagesResponse struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Role         string              `json:"role"`
	Content      []messagesTextBlock `json:"content"`
	Model        string              `json:"model"`
	StopReason   *string             `json:"stop_reason"`
	StopSequence *string             `json:"stop_sequence"`
	Usage        messagesUsage       `json:"usage"`
}

type messagesTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type messagesUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type messagesError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type messagesErrorResponse struct {
	Type      string        `json:"type"`
	Error     messagesError `json:"error"`
	RequestID string        `json:"request_id,omitempty"`
}

func handleMessages(w http.ResponseWriter, r *http.Request, config serverConfig) {
	requestID := fmt.Sprintf("req_%d", responseSequence.Add(1))
	w.Header().Set("request-id", requestID)
	writeError := func(w http.ResponseWriter, status int, message, _ string) {
		writeMessagesError(w, status, message, requestID)
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	var request messagesRequest
	if err := decodeJSON(w, r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large", "")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON request body", "")
		return
	}
	tuning, release, ok := startBenchmarkWithErrors(w, r, config, request.benchmarkBodyControls, writeError)
	if !ok {
		return
	}
	defer release()
	maxTokens, err := validateMessagesRequest(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	// Each output chunk counts as one output token, so max_tokens caps the
	// configured chunk count and reports the matching stop reason.
	stopReason := messagesStopEndTurn
	if maxTokens < int64(tuning.OutputChunks) {
		tuning.OutputChunks = int(maxTokens)
		stopReason = messagesStopMaxTokens
	}
	message := newMessagesResponse(request.Model)
	if request.Stream {
		streamMessages(r.Context(), w, message, tuning, stopReason)
		return
	}
	setMessagesOutput(&message, outputChunks(tuning), stopReason)
	if !waitFor(r.Context(), tuning.TTFT, tuning.TTFTJitter) {
		return
	}
	writeJSON(w, http.StatusOK, message)
}

func validateMessagesRequest(request messagesRequest) (int64, error) {
	if request.Model == "" {
		return 0, errors.New("model is required")
	}
	if len(request.MaxTokens) == 0 || string(request.MaxTokens) == "null" {
		return 0, errors.New("max_tokens is required")
	}
	var maxTokens int64
	if err := json.Unmarshal(request.MaxTokens, &maxTokens); err != nil || maxTokens < 0 {
		return 0, errors.New("max_tokens must be a non-negative integer")
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(request.Messages, &messages); err != nil || len(messages) == 0 {
		return 0, errors.New("messages must be a non-empty array")
	}
	return maxTokens, nil
}

func newMessagesResponse(model string) messagesResponse {
	return messagesResponse{
		ID:      fmt.Sprintf("msg_%d", responseSequence.Add(1)),
		Type:    "message",
		Role:    "assistant",
		Content: []messagesTextBlock{},
		Model:   model,
	}
}

func setMessagesOutput(message *messagesResponse, chunks []string, stopReason string) {
	if len(chunks) > 0 {
		message.Content = []messagesTextBlock{{Type: "text", Text: strings.Join(chunks, "")}}
	}
	message.StopReason = &stopReason
	message.Usage.OutputTokens = len(chunks)
}

func streamMessages(ctx context.Context, w http.ResponseWriter, message messagesResponse, tuning benchmarkTuning, stopReason string) {
	setSSEHeaders(w)
	if !waitFor(ctx, tuning.TTFT, tuning.TTFTJitter) {
		return
	}

	if err := writeSSEJSON(w, "message_start", map[string]any{"type": "message_start", "message": message}); err != nil {
		log.Printf("Messages event write failed: %v", err)
		return
	}
	if tuning.OutputChunks > 0 {
		if err := writeSSEJSON(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": messagesTextBlock{Type: "text", Text: ""}}); err != nil {
			log.Printf("Messages event write failed: %v", err)
			return
		}
	}
	if err := writeSSEJSON(w, "ping", map[string]any{"type": "ping"}); err != nil {
		log.Printf("Messages event write failed: %v", err)
		return
	}
	if messagesStreamStopped(w, tuning, 0) {
		return
	}

	if tuning.OutputChunks > 0 {
		fixedFrame, err := messagesTextDeltaFrame(tuning.Chunk)
		if err != nil {
			log.Printf("Messages event encode failed: %v", err)
			return
		}
		var pacer streamPacer
		defer pacer.stop()
		for index := 0; index < tuning.OutputChunks; index++ {
			if index > 0 && !pacer.wait(ctx, tuning.ITL, tuning.ITLJitter) {
				return
			}
			frame := fixedFrame
			if tuning.ChunkBytes > 0 {
				if frame, err = messagesTextDeltaFrame(randomText(tuning.ChunkBytes)); err != nil {
					log.Printf("Messages event encode failed: %v", err)
					return
				}
			}
			if err := writeSSEFrame(w, frame); err != nil {
				log.Printf("Messages event write failed: %v", err)
				return
			}
			if messagesStreamStopped(w, tuning, index+1) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err := writeSSEJSON(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}); err != nil {
			log.Printf("Messages event write failed: %v", err)
			return
		}
	}

	usage := messagesUsage{OutputTokens: tuning.OutputChunks}
	events := []struct {
		name string
		data any
	}{
		{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": usage}},
		{"message_stop", map[string]any{"type": "message_stop"}},
	}
	for _, event := range events {
		if err := writeSSEJSON(w, event.name, event.data); err != nil {
			log.Printf("Messages event write failed: %v", err)
			return
		}
	}
}

func messagesTextDeltaFrame(chunk string) ([]byte, error) {
	chunkJSON, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 0, len(chunkJSON)+128)
	frame = append(frame, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":"...)
	frame = append(frame, chunkJSON...)
	frame = append(frame, "}}\n\n"...)
	return frame, nil
}

// messagesStreamStopped applies the stream error and truncate controls. An
// injected error uses the Messages error event; a truncated stream closes
// without content_block_stop, message_delta, or message_stop.
func messagesStreamStopped(w http.ResponseWriter, tuning benchmarkTuning, emitted int) bool {
	terminate, truncated := streamTermination(tuning, emitted)
	if terminate && !truncated {
		if err := writeSSEJSON(w, "error", messagesErrorResponse{
			Type:  "error",
			Error: messagesError{Type: "api_error", Message: "injected streaming error"},
		}); err != nil {
			log.Printf("Messages error event write failed: %v", err)
		}
	}
	return terminate
}

func writeMessagesError(w http.ResponseWriter, status int, message, requestID string) {
	writeJSON(w, status, messagesErrorResponse{
		Type:      "error",
		Error:     messagesError{Type: messagesErrorType(status), Message: message},
		RequestID: requestID,
	})
}

func messagesErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	}
	if status >= http.StatusInternalServerError {
		return "api_error"
	}
	return "invalid_request_error"
}
