// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// decisionRouteRequest binds only what routing needs. The body is otherwise
// opaque: it is forwarded unchanged apart from the model rewrite.
type decisionRouteRequest struct {
	Model string `json:"model"`
}

// Decisions serves POST /v1/decisions as a non-streaming passthrough.
func (h *OpenAIProxyHandlers) Decisions(ec echo.Context) error {
	return h.serveDecisionRoute(ec, decisionsEndpointPath)
}

// SystemOne serves POST /v1/systemone as a non-streaming passthrough.
func (h *OpenAIProxyHandlers) SystemOne(ec echo.Context) error {
	return h.serveDecisionRoute(ec, systemOneEndpointPath)
}

func (h *OpenAIProxyHandlers) serveDecisionRoute(ec echo.Context, endpointPath string) error {
	c := must.As[*GatewayContext](ec)
	rawBody, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	// Parse before requiring the function context: a body that is not JSON
	// has no routing key, and the client should hear invalid JSON, not a
	// missing model prefix.
	var request decisionRouteRequest
	if err := json.Unmarshal(rawBody, &request); err != nil {
		return decisionBodyError(err)
	}
	if err := rejectAmbiguousMembers(rawBody, decisionRouteRequest{}); err != nil {
		return err
	}
	reqCtx, err := h.handlers.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}
	requestModel := request.Model
	if err := h.requireDecisionRouteEnabled(c, reqCtx, requestModel, endpointPath); err != nil {
		return err
	}

	inputTokens := estimatedTokenCountForDecisionBody(rawBody, h.decisionImageTokens())
	if err := h.admitProxyRequest(c, reqCtx, inputTokens, 0); err != nil {
		return err
	}

	span := trace.SpanFromContext(c.UserContext())
	span.SetAttributes(
		attribute.String("gateway.endpoint", endpointPath),
		attribute.String("gen_ai.request.model", requestModel),
		attribute.String("gateway.routed_model", reqCtx.Model),
		attribute.String("nvcf.function.id", requestFunctionID(c)),
		attribute.String("nvcf.org.id", reqCtx.OrgID),
		attribute.Int("gen_ai.usage.input_tokens", inputTokens),
	)

	return h.forwardJSONRequest(c, reqCtx, rawBody, inputTokens)
}

// decisionBodyError returns a 400 for a body that could not be bound. Syntax
// errors are marked so System One reports invalid_json rather than
// invalid_request.
func decisionBodyError(err error) error {
	httpErr := echo.NewHTTPError(http.StatusBadRequest, err.Error())
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		httpErr.SetInternal(fmt.Errorf("%w: %w", errInvalidJSON, err))
	}
	return httpErr
}

// requireDecisionRouteEnabled resolves the routed model and refuses it with a
// 404 unless the function declares endpointPath in the model's uris. A model
// with no spec, or a spec with no uris, is not enabled: unlike the other
// routes, these have no default-allow.
func (h *OpenAIProxyHandlers) requireDecisionRouteEnabled(
	c *GatewayContext,
	reqCtx *requestctx.RequestContext,
	requestModel string,
	endpointPath string,
) error {
	routedModel, err := normalizeOpenAIRequestModel(reqCtx, requestModel)
	if err != nil {
		return err
	}
	reqCtx.Model = routedModel
	setRoutingMethodForModel(reqCtx, routedModel)

	if spec, ok := reqCtx.ModelSpecs[routedModel]; ok {
		for _, uri := range spec.URIs {
			if modelURIMatches(uri, endpointPath) {
				return nil
			}
		}
	}

	ctx := c.UserContext()
	telemetry.AddWithContext(
		ctx,
		h.handlers.observability.modelURIAllowlistRejections,
		1,
		attribute.String("endpoint", endpointPath),
	)
	telemetry.Logger(ctx).Warn().
		Str("model", routedModel).
		Str("endpoint", endpointPath).
		Str("mode", "enforce").
		Msg("endpoint not in model uris allowlist")
	return echo.NewHTTPError(
		http.StatusNotFound,
		fmt.Sprintf("model %q is not enabled for %s", routedModel, endpointPath),
	)
}

func (h *OpenAIProxyHandlers) decisionImageTokens() int {
	if h == nil || h.handlers == nil || h.handlers.config == nil {
		return 0
	}
	return h.handlers.config.DecisionImageTokens
}
