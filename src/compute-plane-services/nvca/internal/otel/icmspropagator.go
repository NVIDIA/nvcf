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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	otelpropagation "go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

const (
	traceparentHeader = "traceparent"
	tracestateHeader  = "tracestate"
)

func ContextWithParentSpanFromICMS(ctx context.Context, srTraceCtxCfg nvcav2beta1.ICMSRequestTraceContextConfig) context.Context {
	var err error
	traceState := oteltrace.TraceState{}
	for k, v := range srTraceCtxCfg.TraceState {
		traceState, err = traceState.Insert(k, v)
		if err != nil {
			core.GetLogger(ctx).WithError(err).Warnf("failed to propagate trace state %s=%s", k, v)
		}
	}

	// We don't need to validate the traceparent is empty, let the downstream do that,
	// and we'll put a test on it
	return otelpropagation.TraceContext{}.Extract(ctx, otelpropagation.MapCarrier{
		traceparentHeader: srTraceCtxCfg.TraceParent,
		tracestateHeader:  traceState.String(),
	})
}
