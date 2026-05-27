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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	openairesponses "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

func TestApplyResponsesSessionAffinityPrefersConversationOverHeader(t *testing.T) {
	t.Parallel()

	ctx := newSessionAffinityTestContext()
	ctx.Request().Header.Set(HeaderMultiTurnSessionID, "header-session")
	conversationID := "conversation-session"
	request := &openairesponses.CreateRequest{
		Conversation: &openairesponses.Conversation{ID: &conversationID},
	}

	if err := applyResponsesSessionAffinity(ctx, request); err != nil {
		t.Fatalf("applyResponsesSessionAffinity: %v", err)
	}

	reqCtx := ctx.RequestContext()
	if reqCtx.SessionID != conversationID {
		t.Fatalf("SessionID = %q, want %q", reqCtx.SessionID, conversationID)
	}
	if reqCtx.SessionSource != sessionAffinitySourceConv {
		t.Fatalf("SessionSource = %q, want %q", reqCtx.SessionSource, sessionAffinitySourceConv)
	}
	if !strings.HasPrefix(reqCtx.CacheAffinityKey, "mt:v1:session:") {
		t.Fatalf("CacheAffinityKey = %q, want session source", reqCtx.CacheAffinityKey)
	}
	if strings.Contains(reqCtx.CacheAffinityKey, conversationID) {
		t.Fatalf("CacheAffinityKey leaks raw conversation ID: %q", reqCtx.CacheAffinityKey)
	}
}

func TestApplyResponsesSessionAffinityPrefersPromptCacheKeyOverConversation(t *testing.T) {
	t.Parallel()

	ctx := newSessionAffinityTestContext()
	ctx.Request().Header.Set(HeaderMultiTurnSessionID, "header-session")
	promptCacheKey := "prompt-cache-session"
	conversationID := "conversation-session"
	request := &openairesponses.CreateRequest{
		PromptCacheKey: &promptCacheKey,
		Conversation:   &openairesponses.Conversation{ID: &conversationID},
	}

	if err := applyResponsesSessionAffinity(ctx, request); err != nil {
		t.Fatalf("applyResponsesSessionAffinity: %v", err)
	}

	reqCtx := ctx.RequestContext()
	if reqCtx.SessionID != promptCacheKey {
		t.Fatalf("SessionID = %q, want %q", reqCtx.SessionID, promptCacheKey)
	}
	if reqCtx.SessionSource != sessionAffinitySourcePrompt {
		t.Fatalf("SessionSource = %q, want %q", reqCtx.SessionSource, sessionAffinitySourcePrompt)
	}
	if !strings.HasPrefix(reqCtx.CacheAffinityKey, "mt:v1:session:") {
		t.Fatalf("CacheAffinityKey = %q, want session source", reqCtx.CacheAffinityKey)
	}
	if strings.Contains(reqCtx.CacheAffinityKey, promptCacheKey) ||
		strings.Contains(reqCtx.CacheAffinityKey, conversationID) ||
		strings.Contains(reqCtx.CacheAffinityKey, "header-session") {
		t.Fatalf("CacheAffinityKey leaks raw session value: %q", reqCtx.CacheAffinityKey)
	}
}

func TestApplyResponsesSessionAffinityReusesReturnedBodyIDFromHeader(t *testing.T) {
	t.Parallel()

	first := newSessionAffinityTestContext()
	promptCacheKey := "body-session"
	firstRequest := &openairesponses.CreateRequest{
		PromptCacheKey: &promptCacheKey,
	}

	if err := applyResponsesSessionAffinity(first, firstRequest); err != nil {
		t.Fatalf("first applyResponsesSessionAffinity: %v", err)
	}
	firstReqCtx := first.RequestContext()

	second := newSessionAffinityTestContext()
	second.Request().Header.Set(HeaderMultiTurnSessionID, firstReqCtx.SessionID)
	secondRequest := &openairesponses.CreateRequest{}

	if err := applyResponsesSessionAffinity(second, secondRequest); err != nil {
		t.Fatalf("second applyResponsesSessionAffinity: %v", err)
	}
	secondReqCtx := second.RequestContext()

	if secondReqCtx.SessionID != firstReqCtx.SessionID {
		t.Fatalf("second SessionID = %q, want %q", secondReqCtx.SessionID, firstReqCtx.SessionID)
	}
	if secondReqCtx.SessionSource != sessionAffinitySourceHeader {
		t.Fatalf("second SessionSource = %q, want %q", secondReqCtx.SessionSource, sessionAffinitySourceHeader)
	}
	if secondReqCtx.CacheAffinityKey != firstReqCtx.CacheAffinityKey {
		t.Fatalf("second CacheAffinityKey = %q, want %q", secondReqCtx.CacheAffinityKey, firstReqCtx.CacheAffinityKey)
	}
}

func TestApplyChatSessionAffinityReusesGeneratedPayloadIDFromHeader(t *testing.T) {
	t.Parallel()

	first := newSessionAffinityTestContext()
	firstRequest := &models.ChatCompletionRequest{
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello"),
			},
		},
	}

	if err := applyChatSessionAffinity(first, firstRequest); err != nil {
		t.Fatalf("first applyChatSessionAffinity: %v", err)
	}
	firstReqCtx := first.RequestContext()
	if !strings.HasPrefix(firstReqCtx.SessionID, "mt:v1:payload:") {
		t.Fatalf("first SessionID = %q, want generated payload ID", firstReqCtx.SessionID)
	}

	second := newSessionAffinityTestContext()
	second.Request().Header.Set(HeaderMultiTurnSessionID, firstReqCtx.SessionID)
	secondRequest := &models.ChatCompletionRequest{
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("different follow-up"),
			},
		},
	}

	if err := applyChatSessionAffinity(second, secondRequest); err != nil {
		t.Fatalf("second applyChatSessionAffinity: %v", err)
	}
	secondReqCtx := second.RequestContext()

	if secondReqCtx.SessionID != firstReqCtx.SessionID {
		t.Fatalf("second SessionID = %q, want %q", secondReqCtx.SessionID, firstReqCtx.SessionID)
	}
	if secondReqCtx.SessionSource != sessionAffinitySourceHeader {
		t.Fatalf("second SessionSource = %q, want %q", secondReqCtx.SessionSource, sessionAffinitySourceHeader)
	}
	if secondReqCtx.CacheAffinityKey != firstReqCtx.CacheAffinityKey {
		t.Fatalf("second CacheAffinityKey = %q, want %q", secondReqCtx.CacheAffinityKey, firstReqCtx.CacheAffinityKey)
	}
}

func TestApplyChatSessionAffinityDoesNotOverwriteExistingResponsesAffinity(t *testing.T) {
	t.Parallel()

	ctx := newSessionAffinityTestContext()
	ctx.RequestContext().SessionID = "prompt-cache-session"
	ctx.RequestContext().SessionSource = sessionAffinitySourcePrompt
	ctx.RequestContext().CacheAffinityKey = "mt:v1:prompt_cache_key:existing"

	request := &models.ChatCompletionRequest{
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello"),
			},
		},
	}
	if err := applyChatSessionAffinity(ctx, request); err != nil {
		t.Fatalf("applyChatSessionAffinity: %v", err)
	}

	reqCtx := ctx.RequestContext()
	if reqCtx.SessionID != "prompt-cache-session" {
		t.Fatalf("SessionID = %q, want prompt-cache-session", reqCtx.SessionID)
	}
	if reqCtx.CacheAffinityKey != "mt:v1:prompt_cache_key:existing" {
		t.Fatalf("CacheAffinityKey = %q, want existing value", reqCtx.CacheAffinityKey)
	}
}

func newSessionAffinityTestContext() *GatewayContext {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	rec := httptest.NewRecorder()
	ctx := NewGatewayContext(e.NewContext(req, rec))
	ctx.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "req-session-test",
		RoutingKey: "fn-test",
	})
	return ctx
}
