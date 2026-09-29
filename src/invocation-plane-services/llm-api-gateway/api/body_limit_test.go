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
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
)

func newBodyLimitAPI(limit int64, handlerBody *string) *echo.Echo {
	cfg := config.Default()
	cfg.Server.MaxRequestBodyBytes = limit

	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.POST("/", func(c echo.Context) error {
		// Read twice, as the routing-key lookup and the handler both do.
		first, err := captureRequestBody(c.Request())
		if err != nil {
			return err
		}
		second, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		if string(first) != string(second) {
			return errors.New("second read differs from first read")
		}
		*handlerBody = string(second)
		return c.NoContent(http.StatusOK)
	})
	return e
}

func TestContextMiddlewareEnforcesRequestBodyLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		limit         int64
		body          string
		unknownLength bool
		wantStatus    int
	}{
		{name: "disabled", limit: 0, body: strings.Repeat("a", 64), wantStatus: http.StatusOK},
		{name: "at limit", limit: 8, body: "12345678", wantStatus: http.StatusOK},
		{name: "declared length over limit", limit: 8, body: "123456789", wantStatus: http.StatusRequestEntityTooLarge},
		{name: "streamed body over limit", limit: 8, body: "123456789", unknownLength: true, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "streamed body at limit", limit: 8, body: "12345678", unknownLength: true, wantStatus: http.StatusOK},
		{name: "maximum limit", limit: math.MaxInt64, body: "12345678", unknownLength: true, wantStatus: http.StatusOK},
		{name: "empty body", limit: 8, body: "", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handlerBody := "not called"
			e := newBodyLimitAPI(tt.limit, &handlerBody)

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.unknownLength {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.NotEmpty(t, rec.Header().Get(HeaderRequestID), "context middleware handled the request")
			if tt.wantStatus == http.StatusOK {
				require.Equal(t, tt.body, handlerBody)
				return
			}
			require.Equal(t, "not called", handlerBody)
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read tcp 10.0.0.1:8080: i/o timeout")
}

func TestContextMiddlewareHidesRequestBodyReadError(t *testing.T) {
	t.Parallel()

	handlerBody := "not called"
	e := newBodyLimitAPI(8, &handlerBody)

	req := httptest.NewRequest(http.MethodPost, "/", failingReader{})
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "failed to read request body")
	require.NotContains(t, rec.Body.String(), "10.0.0.1")
	require.NotContains(t, rec.Body.String(), "timeout")
	require.Equal(t, "not called", handlerBody)
}

func TestContextMiddlewareRecordsMetricsForRejectedBodies(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldMeterProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(meterProvider)
	t.Cleanup(func() {
		otel.SetMeterProvider(oldMeterProvider)
		_ = meterProvider.Shutdown(context.Background())
	})

	handlerBody := "not called"
	e := newBodyLimitAPI(8, &handlerBody)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("123456789"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	metrics := collectMetrics(t, reader)
	want := map[string]string{
		"method":      http.MethodPost,
		"route":       "/",
		"status":      "413",
		"function_id": "none",
	}
	assertMetricHasAttributes(t, metrics, "llm_api_gateway_http_requests_total", want)
	assertMetricHasAttributes(t, metrics, "llm_api_gateway_http_request_duration_seconds", want)
}

type countingReader struct {
	reader io.Reader
	read   int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

// The limit must apply before anything buffers the body, including the
// routing-key lookup, or an oversized body is still read into memory.
func TestContextMiddlewareStopsReadingOversizedBodies(t *testing.T) {
	t.Parallel()

	for _, unknownLength := range []bool{false, true} {
		handlerBody := "not called"
		e := newBodyLimitAPI(8, &handlerBody)

		body := &countingReader{reader: strings.NewReader(`{"model":"fn-a/m","padding":"` + strings.Repeat("x", 1<<20) + `"}`)}
		req := httptest.NewRequest(http.MethodPost, "/", body)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.ContentLength = int64(1<<20 + 30)
		if unknownLength {
			req.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		require.LessOrEqual(t, body.read, 9, "unknown length = %v", unknownLength)
	}
}
