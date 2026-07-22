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

package clientmetrics

import (
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/semconv"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/semconv/httpsemconv"
)

// TransportOption configures the metrics RoundTripper.
type TransportOption func(*roundTripper)

// WithURLTemplate sets a fixed, low-cardinality url.template for every request
// this transport carries (for example when a client only calls one route). Omit
// it unless a safe template is known; a raw path must never be used.
func WithURLTemplate(template string) TransportOption {
	return func(rt *roundTripper) {
		rt.urlTemplate = template
	}
}

// NewTransport wraps inner with a RoundTripper that records the RED metric set
// (via the shared Recorder) for every request, tagged with peer.service.
//
// It is meter-gated by construction: when rec is nil the inner transport is
// returned unchanged, so with metrics disabled there is zero overhead and no
// behavioural change. When inner is nil, http.DefaultTransport is used.
func NewTransport(inner http.RoundTripper, rec *Recorder, peerService string, opts ...TransportOption) http.RoundTripper {
	if rec == nil {
		if inner == nil {
			return http.DefaultTransport
		}
		return inner
	}
	if inner == nil {
		inner = http.DefaultTransport
	}
	rt := &roundTripper{inner: inner, rec: rec, peerService: peerService}
	for _, opt := range opts {
		opt(rt)
	}
	return rt
}

type roundTripper struct {
	inner       http.RoundTripper
	rec         *Recorder
	peerService string
	urlTemplate string
}

// RoundTrip times the inner round trip and records the outcome. It never alters
// the request or response and returns the inner error unchanged.
func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := rt.inner.RoundTrip(req)
	dur := time.Since(start)

	statusCode := 0
	respBodySize := int64(-1)
	if resp != nil {
		statusCode = resp.StatusCode
		respBodySize = resp.ContentLength
	}
	errType := semconv.ClassifyError(err)

	// A per-request template (set by a multi-route client via context) takes
	// precedence over the transport's static template.
	urlTemplate := rt.urlTemplate
	if ctxTemplate := URLTemplateFromContext(req.Context()); ctxTemplate != "" {
		urlTemplate = ctxTemplate
	}

	attrs := httpsemconv.ClientAttrs(
		rt.peerService,
		req.Method,
		serverAddress(req),
		urlTemplate,
		statusCode,
		errType,
	)
	rt.rec.RecordHTTPRequest(req.Context(), HTTPObservation{
		Duration:         dur,
		RequestBodySize:  req.ContentLength,
		ResponseBodySize: respBodySize,
		Attrs:            attrs,
	})

	return resp, err
}

// serverAddress returns the target host (without port credentials) for the
// server.address attribute.
func serverAddress(req *http.Request) string {
	if req.URL == nil {
		return ""
	}
	return req.URL.Hostname()
}
