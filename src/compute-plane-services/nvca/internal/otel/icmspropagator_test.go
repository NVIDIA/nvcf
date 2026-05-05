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

package otel

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"

	"github.com/stretchr/testify/assert"
)

func TestExtractSpanFromICMS(t *testing.T) {
	tests := []struct {
		name           string
		otelSpanCtx    nvcav2beta1.ICMSRequestTraceContextConfig
		wantCtx        context.Context
		wantTraceID    string
		wantTraceState string
	}{
		{
			name:        "empty otelSpanCtx",
			otelSpanCtx: nvcav2beta1.ICMSRequestTraceContextConfig{},
			wantCtx:     context.Background(),
		},
		{
			name: "empty traceparent",
			otelSpanCtx: nvcav2beta1.ICMSRequestTraceContextConfig{
				TraceParent: "",
				TraceState:  map[string]string{},
			},
			wantCtx: context.Background(),
		},
		{
			name: "valid traceparent and tracestate",
			otelSpanCtx: nvcav2beta1.ICMSRequestTraceContextConfig{
				TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
				TraceState: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			wantCtx:        nil,
			wantTraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
			wantTraceState: "key1=value1,key2=value2",
		},
		{
			name: "invalid traceparent",
			otelSpanCtx: nvcav2beta1.ICMSRequestTraceContextConfig{
				TraceParent: "0000f067aa0ba902b7-01",
				TraceState: map[string]string{
					"key1": "value1",
					"key2": "", // invalid value
				},
			},
			wantCtx: context.Background(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			gotCtx := ContextWithParentSpanFromICMS(ctx, tt.otelSpanCtx)
			gotSpan := trace.SpanFromContext(gotCtx)
			if tt.wantCtx == nil {
				assert.Equal(t, tt.wantTraceID, gotSpan.SpanContext().TraceID().String())
				assert.ElementsMatch(t, strings.Split(tt.wantTraceState, ","), strings.Split(gotSpan.SpanContext().TraceState().String(), ","))
			} else {
				assert.Equal(t, tt.wantCtx, gotCtx)
			}
		})
	}
}
