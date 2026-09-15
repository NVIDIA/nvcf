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
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// MeterProviderConfig configures the OTel metrics pipeline.
type MeterProviderConfig struct {
	// Enabled gates the whole pipeline. When false, SetupMeterProvider returns a
	// no-op MeterProvider so instrumented call sites emit nothing and behave
	// exactly as before. Nothing is installed globally in either case.
	Enabled bool
	// Registerer is the Prometheus registry the OTel->Prometheus bridge registers
	// into. It must be the same registry served at /metrics so OTel-sourced series
	// appear alongside the existing client_golang metrics. When nil,
	// prometheus.DefaultRegisterer is used.
	Registerer prometheus.Registerer
}

// SetupMeterProvider builds an OTel MeterProvider backed by the Prometheus
// exporter, which registers as a collector into cfg.Registerer. When cfg.Enabled
// is false it returns a no-op provider and a no-op shutdown. Exposition stays
// pull-based: metrics are rendered only when /metrics is scraped, matching the
// existing behaviour.
//
// The provider is returned rather than installed as the OTel global on purpose.
// Registering it globally would also activate every other OTel instrumentation
// library in the process that falls back to otel.GetMeterProvider() (otelhttp on
// the fnds and auth clients, otelmux on the agent's HTTP servers). Those emit
// into the same http.client.* metric family with a different attribute set and
// without NVCA's default labels, which would put two label schemas in one
// family. Clients are instrumented explicitly through a Recorder built from this
// provider instead, so every NVCA-sourced series shares one vocabulary.
//
// The returned shutdown function releases the provider; callers should defer it.
func SetupMeterProvider(cfg MeterProviderConfig) (metric.MeterProvider, func(), error) {
	if !cfg.Enabled {
		return metricnoop.NewMeterProvider(), func() {}, nil
	}

	registerer := cfg.Registerer
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	exporter, err := promexporter.New(promexporter.WithRegisterer(registerer))
	if err != nil {
		return nil, nil, fmt.Errorf("creating otel prometheus exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	shutdown := func() {
		// Best-effort flush/release on shutdown; the process is exiting so a
		// failed shutdown is not actionable.
		_ = mp.Shutdown(context.Background())
	}
	return mp, shutdown, nil
}
