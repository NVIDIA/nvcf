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

// Package clientmetrics instruments NVCA's outbound dependency clients with
// OpenTelemetry metrics. It provides the shared Recorder (the common recording
// logic used by every per-transport decorator) and an http.RoundTripper that
// records the RED metric set for outbound HTTP calls.
package clientmetrics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Peer-service names identify each outbound dependency on its metrics. They are
// the bounded value set for the peer.service label; add a new one here when
// instrumenting a new dependency.
const (
	PeerServiceICMS  = "icms"
	PeerServiceSIS   = "sis"
	PeerServiceReVal = "reval"
	PeerServiceNGC   = "ngc"
)

// meterName is the instrumentation scope for all client metrics.
const meterName = "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/clientmetrics"

// OTel instrument names; the Prometheus bridge renders them with the unit suffix
// (for example http_client_request_duration_seconds).
const (
	durationInstrumentName     = "http.client.request.duration"
	requestBodySizeInstrument  = "http.client.request.body.size"
	responseBodySizeInstrument = "http.client.response.body.size"
)

// Recorder holds the OTel instruments shared by every outbound-client decorator.
// It is the single "record a call" layer: create it once from a MeterProvider
// and reuse it across HTTP and (later) messaging decorators.
type Recorder struct {
	duration     metric.Float64Histogram
	reqBodySize  metric.Int64Histogram
	respBodySize metric.Int64Histogram
	// defaultAttrs are stamped on every recorded series, for example the NVCA
	// default labels (nca id, cluster, version), so OTel-sourced series carry
	// the same identity labels as the legacy client_golang metrics.
	defaultAttrs []attribute.KeyValue
}

// NewRecorder creates a Recorder from the given MeterProvider. defaultAttrs are
// applied to every series it records. Passing the global no-op provider yields
// instruments that record nothing, so a Recorder is always safe to construct and
// call regardless of whether metrics are enabled.
func NewRecorder(mp metric.MeterProvider, defaultAttrs []attribute.KeyValue) (*Recorder, error) {
	meter := mp.Meter(meterName)
	duration, err := meter.Float64Histogram(
		durationInstrumentName,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of outbound HTTP client requests, by dependency and outcome."),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", durationInstrumentName, err)
	}
	reqBodySize, err := meter.Int64Histogram(
		requestBodySizeInstrument,
		metric.WithUnit("By"),
		metric.WithDescription("Size of outbound HTTP client request bodies, in bytes."),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", requestBodySizeInstrument, err)
	}
	respBodySize, err := meter.Int64Histogram(
		responseBodySizeInstrument,
		metric.WithUnit("By"),
		metric.WithDescription("Size of HTTP client response bodies, in bytes."),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", responseBodySizeInstrument, err)
	}
	return &Recorder{
		duration:     duration,
		reqBodySize:  reqBodySize,
		respBodySize: respBodySize,
		defaultAttrs: defaultAttrs,
	}, nil
}

// HTTPObservation is one outbound HTTP call to record. RequestBodySize and
// ResponseBodySize are the declared Content-Length values; a negative value
// means unknown (for example a chunked body) and is not recorded.
type HTTPObservation struct {
	Duration         time.Duration
	RequestBodySize  int64
	ResponseBodySize int64
	Attrs            []attribute.KeyValue
}

// RecordHTTPRequest records one outbound HTTP call: duration, and request and
// response body sizes when known, all keyed by the default attributes plus the
// supplied semconv attributes. A nil Recorder is a no-op so callers need not
// branch on whether instrumentation is enabled.
func (r *Recorder) RecordHTTPRequest(ctx context.Context, obs HTTPObservation) {
	if r == nil || r.duration == nil {
		return
	}
	attrs := obs.Attrs
	if len(r.defaultAttrs) > 0 {
		merged := make([]attribute.KeyValue, 0, len(r.defaultAttrs)+len(attrs))
		merged = append(merged, r.defaultAttrs...)
		merged = append(merged, attrs...)
		attrs = merged
	}
	opt := metric.WithAttributes(attrs...)
	r.duration.Record(ctx, obs.Duration.Seconds(), opt)
	if obs.RequestBodySize >= 0 {
		r.reqBodySize.Record(ctx, obs.RequestBodySize, opt)
	}
	if obs.ResponseBodySize >= 0 {
		r.respBodySize.Record(ctx, obs.ResponseBodySize, opt)
	}
}
