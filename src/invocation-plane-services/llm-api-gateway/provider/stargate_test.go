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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/servicetier"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

func TestStargateProviderMetricsIncludeFunctionID(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldMeterProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(meterProvider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldMeterProvider)
		_ = meterProvider.Shutdown(context.Background())
	})

	p := &StargateProvider{
		upstreamRequestsTotal:   telemetry.UpstreamRequestsTotal(),
		upstreamRequestDuration: telemetry.UpstreamRequestDuration(),
	}
	p.recordUpstreamRequest(
		context.Background(),
		&requestctx.RequestContext{RoutingKey: "fn-upstream"},
		time.Now().Add(-time.Millisecond),
		http.StatusOK,
		nil,
	)
	p.recordUpstreamRequest(
		context.Background(),
		nil,
		time.Now().Add(-time.Millisecond),
		http.StatusOK,
		nil,
	)

	var resourceMetrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &resourceMetrics))
	want := map[string]map[string]bool{
		"llm_api_gateway_upstream_requests_total": {
			"fn-upstream": false,
			"none":        false,
		},
		"llm_api_gateway_upstream_request_duration_seconds": {
			"fn-upstream": false,
			"none":        false,
		},
	}
	for _, scope := range resourceMetrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			functionIDs, ok := want[metric.Name]
			if !ok {
				continue
			}
			for functionID := range functionIDs {
				if metricHasFunctionID(metric.Data, functionID) {
					functionIDs[functionID] = true
				}
			}
		}
	}
	for name, functionIDs := range want {
		for functionID, found := range functionIDs {
			if !found {
				t.Fatalf("metric %q missing function_id=%s", name, functionID)
			}
		}
	}
}

func metricHasFunctionID(data metricdata.Aggregation, want string) bool {
	hasFunctionID := func(attrs attribute.Set) bool {
		value, ok := attrs.Value(attribute.Key("function_id"))
		return ok && value.AsString() == want
	}
	switch typed := data.(type) {
	case metricdata.Sum[int64]:
		for _, point := range typed.DataPoints {
			if hasFunctionID(point.Attributes) {
				return true
			}
		}
	case metricdata.Histogram[float64]:
		for _, point := range typed.DataPoints {
			if hasFunctionID(point.Attributes) {
				return true
			}
		}
	}
	return false
}

func TestStargateProviderCompleteForwardsChatPayloadAndRoutingHeaders(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "upstream-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("forward this message"),
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
		InputTokens: 3,
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:        "req-123",
		BearerToken:      "secret-token",
		RoutingKey:       "fn-abc",
		Model:            "upstream-model",
		RoutingMethod:    "experimental_method",
		TargetRegion:     "us-west1",
		CacheAffinityKey: "mt:v1:header:hash",
	}

	wantEstimate := routingTokenEstimate(request)

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, stargateChatCompletionsPath, r.URL.Path)
		require.Equal(t, contentTypeJSON, r.Header.Get(headerContentType))
		require.Equal(t, contentTypeSSE, r.Header.Get(headerAccept))
		require.Equal(t, "Bearer secret-token", r.Header.Get(headerAuthorization))
		require.Equal(t, "req-123", r.Header.Get(headerRequestID))
		require.Equal(t, "us-west1", r.Header.Get(headerTargetRegion))
		require.Equal(t, "us-west1", r.Header.Get(headerLegacyTargetRegion))
		require.Equal(t, "fn-abc", r.Header.Get(headerRoutingKey))
		require.Equal(t, "upstream-model", r.Header.Get(headerModel))
		require.Equal(t, "experimental_method", r.Header.Get(headerRoutingMethod))
		require.Equal(t, "mt:v1:header:hash", r.Header.Get(headerCacheAffinityKey))
		require.Equal(t, fmt.Sprintf("%d", wantEstimate), r.Header.Get(headerInputTokens))
		require.Equal(t, fmt.Sprintf("%d", wantEstimate), r.Header.Get(headerTokenEstimate))

		var payload models.ChatCompletionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.NotNil(t, payload.Stream)
		require.True(t, ptr.Deref(payload.Stream))
		require.NotNil(t, payload.StreamOptions)
		require.NotNil(t, payload.StreamOptions.IncludeUsage)
		require.True(t, ptr.Deref(payload.StreamOptions.IncludeUsage))
		require.NotNil(t, payload.Messages)
		require.Len(t, *payload.Messages, 1)
		require.Equal(t, models.ChatCompletionRoleUser, (*payload.Messages)[0].Role)
		text, err := (*payload.Messages)[0].Content.TrySingleText()
		require.NoError(t, err)
		require.Equal(t, "forward this message", text)
		require.NotNil(t, payload.Tools)
		require.Len(t, *payload.Tools, 1)
		require.Nil(t, payload.Functions)

		responseBody := sseChatBody(t,
			models.ChatCompletionChunk{
				ID:          "chatcmpl-test",
				Object:      models.ObjectChatCompletionChunk,
				CreatedAt:   123,
				Model:       "upstream-model",
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
			models.ChatCompletionChunk{
				ID:          "chatcmpl-test",
				Object:      models.ObjectChatCompletionChunk,
				CreatedAt:   123,
				Model:       "upstream-model",
				ServiceTier: servicetier.Auto,
				Choices: []models.ChatCompletionChunkChoice{
					{
						Index: 0,
						Delta: models.ChatCompletionChunkDelta{
							Content: ptr.To("from stargate"),
						},
						FinishReason: ptr.To(models.FinishReasonStop),
					},
				},
			},
		)

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeSSE},
			},
			Body:    io.NopCloser(strings.NewReader(responseBody)),
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(context.Background(), reqCtx, request)
	require.NoError(t, err)
	require.Equal(t, "chatcmpl-test", response.ID)
	require.Equal(t, models.ObjectChatCompletion, response.Object)
	require.Equal(t, "upstream-model", response.Model)
	require.Len(t, response.Choices, 1)
	require.Equal(t, models.ChatCompletionRoleAssistant, response.Choices[0].Message.Role)
	require.Equal(t, "hello from stargate", ptr.Deref(response.Choices[0].Message.Content))
	require.Equal(t, models.FinishReasonStop, response.Choices[0].FinishReason)
}

func TestStargateProviderCompleteAggregatesUsageFromStream(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "usage-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("usage please"),
				},
			},
		},
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-usage",
		RoutingKey: "fn-usage",
		Model:      "usage-model",
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeSSE},
			},
			Body: io.NopCloser(strings.NewReader(sseChatBody(t,
				models.ChatCompletionChunk{
					ID:        "chatcmpl-usage",
					Object:    models.ObjectChatCompletionChunk,
					CreatedAt: 123,
					Model:     "usage-model",
					Choices: []models.ChatCompletionChunkChoice{
						{
							Index: 0,
							Delta: models.ChatCompletionChunkDelta{
								Role:    ptr.To(models.ChatCompletionRoleAssistant),
								Content: ptr.To("done"),
							},
							FinishReason: ptr.To(models.FinishReasonStop),
						},
					},
				},
				models.ChatCompletionChunk{
					ID:        "chatcmpl-usage",
					Object:    models.ObjectChatCompletionChunk,
					CreatedAt: 123,
					Model:     "usage-model",
					Usage: &models.ChatCompletionUsage{
						PromptTokens:     2,
						CompletionTokens: 3,
						TotalTokens:      5,
					},
				},
			))),
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(context.Background(), reqCtx, request)
	require.NoError(t, err)
	require.Equal(t, uint32(2), response.Usage.PromptTokens)
	require.Equal(t, uint32(3), response.Usage.CompletionTokens)
	require.Equal(t, uint32(5), response.Usage.TotalTokens)
}

func TestStargateProviderCompleteAggregatesStreamingToolCalls(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "tool-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("call a tool"),
				},
			},
		},
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-tool",
		RoutingKey: "fn-tool",
		Model:      "tool-model",
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeSSE},
			},
			Body: io.NopCloser(strings.NewReader(sseChatBody(t,
				models.ChatCompletionChunk{
					ID:        "chatcmpl-tool",
					Object:    models.ObjectChatCompletionChunk,
					CreatedAt: 123,
					Model:     "tool-model",
					Choices: []models.ChatCompletionChunkChoice{
						{
							Index: 0,
							Delta: models.ChatCompletionChunkDelta{
								Role: ptr.To(models.ChatCompletionRoleAssistant),
								ToolCalls: &[]models.ChatCompletionToolCallChunk{
									{
										ID:    "call-1",
										Type:  models.ToolTypeFunction,
										Index: 0,
										Function: models.ChatCompletionFunctionCall{
											Name:      "lookup_weather",
											Arguments: `{"city":`,
										},
									},
								},
							},
						},
					},
				},
				models.ChatCompletionChunk{
					ID:        "chatcmpl-tool",
					Object:    models.ObjectChatCompletionChunk,
					CreatedAt: 123,
					Model:     "tool-model",
					Choices: []models.ChatCompletionChunkChoice{
						{
							Index: 0,
							Delta: models.ChatCompletionChunkDelta{
								ToolCalls: &[]models.ChatCompletionToolCallChunk{
									{
										Index: 0,
										Function: models.ChatCompletionFunctionCall{
											Arguments: `"Berlin"}`,
										},
									},
								},
							},
							FinishReason: ptr.To(models.FinishReasonToolCalls),
						},
					},
				},
			))),
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(context.Background(), reqCtx, request)
	require.NoError(t, err)
	require.Len(t, response.Choices, 1)
	require.Equal(t, models.FinishReasonToolCalls, response.Choices[0].FinishReason)
	require.Equal(t, models.ChatCompletionRoleAssistant, response.Choices[0].Message.Role)
	require.Nil(t, response.Choices[0].Message.Content)
	require.NotNil(t, response.Choices[0].Message.ToolCalls)
	require.Len(t, *response.Choices[0].Message.ToolCalls, 1)
	toolCall := (*response.Choices[0].Message.ToolCalls)[0]
	require.Equal(t, "call-1", toolCall.ID)
	require.Equal(t, models.ToolTypeFunction, toolCall.Type)
	require.Equal(t, "lookup_weather", toolCall.Function.Name)
	require.Equal(t, `{"city":"Berlin"}`, toolCall.Function.Arguments)
}

func TestStargateProviderCompleteAggregatesMultipleChoicesAndReasoning(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "choice-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("choices"),
				},
			},
		},
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-choice",
		RoutingKey: "fn-choice",
		Model:      "choice-model",
	}
	fingerprint := "fp-first"

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeSSE},
			},
			Body: io.NopCloser(strings.NewReader(sseChatBody(t,
				models.ChatCompletionChunk{
					ID:                "chatcmpl-choice",
					Object:            models.ObjectChatCompletionChunk,
					CreatedAt:         123,
					Model:             "choice-model",
					SystemFingerprint: ptr.To(fingerprint),
					ServiceTier:       servicetier.Auto,
					Choices: []models.ChatCompletionChunkChoice{
						{
							Index: 1,
							Delta: models.ChatCompletionChunkDelta{
								Content: ptr.To("second "),
							},
						},
						{
							Index: 0,
							Delta: models.ChatCompletionChunkDelta{
								Content:   ptr.To("first "),
								Reasoning: ptr.To("think "),
							},
						},
					},
				},
				models.ChatCompletionChunk{
					ID:        "chatcmpl-choice",
					Object:    models.ObjectChatCompletionChunk,
					CreatedAt: 123,
					Model:     "choice-model",
					Choices: []models.ChatCompletionChunkChoice{
						{
							Index: 1,
							Delta: models.ChatCompletionChunkDelta{
								Content: ptr.To("choice"),
							},
							FinishReason: ptr.To(models.FinishReasonLength),
						},
						{
							Index: 0,
							Delta: models.ChatCompletionChunkDelta{
								Content:   ptr.To("choice"),
								Reasoning: ptr.To("done"),
							},
							FinishReason: ptr.To(models.FinishReasonStop),
						},
					},
				},
			))),
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(context.Background(), reqCtx, request)
	require.NoError(t, err)
	require.Equal(t, "chatcmpl-choice", response.ID)
	require.Equal(t, int64(123), response.CreatedAt)
	require.Equal(t, "choice-model", response.Model)
	require.Equal(t, fingerprint, ptr.Deref(response.SystemFingerprint))
	require.Equal(t, servicetier.Auto, response.ServiceTier)
	require.Len(t, response.Choices, 2)
	require.Equal(t, uint32(0), response.Choices[0].Index)
	require.Equal(t, models.ChatCompletionRoleAssistant, response.Choices[0].Message.Role)
	require.Equal(t, "first choice", ptr.Deref(response.Choices[0].Message.Content))
	require.Equal(t, "think done", ptr.Deref(response.Choices[0].Message.Reasoning))
	require.Equal(t, models.FinishReasonStop, response.Choices[0].FinishReason)
	require.Equal(t, uint32(1), response.Choices[1].Index)
	require.Equal(t, "second choice", ptr.Deref(response.Choices[1].Message.Content))
	require.Equal(t, models.FinishReasonLength, response.Choices[1].FinishReason)
}

func TestStargateProviderCompletePropagatesStreamingHTTPError(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "error-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("fail"),
				},
			},
		},
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-error",
		RoutingKey: "fn-error",
		Model:      "error-model",
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		require.Equal(t, contentTypeSSE, r.Header.Get(headerAccept))

		var payload models.ChatCompletionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.NotNil(t, payload.Stream)
		require.True(t, ptr.Deref(payload.Stream))

		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header: http.Header{
				headerContentType: []string{contentTypeJSON},
			},
			Body:    io.NopCloser(strings.NewReader(`{"error":{"message":"stream denied"}}`)),
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(context.Background(), reqCtx, request)
	require.Error(t, err)
	require.Nil(t, response)
	require.Contains(t, err.Error(), "stream denied")
}

func TestStargateProviderCompletePropagatesMalformedStreamChunk(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "malformed-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("malformed"),
				},
			},
		},
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-malformed",
		RoutingKey: "fn-malformed",
		Model:      "malformed-model",
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeSSE},
			},
			Body:    io.NopCloser(strings.NewReader("data: {\n\n")),
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(context.Background(), reqCtx, request)
	require.Error(t, err)
	require.Nil(t, response)
	require.Contains(t, err.Error(), "decode stargate stream chunk")
}

func TestStargateProviderCompletePropagatesStreamContextCancellation(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "cancel-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("cancel"),
				},
			},
		},
	}
	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-cancel",
		RoutingKey: "fn-cancel",
		Model:      "cancel-model",
	}
	ctx, cancel := context.WithCancel(context.Background())

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()
		cancel()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeSSE},
			},
			Body:    contextReadCloser{ctx: r.Context()},
			Request: r,
		}, nil
	})}

	response, err := provider.Complete(ctx, reqCtx, request)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled), "err = %v", err)
	require.Nil(t, response)
}

func TestStargateProviderReadStreamDoesNotBlockOnFullEventChannelAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := make(chan StreamEvent, 1)
	events <- StreamEvent{Chunk: &models.ChatCompletionChunk{}}
	body := io.NopCloser(strings.NewReader(sseChatBody(t, models.ChatCompletionChunk{
		ID:        "chatcmpl-cancel",
		Object:    models.ObjectChatCompletionChunk,
		CreatedAt: 123,
		Model:     "cancel-model",
		Choices: []models.ChatCompletionChunkChoice{
			{
				Index: 0,
				Delta: models.ChatCompletionChunkDelta{
					Content: ptr.To("hello"),
				},
			},
		},
	})))

	done := make(chan struct{})
	go func() {
		defer close(done)
		(&StargateProvider{}).readStream(ctx, func() {}, body, events)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("readStream blocked sending cancellation error to a full events channel")
	}
}

func TestStargateProviderCompletePropagatesTraceContext(t *testing.T) {
	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTextMapPropagator(oldPropagator)
	})

	tracerProvider := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
	})

	var traceparent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
		w.Header().Set(headerContentType, contentTypeSSE)
		_, _ = w.Write([]byte(sseChatBody(t, models.ChatCompletionChunk{
			ID:        "chatcmpl-test",
			Object:    models.ObjectChatCompletionChunk,
			CreatedAt: 123,
			Model:     "upstream-model",
			Choices: []models.ChatCompletionChunkChoice{
				{
					Index: 0,
					Delta: models.ChatCompletionChunkDelta{
						Role:    ptr.To(models.ChatCompletionRoleAssistant),
						Content: ptr.To("ok"),
					},
					FinishReason: ptr.To(models.FinishReasonStop),
				},
			},
		})))
	}))
	t.Cleanup(server.Close)

	provider, err := NewStargateProvider(config.StargateConfig{URL: server.URL})
	require.NoError(t, err)

	ctx, span := tracerProvider.Tracer("test").Start(context.Background(), "test-parent")
	defer span.End()

	_, err = provider.Complete(ctx, &requestctx.RequestContext{RequestID: "req-trace", RoutingKey: "fn-trace"}, &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "upstream-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("hello"),
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, traceparent)
	require.Contains(t, traceparent, span.SpanContext().TraceID().String())
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

func TestStargateProviderProxyForwardsRoutingMethod(t *testing.T) {
	t.Parallel()

	reqCtx := &requestctx.RequestContext{
		RequestID:        "req-proxy",
		RoutingKey:       "fn-proxy",
		Model:            "proxy-model",
		RoutingMethod:    "least_loaded",
		CacheAffinityKey: "mt:v1:header:proxy-hash",
	}
	request := &ProxyRequest{
		Method:        http.MethodPost,
		Path:          "/v1/embeddings",
		Body:          io.NopCloser(strings.NewReader(`{"model":"proxy-model","input":"hello"}`)),
		InputTokens:   11,
		TokenEstimate: 29,
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/embeddings", r.URL.Path)
		require.Equal(t, "req-proxy", r.Header.Get(headerRequestID))
		require.Equal(t, "fn-proxy", r.Header.Get(headerRoutingKey))
		require.Equal(t, "proxy-model", r.Header.Get(headerModel))
		require.Equal(t, "least_loaded", r.Header.Get(headerRoutingMethod))
		require.Equal(t, "mt:v1:header:proxy-hash", r.Header.Get(headerCacheAffinityKey))
		require.Equal(t, "11", r.Header.Get(headerInputTokens))
		require.Equal(t, "29", r.Header.Get(headerTokenEstimate))

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeJSON},
			},
			Body:    io.NopCloser(strings.NewReader(`{"object":"list","data":[]}`)),
			Request: r,
		}, nil
	})}

	response, err := provider.Proxy(context.Background(), reqCtx, request)
	require.NoError(t, err)
	defer response.Body.Close()

	require.Equal(t, http.StatusOK, response.StatusCode)
}

func TestStargateProviderNewOutboundRequestForwardsPriority(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "upstream-model",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("hello"),
				},
			},
		},
		InputTokens: 3,
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)

	// A resolved priority is forwarded as the header value.
	resolved, err := provider.newOutboundRequest(
		&requestctx.RequestContext{RoutingKey: "fn-abc", Priority: ptr.To(uint32(3))},
		request,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "3", resolved.Header.Get(headerPriority))

	// An explicit zero (highest priority) is forwarded, distinct from unset.
	zero, err := provider.newOutboundRequest(
		&requestctx.RequestContext{RoutingKey: "fn-abc", Priority: ptr.To(uint32(0))},
		request,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "0", zero.Header.Get(headerPriority))

	// Unset priority omits the header entirely.
	unset, err := provider.newOutboundRequest(
		&requestctx.RequestContext{RoutingKey: "fn-abc"},
		request,
		false,
	)
	require.NoError(t, err)
	require.Empty(t, unset.Header.Get(headerPriority))
}

func TestStargateProviderProxyForwardsPriority(t *testing.T) {
	t.Parallel()

	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-proxy",
		RoutingKey: "fn-proxy",
		Model:      "proxy-model",
		Priority:   ptr.To(uint32(7)),
	}
	request := &ProxyRequest{
		Method: http.MethodPost,
		Path:   "/v1/embeddings",
		Body:   io.NopCloser(strings.NewReader(`{"model":"proxy-model","input":"hello"}`)),
		// A client-supplied X-Priority survives the header clone; the resolved
		// value must replace it, not append to it.
		Header: http.Header{headerPriority: []string{"9"}},
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		require.Equal(t, []string{"7"}, r.Header.Values(headerPriority))

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeJSON},
			},
			Body:    io.NopCloser(strings.NewReader(`{"object":"list","data":[]}`)),
			Request: r,
		}, nil
	})}

	response, err := provider.Proxy(context.Background(), reqCtx, request)
	require.NoError(t, err)
	defer response.Body.Close()

	require.Equal(t, http.StatusOK, response.StatusCode)
}

func TestStargateProviderProxyOmitsPriorityWhenUnset(t *testing.T) {
	t.Parallel()

	reqCtx := &requestctx.RequestContext{
		RequestID:  "req-proxy",
		RoutingKey: "fn-proxy",
		Model:      "proxy-model",
	}
	request := &ProxyRequest{
		Method: http.MethodPost,
		Path:   "/v1/embeddings",
		Body:   io.NopCloser(strings.NewReader(`{"model":"proxy-model","input":"hello"}`)),
	}

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Helper()

		require.Empty(t, r.Header.Values(headerPriority))

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				headerContentType: []string{contentTypeJSON},
			},
			Body:    io.NopCloser(strings.NewReader(`{"object":"list","data":[]}`)),
			Request: r,
		}, nil
	})}

	response, err := provider.Proxy(context.Background(), reqCtx, request)
	require.NoError(t, err)
	defer response.Body.Close()

	require.Equal(t, http.StatusOK, response.StatusCode)
}

func sseChatBody(t *testing.T, chunks ...models.ChatCompletionChunk) string {
	t.Helper()

	var body strings.Builder
	for _, chunk := range chunks {
		payload, err := json.Marshal(chunk)
		require.NoError(t, err)
		_, err = fmt.Fprintf(&body, "data: %s\n\n", payload)
		require.NoError(t, err)
	}

	_, err := fmt.Fprint(&body, "data: [DONE]\n\n")
	require.NoError(t, err)
	return body.String()
}

type contextReadCloser struct {
	ctx context.Context
}

func (r contextReadCloser) Read(_ []byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r contextReadCloser) Close() error {
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		req.URL = &url.URL{}
	}
	return f(req)
}
