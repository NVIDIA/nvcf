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
	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// MeterProviderConfig configures the OTel metrics pipeline.
type MeterProviderConfig struct {
	// Enabled gates the whole pipeline. When false, SetupMeterProvider installs
	// a no-op MeterProvider so instrumented call sites emit nothing and behave
	// exactly as before.
	Enabled bool
	// Registerer is the Prometheus registry the OTel->Prometheus bridge registers
	// into. It must be the same registry served at /metrics so OTel-sourced series
	// appear alongside the existing client_golang metrics. When nil,
	// prometheus.DefaultRegisterer is used.
	Registerer prometheus.Registerer
}

// SetupMeterProvider builds an OTel MeterProvider and registers it as the global
// provider, so any code calling otel.Meter(...) starts producing metrics. When
// cfg.Enabled is false it installs a no-op provider and returns a no-op shutdown.
//
// Note: this is a process-wide side effect via otel.SetMeterProvider, intended to
// be called once at startup. Tests that call it more than once will overwrite the
// global provider; assert through the returned MeterProvider rather than
// otel.GetMeterProvider().
//
// The provider is backed by the OTel Prometheus exporter, which registers as a
// collector into cfg.Registerer. Exposition stays pull-based: metrics are
// rendered only when /metrics is scraped, matching the existing behaviour.
//
// The returned shutdown function releases the provider; callers should defer it.
func SetupMeterProvider(cfg MeterProviderConfig) (metric.MeterProvider, func(), error) {
	if !cfg.Enabled {
		mp := metricnoop.NewMeterProvider()
		otel.SetMeterProvider(mp)
		return mp, func() {}, nil
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
	otel.SetMeterProvider(mp)

	shutdown := func() {
		// Best-effort flush/release on shutdown; the process is exiting so a
		// failed shutdown is not actionable.
		_ = mp.Shutdown(context.Background())
	}
	return mp, shutdown, nil
}
