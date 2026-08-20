// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package metrics provides bounded Prometheus instrumentation for the uploader.
package metrics

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "nvcf_dynamo_request_trace_uploader"

var (
	captureTypes     = []string{"trace", "audit"}
	segmentOutcomes  = []string{"discovered", "prepared", "submitted", "completed", "quarantined", "empty"}
	operations       = []string{"submit", "status"}
	operationResults = []string{"success", "retryable_error", "terminal_error"}
)

// Metrics contains all uploader metrics. Callers provide a registry so tests
// have isolated registration and callers can compose this handler explicitly.
type Metrics struct {
	SegmentsTotal               *prometheus.CounterVec
	UploadedBytesTotal          *prometheus.CounterVec
	OperationAttemptsTotal      *prometheus.CounterVec
	OperationDurationSeconds    *prometheus.HistogramVec
	PendingSegments             prometheus.Gauge
	PendingBytes                prometheus.Gauge
	OldestPendingSeconds        prometheus.Gauge
	QuarantinedSegments         prometheus.Gauge
	SourceDeleteFailuresTotal   prometheus.Counter
	LastSuccessTimestampSeconds prometheus.Gauge
	registry                    *prometheus.Registry
}

// New registers and pre-initializes the uploader metrics.
func New(registry *prometheus.Registry) (*Metrics, error) {
	if registry == nil {
		return nil, fmt.Errorf("metrics registry is required")
	}
	m := &Metrics{
		SegmentsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "segments_total",
			Help:      "Request-trace segments by capture type and lifecycle outcome.",
		}, []string{"capture_type", "outcome"}),
		UploadedBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "uploaded_bytes_total",
			Help:      "Bytes in completed request-trace segment uploads.",
		}, []string{"capture_type"}),
		OperationAttemptsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "operation_attempts_total",
			Help:      "Remote operation attempts by operation and result.",
		}, []string{"operation", "result"}),
		OperationDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "operation_duration_seconds",
			Help:      "Remote operation duration by operation.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation"}),
		PendingSegments: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "pending_segments",
			Help:      "Closed request-trace segments awaiting upload.",
		}),
		PendingBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "pending_bytes",
			Help:      "Bytes in closed request-trace segments awaiting upload.",
		}),
		OldestPendingSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "oldest_pending_seconds",
			Help:      "Age of the oldest closed request-trace segment awaiting upload.",
		}),
		QuarantinedSegments: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "quarantined_segments",
			Help:      "Request-trace segments retained in quarantine.",
		}),
		SourceDeleteFailuresTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "source_delete_failures_total",
			Help:      "Source segment deletion failures after terminal success.",
		}),
		LastSuccessTimestampSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "last_success_timestamp_seconds",
			Help:      "Unix timestamp of the most recent terminal upload success.",
		}),
		registry: registry,
	}
	collectors := []prometheus.Collector{
		m.SegmentsTotal,
		m.UploadedBytesTotal,
		m.OperationAttemptsTotal,
		m.OperationDurationSeconds,
		m.PendingSegments,
		m.PendingBytes,
		m.OldestPendingSeconds,
		m.QuarantinedSegments,
		m.SourceDeleteFailuresTotal,
		m.LastSuccessTimestampSeconds,
	}
	for _, collector := range collectors {
		if err := registry.Register(collector); err != nil {
			return nil, fmt.Errorf("register uploader metric: %w", err)
		}
	}
	for _, captureType := range captureTypes {
		m.UploadedBytesTotal.WithLabelValues(captureType)
		for _, outcome := range segmentOutcomes {
			m.SegmentsTotal.WithLabelValues(captureType, outcome)
		}
	}
	for _, operation := range operations {
		m.OperationDurationSeconds.WithLabelValues(operation)
		for _, result := range operationResults {
			m.OperationAttemptsTotal.WithLabelValues(operation, result)
		}
	}
	return m, nil
}

// Handler returns a Prometheus HTTP handler for the service-local registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
