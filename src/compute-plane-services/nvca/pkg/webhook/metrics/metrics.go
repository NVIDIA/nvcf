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

// Adapted from https://github.com/kubernetes-sigs/controller-runtime/blob/23ce864/pkg/webhook/internal/metrics/metrics.go

package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	ictx "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/context"
)

const (
	requestLatencyMetricName  = "nvca_webhook_latency_seconds"
	requestTotalMetricName    = "nvca_webhook_requests_total"
	requestInFlightMetricName = "nvca_webhook_requests_in_flight"

	webhookLabel  = "webhook"
	httpCodeLabel = "code"
)

// Metrics holds Prometheus collectors for webhook instrumentation.
type Metrics struct {
	RequestLatency  *prometheus.HistogramVec
	RequestTotal    *prometheus.CounterVec
	RequestInFlight *prometheus.GaugeVec

	registerer prometheus.Registerer
}

// Option configures a Metrics instance.
type Option func(*Metrics)

// WithRegisterer overrides the default prometheus.DefaultRegisterer.
func WithRegisterer(r prometheus.Registerer) Option {
	return func(m *Metrics) {
		m.registerer = r
	}
}

const ctxKey ictx.Key = "wh_metrics"

func WithDefaultMetrics(parent context.Context, opts ...Option) context.Context {
	return WithMetrics(parent, newMetrics(opts...))
}

func WithMetrics(parent context.Context, m *Metrics) context.Context {
	return context.WithValue(parent, ctxKey, m)
}

func FromContext(ctx context.Context) *Metrics {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(ctxKey).(*Metrics)
	return m
}

// newMetrics constructs and registers all webhook metrics.
func newMetrics(opts ...Option) *Metrics {
	m := &Metrics{
		registerer: prometheus.DefaultRegisterer,
	}
	for _, opt := range opts {
		opt(m)
	}

	promFactory := promauto.With(m.registerer)

	m.RequestLatency = promFactory.NewHistogramVec(prometheus.HistogramOpts{
		Name:                            requestLatencyMetricName,
		Help:                            "Histogram of the latency of processing admission requests",
		Buckets:                         prometheus.ExponentialBuckets(10e-9, 10, 12),
		NativeHistogramBucketFactor:     1.1,
		NativeHistogramMaxBucketNumber:  100,
		NativeHistogramMinResetDuration: 1 * time.Hour,
	}, []string{webhookLabel})

	m.RequestTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: requestTotalMetricName,
		Help: "Total number of admission requests by HTTP status code.",
	}, []string{webhookLabel, httpCodeLabel})

	m.RequestInFlight = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: requestInFlightMetricName,
		Help: "Current number of admission requests being served.",
	}, []string{webhookLabel})

	return m
}

// destroy unregisters all metrics from the registerer. It is intended only for testing purposes.
func (m *Metrics) destroy() { //nolint:unused
	m.registerer.Unregister(m.RequestLatency)
	m.registerer.Unregister(m.RequestTotal)
	m.registerer.Unregister(m.RequestInFlight)
}

// InstrumentedHook wraps an http.Handler with latency, counter, and in-flight metrics.
func (m *Metrics) InstrumentedHook(path string, hookRaw http.Handler) http.Handler {
	if m == nil {
		return hookRaw
	}
	lbl := prometheus.Labels{webhookLabel: path}

	reqLatency := m.RequestLatency.MustCurryWith(lbl)
	reqTotal := m.RequestTotal.MustCurryWith(lbl)
	// Initialize the most likely HTTP status codes.
	reqTotal.WithLabelValues("200")
	reqTotal.WithLabelValues("500")
	reqInFlight := m.RequestInFlight.With(lbl)

	return promhttp.InstrumentHandlerDuration(
		reqLatency,
		promhttp.InstrumentHandlerCounter(
			reqTotal,
			promhttp.InstrumentHandlerInFlight(reqInFlight, hookRaw),
		),
	)
}
