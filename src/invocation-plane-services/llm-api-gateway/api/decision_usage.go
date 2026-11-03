// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"math"
	"net/http"
	"strings"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// decisionUsageObserver watches a decision route response body for
// usage.input_tokens without buffering it. It reuses the Messages scanner,
// which validates the JSON incrementally and skips large content.
type decisionUsageObserver struct {
	scanner messagesJSONUsageScanner
}

func (o *decisionUsageObserver) Write(p []byte) (int, error) {
	o.scanner.Write(p)
	return len(p), nil
}

// inputTokens returns usage.input_tokens when the body was a complete JSON
// object that reported it as a non-negative number.
func (o *decisionUsageObserver) inputTokens() (int, bool) {
	s := &o.scanner
	if !s.done || s.invalid || s.oversized || s.usage.InputTokens == nil || *s.usage.InputTokens < 0 {
		return 0, false
	}
	return *s.usage.InputTokens, true
}

// observesDecisionUsage reports whether the response body should be scanned:
// a 2xx response on a decision route that is not content-encoded.
func observesDecisionUsage(r *http.Request, resp *provider.ProxyResponse) bool {
	switch r.URL.Path {
	case decisionsEndpointPath, systemOneEndpointPath:
	default:
		return false
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || resp.Body == nil {
		return false
	}
	encoding := resp.Header.Get(echo.HeaderContentEncoding)
	return encoding == "" || strings.EqualFold(encoding, "identity")
}

// recordDecisionUsage records what the backend reported after the response
// was delivered. The estimate stays the admission value; the reported count is
// only an observation, and a response without usage counts nothing.
func (h *OpenAIProxyHandlers) recordDecisionUsage(c *GatewayContext, observer *decisionUsageObserver) {
	ctx := c.UserContext()
	endpoint := c.Request().URL.Path
	functionID := requestFunctionID(c)
	status := "absent"
	if tokens, ok := observer.inputTokens(); ok {
		status = "observed"
		h.handlers.observability.recordLLMUsage(ctx, endpoint, functionID,
			&models.ChatCompletionUsage{PromptTokens: uint32(min(int64(tokens), math.MaxUint32))}, false)
	}
	telemetry.AddWithContext(ctx, h.handlers.observability.decisionUsage, 1,
		attribute.String("endpoint", endpoint),
		attribute.String("status", status),
		telemetry.FunctionIDAttribute(functionID),
	)
}
