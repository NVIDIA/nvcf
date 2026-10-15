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
	PeerServiceFNDS  = "fnds"
	PeerServiceSQS   = "sqs"
	PeerServiceNATS  = "nats"
	PeerServiceAuth  = "auth"
)

// meterName is the instrumentation scope for all client metrics.
const meterName = "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/clientmetrics"

// DurationBucketsSeconds are the explicit histogram boundaries for operation
// duration, in seconds. They follow the OpenTelemetry Semantic Conventions
// recommendation for http.client.request.duration and cover the millisecond to
// ten-second range NVCA's dependencies actually operate in. The OTel SDK
// default boundaries top out at 10000 and are shaped for milliseconds, so with
// a seconds-valued instrument every observation would land in one bucket and
// quantile queries would be meaningless.
var DurationBucketsSeconds = []float64{
	0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10,
}

// SizeBucketsBytes are the explicit histogram boundaries for payload size, in
// bytes. The decade progression keeps a bounded bucket count while still
// resolving the kilobyte to megabyte range NVCA payloads fall in.
var SizeBucketsBytes = []float64{
	0, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000,
}

// InstrumentSpec declares one histogram instrument in OTel semconv terms. Name
// is the semconv instrument name; the Prometheus bridge appends the unit suffix
// (http.client.request.duration with unit "s" becomes
// http_client_request_duration_seconds).
type InstrumentSpec struct {
	Name        string
	Unit        string
	Description string
	Buckets     []float64
}

// Family groups the instruments recorded for one transport type. Duration is
// required and is what gives Rate, Errors and Duration; RequestSize and
// ResponseSize are optional and omitted for transports with no payload size.
//
// Declare one Family per transport. Adding a transport means adding a Family
// and a decorator, never editing the Recorder.
type Family struct {
	Duration     InstrumentSpec
	RequestSize  *InstrumentSpec
	ResponseSize *InstrumentSpec
}

// HTTPClientFamily is the semconv instrument set for outbound HTTP calls.
var HTTPClientFamily = Family{
	Duration: InstrumentSpec{
		Name:        "http.client.request.duration",
		Unit:        "s",
		Description: "Duration of outbound HTTP client requests, by dependency and outcome.",
		Buckets:     DurationBucketsSeconds,
	},
	RequestSize: &InstrumentSpec{
		Name:        "http.client.request.body.size",
		Unit:        "By",
		Description: "Size of outbound HTTP client request bodies, in bytes.",
		Buckets:     SizeBucketsBytes,
	},
	ResponseSize: &InstrumentSpec{
		Name:        "http.client.response.body.size",
		Unit:        "By",
		Description: "Size of HTTP client response bodies, in bytes.",
		Buckets:     SizeBucketsBytes,
	},
}

// MessagingClientFamily is the semconv instrument set for queue operations
// (SQS, NATS). Messages carry no Content-Length, so only duration is recorded.
var MessagingClientFamily = Family{
	Duration: InstrumentSpec{
		Name:        "messaging.client.operation.duration",
		Unit:        "s",
		Description: "Duration of messaging client operations, by dependency and outcome.",
		Buckets:     DurationBucketsSeconds,
	},
}

// Recorder owns the meter and the NVCA default labels. It is the single
// "record an operation" layer: create it once from a MeterProvider, then
// resolve one Instruments set per transport Family.
type Recorder struct {
	meter metric.Meter
	// defaultAttrs are stamped on every recorded series, for example the NVCA
	// default labels (nca id, cluster, version), so OTel-sourced series carry
	// the same identity labels as the legacy client_golang metrics.
	defaultAttrs []attribute.KeyValue
	// http is the pre-resolved HTTP instrument set backing RecordHTTPRequest.
	http *Instruments
}

// Instruments is a resolved instrument set for one Family. Resolve it once at
// construction and reuse it per call; a nil Instruments records nothing.
type Instruments struct {
	rec          *Recorder
	duration     metric.Float64Histogram
	requestSize  metric.Int64Histogram
	responseSize metric.Int64Histogram
}

// Observation is one completed operation. RequestSize and ResponseSize are
// declared payload sizes; a negative value means unknown (for example a chunked
// HTTP body) and is not recorded.
type Observation struct {
	Duration     time.Duration
	RequestSize  int64
	ResponseSize int64
	Attrs        []attribute.KeyValue
}

// NewRecorder creates a Recorder from the given MeterProvider. defaultAttrs are
// applied to every series it records. Passing a no-op provider yields
// instruments that record nothing, so a Recorder is always safe to construct and
// call regardless of whether metrics are enabled.
func NewRecorder(mp metric.MeterProvider, defaultAttrs []attribute.KeyValue) (*Recorder, error) {
	r := &Recorder{
		meter:        mp.Meter(meterName),
		defaultAttrs: defaultAttrs,
	}
	httpInstruments, err := r.Instruments(HTTPClientFamily)
	if err != nil {
		return nil, err
	}
	r.http = httpInstruments
	return r, nil
}

// Instruments resolves the instrument set for f. Call it once per Family at
// construction time, then reuse the result. This is the extension point for a
// new transport: declare a Family, resolve it here, and record through it.
func (r *Recorder) Instruments(f Family) (*Instruments, error) {
	if r == nil {
		return nil, nil
	}
	duration, err := r.meter.Float64Histogram(
		f.Duration.Name,
		metric.WithUnit(f.Duration.Unit),
		metric.WithDescription(f.Duration.Description),
		metric.WithExplicitBucketBoundaries(f.Duration.Buckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", f.Duration.Name, err)
	}
	insts := &Instruments{rec: r, duration: duration}
	if f.RequestSize != nil {
		if insts.requestSize, err = r.sizeHistogram(*f.RequestSize); err != nil {
			return nil, err
		}
	}
	if f.ResponseSize != nil {
		if insts.responseSize, err = r.sizeHistogram(*f.ResponseSize); err != nil {
			return nil, err
		}
	}
	return insts, nil
}

func (r *Recorder) sizeHistogram(spec InstrumentSpec) (metric.Int64Histogram, error) {
	h, err := r.meter.Int64Histogram(
		spec.Name,
		metric.WithUnit(spec.Unit),
		metric.WithDescription(spec.Description),
		metric.WithExplicitBucketBoundaries(spec.Buckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", spec.Name, err)
	}
	return h, nil
}

// Record records one completed operation: duration, and payload sizes when the
// Family declares them and the size is known. A nil Instruments is a no-op so
// callers need not branch on whether instrumentation is enabled.
func (i *Instruments) Record(ctx context.Context, obs Observation) {
	if i == nil || i.duration == nil {
		return
	}
	attrs := obs.Attrs
	if i.rec != nil && len(i.rec.defaultAttrs) > 0 {
		merged := make([]attribute.KeyValue, 0, len(i.rec.defaultAttrs)+len(attrs))
		merged = append(merged, i.rec.defaultAttrs...)
		merged = append(merged, attrs...)
		attrs = merged
	}
	opt := metric.WithAttributes(attrs...)
	i.duration.Record(ctx, obs.Duration.Seconds(), opt)
	if i.requestSize != nil && obs.RequestSize >= 0 {
		i.requestSize.Record(ctx, obs.RequestSize, opt)
	}
	if i.responseSize != nil && obs.ResponseSize >= 0 {
		i.responseSize.Record(ctx, obs.ResponseSize, opt)
	}
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

// RecordHTTPRequest records one outbound HTTP call through the HTTP Family. It
// is a thin wrapper over Instruments.Record kept for the HTTP RoundTripper. A
// nil Recorder is a no-op.
func (r *Recorder) RecordHTTPRequest(ctx context.Context, obs HTTPObservation) {
	if r == nil {
		return
	}
	r.http.Record(ctx, Observation{
		Duration:     obs.Duration,
		RequestSize:  obs.RequestBodySize,
		ResponseSize: obs.ResponseBodySize,
		Attrs:        obs.Attrs,
	})
}
