/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gateway

import (
	"ai-api-gateway-service/middleware"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewVanityDirectorRejectsInvalidAPIHost(t *testing.T) {
	tests := []string{
		"",
		"example.com",
		"https://",
		"://example.com",
	}

	for _, tc := range tests {
		t.Run(tc, func(t *testing.T) {
			director, err := NewVanityDirector(tc, http.DefaultTransport)
			assert.Nil(t, director)
			assert.Error(t, err)
		})
	}
}

func newVanityExecRequest(functionID string, functionVersionID string, pathOverride *string, usePexec bool, eol time.Time, offlineMessage string) VanityExecRequest {
	return VanityExecRequest{
		FunctionID:        functionID,
		FunctionVersionID: functionVersionID,
		PathOverride:      pathOverride,
		UsePexec:          usePexec,
		EOL:               eol,
		OfflineMessage:    offlineMessage,
	}
}

func TestDeprecationHeaderWithEOL(t *testing.T) {
	// Use relative dates that are always in the future
	eolDate1 := time.Now().AddDate(1, 0, 0).UTC().Truncate(24 * time.Hour) // 1 year from now
	eolDate2 := time.Now().AddDate(2, 0, 0).UTC().Truncate(24 * time.Hour) // 2 years from now

	tests := []struct {
		name                  string
		eol                   time.Time
		expectedHeaderPresent bool
	}{
		{
			name:                  "EOL date set",
			eol:                   eolDate1,
			expectedHeaderPresent: true,
		},
		{
			name:                  "EOL date empty",
			eol:                   time.Time{},
			expectedHeaderPresent: false,
		},
		{
			name:                  "EOL date with different value",
			eol:                   eolDate2,
			expectedHeaderPresent: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock upstream server
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"result": "success"}`))
			}))
			defer upstreamServer.Close()

			// Create a VanityDirector
			director, err := NewVanityDirector(upstreamServer.URL, http.DefaultTransport)
			assert.NoError(t, err)

			// Create a test request
			req := httptest.NewRequest("POST", "/test", nil)
			recorder := httptest.NewRecorder()

			// Call ServeExec with the EOL parameter
			director.ServeExec(newVanityExecRequest("func-123", "version-456", nil, false, tc.eol, ""), recorder, req)

			// Check the response
			result := recorder.Result()
			defer result.Body.Close()

			// Verify the Deprecation header
			deprecationHeader := result.Header.Get("Deprecation")
			if tc.expectedHeaderPresent {
				assert.NotEmpty(t, deprecationHeader, "Deprecation header should be present")
				assert.Equal(t, tc.eol.Format(time.RFC3339), deprecationHeader, "Deprecation header value should match")
			} else {
				assert.Empty(t, deprecationHeader, "Deprecation header should not be present")
			}
		})
	}
}

func TestDeprecationHeaderWithPexec(t *testing.T) {
	// Create a mock upstream server
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "success"}`))
	}))
	defer upstreamServer.Close()

	// Create a VanityDirector
	director, err := NewVanityDirector(upstreamServer.URL, http.DefaultTransport)
	assert.NoError(t, err)

	// Create a test request
	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	// Call ServeExec with usePexec=true and EOL set (1 year from now)
	eolDate := time.Now().AddDate(1, 0, 0).UTC().Truncate(24 * time.Hour)
	director.ServeExec(newVanityExecRequest("func-123", "version-456", nil, true, eolDate, ""), recorder, req)

	// Check the response
	result := recorder.Result()
	defer result.Body.Close()

	// Verify the Deprecation header is still set even with pexec
	deprecationHeader := result.Header.Get("Deprecation")
	assert.NotEmpty(t, deprecationHeader, "Deprecation header should be present with pexec")
	assert.Equal(t, eolDate.Format(time.RFC3339), deprecationHeader, "Deprecation header value should match")
}

func TestDeprecationHeaderWithPathOverride(t *testing.T) {
	// Create a mock upstream server
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "success"}`))
	}))
	defer upstreamServer.Close()

	// Create a VanityDirector
	director, err := NewVanityDirector(upstreamServer.URL, http.DefaultTransport)
	assert.NoError(t, err)

	// Create a test request
	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	// Call ServeExec with pathOverride and EOL set (1 year from now)
	pathOverride := "/custom/path"
	eolDate := time.Now().AddDate(1, 0, 0).UTC().Truncate(24 * time.Hour)
	director.ServeExec(newVanityExecRequest("func-123", "version-456", &pathOverride, false, eolDate, ""), recorder, req)

	// Check the response
	result := recorder.Result()
	defer result.Body.Close()

	// Verify the Deprecation header is still set with path override
	deprecationHeader := result.Header.Get("Deprecation")
	assert.NotEmpty(t, deprecationHeader, "Deprecation header should be present with path override")
	assert.Equal(t, eolDate.Format(time.RFC3339), deprecationHeader, "Deprecation header value should match")
}

func TestModifyTooManyRequestsResponse(t *testing.T) {
	tests := []struct {
		name            string
		statusCode      int
		responseBody    string
		message         string
		expectedBody    string // for unmodified cases, compared as raw string
		expectedMessage string // for OpenAI format: checks error.message
		expectedDetail  string // for RFC 7807 format: checks detail field
	}{
		{
			name:            "429 with message appends to OpenAI error message",
			statusCode:      http.StatusTooManyRequests,
			responseBody:    `{"error":{"message":"Rate limit exceeded","type":"rate_limit_exceeded","param":null,"code":"429"}}`,
			message:         "Check out this model at a partner!",
			expectedMessage: "Rate limit exceeded Check out this model at a partner!",
		},
		{
			name:         "429 without message passes through unchanged",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error":{"message":"Rate limit exceeded","type":"rate_limit_exceeded","param":null,"code":"429"}}`,
			message:      "",
			expectedBody: `{"error":{"message":"Rate limit exceeded","type":"rate_limit_exceeded","param":null,"code":"429"}}`,
		},
		{
			name:         "Non-429 response is untouched",
			statusCode:   http.StatusOK,
			responseBody: `{"result":"success"}`,
			message:      "Check out this model at a partner!",
			expectedBody: `{"result":"success"}`,
		},
		{
			name:         "429 with non-JSON body passes through unchanged",
			statusCode:   http.StatusTooManyRequests,
			responseBody: "plain text error",
			message:      "Check out this model at a partner!",
			expectedBody: "plain text error",
		},
		{
			name:         "429 with JSON but no recognized format passes through unchanged",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"status":"too many requests"}`,
			message:      "Check out this model at a partner!",
			expectedBody: `{"status":"too many requests"}`,
		},
		{
			name:           "429 with RFC 7807 format sets detail field",
			statusCode:     http.StatusTooManyRequests,
			responseBody:   `{"status":429,"title":"Too Many Requests"}`,
			message:        "Check out this model at a partner!",
			expectedDetail: "Check out this model at a partner!",
		},
		{
			name:           "429 with RFC 7807 format appends to existing detail",
			statusCode:     http.StatusTooManyRequests,
			responseBody:   `{"status":429,"title":"Too Many Requests","detail":"Rate limit exceeded."}`,
			message:        "Check out this model at a partner!",
			expectedDetail: "Rate limit exceeded. Check out this model at a partner!",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/test", nil)
			if tc.message != "" {
				ctx := context.WithValue(req.Context(), tooManyRequestsKey, tc.message)
				req = req.WithContext(ctx)
			}

			resp := &http.Response{
				StatusCode: tc.statusCode,
				Body:       io.NopCloser(bytes.NewBufferString(tc.responseBody)),
				Header:     make(http.Header),
				Request:    req,
			}

			err := modifyTooManyRequestsResponse(resp)
			assert.NoError(t, err)

			body, err := io.ReadAll(resp.Body)
			assert.NoError(t, err)

			switch {
			case tc.expectedMessage != "":
				var parsed map[string]any
				err := json.Unmarshal(body, &parsed)
				assert.NoError(t, err)
				errorObj := parsed["error"].(map[string]any)
				assert.Equal(t, tc.expectedMessage, errorObj["message"])
			case tc.expectedDetail != "":
				var parsed map[string]any
				err := json.Unmarshal(body, &parsed)
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedDetail, parsed["detail"])
			default:
				assert.Equal(t, tc.expectedBody, string(body))
			}
		})
	}
}

func TestTooManyRequestsMessageIntegration(t *testing.T) {
	upstreamBody := `{"error":{"message":"Too many requests","type":"rate_limit_exceeded","param":null,"code":"429"}}`

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(upstreamBody))
	}))
	defer upstreamServer.Close()

	director, err := NewVanityDirector(upstreamServer.URL, http.DefaultTransport)
	assert.NoError(t, err)

	t.Run("429 with message in context", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/test", nil)
		ctx := context.WithValue(req.Context(), tooManyRequestsKey, "Try a partner API!")
		req = req.WithContext(ctx)
		recorder := httptest.NewRecorder()

		director.ServeExec(newVanityExecRequest("func-123", "", nil, false, time.Time{}, ""), recorder, req)

		result := recorder.Result()
		defer result.Body.Close()

		assert.Equal(t, http.StatusTooManyRequests, result.StatusCode)

		body, err := io.ReadAll(result.Body)
		assert.NoError(t, err)

		var parsed map[string]any
		err = json.Unmarshal(body, &parsed)
		assert.NoError(t, err)

		errorObj := parsed["error"].(map[string]any)
		assert.Equal(t, "Too many requests Try a partner API!", errorObj["message"])
	})

	t.Run("429 without message in context", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/test", nil)
		recorder := httptest.NewRecorder()

		director.ServeExec(newVanityExecRequest("func-123", "", nil, false, time.Time{}, ""), recorder, req)

		result := recorder.Result()
		defer result.Body.Close()

		assert.Equal(t, http.StatusTooManyRequests, result.StatusCode)

		body, err := io.ReadAll(result.Body)
		assert.NoError(t, err)

		var parsed map[string]any
		err = json.Unmarshal(body, &parsed)
		assert.NoError(t, err)

		errorObj := parsed["error"].(map[string]any)
		assert.Equal(t, "Too many requests", errorObj["message"])
	})
}

func TestServeExecReturnsProxyErrorAsProblemDetails(t *testing.T) {
	core, observedLogs := observer.New(zap.WarnLevel)
	undoLogger := zap.ReplaceGlobals(zap.New(core))
	defer undoLogger()

	director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}))
	assert.NoError(t, err)

	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	err = director.ServeExec(VanityExecRequest{
		FunctionID:        "func-123",
		FunctionVersionID: "version-456",
	}, recorder, req)

	assert.ErrorIs(t, err, context.Canceled)
	result := recorder.Result()
	defer result.Body.Close()
	assert.Equal(t, http.StatusBadGateway, result.StatusCode)
	assert.Equal(t, "application/problem+json", result.Header.Get("Content-Type"))

	var pd ProblemDetails
	err = json.NewDecoder(result.Body).Decode(&pd)
	assert.NoError(t, err)
	assert.Equal(t, "about:blank", pd.Type)
	assert.Equal(t, "Bad Gateway", pd.Title)
	assert.Equal(t, http.StatusBadGateway, pd.Status)
	assert.Equal(t, "Upstream request failed.", pd.Detail)

	assert.Len(t, observedLogs.FilterMessage("proxy request failed").All(), 1)
}

// A confirmed client disconnect (canceled inbound request context) is accounted
// as 499 rather than a generic 502.
func TestServeExecClientDisconnectReturns499(t *testing.T) {
	core, observedLogs := observer.New(zap.WarnLevel)
	undoLogger := zap.ReplaceGlobals(zap.New(core))
	defer undoLogger()

	director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}))
	assert.NoError(t, err)

	// The caller has already gone away: its request context is canceled before
	// proxying completes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/test", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	err = director.ServeExec(VanityExecRequest{
		FunctionID:        "func-123",
		FunctionVersionID: "version-456",
	}, recorder, req)

	assert.ErrorIs(t, err, context.Canceled)
	result := recorder.Result()
	defer result.Body.Close()
	assert.Equal(t, statusClientClosedRequest, result.StatusCode)
	assert.Equal(t, "application/problem+json", result.Header.Get("Content-Type"))

	var pd ProblemDetails
	err = json.NewDecoder(result.Body).Decode(&pd)
	assert.NoError(t, err)
	assert.Equal(t, "about:blank", pd.Type)
	assert.Equal(t, "Client Closed Request", pd.Title)
	assert.Equal(t, statusClientClosedRequest, pd.Status)

	assert.Len(t, observedLogs.FilterMessage("client closed request before upstream response").All(), 1)
}

// A genuine upstream transport failure (inbound context still live) stays 502.
func TestServeExecGenuineProxyFailureReturns502(t *testing.T) {
	director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	assert.NoError(t, err)

	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	err = director.ServeExec(VanityExecRequest{
		FunctionID:        "func-123",
		FunctionVersionID: "version-456",
	}, recorder, req)

	assert.Error(t, err)
	result := recorder.Result()
	defer result.Body.Close()
	assert.Equal(t, http.StatusBadGateway, result.StatusCode)

	var pd ProblemDetails
	err = json.NewDecoder(result.Body).Decode(&pd)
	assert.NoError(t, err)
	assert.Equal(t, "Bad Gateway", pd.Title)
	assert.Equal(t, http.StatusBadGateway, pd.Status)
}

func TestOfflineMessageReturns503(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when endpoint is offline")
	}))
	defer upstreamServer.Close()

	director, err := NewVanityDirector(upstreamServer.URL, http.DefaultTransport)
	assert.NoError(t, err)

	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	director.ServeExec(newVanityExecRequest("func-123", "version-456", nil, false, time.Time{}, "Endpoint is under maintenance"), recorder, req)

	result := recorder.Result()
	defer result.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, result.StatusCode)
	assert.Equal(t, "application/problem+json", result.Header.Get("Content-Type"))
	assert.Equal(t, "10800", result.Header.Get("Retry-After"))

	var pd ProblemDetails
	err = json.NewDecoder(result.Body).Decode(&pd)
	assert.NoError(t, err)
	assert.Equal(t, "about:blank", pd.Type)
	assert.Equal(t, "Service Unavailable", pd.Title)
	assert.Equal(t, http.StatusServiceUnavailable, pd.Status)
	assert.Equal(t, "Endpoint is under maintenance", pd.Detail)
}

func TestOfflineMessageTakesPriorityOverExpiredEOL(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when endpoint is offline")
	}))
	defer upstreamServer.Close()

	director, err := NewVanityDirector(upstreamServer.URL, http.DefaultTransport)
	assert.NoError(t, err)

	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	expiredEOL := time.Now().AddDate(-1, 0, 0)
	director.ServeExec(newVanityExecRequest("func-123", "version-456", nil, false, expiredEOL, "Down for migration"), recorder, req)

	result := recorder.Result()
	defer result.Body.Close()

	// Should get 503 (offline) not 410 (gone/expired)
	assert.Equal(t, http.StatusServiceUnavailable, result.StatusCode)

	var pd ProblemDetails
	err = json.NewDecoder(result.Body).Decode(&pd)
	assert.NoError(t, err)
	assert.Equal(t, "Service Unavailable", pd.Title)
	assert.Equal(t, "Down for migration", pd.Detail)
}

// End-to-end at the telemetry layer: drive a request through the real otelhttp
// server middleware wrapping ServeExec and assert what http.server.request.duration
// records. A confirmed client disconnect (canceled inbound context) must land as
// http.response.status_code=499, not 502.
func TestServeExecClientDisconnectRecords499Metric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}))
	require.NoError(t, err)

	handler := middleware.ServerTelemetryMiddleware(otelhttp.WithMeterProvider(provider))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = director.ServeExec(VanityExecRequest{FunctionID: "func-123", FunctionVersionID: "version-456"}, w, r)
		}),
	)

	// The caller has already gone away before proxying completes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, statusClientClosedRequest, rec.Code)

	metrics := collectGatewayMetrics(t, reader)
	assert.True(t, gatewayMetricHasStatusCode(metrics, statusClientClosedRequest),
		"gateway must record http.server.request.duration with status_code=499 on client disconnect")
	assert.False(t, gatewayMetricHasStatusCode(metrics, http.StatusBadGateway),
		"client disconnect must not be recorded as 502")
}

// A genuine upstream transport failure (inbound context still live) must be
// recorded as 502, not 499.
func TestServeExecGenuineFailureRecords502Metric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })

	director, err := NewVanityDirector("https://nvcf.example.test", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	require.NoError(t, err)

	handler := middleware.ServerTelemetryMiddleware(otelhttp.WithMeterProvider(provider))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = director.ServeExec(VanityExecRequest{FunctionID: "func-123", FunctionVersionID: "version-456"}, w, r)
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)

	metrics := collectGatewayMetrics(t, reader)
	assert.True(t, gatewayMetricHasStatusCode(metrics, http.StatusBadGateway),
		"genuine upstream failure must record status_code=502")
	assert.False(t, gatewayMetricHasStatusCode(metrics, statusClientClosedRequest),
		"genuine upstream failure must not be recorded as 499")
}

func collectGatewayMetrics(t *testing.T, reader sdkmetric.Reader) []metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Metrics...)
	}
	return out
}

func gatewayMetricHasStatusCode(metrics []metricdata.Metrics, code int) bool {
	for _, metric := range metrics {
		if metric.Name != "http.server.request.duration" {
			continue
		}
		histogram, ok := metric.Data.(metricdata.Histogram[float64])
		if !ok {
			continue
		}
		for _, point := range histogram.DataPoints {
			if value, ok := point.Attributes.Value("http.response.status_code"); ok &&
				value == attribute.Int64Value(int64(code)) {
				return true
			}
		}
	}
	return false
}
