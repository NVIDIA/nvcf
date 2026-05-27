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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	echo "github.com/labstack/echo/v4"

	openairesponses "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

const (
	HeaderMultiTurnSessionID = "x-multi-turn-session-id"

	sessionIDMaxLen              = 256
	sessionAffinitySourceHeader  = "header"
	sessionAffinitySourcePayload = "payload"
	sessionAffinitySourcePrompt  = "prompt_cache_key"
	sessionAffinitySourceConv    = "conversation_id"
	sessionAffinitySourceSession = "session"
)

func applyResponsesSessionAffinity(
	c *GatewayContext,
	request *openairesponses.CreateRequest,
) error {
	if c == nil || request == nil {
		return nil
	}
	reqCtx := c.RequestContext()
	if reqCtx == nil || reqCtx.SessionID != "" {
		return nil
	}

	headerSessionID, err := sessionIDFromHeader(c.Request().Header.Get(HeaderMultiTurnSessionID))
	if err != nil {
		return err
	}

	if value := stringPtrValue(request.PromptCacheKey); value != "" {
		return setSessionAffinity(reqCtx, sessionAffinitySourcePrompt, value)
	}
	if request.Conversation != nil {
		if value := stringPtrValue(request.Conversation.ID); value != "" {
			return setSessionAffinity(reqCtx, sessionAffinitySourceConv, value)
		}
	}
	if headerSessionID != "" {
		return setSessionAffinity(reqCtx, sessionAffinitySourceHeader, headerSessionID)
	}

	payload, err := json.Marshal(request.Input)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("marshal response input for session affinity: %s", err.Error()))
	}
	setPayloadSessionAffinity(reqCtx, payload)
	return nil
}

func applyChatSessionAffinity(
	c *GatewayContext,
	request *models.ChatCompletionRequest,
) error {
	if c == nil || request == nil {
		return nil
	}
	reqCtx := c.RequestContext()
	if reqCtx == nil || reqCtx.SessionID != "" {
		return nil
	}

	headerSessionID, err := sessionIDFromHeader(c.Request().Header.Get(HeaderMultiTurnSessionID))
	if err != nil {
		return err
	}
	if headerSessionID != "" {
		return setSessionAffinity(reqCtx, sessionAffinitySourceHeader, headerSessionID)
	}

	payload, err := json.Marshal(request.Messages)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("marshal chat messages for session affinity: %s", err.Error()))
	}
	setPayloadSessionAffinity(reqCtx, payload)
	return nil
}

func sessionIDFromHeader(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if err := validateSessionID(value); err != nil {
		return "", echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("%s is invalid: %s", HeaderMultiTurnSessionID, err.Error()))
	}
	return value, nil
}

func setSessionAffinity(
	reqCtx *requestctx.RequestContext,
	source string,
	sessionID string,
) error {
	if reqCtx == nil {
		return nil
	}
	if err := validateSessionID(sessionID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("session id from %s is invalid: %s", source, err.Error()))
	}
	reqCtx.SessionID = sessionID
	reqCtx.SessionSource = source
	reqCtx.CacheAffinityKey = affinityKeyForSessionID(sessionID)
	return nil
}

func setPayloadSessionAffinity(reqCtx *requestctx.RequestContext, payload []byte) {
	if reqCtx == nil {
		return
	}
	key := affinityKey(sessionAffinitySourcePayload, payload)
	reqCtx.SessionID = key
	reqCtx.SessionSource = sessionAffinitySourcePayload
	reqCtx.CacheAffinityKey = key
}

func affinityKey(source string, material []byte) string {
	sum := sha256.Sum256(material)
	return "mt:v1:" + source + ":" + hex.EncodeToString(sum[:])
}

func affinityKeyForSessionID(sessionID string) string {
	if isAffinityKey(sessionID) {
		return sessionID
	}
	return affinityKey(sessionAffinitySourceSession, []byte(sessionID))
}

func isAffinityKey(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 4 || parts[0] != "mt" || parts[1] != "v1" || len(parts[3]) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(parts[3])
	return err == nil
}

func validateSessionID(value string) error {
	if value == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(value) > sessionIDMaxLen {
		return fmt.Errorf("must be at most %d bytes", sessionIDMaxLen)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("must not contain control characters")
		}
	}
	return nil
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	if *value == "" {
		return ""
	}
	return *value
}

func setMultiTurnSessionResponseHeader(c *GatewayContext) {
	if c == nil || c.RequestContext() == nil || c.RequestContext().SessionID == "" {
		return
	}
	c.Response().Header().Set(HeaderMultiTurnSessionID, c.RequestContext().SessionID)
}
