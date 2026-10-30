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

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
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

func TestSystemOneGatewayErrorsUseSystemOneShape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status   int
		code     string
		internal error
	}{
		{http.StatusBadRequest, "invalid_request", nil},
		{http.StatusBadRequest, "invalid_json", errInvalidJSON},
		{http.StatusUnauthorized, "invalid_api_key", nil},
		{http.StatusForbidden, "invalid_request", nil},
		{http.StatusNotFound, "not_found", nil},
		{http.StatusRequestEntityTooLarge, "request_too_large", nil},
		{http.StatusUnprocessableEntity, "invalid_request", nil},
		{http.StatusTooManyRequests, "rate_limit_exceeded", nil},
		{http.StatusInternalServerError, "internal_error", nil},
		{http.StatusNotImplemented, "internal_error", nil},
		{http.StatusBadGateway, "provider_unavailable", nil},
		{http.StatusServiceUnavailable, "provider_unavailable", nil},
		{statusOverloaded, "provider_unavailable", nil},
		{http.StatusGatewayTimeout, "provider_timeout", nil},
	} {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.code), func(t *testing.T) {
			t.Parallel()

			err := echo.NewHTTPError(tc.status, "something failed")
			if tc.internal != nil {
				err = err.SetInternal(tc.internal)
			}
			rec := serveDecisionError(t, "/v1/systemone", nil, err)

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, echo.MIMEApplicationJSON, rec.Header().Get(echo.HeaderContentType))
			require.JSONEq(t, systemOneErrorJSON(t, tc.code, "something failed", "request-a"), rec.Body.String())
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
	require.JSONEq(t, openAIErrorJSON(t, "invalid_request_error", "no such model", nil), rec.Body.String())
	require.Equal(t, "request-a", rec.Header().Get(HeaderRequestID))
	require.Empty(t, rec.Header().Get(headerMessagesRequestID))
}

func TestSystemOneErrorForPlainErrorIsInternalError(t *testing.T) {
	t.Parallel()

	rec := serveDecisionError(t, "/v1/systemone", nil, errors.New("boom"))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.JSONEq(t, systemOneErrorJSON(t, "internal_error", "Internal Server Error", "request-a"), rec.Body.String())
}
