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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/telemetry"
)

func TestNewContextMiddlewareRecordsTraceParentAndStatus(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() {
		_ = tracerProvider.Shutdown(context.Background())
	}()

	oldTracer := telemetry.Tracer
	telemetry.Tracer = sync.OnceValue(func() trace.Tracer {
		return tracerProvider.Tracer("test")
	})
	defer func() {
		telemetry.Tracer = oldTracer
	}()

	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(oldPropagator)

	cfg := config.Default()

	e := echo.New()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"fn-chat/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")

	rec := httptest.NewRecorder()
	ec := e.NewContext(req, rec)

	handler := NewContextMiddleware(cfg)(func(ec echo.Context) error {
		return ec.NoContent(http.StatusAccepted)
	})

	if err := handler(ec); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}

	span := spans[0]
	if got := span.Parent().TraceID().String(); got != "11111111111111111111111111111111" {
		t.Fatalf("parent trace id = %q, want %q", got, "11111111111111111111111111111111")
	}
	if got := span.Parent().SpanID().String(); got != "2222222222222222" {
		t.Fatalf("parent span id = %q, want %q", got, "2222222222222222")
	}

	attrs := span.Attributes()
	assertHasAttribute(t, attrs, "http.response.status_code", int64(http.StatusAccepted))
	assertHasAttribute(t, attrs, "url.path", "/v1/chat/completions")
	assertHasAttribute(t, attrs, "http.request.method", http.MethodPost)
}

func assertHasAttribute(t *testing.T, attrs []attribute.KeyValue, key string, want any) {
	t.Helper()

	for _, attr := range attrs {
		if string(attr.Key) != key {
			continue
		}
		switch v := want.(type) {
		case int64:
			if attr.Value.AsInt64() != v {
				t.Fatalf("attribute %q = %d, want %d", key, attr.Value.AsInt64(), v)
			}
		case string:
			if attr.Value.AsString() != v {
				t.Fatalf("attribute %q = %q, want %q", key, attr.Value.AsString(), v)
			}
		default:
			t.Fatalf("unsupported wanted type %T", want)
		}
		return
	}

	t.Fatalf("missing attribute %q", key)
}
