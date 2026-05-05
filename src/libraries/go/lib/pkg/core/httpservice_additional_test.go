// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	logrutest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
)

func TestNextRequestID(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	var counter uint64

	// Test sequential IDs
	id1 := nextRequestID(ctx, &counter)
	assert.Equal(t, "000001", id1)

	id2 := nextRequestID(ctx, &counter)
	assert.Equal(t, "000002", id2)

	// Test rollover after 999999
	counter = 999999
	id3 := nextRequestID(ctx, &counter)
	assert.Equal(t, "000000", id3)

	// Test multiple calls
	for i := 1; i <= 100; i++ {
		id := nextRequestID(ctx, &counter)
		assert.Len(t, id, 6)
	}
}

func TestHTTPMiddlewareWithAllOptions(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	reg := prometheus.NewRegistry()

	r := mux.NewRouter()
	r.Use(NewHTTPMiddleware(ctx,
		WithRequestMetrics("testservice"),
		WithHandlerTimeout(time.Second),
		WithRequestBodyLimit(100),
		WithPrometheusRegisterer(reg),
		WithTelemetry("testservice", attribute.String("env", "test")),
	)...)

	r.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}).Methods("GET")

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "ok", string(body))
}

func TestHTTPMiddlewareWithZeroTimeout(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	r := mux.NewRouter()
	r.Use(NewHTTPMiddleware(ctx, WithHandlerTimeout(0))...)

	r.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}).Methods("GET")

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTTPMiddlewareWithZeroBodyLimit(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	r := mux.NewRouter()
	r.Use(NewHTTPMiddleware(ctx, WithRequestBodyLimit(0))...)

	r.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}).Methods("POST")

	largeBody := strings.Repeat("a", 10*1024*1024) // 10MB
	req := httptest.NewRequest("POST", "/test", strings.NewReader(largeBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, largeBody, string(body))
}

func TestHTTPServiceWithCustomTimeouts(t *testing.T) {
	s := NewHTTPService("localhost:0")
	s.ReadTimeout = 30 * time.Second
	s.WriteTimeout = 60 * time.Second
	s.IdleTimeout = 300 * time.Second
	s.ShutDownGracePeriod = 10 * time.Second
	s.SocketPermission = 0755

	assert.Equal(t, 30*time.Second, s.ReadTimeout)
	assert.Equal(t, 60*time.Second, s.WriteTimeout)
	assert.Equal(t, 300*time.Second, s.IdleTimeout)
	assert.Equal(t, 10*time.Second, s.ShutDownGracePeriod)
	assert.Equal(t, 0755, int(s.SocketPermission))
}

func TestHTTPHealthHandler_Success(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	handler := HTTPHealthHandler(ctx)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "ok\n", string(body))
}

func TestHTTPVersionHandler_WithGitHash(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	version.Version = "1.0.0"
	version.GitHash = "abc123"
	defer func() {
		version.Version = ""
		version.GitHash = ""
	}()

	handler := HTTPVersionHandler(ctx)

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "1.0.0+abc123\n", string(body))
}

func TestHTTPAddAdminRoute(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	r := mux.NewRouter()
	HTTPAddAdminRoute(ctx, r)

	req := httptest.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "log-level")
}

func TestHTTPMiddleware_CreatesDefaultExcludedRoutes(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	middlewares := NewHTTPMiddleware(ctx, WithTelemetry("test"))
	assert.NotNil(t, middlewares)
	assert.Greater(t, len(middlewares), 0)
}

func TestHTTPAddHealthRoute(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	r := mux.NewRouter()
	HTTPAddHealthRoute(ctx, r)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTTPAddVersionRoute(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	version.Version = "2.0.0"
	version.GitHash = "def456"
	defer func() { version.Version, version.GitHash = "", "" }()

	r := mux.NewRouter()
	HTTPAddVersionRoute(ctx, r)

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "2.0.0")
}

func TestHTTPAddMetricsRoute(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	r := mux.NewRouter()
	HTTPAddMetricsRoute(ctx, r)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	// Prometheus metrics handler returns some default metrics
	assert.NotEmpty(t, string(body))
}

func TestHTTPCodeError_Error(t *testing.T) {
	err := HTTPCodeError(404)
	assert.Equal(t, "unexpected HTTP status code 404", err.Error())

	err = HTTPCodeError(500)
	assert.Equal(t, "unexpected HTTP status code 500", err.Error())

	err = HTTPCodeError(200)
	assert.Equal(t, "unexpected HTTP status code 200", err.Error())
}

func TestHTTPCodeError_MultipleStatusCodes(t *testing.T) {
	testCases := []struct {
		code     int
		expected string
	}{
		{100, "unexpected HTTP status code 100"},
		{201, "unexpected HTTP status code 201"},
		{301, "unexpected HTTP status code 301"},
		{400, "unexpected HTTP status code 400"},
		{401, "unexpected HTTP status code 401"},
		{403, "unexpected HTTP status code 403"},
		{404, "unexpected HTTP status code 404"},
		{500, "unexpected HTTP status code 500"},
		{502, "unexpected HTTP status code 502"},
		{503, "unexpected HTTP status code 503"},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("code_%d", tc.code), func(t *testing.T) {
			err := HTTPCodeError(tc.code)
			assert.Equal(t, tc.expected, err.Error())
		})
	}
}

func TestHTTPMiddleware_MultipleAttributes(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	reg := prometheus.NewRegistry()

	r := mux.NewRouter()
	r.Use(NewHTTPMiddleware(ctx,
		WithTelemetry("testservice",
			attribute.String("env", "test"),
			attribute.String("region", "us-west"),
			attribute.Int("version", 1),
		),
		WithRequestMetrics("testmetrics"),
		WithPrometheusRegisterer(reg),
	)...)

	r.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}).Methods("GET")

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTTPService_AddRoutes(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	s := NewHTTPService("localhost:0")

	s.AddHealthRoute(ctx)
	s.AddVersionRoute(ctx)
	s.AddMetricsRoute(ctx)
	s.AddAdminRoute(ctx)

	// Verify routes are added
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest("GET", "/version", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest("GET", "/metrics", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest("GET", "/admin", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestWithRequestMetrics_EmptyString(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	r := mux.NewRouter()
	r.Use(NewHTTPMiddleware(ctx, WithRequestMetrics(""))...)

	r.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}).Methods("GET")

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestWithTelemetry_EmptyAttributes(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	r := mux.NewRouter()
	r.Use(NewHTTPMiddleware(ctx, WithTelemetry("service"))...)

	r.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}).Methods("GET")

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// spyLogger creates a logrus logger whose entries are captured by the returned
// hook, and injects it into ctx so that GetLogger(ctx) returns it.
func spyLogger(ctx context.Context) (context.Context, *logrutest.Hook) {
	logger, hook := logrutest.NewNullLogger()
	logger.SetLevel(logrus.ErrorLevel)
	return WithLogger(ctx, logger.WithContext(ctx)), hook
}

// countErrors returns the number of entries at ErrorLevel or above.
func countErrors(hook *logrutest.Hook) int {
	n := 0
	for _, e := range hook.AllEntries() {
		if e.Level <= logrus.ErrorLevel {
			n++
		}
	}
	return n
}

func TestHTTPHealthHandler_WriteFails(t *testing.T) {
	// Build a spy-logger context and inject it into the request so that
	// GetLogger(r.Context()) inside the handler records log.Error calls.
	baseCtx := context.Background()
	spyCtx, hook := spyLogger(baseCtx)

	handler := HTTPHealthHandler(spyCtx)

	req := httptest.NewRequest("GET", "/healthz", nil)
	req = req.WithContext(spyCtx) // handler reads logger from request context
	bw := &brokenWriter{}

	// Handler must complete without panicking.
	assert.NotPanics(t, func() {
		handler.ServeHTTP(bw, req)
	})

	// The first Write call was attempted and failed.
	assert.True(t, bw.writeCalled, "expected Write to be called")

	// The body write fails, triggering the error path which calls http.Error;
	// http.Error makes a second Write that also fails — so writeCount == 2 and
	// no successful write ever occurred after the first failure.
	assert.Equal(t, 2, bw.writeCount,
		"expected exactly two Write attempts (body + http.Error), both failing")

	// The handler must have logged at least one error.
	assert.GreaterOrEqual(t, countErrors(hook), 1,
		"expected at least one Error log entry from the handler")
}

func TestHTTPVersionHandler_WriteFails(t *testing.T) {
	version.Version = "1.0.0"
	version.GitHash = "abc"
	defer func() { version.Version, version.GitHash = "", "" }()

	// Build a spy-logger context and inject it into the request so that
	// GetLogger(r.Context()) inside the handler records log.Error calls.
	baseCtx := context.Background()
	spyCtx, hook := spyLogger(baseCtx)

	handler := HTTPVersionHandler(spyCtx)

	req := httptest.NewRequest("GET", "/version", nil)
	req = req.WithContext(spyCtx) // handler reads logger from request context
	bw := &brokenWriter{}

	// Handler must complete without panicking.
	assert.NotPanics(t, func() {
		handler.ServeHTTP(bw, req)
	})

	// The first Write call was attempted and failed.
	assert.True(t, bw.writeCalled, "expected Write to be called")

	// The body write fails, triggering the error path which calls http.Error;
	// http.Error makes a second Write that also fails — so writeCount == 2 and
	// no successful write ever occurred after the first failure.
	assert.Equal(t, 2, bw.writeCount,
		"expected exactly two Write attempts (body + http.Error), both failing")

	// The handler must have logged at least one error.
	assert.GreaterOrEqual(t, countErrors(hook), 1,
		"expected at least one Error log entry from the handler")
}
