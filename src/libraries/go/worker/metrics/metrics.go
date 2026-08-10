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

package metrics

import (
	"net/http"
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/otel/sdk/metric"

	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
)

const (
	WorkerInitRootNamespace = "worker_init"
	NvcfRootNamespace       = "nvcf_worker_service"
	NvctRootNamespace       = "nvct_worker_service"
	NatsNamespace           = NvcfRootNamespace + "_nats"
)

// Shared metrics among all containers
var (
	TokenRefreshFailureGauge *prometheus.GaugeVec
)

var registerSharedMetricsOnce sync.Once

func registerSharedMetrics(rootNamespace string) {
	registerSharedMetricsOnce.Do(func() {
		TokenRefreshFailureGauge = promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: rootNamespace,
				Name:      "token_refresh_failure_total",
				Help:      "total number of token refresh failures",
			}, []string{"token_type"})
	})
}

func Initialize(rootNamespace string, nc *nats.Conn) error {
	registerSharedMetrics(rootNamespace)

	if nc != nil {
		initNatsStats(nc)
	}

	promExporter, err := otelprom.New()
	if err != nil {
		return err
	}
	provider := metric.NewMeterProvider(metric.WithReader(promExporter))
	err = host.Start(host.WithMeterProvider(provider))
	if err != nil {
		return err
	}

	// XXX: WAR until we migrate completely to the Go worker.
	// Use default Prometheus http handler.
	// Per docs, all metrics are shared between all default handlers.
	mux := http.NewServeMux()
	mux.Handle("/", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:    ":8010",
		Handler: mux,
	}
	go func() { _ = metricsServer.ListenAndServe() }()

	return nil
}

// we can only register one nats connection to the global metrics registry.
// this issue only comes up in tests since during normal operation there will only ever
// be one nats connection, created at startup. reconnects are handled internally.
var initNatsOnce sync.Once

func initNatsStats(nc *nats.Conn) {
	initNatsOnce.Do(func() {
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: NatsNamespace,
			Name:      "out_bytes",
			Help:      "The number of output bytes for this nats connection.",
		}, func() float64 { return float64(nc.Stats().OutBytes) })
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: NatsNamespace,
			Name:      "in_bytes",
			Help:      "The number of input bytes for this nats connection.",
		}, func() float64 { return float64(nc.Stats().InBytes) })
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: NatsNamespace,
			Name:      "out_msgs",
			Help:      "The number of output messages for this nats connection.",
		}, func() float64 { return float64(nc.Stats().OutMsgs) })
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: NatsNamespace,
			Name:      "in_msgs",
			Help:      "The number of input messages for this nats connection.",
		}, func() float64 { return float64(nc.Stats().InMsgs) })
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: NatsNamespace,
			Name:      "reconnects",
			Help:      "The number of reconnect attempts for this nats connection.",
		}, func() float64 { return float64(nc.Stats().Reconnects) })
	})
}
