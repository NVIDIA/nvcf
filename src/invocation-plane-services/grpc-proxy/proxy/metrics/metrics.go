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
package metrics

import (
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
)

const (
	RootNamespace = "nvcf_grpc_proxy_service"
	NatsNamespace = RootNamespace + "_nats"
)

var (
	// ExpiringMetrics provides expiring metrics with built-in support for high-cardinality labels
	// that automatically expire when unused to prevent memory issues in Prometheus
	expiringMetrics *ExpiringMetrics = lo.Must(NewExpiringMetrics(6 * time.Hour))

	ActiveHttpRequestsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: RootNamespace,
			Name:      "active_http_requests_total",
			Help:      "total active client http requests",
		})

	ActiveClientConnectionsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: RootNamespace,
			Name:      "active_connections_total",
			Help:      "total active client tcp connections",
		})

	SessionInitTimeCounter = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: RootNamespace,
			Name:      "session_init_seconds_total",
			Help:      "total seconds spent initializing the session",
		}, []string{"is_reconnect"})

	NatsErrorCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: NatsNamespace,
			Name:      "error_total",
			Help:      "total nats errors on a nats connection",
		})

	NatsReconnectCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: NatsNamespace,
			Name:      "reconnect_total",
			Help:      "total nats reconnects on a nats connection",
		})

	NatsLameDuckCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: NatsNamespace,
			Name:      "lame_duck_total",
			Help:      "total number of lame duck messages",
		})

	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "out_bytes",
		Help:      "The number of output bytes for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().OutBytes)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "in_bytes",
		Help:      "The number of input bytes for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().InBytes)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "out_msgs",
		Help:      "The number of output messages for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().OutMsgs)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "in_msgs",
		Help:      "The number of input messages for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().InMsgs)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "reconnects",
		Help:      "The number of reconnect attempts for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().Reconnects)
	})
)

func init() {
	// Set up OpenTelemetry metrics with Prometheus exporter
	exporter := lo.Must(otelprom.New())
	provider := metric.NewMeterProvider(metric.WithReader(exporter))
	otel.SetMeterProvider(provider)
}

var nc atomic.Pointer[nats.Conn]

func SetNatsStatsConnection(newNc *nats.Conn) {
	nc.Store(newNc)
}

func IncrFunctionRequest(functionID, functionVersionID, ncaID string) {
	expiringMetrics.IncrFunctionRequest(functionID, functionVersionID, ncaID)
}
