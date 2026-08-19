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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

func TestSetupMeterProvider_DisabledInstallsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	mp, shutdown, err := SetupMeterProvider(MeterProviderConfig{Enabled: false, Registerer: reg})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer shutdown()

	// Recording through the no-op provider must not register any Prometheus series.
	meter := mp.Meter("test")
	c, err := meter.Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	c.Add(context.Background(), 1, metric.WithAttributes())

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(families) != 0 {
		t.Fatalf("expected no series from a disabled provider, got %d families", len(families))
	}
}

func TestSetupMeterProvider_EnabledExportsToRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	mp, shutdown, err := SetupMeterProvider(MeterProviderConfig{Enabled: true, Registerer: reg})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer shutdown()

	meter := mp.Meter("test")
	c, err := meter.Int64Counter("test.requests")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	c.Add(context.Background(), 1)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found bool
	for _, f := range families {
		if f.GetName() == "test_requests_total" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected test_requests_total exported through the registry")
	}
}

// TestSetupMeterProvider_AdditiveToExistingSeries verifies OTel metrics are
// additive: an existing client_golang metric registered before the bridge is
// unchanged and coexists with OTel-sourced series in the same registry. This is
// the scrape-diff guarantee for existing nvca_* metrics.
func TestSetupMeterProvider_AdditiveToExistingSeries(t *testing.T) {
	reg := prometheus.NewRegistry()

	// An existing legacy metric, registered before the OTel bridge.
	legacy := promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
		Name: "nvca_legacy_requests_total",
		Help: "existing metric",
	}, []string{"operation"})
	legacy.WithLabelValues("heartbeat").Inc()

	before := seriesSnapshot(t, reg)

	mp, shutdown, err := SetupMeterProvider(MeterProviderConfig{Enabled: true, Registerer: reg})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer shutdown()

	c, err := mp.Meter("test").Int64Counter("nvca.client.requests")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	c.Add(context.Background(), 1)

	after := seriesSnapshot(t, reg)

	// The legacy series must be byte-for-byte unchanged.
	if before["nvca_legacy_requests_total"] != after["nvca_legacy_requests_total"] {
		t.Fatalf("legacy series changed after enabling OTel bridge:\nbefore=%q\nafter=%q",
			before["nvca_legacy_requests_total"], after["nvca_legacy_requests_total"])
	}
	// The new OTel series must be present (additive).
	if _, ok := after["nvca_client_requests_total"]; !ok {
		t.Fatalf("expected additive OTel series nvca_client_requests_total, got families: %v", after)
	}
}

// TestSetupMeterProvider_DoesNotInstallGlobal pins the scoping decision.
// SetupMeterProvider must return the provider without registering it as the OTel
// global: a global provider also activates every other instrumentation library
// that falls back to otel.GetMeterProvider() (otelhttp on the fnds and auth
// clients, otelmux on the agent's HTTP servers). Those emit into the same
// http.client.* family with a different attribute set and without NVCA's default
// labels, putting two label schemas in one metric family. Clients are
// instrumented explicitly through a Recorder built from the returned provider.
func TestSetupMeterProvider_DoesNotInstallGlobal(t *testing.T) {
	before := otel.GetMeterProvider()

	mp, shutdown, err := SetupMeterProvider(MeterProviderConfig{
		Enabled:    true,
		Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	defer shutdown()
	require.NotNil(t, mp)

	assert.Same(t, before, otel.GetMeterProvider(),
		"SetupMeterProvider must not install the global meter provider")
	assert.NotSame(t, mp, otel.GetMeterProvider(),
		"the returned provider must not be the global one")
}

// seriesSnapshot returns a map of metric family name to a stable string
// representation of its samples for diffing.
func seriesSnapshot(t *testing.T, reg *prometheus.Registry) map[string]string {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]string{}
	for _, f := range families {
		out[f.GetName()] = f.String()
	}
	return out
}
