// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvcf/src/invocation-plane-services/llm-gateway/config"
)

// decisionErrorAPI serves path with a handler that sets backendHeaders, as a
// relayed backend response would, and then fails with err.
func decisionErrorAPI(path string, backendHeaders http.Header, err error) *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = newHTTPErrorHandler(false)
	e.Use(NewContextMiddleware(config.Default()))
	e.POST(path, func(c echo.Context) error {
		for key, values := range backendHeaders {
			for _, value := range values {
				c.Response().Header().Add(key, value)
			}
		}
		return err
	})
	return e
}

func serveDecisionError(t *testing.T, path string, backendHeaders http.Header, err error) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(HeaderRequestID, "request-a")
	rec := httptest.NewRecorder()
	decisionErrorAPI(path, backendHeaders, err).ServeHTTP(rec, req)
	return rec
}

func systemOneErrorJSON(t *testing.T, code, message, requestID string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"error":      map[string]string{"code": code, "message": message, "request_id": requestID},
		"request_id": requestID,
	})
	require.NoError(t, err)
	return string(body)
}

func TestErrorShapeFor(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]errorShape{
		"/v1/messages":         shapeAnthropic,
		"/v1/decisions":        shapeOpenAI,
		"/v1/systemone":        shapeSystemOne,
		"/v1/chat/completions": shapeOpenAI,
		"/v1/embeddings":       shapeOpenAI,
		"/unknown":             shapeOpenAI,
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		require.Equal(t, want, errorShapeFor(req), path)
	}
}

// decisionErrorCodeCases is the canonical status-to-code table the gateway and
// Vanity share for the decision routes.
var decisionErrorCodeCases = []struct {
	status                       int
	internal                     error
	openAIType                   string
	decisionsCode, systemOneCode string
}{
	{http.StatusBadRequest, nil, "invalid_request_error", "invalid_request", "invalid_request"},
	{http.StatusBadRequest, errInvalidJSON, "invalid_request_error", "invalid_json", "invalid_json"},
	{http.StatusUnauthorized, nil, "authentication_error", "invalid_api_key", "invalid_api_key"},
	{http.StatusForbidden, nil, "permission_error", "forbidden", "forbidden"},
	{http.StatusNotFound, nil, "invalid_request_error", "model_not_found", "not_found"},
	{http.StatusMethodNotAllowed, nil, "invalid_request_error", "invalid_request", "invalid_request"},
	{http.StatusGone, nil, "invalid_request_error", "model_expired", "not_found"},
	{http.StatusRequestEntityTooLarge, nil, "invalid_request_error", "request_too_large", "request_too_large"},
	{http.StatusUnsupportedMediaType, nil, "invalid_request_error", "unsupported_media_type", "unsupported_media_type"},
	{http.StatusUnprocessableEntity, nil, "invalid_request_error", "invalid_request", "invalid_request"},
	{http.StatusTooManyRequests, nil, "rate_limit_error", "rate_limit_exceeded", "rate_limit_exceeded"},
	{499, nil, "invalid_request_error", "request_cancelled", "request_cancelled"},
	{http.StatusInternalServerError, nil, "server_error", "internal_error", "internal_error"},
	{http.StatusNotImplemented, nil, "server_error", "internal_error", "internal_error"},
	{http.StatusBadGateway, nil, "server_error", "provider_unavailable", "provider_unavailable"},
	{http.StatusServiceUnavailable, nil, "server_error", "provider_unavailable", "provider_unavailable"},
	{statusOverloaded, nil, "overloaded_error", "provider_unavailable", "provider_unavailable"},
	{http.StatusGatewayTimeout, nil, "server_error", "provider_timeout", "provider_timeout"},
}

func TestDecisionsGatewayErrorsUseCanonicalCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range decisionErrorCodeCases {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.decisionsCode), func(t *testing.T) {
			t.Parallel()

			err := echo.NewHTTPError(tc.status, "something failed")
			if tc.internal != nil {
				err = err.SetInternal(tc.internal)
			}
			rec := serveDecisionError(t, "/v1/decisions", nil, err)

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.JSONEq(t, openAIErrorJSON(t, tc.openAIType, "something failed", tc.decisionsCode), rec.Body.String())
		})
	}
}

func TestSystemOneGatewayErrorsUseSystemOneShape(t *testing.T) {
	t.Parallel()

	for _, tc := range decisionErrorCodeCases {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.systemOneCode), func(t *testing.T) {
			t.Parallel()

			err := echo.NewHTTPError(tc.status, "something failed")
			if tc.internal != nil {
				err = err.SetInternal(tc.internal)
			}
			rec := serveDecisionError(t, "/v1/systemone", nil, err)

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, echo.MIMEApplicationJSON, rec.Header().Get(echo.HeaderContentType))
			require.JSONEq(t, systemOneErrorJSON(t, tc.systemOneCode, "something failed", "request-a"), rec.Body.String())
			require.Equal(t, "request-a", rec.Header().Get(HeaderRequestID))
			require.Empty(t, rec.Header().Get(headerMessagesRequestID))
			for name := range rec.Header() {
				require.False(t, strings.HasPrefix(strings.ToLower(name), "x-system-one-"), name)
			}
		})
	}
}

func TestSystemOneErrorRequestIDMatchesHeaderAfterBackendHeadersCopied(t *testing.T) {
	t.Parallel()

	rec := serveDecisionError(t, "/v1/systemone",
		http.Header{HeaderRequestID: {"backend-id"}, "X-System-One-Error-Code": {"backend_code"}},
		echo.NewHTTPError(http.StatusBadGateway, "router rejected"))

	require.Equal(t, "request-a", rec.Header().Get(HeaderRequestID))
	require.JSONEq(t, systemOneErrorJSON(t, "provider_unavailable", "router rejected", "request-a"), rec.Body.String())
}

func TestDecisionsGatewayErrorsUseOpenAIShape(t *testing.T) {
	t.Parallel()

	rec := serveDecisionError(t, "/v1/decisions", nil, echo.NewHTTPError(http.StatusNotFound, "no such model"))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.JSONEq(t, openAIErrorJSON(t, "invalid_request_error", "no such model", "model_not_found"), rec.Body.String())
	require.Equal(t, "request-a", rec.Header().Get(HeaderRequestID))
	require.Empty(t, rec.Header().Get(headerMessagesRequestID))
}

func TestSystemOneErrorForPlainErrorIsInternalError(t *testing.T) {
	t.Parallel()

	rec := serveDecisionError(t, "/v1/systemone", nil, errors.New("boom"))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.JSONEq(t, systemOneErrorJSON(t, "internal_error", "Internal Server Error", "request-a"), rec.Body.String())
}
