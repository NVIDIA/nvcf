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

package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/consul-template/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.19.0"
)

const (
	otelScopeName    = "ess-agent"
	otelScopeVersion = "v0.1.0"
)

// Telemetry manages the telemetry sinks and abstracts the caller from the
// which provider is configured.
type Telemetry struct {
	meter       metric.Meter
	Instruments *Instruments
}

// TelemetryParams composite structure containing
type TelemetryParams struct {
	C    *config.TelemetryConfig
	Tags map[string]string
}

// Init initializes metric reporting.
func New(params TelemetryParams) (*Telemetry, error) {
	if params.C == nil {
		return nil, errors.New("nil telemetry config provided")
	}
	// set up the OpenTelemetry SDK based on the configuration
	meterProvider, err := setupMeterProvider(params.C, params.Tags)
	if err != nil {
		log.Fatalf("failed to set up meter provider: %v", err)
	}

	// set the global meter provider
	otel.SetMeterProvider(meterProvider)

	// start HTTP server for Prometheus if enabled
	if params.C.Prometheus != nil {
		http.Handle("/metrics", promhttp.Handler())
		addr := fmt.Sprintf("%s:%d", *params.C.Prometheus.IP, *params.C.Prometheus.Port)
		go func(addr string) {
			if *params.C.Prometheus.TLSDisable {
				log.Printf("[DEBUG] (telemetry) configured a non TLS listener on addr: %s", addr)
				if err := http.ListenAndServe(addr, nil); err != nil {
					log.Fatalf("failed to run Prometheus /metrics endpoint: %v", err)
				}
			} else {
				log.Printf("[DEBUG] (telemetry) configured a TLS listener on addr: %s", addr)
				if err := http.ListenAndServeTLS(addr, *params.C.Prometheus.TLSCertPath, *params.C.Prometheus.TLSKeyPath, nil); err != nil {
					log.Fatalf("failed to run Prometheus /metrics endpoint: %v", err)
				}
			}
		}(addr)
	}

	meter := meterProvider.Meter(otelScopeName)
	instruments, err := newInstruments(instrumentsParams{
		meter: meter,
		tags:  params.Tags,
	})
	if err != nil {
		return nil, err
	}

	return &Telemetry{
		meter:       meter,
		Instruments: instruments,
	}, nil
}

func setupMeterProvider(cfg *config.TelemetryConfig, tags map[string]string) (*sdkmetric.MeterProvider, error) {
	var r1 sdkmetric.Reader
	var r2 sdkmetric.Reader

	// set up Prometheus exporter if enabled
	var reportingInterval time.Duration
	if cfg.Prometheus != nil {
		// TODO figure out why otel scope version is not getting propogated. Until then keep otel scopes disabled with
		// prometheus.WithoutScopeInfo
		exporter, err := prometheus.New(prometheus.WithoutUnits(), prometheus.WithoutScopeInfo())
		if err != nil {
			return nil, fmt.Errorf("failed to create Prometheus exporter: %w", err)
		}
		r1 = exporter
	}

	if cfg.Stdout != nil {
		// print with a JSON encoder that indents with two spaces.
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")

		exporter, err := stdoutmetric.New(
			stdoutmetric.WithEncoder(enc),
			stdoutmetric.WithoutTimestamps(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create stdout exporter: %w", err)
		}
		reportingInterval = *cfg.Stdout.ReportingInterval
		r2 = sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(reportingInterval))
	}

	var attributes []attribute.KeyValue
	for k, v := range tags {
		attributes = append(attributes, attribute.Key(k).String(v))
	}

	attributes = append(attributes, semconv.OTelScopeVersion(otelScopeVersion))
	attributes = append(attributes, semconv.OTelScopeName(otelScopeName))
	opts := []sdkmetric.Option{
		sdkmetric.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			attributes...,
		)),
	}
	if r1 != nil {
		opts = append(opts, sdkmetric.WithReader(r1))
	}
	if r2 != nil {
		opts = append(opts, sdkmetric.WithReader(r2))
	}
	meterProvider := sdkmetric.NewMeterProvider(opts...)
	return meterProvider, nil
}
