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

package tracing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNatsHeaderCarrier(t *testing.T) {
	c := NatsHeaderCarrier{}
	c.Set("Traceparent", "00-abc")
	c.Set("X-Other", "v")

	require.Equal(t, "00-abc", c.Get("Traceparent"))
	// The whole reason this carrier exists: nats.Header is case-sensitive, unlike
	// the stdlib http.Header-based carrier.
	require.Equal(t, "", c.Get("traceparent"), "lookup must be case-sensitive")
	require.ElementsMatch(t, []string{"Traceparent", "X-Other"}, c.Keys())
}

func TestRecordSpanError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	_, span := tp.Tracer("test").Start(context.Background(), "op")

	want := errors.New("boom")
	require.Equal(t, want, RecordSpanError(span, want), "returns the same error for chaining")
	span.End()

	ended := sr.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, codes.Error, ended[0].Status().Code)
	require.Equal(t, "boom", ended[0].Status().Description)
	require.NotEmpty(t, ended[0].Events(), "RecordError adds an exception event")
}
