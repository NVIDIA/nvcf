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
	"net/http"
	"net/http/httptrace"

	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// otelhttp.Transport does not expose CloseIdleConnections so we have to unwrap it here
func NewOtelTransport(rt http.RoundTripper) http.RoundTripper {
	wrappedRt := &roundTripWrapper{
		RoundTripper: otelhttp.NewTransport(rt, otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
			return otelhttptrace.NewClientTrace(ctx)
		})),
	}
	if idler, ok := wrappedRt.RoundTripper.(closeIdler); ok {
		wrappedRt.idler = idler
	} else if idler, ok := rt.(closeIdler); ok {
		wrappedRt.idler = idler
	}
	return wrappedRt
}

type closeIdler interface {
	CloseIdleConnections()
}

type roundTripWrapper struct {
	http.RoundTripper
	idler closeIdler
}

func (w *roundTripWrapper) CloseIdleConnections() {
	if w.idler != nil {
		w.idler.CloseIdleConnections()
	}
}
