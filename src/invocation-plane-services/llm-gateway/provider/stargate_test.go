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

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/servicetier"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
)

func TestStargateProviderCompleteForwardsRenderedPromptAndRoutingHeaders(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "upstream-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("ignored once rendered"),
				},
			},
			Tools: &[]models.ChatTool{
				{
					Type: models.ToolTypeFunction,
					Function: models.ChatFunctionSpec{
						Name: "lookup_weather",
					},
				},
			},
		},
		RenderedPrompt: ptr.To("rendered prompt that is longer than the original payload"),
		InputTokens:    3,
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:    "req-123",
		BearerToken:  "secret-token",
		RoutingKey:   "fn-abc",
		Model:        "upstream-model",
		TargetRegion: "us-west1",
	}

	wantEstimate := routingTokenEstimate(request)

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, stargateChatCompletionsPath, r.URL.Path)
		require.Equal(t, contentTypeJSON, r.Header.Get(headerContentType))
		require.Equal(t, contentTypeJSON, r.Header.Get(headerAccept))
		require.Equal(t, "Bearer secret-token", r.Header.Get(headerAuthorization))
		require.Equal(t, "req-123", r.Header.Get(headerRequestID))
		require.Equal(t, "us-west1", r.Header.Get(headerTargetRegion))
		require.Equal(t, "fn-abc", r.Header.Get(headerRoutingKey))
		require.Equal(t, "upstream-model", r.Header.Get(headerModel))
		require.Equal(t, fmt.Sprintf("%d", wantEstimate), r.Header.Get(headerInputTokens))
		require.Equal(t, fmt.Sprintf("%d", wantEstimate), r.Header.Get(headerTokenEstimate))

		var payload models.ChatCompletionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.NotNil(t, payload.Stream)
		require.False(t, ptr.Deref(payload.Stream))
		require.NotNil(t, payload.Messages)
		require.Len(t, *payload.Messages, 1)
		require.Equal(t, models.ChatCompletionRoleUser, (*payload.Messages)[0].Role)
		text, err := (*payload.Messages)[0].Content.TrySingleText()
		require.NoError(t, err)
		require.Equal(t, ptr.Deref(request.RenderedPrompt), text)
		require.Nil(t, payload.Tools)
		require.Nil(t, payload.Functions)

		responseBody, err := json.Marshal(&models.ChatCompletionResponse{
			ID:          "chatcmpl-test",
			Object:      models.ObjectChatCompletion,
			CreatedAt:   123,
			Model:       "upstream-model",
			ServiceTier: servicetier.Auto,
			Choices: []models.ChatCompletionChoice{
				{
					Index: 0,
					Message: models.ChatCompletionMessage{
						Role:    models.ChatCompletionRoleAssistant,
						Content: ptr.To("hello from stargate"),
					},
					FinishReason: models.FinishReasonStop,
				},
			},
		})
		require.NoError(t, err)

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeJSON},
			},
			Body:    io.NopCloser(strings.NewReader(string(responseBody))),
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(context.Background(), reqCtx, request)
	require.NoError(t, err)
	require.Equal(t, "chatcmpl-test", response.ID)
	require.Equal(t, "upstream-model", response.Model)
	require.Len(t, response.Choices, 1)
	require.Equal(t, "hello from stargate", ptr.Deref(response.Choices[0].Message.Content))
}

func TestStargateProviderStreamParsesSSE(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "stream-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("hello"),
				},
			},
		},
		InputTokens: 11,
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-stream",
		RoutingKey: "fn-stream",
		Model:      "stream-model",
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		require.Equal(t, contentTypeSSE, r.Header.Get(headerAccept))
		require.Equal(t, "fn-stream", r.Header.Get(headerRoutingKey))
		require.Equal(t, "11", r.Header.Get(headerInputTokens))
		require.Equal(t, "11", r.Header.Get(headerTokenEstimate))

		var payload models.ChatCompletionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.NotNil(t, payload.Stream)
		require.True(t, ptr.Deref(payload.Stream))

		chunks := []models.ChatCompletionChunk{
			{
				ID:          "chatcmpl-stream",
				Object:      models.ObjectChatCompletionChunk,
				CreatedAt:   123,
				Model:       "stream-model",
				ServiceTier: servicetier.Auto,
				Choices: []models.ChatCompletionChunkChoice{
					{
						Index: 0,
						Delta: models.ChatCompletionChunkDelta{
							Role:    ptr.To(models.ChatCompletionRoleAssistant),
							Content: ptr.To("hello "),
						},
					},
				},
			},
			{
				ID:          "chatcmpl-stream",
				Object:      models.ObjectChatCompletionChunk,
				CreatedAt:   123,
				Model:       "stream-model",
				ServiceTier: servicetier.Auto,
				Choices: []models.ChatCompletionChunkChoice{
					{
						Index: 0,
						Delta: models.ChatCompletionChunkDelta{
							Content: ptr.To("world"),
						},
						FinishReason: ptr.To(models.FinishReasonStop),
					},
				},
			},
		}

		var body strings.Builder
		for _, chunk := range chunks {
			payload, err := json.Marshal(chunk)
			require.NoError(t, err)
			_, err = fmt.Fprintf(&body, "data: %s\n\n", payload)
			require.NoError(t, err)
		}

		_, err := fmt.Fprint(&body, "data: [DONE]\n\n")
		require.NoError(t, err)

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeSSE},
			},
			Body:    io.NopCloser(strings.NewReader(body.String())),
			Request: r,
		}, nil
	})}

	stream, err := provider.Stream(context.Background(), reqCtx, request)
	require.NoError(t, err)

	var chunks []*models.ChatCompletionChunk
	for event := range stream {
		require.NoError(t, event.Err)
		require.NotNil(t, event.Chunk)
		chunks = append(chunks, event.Chunk)
	}

	require.Len(t, chunks, 2)
	require.Equal(t, "hello ", ptr.Deref(chunks[0].Choices[0].Delta.Content))
	require.Equal(t, "world", ptr.Deref(chunks[1].Choices[0].Delta.Content))
	require.Equal(t, models.FinishReasonStop, ptr.Deref(chunks[1].Choices[0].FinishReason))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		req.URL = &url.URL{}
	}
	return f(req)
}
