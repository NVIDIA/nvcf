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
	"sync"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

func rateLimitConfig() *config.Config {
	return config.Default()
}

func TestNormalizeChatRequestRunsAdmissionBeforeFinalize(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	cfg := rateLimitConfig()
	handlers := NewHandlers(cfg, nil, limiter)
	gc, _ := newRateLimitGatewayContext()
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-123",
		OrgID:      "nca-456",
		RoutingKey: "fn-chat",
		Model:      "company-name/model-name",
		ModelSpecs: map[string]nvcf.ModelSpec{
			"company-name/model-name": {
				TokenRateLimit: "100-M,1000-D",
			},
		},
	})

	request := &models.ChatCompletionRequest{
		Model: "fn-chat/company-name/model-name",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello world"),
			},
		},
		MaxCompletionTokens: ptr.To(uint32(12)),
	}

	normalized, err := handlers.normalizeChatRequest(gc, request)
	if err != nil {
		t.Fatalf("normalize chat request: %v", err)
	}
	if normalized.AdmissionPlan == nil {
		t.Fatal("admission plan was not set")
	}
	if normalized.MaxOutputTokens != 12 {
		t.Fatalf("max output tokens = %d, want 12", normalized.MaxOutputTokens)
	}

	handlers.finalizeTokenConsumption(
		gc.UserContext(),
		normalized,
		&models.ChatCompletionUsage{
			PromptTokens:     9,
			CompletionTokens: 5,
			TotalTokens:      14,
		},
	)

	calls := limiter.Calls()
	if hasRateLimitKeySuffix(calls, ":requests_minute") {
		t.Fatal("unexpected request-minute calls for token-only limits")
	}
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_minute",
		tokensRequested: int64(normalized.InputTokens + normalized.MaxOutputTokens),
		testOnly:        true,
	}) {
		t.Fatal("missing org-scoped token admission test call")
	}
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_minute",
		tokensRequested: int64(normalized.InputTokens + normalized.MaxOutputTokens),
		mustConsume:     true,
	}) {
		t.Fatal("missing org-scoped token admission commit call")
	}
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_day",
		tokensRequested: int64(normalized.InputTokens + normalized.MaxOutputTokens),
		testOnly:        true,
	}) {
		t.Fatal("missing org-scoped token-day admission test call")
	}
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_day",
		tokensRequested: int64(normalized.InputTokens + normalized.MaxOutputTokens),
		mustConsume:     true,
	}) {
		t.Fatal("missing org-scoped token-day admission commit call")
	}
}

func TestStreamChatCompletionFinalizesTokensFromStreamUsage(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	plan := ratelimit.NewAdmissionPlan(
		limiter,
		ratelimit.ResourceLimit{
			SubjectKey:      rateLimitSubjectKey("nca-456", "", "fn-chat"),
			SubjectRepr:     "org `nca-456` function `fn-chat`",
			Level:           ratelimit.LevelOrg,
			TokensPerMinute: 100,
		},
		"req-stream",
	)

	handlers := NewHandlers(nil, &stubStreamProvider{
		events: []provider.StreamEvent{
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
						PromptTokens:     7,
						CompletionTokens: 5,
						TotalTokens:      12,
					},
				},
			},
		},
	}, limiter, nil)

	gc, _ := newRateLimitGatewayContext()
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-stream",
		RoutingKey: "fn-chat",
		Model:      "company-name/model-name",
	})

	request := &provider.NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "fn-chat/company-name/model-name",
			Messages: &[]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleUser,
					Content: models.SingleTextContent("hello"),
				},
			},
		},
		InputTokens:   4,
		AdmissionPlan: plan,
	}

	err := handlers.AsOpenAIChatHandlers().streamChatCompletion(gc, request)
	if err != nil {
		t.Fatalf("stream chat completion: %v", err)
	}
	if !strings.Contains(gc.Response().Writer.(*httptest.ResponseRecorder).Body.String(), "data: [DONE]") {
		t.Fatal("expected stream terminator in response body")
	}

	calls := limiter.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if !calls[0].mustConsume {
		t.Fatal("stream finalization should use must-consume mode")
	}
	if calls[0].tokensRequested != 12 {
		t.Fatalf("stream finalized tokens = %d, want 12", calls[0].tokensRequested)
	}
}

func TestFinalizeTokenConsumptionReleasesReservationWithoutUsage(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	plan := ratelimit.NewAdmissionPlan(
		limiter,
		ratelimit.ResourceLimit{
			SubjectKey:      rateLimitSubjectKey("nca-456", "", "fn-chat"),
			SubjectRepr:     "org `nca-456` function `fn-chat`",
			Level:           ratelimit.LevelOrg,
			TokensPerMinute: 100,
		},
		"req-zero-usage",
	)

	normalized := &provider.NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "fn-chat/company-name/model-name",
		},
		InputTokens:     4,
		MaxOutputTokens: 6,
		AdmissionPlan:   plan,
	}

	if _, err := plan.Commit(context.Background(), ratelimit.ResourceRequest{
		InputTokens:  4,
		OutputTokens: 6,
	}); err != nil {
		t.Fatalf("commit tokens: %v", err)
	}

	handlers := NewHandlers(nil, nil, limiter)
	handlers.finalizeTokenConsumption(
		context.Background(),
		normalized,
		&models.ChatCompletionUsage{},
	)

	calls := limiter.Calls()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}

	expectedRelease := int64(-normalized.MaxOutputTokens)
	if calls[1].tokensRequested != expectedRelease {
		t.Fatalf("tokens requested = %d, want %d", calls[1].tokensRequested, expectedRelease)
	}
}

func TestFinalizeTokenConsumptionKeepsPromptChargeWhenUsageOmitsPromptTokens(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	plan := ratelimit.NewAdmissionPlan(
		limiter,
		ratelimit.ResourceLimit{
			SubjectKey:      rateLimitSubjectKey("nca-456", "", "fn-chat"),
			SubjectRepr:     "org `nca-456` function `fn-chat`",
			Level:           ratelimit.LevelOrg,
			TokensPerMinute: 100,
		},
		"req-partial-usage",
	)

	normalized := &provider.NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "fn-chat/company-name/model-name",
		},
		InputTokens:     4,
		MaxOutputTokens: 6,
		AdmissionPlan:   plan,
	}

	if _, err := plan.Commit(context.Background(), ratelimit.ResourceRequest{
		InputTokens:  4,
		OutputTokens: 6,
	}); err != nil {
		t.Fatalf("commit tokens: %v", err)
	}

	handlers := NewHandlers(nil, nil, limiter)
	handlers.finalizeTokenConsumption(
		context.Background(),
		normalized,
		&models.ChatCompletionUsage{
			CompletionTokens: 3,
			TotalTokens:      3,
		},
	)

	calls := limiter.Calls()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}
	if calls[1].tokensRequested != -3 {
		t.Fatalf("tokens requested = %d, want -3", calls[1].tokensRequested)
	}
}

func TestStreamChatCompletionFinalizesTokensWithContextWithoutCancel(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	plan := ratelimit.NewAdmissionPlan(
		limiter,
		ratelimit.ResourceLimit{
			SubjectKey:      rateLimitSubjectKey("nca-456", "", "fn-chat"),
			SubjectRepr:     "org `nca-456` function `fn-chat`",
			Level:           ratelimit.LevelOrg,
			TokensPerMinute: 100,
		},
		"req-canceled",
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handlers := NewHandlers(nil, &stubStreamProvider{
		events: []provider.StreamEvent{
			{Err: context.Canceled},
		},
	}, limiter, nil)

	gc, _ := newRateLimitGatewayContext()
	gc.SetUserContext(ctx)
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-canceled",
		RoutingKey: "fn-chat",
		Model:      "company-name/model-name",
	})

	request := &provider.NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: "fn-chat/company-name/model-name",
		},
		InputTokens:     4,
		MaxOutputTokens: 3,
		AdmissionPlan:   plan,
	}

	if _, err := plan.Commit(context.Background(), ratelimit.ResourceRequest{
		InputTokens:  4,
		OutputTokens: 3,
	}); err != nil {
		t.Fatalf("commit tokens: %v", err)
	}

	if err := handlers.AsOpenAIChatHandlers().streamChatCompletion(gc, request); err != nil {
		t.Fatalf("stream chat completion: %v", err)
	}

	calls := limiter.Calls()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}
	if calls[1].contextErr != nil {
		t.Fatalf("finalize context err = %v, want nil", calls[1].contextErr)
	}
	if calls[1].tokensRequested != -3 {
		t.Fatalf("released tokens = %d, want -3", calls[1].tokensRequested)
	}
}

func TestNormalizeChatRequestUsesMinimumOutputReservationWithoutMaxTokens(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	cfg := rateLimitConfig()
	handlers := NewHandlers(cfg, nil, limiter)
	gc, rec := newRateLimitGatewayContext()
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-default-output",
		OrgID:      "nca-456",
		RoutingKey: "fn-chat",
		Model:      "company-name/model-name",
		ModelSpecs: map[string]nvcf.ModelSpec{
			"company-name/model-name": {
				TokenRateLimit: "100-M,1000-D",
			},
		},
	})

	request := &models.ChatCompletionRequest{
		Model: "fn-chat/company-name/model-name",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello world"),
			},
		},
	}

	normalized, err := handlers.normalizeChatRequest(gc, request)
	if err != nil {
		t.Fatalf("normalize chat request: %v", err)
	}
	if normalized.MaxOutputTokens != 1 {
		t.Fatalf("max output tokens = %d, want 1", normalized.MaxOutputTokens)
	}

	calls := limiter.Calls()
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_minute",
		tokensRequested: int64(normalized.InputTokens + 1),
		testOnly:        true,
	}) {
		t.Fatalf("missing token admission test call for %d tokens", normalized.InputTokens+1)
	}
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_minute",
		tokensRequested: int64(normalized.InputTokens + 1),
		mustConsume:     true,
	}) {
		t.Fatalf("missing token admission commit call for %d tokens", normalized.InputTokens+1)
	}
	if got := rec.Header().Get("X-Ratelimit-Limit-Requests"); got != "" {
		t.Fatalf("X-Ratelimit-Limit-Requests = %q, want empty", got)
	}
	if got := rec.Header().Get("X-Ratelimit-Limit-Tokens"); got != "100" {
		t.Fatalf("X-Ratelimit-Limit-Tokens = %q, want 100", got)
	}
	if got := rec.Header().Get("X-Ratelimit-Remaining-Tokens"); got == "" {
		t.Fatal("X-Ratelimit-Remaining-Tokens was not set")
	}
}

func TestNormalizeChatRequestDoesNotConsumeRequestsWhenTokenAdmissionFails(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{
		currentValueFn: func(call rateLimitCall) int64 {
			if call.testOnly && strings.HasSuffix(call.key, ":tokens_minute") {
				return max(0, call.tokensRequested-1)
			}
			return call.limit.Limit
		},
	}
	cfg := rateLimitConfig()
	handlers := NewHandlers(cfg, nil, limiter)
	gc, rec := newRateLimitGatewayContext()
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-token-reject",
		OrgID:      "nca-456",
		RoutingKey: "fn-chat",
		Model:      "company-name/model-name",
		ModelSpecs: map[string]nvcf.ModelSpec{
			"company-name/model-name": {
				TokenRateLimit: "100-M,1000-D",
			},
		},
	})

	request := &models.ChatCompletionRequest{
		Model: "fn-chat/company-name/model-name",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello world"),
			},
		},
		MaxCompletionTokens: ptr.To(uint32(12)),
	}

	_, err := handlers.normalizeChatRequest(gc, request)
	if err == nil {
		t.Fatal("normalize chat request unexpectedly succeeded")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want 429 rate limit error", err)
	}

	calls := limiter.Calls()
	for _, call := range calls {
		if call.mustConsume {
			t.Fatalf("unexpected must-consume call: %+v", call)
		}
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("Retry-After was not set")
	}
}

func TestNormalizeChatRequestCommitsEstimatedPromptTokens(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	cfg := rateLimitConfig()
	handlers := NewHandlers(
		cfg,
		nil,
		limiter,
	)
	gc, _ := newRateLimitGatewayContext()
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-tokenized-input",
		OrgID:      "nca-456",
		RoutingKey: "fn-chat",
		Model:      "company-name/model-name",
		ModelSpecs: map[string]nvcf.ModelSpec{
			"company-name/model-name": {
				TokenRateLimit: "100-M,1000-D",
			},
		},
	})

	request := &models.ChatCompletionRequest{
		Model: "fn-chat/company-name/model-name",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello world"),
			},
		},
		MaxCompletionTokens: ptr.To(uint32(4)),
	}

	normalized, err := handlers.normalizeChatRequest(gc, request)
	if err != nil {
		t.Fatalf("normalize chat request: %v", err)
	}
	estimatedInputTokens := estimatedInputTokensForNormalizedRequest(
		"company-name/model-name",
		request,
	)
	if normalized.InputTokens != estimatedInputTokens {
		t.Fatalf("normalized input tokens = %d, want %d", normalized.InputTokens, estimatedInputTokens)
	}

	calls := limiter.Calls()
	estimatedTokens := int64(estimatedInputTokens + 4)
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_minute",
		tokensRequested: estimatedTokens,
		testOnly:        true,
	}) {
		t.Fatalf("missing token admission test call for %d tokens", estimatedTokens)
	}
	if !hasRateLimitCall(calls, rateLimitCall{
		key:             rateLimitSubjectKey("nca-456", "", "fn-chat") + ":tokens_minute",
		tokensRequested: estimatedTokens,
		mustConsume:     true,
	}) {
		t.Fatal("missing token admission commit call for estimated prompt count")
	}
}

func TestParseTokenRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    parsedTokenRateLimit
		wantErr string
	}{
		{
			name: "minute only",
			raw:  "9000-M",
			want: parsedTokenRateLimit{tokensPerMinute: 9000},
		},
		{
			name: "minute and day",
			raw:  "9000-M,100000-D",
			want: parsedTokenRateLimit{tokensPerMinute: 9000, tokensPerDay: 100000},
		},
		{
			name: "second only",
			raw:  "100-S",
			want: parsedTokenRateLimit{tokensPerSecond: 100},
		},
		{
			name: "hour only",
			raw:  "25000-H",
			want: parsedTokenRateLimit{tokensPerHour: 25000},
		},
		{
			name: "week only",
			raw:  "500000-W",
			want: parsedTokenRateLimit{tokensPerWeek: 500000},
		},
		{
			name: "month only",
			raw:  "2000000-MO",
			want: parsedTokenRateLimit{tokensPerMonth: 2000000},
		},
		{
			name: "minute and month",
			raw:  "9000-M,2000000-MO",
			want: parsedTokenRateLimit{tokensPerMinute: 9000, tokensPerMonth: 2000000},
		},
		{
			name: "combined all levels",
			raw:  "100-S,9000-M,25000-H,100000-D,500000-W,2000000-MO",
			want: parsedTokenRateLimit{
				tokensPerSecond: 100,
				tokensPerMinute: 9000,
				tokensPerHour:   25000,
				tokensPerDay:    100000,
				tokensPerWeek:   500000,
				tokensPerMonth:  2000000,
			},
		},
		{
			name:    "invalid fragment",
			raw:     "9000",
			wantErr: `invalid token rate limit fragment "9000"`,
		},
		{
			name:    "unknown level",
			raw:     "9000-X",
			wantErr: `unsupported token rate limit level "X"`,
		},
		{
			name:    "lowercase level",
			raw:     "100-s",
			wantErr: `unsupported token rate limit level "s"`,
		},
		{
			name:    "zero value",
			raw:     "0-S",
			wantErr: "token rate limit must be positive",
		},
		{
			name:    "negative value",
			raw:     "-1-S",
			wantErr: "token rate limit must be positive",
		},
		{
			name:    "duplicate minute level",
			raw:     "9000-M,1000-M",
			wantErr: "duplicate minute token rate limit",
		},
		{
			name:    "duplicate second level",
			raw:     "100-S,200-S",
			wantErr: "duplicate second token rate limit",
		},
		{
			name:    "duplicate hour level",
			raw:     "25000-H,50000-H",
			wantErr: "duplicate hour token rate limit",
		},
		{
			name:    "duplicate day level",
			raw:     "100000-D,200000-D",
			wantErr: "duplicate day token rate limit",
		},
		{
			name:    "duplicate week level",
			raw:     "500000-W,600000-W",
			wantErr: "duplicate week token rate limit",
		},
		{
			name:    "duplicate month level",
			raw:     "2000000-MO,3000000-MO",
			wantErr: "duplicate month token rate limit",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseTokenRateLimit(tc.raw)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse token rate limit: %v", err)
			}
			if got != tc.want {
				t.Fatalf("parsed token rate limit = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNormalizeChatRequestParsesSingleTokenRateLimitUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		keySuffix string
	}{
		{name: "second", raw: "100-S", keySuffix: ":tokens_second"},
		{name: "minute", raw: "9000-M", keySuffix: ":tokens_minute"},
		{name: "hour", raw: "25000-H", keySuffix: ":tokens_hour"},
		{name: "day", raw: "100000-D", keySuffix: ":tokens_day"},
		{name: "week", raw: "500000-W", keySuffix: ":tokens_week"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			limiter := &recordingRateLimiter{}
			cfg := rateLimitConfig()
			handlers := NewHandlers(cfg, nil, limiter)
			gc, _ := newRateLimitGatewayContext()
			gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
				RequestID:  "req-123",
				OrgID:      "nca-456",
				RoutingKey: "fn-chat",
				Model:      "company-name/model-name",
				ModelSpecs: map[string]nvcf.ModelSpec{
					"company-name/model-name": {
						TokenRateLimit: tc.raw,
					},
				},
			})

			request := &models.ChatCompletionRequest{
				Model: "fn-chat/company-name/model-name",
				Messages: &[]models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("hello world"),
					},
				},
				MaxCompletionTokens: ptr.To(uint32(12)),
			}

			normalized, err := handlers.normalizeChatRequest(gc, request)
			if err != nil {
				t.Fatalf("normalize chat request: %v", err)
			}
			if normalized.AdmissionPlan == nil {
				t.Fatal("admission plan was not set")
			}
			if !hasRateLimitKeySuffix(limiter.Calls(), tc.keySuffix) {
				t.Fatalf("missing rate limit call with suffix %q", tc.keySuffix)
			}
		})
	}
}

func TestNormalizeChatRequestParsesCombinedTokenRateLimitUnits(t *testing.T) {
	t.Parallel()

	limiter := &recordingRateLimiter{}
	cfg := rateLimitConfig()
	handlers := NewHandlers(cfg, nil, limiter)
	gc, _ := newRateLimitGatewayContext()
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-123",
		OrgID:      "nca-456",
		RoutingKey: "fn-chat",
		Model:      "company-name/model-name",
		ModelSpecs: map[string]nvcf.ModelSpec{
			"company-name/model-name": {
				TokenRateLimit: "100-S,9000-M,25000-H,100000-D,500000-W",
			},
		},
	})

	request := &models.ChatCompletionRequest{
		Model: "fn-chat/company-name/model-name",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello world"),
			},
		},
		MaxCompletionTokens: ptr.To(uint32(12)),
	}

	normalized, err := handlers.normalizeChatRequest(gc, request)
	if err != nil {
		t.Fatalf("normalize chat request: %v", err)
	}
	if normalized.AdmissionPlan == nil {
		t.Fatal("admission plan was not set")
	}

	for _, suffix := range []string{
		":tokens_second",
		":tokens_minute",
		":tokens_hour",
		":tokens_day",
		":tokens_week",
	} {
		if !hasRateLimitKeySuffix(limiter.Calls(), suffix) {
			t.Fatalf("missing rate limit call with suffix %q", suffix)
		}
	}
}

func TestNormalizeChatRequestParsesInputOutputTokenRateLimitUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		inputRaw        string
		outputRaw       string
		wantKeySuffixes []string
		unwantKeySuffix string
	}{
		{
			name:            "input only",
			inputRaw:        "100-S",
			wantKeySuffixes: []string{":input_tokens_second"},
			unwantKeySuffix: ":output_tokens_second",
		},
		{
			name:            "output only",
			outputRaw:       "9000-M",
			wantKeySuffixes: []string{":output_tokens_minute"},
			unwantKeySuffix: ":input_tokens_minute",
		},
		{
			name:            "input and output combined",
			inputRaw:        "100-S,9000-M",
			outputRaw:       "50-S",
			wantKeySuffixes: []string{":input_tokens_second", ":input_tokens_minute", ":output_tokens_second"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			limiter := &recordingRateLimiter{}
			cfg := rateLimitConfig()
			handlers := NewHandlers(cfg, nil, limiter)
			gc, _ := newRateLimitGatewayContext()
			gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
				RequestID:  "req-123",
				OrgID:      "nca-456",
				RoutingKey: "fn-chat",
				Model:      "company-name/model-name",
				ModelSpecs: map[string]nvcf.ModelSpec{
					"company-name/model-name": {
						InputTokenRateLimit:  tc.inputRaw,
						OutputTokenRateLimit: tc.outputRaw,
					},
				},
			})

			request := &models.ChatCompletionRequest{
				Model: "fn-chat/company-name/model-name",
				Messages: &[]models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent("hello world"),
					},
				},
				MaxCompletionTokens: ptr.To(uint32(12)),
			}

			normalized, err := handlers.normalizeChatRequest(gc, request)
			if err != nil {
				t.Fatalf("normalize chat request: %v", err)
			}
			if normalized.AdmissionPlan == nil {
				t.Fatal("admission plan was not set")
			}

			for _, suffix := range tc.wantKeySuffixes {
				if !hasRateLimitKeySuffix(limiter.Calls(), suffix) {
					t.Fatalf("missing rate limit call with suffix %q", suffix)
				}
			}
			if tc.unwantKeySuffix != "" && hasRateLimitKeySuffix(limiter.Calls(), tc.unwantKeySuffix) {
				t.Fatalf("unexpected rate limit call with suffix %q", tc.unwantKeySuffix)
			}
		})
	}
}

func TestCallerLimitResolverParsesTokenLimitLevels(t *testing.T) {
	t.Parallel()

	limits, err := CallerLimitResolver{}.ResolveLimits(
		context.Background(),
		&requestctx.RequestContext{
			OrgID:      "nca-456",
			RoutingKey: "fn-chat",
			Model:      "company-name/model-name",
			ModelSpecs: map[string]nvcf.ModelSpec{
				"company-name/model-name": {
					TokenRateLimit: "9000-M,100000-D",
				},
			},
		},
		"/v1/chat/completions",
	)
	if err != nil {
		t.Fatalf("resolve limits: %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(limits))
	}
	if limits[0].SubjectKey != rateLimitSubjectKey("nca-456", "", "fn-chat") {
		t.Fatalf("subject key = %q", limits[0].SubjectKey)
	}
	if limits[0].TokensPerMinute != 9000 {
		t.Fatalf("tokens per minute = %d, want 9000", limits[0].TokensPerMinute)
	}
	if limits[0].TokensPerDay != 100000 {
		t.Fatalf("tokens per day = %d, want 100000", limits[0].TokensPerDay)
	}
	if limits[0].RequestsPerMinute != 0 {
		t.Fatalf("requests per minute = %d, want 0", limits[0].RequestsPerMinute)
	}
}

func TestCallerLimitResolverParsesInputOutputTokenLimitLevels(t *testing.T) {
	t.Parallel()

	limits, err := CallerLimitResolver{}.ResolveLimits(
		context.Background(),
		&requestctx.RequestContext{
			OrgID:      "nca-456",
			RoutingKey: "fn-chat",
			Model:      "company-name/model-name",
			ModelSpecs: map[string]nvcf.ModelSpec{
				"company-name/model-name": {
					InputTokenRateLimit:  "3000-M,50000-D",
					OutputTokenRateLimit: "1000-M,20000-D",
				},
			},
		},
		"/v1/chat/completions",
	)
	if err != nil {
		t.Fatalf("resolve limits: %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(limits))
	}
	if limits[0].InputTokensPerMinute != 3000 {
		t.Fatalf("input tokens per minute = %d, want 3000", limits[0].InputTokensPerMinute)
	}
	if limits[0].InputTokensPerDay != 50000 {
		t.Fatalf("input tokens per day = %d, want 50000", limits[0].InputTokensPerDay)
	}
	if limits[0].OutputTokensPerMinute != 1000 {
		t.Fatalf("output tokens per minute = %d, want 1000", limits[0].OutputTokensPerMinute)
	}
	if limits[0].OutputTokensPerDay != 20000 {
		t.Fatalf("output tokens per day = %d, want 20000", limits[0].OutputTokensPerDay)
	}
	if limits[0].TokensPerMinute != 0 {
		t.Fatalf("tokens per minute = %d, want 0", limits[0].TokensPerMinute)
	}
}

func TestCallerLimitResolverParsesMonthTokenLimitLevels(t *testing.T) {
	t.Parallel()

	limits, err := CallerLimitResolver{}.ResolveLimits(
		context.Background(),
		&requestctx.RequestContext{
			OrgID:      "nca-456",
			RoutingKey: "fn-chat",
			Model:      "company-name/model-name",
			ModelSpecs: map[string]nvcf.ModelSpec{
				"company-name/model-name": {
					TokenRateLimit:       "5000000-MO",
					InputTokenRateLimit:  "3000000-MO",
					OutputTokenRateLimit: "1000000-MO",
				},
			},
		},
		"/v1/chat/completions",
	)
	if err != nil {
		t.Fatalf("resolve limits: %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(limits))
	}
	if limits[0].TokensPerMonth != 5000000 {
		t.Fatalf("tokens per month = %d, want 5000000", limits[0].TokensPerMonth)
	}
	if limits[0].InputTokensPerMonth != 3000000 {
		t.Fatalf("input tokens per month = %d, want 3000000", limits[0].InputTokensPerMonth)
	}
	if limits[0].OutputTokensPerMonth != 1000000 {
		t.Fatalf("output tokens per month = %d, want 1000000", limits[0].OutputTokensPerMonth)
	}
}

func TestChooseTokenStatsPrefersCombinedTokensOverInputOutput(t *testing.T) {
	t.Parallel()

	results := map[ratelimit.LimitDimension]*ratelimit.RateLimitResult{
		ratelimit.TokensPerMinute: {
			CurrentValue: 100,
			Requested:    10,
			RateLimit:    ratelimit.RateLimit{Limit: 100, Period: time.Minute},
		},
		ratelimit.InputTokensPerMinute: {
			CurrentValue: 50,
			Requested:    5,
			RateLimit:    ratelimit.RateLimit{Limit: 50, Period: time.Minute},
		},
	}

	limit, remaining, _, ok := chooseTokenStats(results)
	if !ok {
		t.Fatal("expected a result")
	}
	if limit != 100 {
		t.Fatalf("limit = %d, want 100", limit)
	}
	if remaining != 90 {
		t.Fatalf("remaining = %d, want 90", remaining)
	}
}

func TestChooseTokenStatsSumsInputOutputAtSamePeriod(t *testing.T) {
	t.Parallel()

	results := map[ratelimit.LimitDimension]*ratelimit.RateLimitResult{
		ratelimit.InputTokensPerHour: {
			CurrentValue: 100,
			Requested:    10,
			RateLimit:    ratelimit.RateLimit{Limit: 100, Period: time.Hour},
		},
		ratelimit.OutputTokensPerHour: {
			CurrentValue: 40,
			Requested:    5,
			RateLimit:    ratelimit.RateLimit{Limit: 40, Period: time.Hour},
		},
	}

	limit, remaining, _, ok := chooseTokenStats(results)
	if !ok {
		t.Fatal("expected a result")
	}
	if limit != 140 {
		t.Fatalf("limit = %d, want 140", limit)
	}
	if remaining != 125 {
		t.Fatalf("remaining = %d, want 125", remaining)
	}
}

func TestChooseTokenStatsPrefersTighterInputOutputPeriodOverFiner(t *testing.T) {
	t.Parallel()

	results := map[ratelimit.LimitDimension]*ratelimit.RateLimitResult{
		// Finer period, but loose: barely touched, huge remaining.
		ratelimit.InputTokensPerSecond: {
			CurrentValue: 1000,
			Requested:    1,
			RateLimit:    ratelimit.RateLimit{Limit: 1000, Period: time.Second},
		},
		// Coarser period, but nearly exhausted: this is what's actually about to
		// throttle the caller, and must not be hidden behind the finer, looser one.
		ratelimit.InputTokensPerMinute: {
			CurrentValue: 100,
			Requested:    99,
			RateLimit:    ratelimit.RateLimit{Limit: 100, Period: time.Minute},
		},
	}

	limit, remaining, _, ok := chooseTokenStats(results)
	if !ok {
		t.Fatal("expected a result")
	}
	if limit != 100 {
		t.Fatalf("limit = %d, want 100 (the tighter per-minute limit)", limit)
	}
	if remaining != 1 {
		t.Fatalf("remaining = %d, want 1 (the tighter per-minute remaining)", remaining)
	}
}

func TestChooseTokenStatsPrefersFinerInputOutputPeriod(t *testing.T) {
	t.Parallel()

	results := map[ratelimit.LimitDimension]*ratelimit.RateLimitResult{
		ratelimit.InputTokensPerDay: {
			CurrentValue: 1000,
			Requested:    100,
			RateLimit:    ratelimit.RateLimit{Limit: 1000, Period: 24 * time.Hour},
		},
		ratelimit.InputTokensPerMinute: {
			CurrentValue: 10,
			Requested:    1,
			RateLimit:    ratelimit.RateLimit{Limit: 10, Period: time.Minute},
		},
	}

	limit, remaining, _, ok := chooseTokenStats(results)
	if !ok {
		t.Fatal("expected a result")
	}
	if limit != 10 {
		t.Fatalf("limit = %d, want 10 (per-minute should win over per-day)", limit)
	}
	if remaining != 9 {
		t.Fatalf("remaining = %d, want 9", remaining)
	}
}

func TestChooseTokenStatsReturnsFalseWhenNoResults(t *testing.T) {
	t.Parallel()

	_, _, _, ok := chooseTokenStats(map[ratelimit.LimitDimension]*ratelimit.RateLimitResult{})
	if ok {
		t.Fatal("expected ok = false for empty results")
	}
}

func TestChooseTokenStatsIncludesMonthInInputOutputPeriods(t *testing.T) {
	t.Parallel()

	results := map[ratelimit.LimitDimension]*ratelimit.RateLimitResult{
		ratelimit.InputTokensPerMonth: {
			CurrentValue: 1000,
			Requested:    1000,
			RateLimit:    ratelimit.RateLimit{Limit: 1000, Period: 30 * 24 * time.Hour},
		},
	}

	limit, remaining, _, ok := chooseTokenStats(results)
	if !ok {
		t.Fatal("expected a result")
	}
	if limit != 1000 {
		t.Fatalf("limit = %d, want 1000", limit)
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0", remaining)
	}
}

func newRateLimitGatewayContext() (*GatewayContext, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	gc := NewGatewayContext(e.NewContext(req, rec))
	gc.store.Set(contextKeyRequestID, "req-test")
	return gc, rec
}

type recordingRateLimiter struct {
	mu             sync.Mutex
	calls          []rateLimitCall
	currentValueFn func(rateLimitCall) int64
}

type rateLimitCall struct {
	key             string
	limit           ratelimit.RateLimit
	tokensRequested int64
	testOnly        bool
	mustConsume     bool
	requestID       string
	contextErr      error
}

func (r *recordingRateLimiter) CheckLimit(
	ctx context.Context,
	key string,
	limit ratelimit.RateLimit,
	tokensRequested int64,
	testOnly bool,
	requestID string,
	mustConsume bool,
) (*ratelimit.RateLimitResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	call := rateLimitCall{
		key:             key,
		limit:           limit,
		tokensRequested: tokensRequested,
		testOnly:        testOnly,
		mustConsume:     mustConsume,
		requestID:       requestID,
		contextErr:      ctx.Err(),
	}
	r.calls = append(r.calls, call)

	currentValue := limit.Limit
	if r.currentValueFn != nil {
		currentValue = r.currentValueFn(call)
	} else if currentValue < tokensRequested {
		currentValue = tokensRequested
	}

	return &ratelimit.RateLimitResult{
		CurrentValue: currentValue,
		Requested:    tokensRequested,
		RateLimit:    limit,
	}, nil
}

func (r *recordingRateLimiter) Reset(_ context.Context, _ ...string) error {
	return nil
}

func (r *recordingRateLimiter) Calls() []rateLimitCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	dst := make([]rateLimitCall, len(r.calls))
	copy(dst, r.calls)
	return dst
}

func hasRateLimitCall(calls []rateLimitCall, want rateLimitCall) bool {
	for _, call := range calls {
		if call.key != want.key {
			continue
		}
		if call.tokensRequested != want.tokensRequested {
			continue
		}
		if call.testOnly != want.testOnly {
			continue
		}
		if call.mustConsume != want.mustConsume {
			continue
		}
		return true
	}
	return false
}

func hasRateLimitKeySuffix(calls []rateLimitCall, suffix string) bool {
	for _, call := range calls {
		if strings.HasSuffix(call.key, suffix) {
			return true
		}
	}
	return false
}

type stubStreamProvider struct {
	events []provider.StreamEvent
}

func (s *stubStreamProvider) Complete(
	_ context.Context,
	_ *requestctx.RequestContext,
	_ *provider.NormalizedRequest,
) (*models.ChatCompletionResponse, error) {
	return nil, nil
}

func (s *stubStreamProvider) Stream(
	_ context.Context,
	_ *requestctx.RequestContext,
	_ *provider.NormalizedRequest,
) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, len(s.events))
	go func() {
		defer close(ch)
		for _, event := range s.events {
			ch <- event
		}
	}()
	return ch, nil
}
