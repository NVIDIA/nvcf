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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	openairesponses "github.com/NVIDIA/nvcf/llm-api-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/provider"
	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
)

func TestCreateResponseDelegatesUnaryThroughChatHandler(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	handlers := NewHandlers(
		cfg,
		&stubResponsesProvider{
			completeResponse: &models.ChatCompletionResponse{
				ID:        "chatcmpl-123",
				Object:    models.ObjectChatCompletion,
				CreatedAt: 123,
				Model:     "gateway-model",
				Choices: []models.ChatCompletionChoice{
					{
						Index: 0,
						Message: models.ChatCompletionMessage{
							Role:    models.ChatCompletionRoleAssistant,
							Content: ptr.To("hello from gateway"),
						},
						FinishReason: models.FinishReasonStop,
					},
				},
				Usage: models.ChatCompletionUsage{
					PromptTokens:     5,
					CompletionTokens: 3,
					TotalTokens:      8,
				},
			},
		},
		nil,
		nil,
	)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello"}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"object":"response"`) {
		t.Fatalf("response body missing responses object: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model":"fn-chat/company-name/model-name"`) {
		t.Fatalf("response body missing model: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hello from gateway"`) {
		t.Fatalf("response body missing assistant text: %s", rec.Body.String())
	}
}

func TestCreateResponseDelegatesStreamThroughChatHandler(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	handlers := NewHandlers(
		cfg,
		&stubResponsesProvider{
			streamEvents: []provider.StreamEvent{
				{
					Chunk: &models.ChatCompletionChunk{
						Choices: []models.ChatCompletionChunkChoice{
							{
								Delta: models.ChatCompletionChunkDelta{
									Content: ptr.To("hello "),
								},
							},
						},
					},
				},
				{
					Chunk: &models.ChatCompletionChunk{
						Choices: []models.ChatCompletionChunkChoice{
							{
								Delta: models.ChatCompletionChunkDelta{
									Content: ptr.To("world"),
								},
							},
						},
						Usage: &models.ChatCompletionUsage{
							PromptTokens:     5,
							CompletionTokens: 2,
							TotalTokens:      7,
						},
					},
				},
			},
		},
		nil,
		nil,
	)

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	handlers.AsResponsesHandlers().RegisterRoutes(e.Group(""))

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","input":"hello","stream":true}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: "+openairesponses.EventTypeResponseCreated) {
		t.Fatalf("stream body missing response.created: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: "+openairesponses.EventTypeResponseCompleted) {
		t.Fatalf("stream body missing response.completed: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model":"fn-chat/company-name/model-name"`) {
		t.Fatalf("stream body missing model: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"text":"hello world"`) {
		t.Fatalf("stream body missing completed assistant text: %s", rec.Body.String())
	}
}

type stubResponsesProvider struct {
	completeResponse *models.ChatCompletionResponse
	streamEvents     []provider.StreamEvent
}

func (s *stubResponsesProvider) Complete(
	_ context.Context,
	_ *requestctx.RequestContext,
	_ *provider.NormalizedRequest,
) (*models.ChatCompletionResponse, error) {
	return s.completeResponse, nil
}

func (s *stubResponsesProvider) Stream(
	_ context.Context,
	_ *requestctx.RequestContext,
	_ *provider.NormalizedRequest,
) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, len(s.streamEvents))
	go func() {
		defer close(ch)
		for _, event := range s.streamEvents {
			ch <- event
		}
	}()
	return ch, nil
}
