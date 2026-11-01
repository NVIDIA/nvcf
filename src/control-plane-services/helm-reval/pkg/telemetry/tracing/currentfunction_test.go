// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package tracing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/tracing"
)

func TestCurrentFunction_Skip0_ReturnsSelf(t *testing.T) {
	full, short := tracing.CurrentFunction(0)
	// skip=0 → points at the CurrentFunction implementation itself
	assert.NotEmpty(t, full)
	assert.NotEmpty(t, short)
	assert.True(t, strings.Contains(full, "currentfunction.go") || strings.Contains(full, "tracing"),
		"expected reference to tracing package, got: %s", full)
}

func TestCurrentFunction_Skip1_ReturnsCaller(t *testing.T) {
	full, short := tracing.CurrentFunction(1)
	// skip=1 → should point at this test function
	assert.NotEmpty(t, full)
	assert.NotEmpty(t, short)
	assert.True(t, strings.Contains(full, "currentfunction_test") || strings.Contains(short, "TestCurrentFunction"),
		"expected caller reference, got full=%s short=%s", full, short)
}

func TestCurrentFunction_HighSkip_ReturnsUnknown(t *testing.T) {
	// A very large skip value exceeds the call stack and returns "unknown".
	full, short := tracing.CurrentFunction(9999)
	assert.Equal(t, "unknown", full)
	assert.Equal(t, "unknown", short)
}

func TestCurrentFunction_Skip1_ContainsLineNumber(t *testing.T) {
	full, _ := tracing.CurrentFunction(1)
	// Full format is "file:line [funcname]"
	assert.Contains(t, full, ":")
	assert.Contains(t, full, "[")
	assert.Contains(t, full, "]")
}

// ── GetTracer ─────────────────────────────────────────────────────────────────

func TestGetTracer_ReturnsNonNil(t *testing.T) {
	tr := tracing.GetTracer()
	require.NotNil(t, tr)
}

// ── GetTraceID ────────────────────────────────────────────────────────────────

func TestGetTraceID_NoSpan_ReturnsEmpty(t *testing.T) {
	id := tracing.GetTraceID(context.Background())
	assert.Equal(t, "", id)
}

func TestGetTraceID_WithRealSpan_ReturnsNonEmpty(t *testing.T) {
	// Use an SDK tracer provider to get a real span with a valid trace ID.
	tp := sdktrace.NewTracerProvider()
	tracer := tp.Tracer("test-tracer")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	id := tracing.GetTraceID(ctx)
	assert.NotEmpty(t, id, "expected a non-empty trace ID from a real SDK span")
}

// ── NewOtelTraceMiddleware ────────────────────────────────────────────────────

func TestNewOtelTraceMiddleware_CallsNext(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	mw := tracing.NewOtelTraceMiddleware()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, r)
	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
}
