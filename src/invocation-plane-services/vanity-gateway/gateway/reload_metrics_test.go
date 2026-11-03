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

package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	config "ai-api-gateway-service/gateway_config"
	"ai-api-gateway-service/internal/reloadableconfig"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installReloadMetrics points the reload metrics at a manual reader and the
// recorded timestamp at the returned clock value (unix seconds).
func installReloadMetrics(t *testing.T) (*sdkmetric.ManualReader, *atomic.Int64) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previousProvider := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	previousInstruments := reloadInstrumentsRef.Load()
	require.NoError(t, setupReloadMetrics())

	now := &atomic.Int64{}
	installed := reloadInstrumentsRef.Load()
	reloadInstrumentsRef.Store(&reloadInstruments{
		lastReload: installed.lastReload,
		failures:   installed.failures,
		clock:      func() time.Time { return time.Unix(now.Load(), 0) },
	})
	t.Cleanup(func() {
		// Goroutines still draining after the test read the instruments
		// atomically, so restoring them here cannot race.
		reloadInstrumentsRef.Store(previousInstruments)
		otel.SetMeterProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return reader, now
}

func collectReloadMetrics(t *testing.T, reader *sdkmetric.ManualReader) (lastReload *int64, failures map[string]int64) {
	t.Helper()

	var resourceMetrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &resourceMetrics))

	failures = map[string]int64{}
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		for _, metric := range scopeMetrics.Metrics {
			switch data := metric.Data.(type) {
			case metricdata.Gauge[int64]:
				if metric.Name != lastReloadMetricName {
					continue
				}
				require.Len(t, data.DataPoints, 1)
				value := data.DataPoints[0].Value
				lastReload = &value
			case metricdata.Sum[int64]:
				if metric.Name != reloadFailuresMetricName {
					continue
				}
				for _, point := range data.DataPoints {
					stage, ok := point.Attributes.Value(reloadStageLabel)
					require.True(t, ok)
					failures[stage.AsString()] = point.Value
				}
			}
		}
	}
	return lastReload, failures
}

func TestReloadMetricsAreInitializedAtZeroWithoutAReload(t *testing.T) {
	reader, _ := installReloadMetrics(t)

	lastReload, failures := collectReloadMetrics(t, reader)

	require.Nil(t, lastReload, "the gauge is only set after a successful load and router swap")
	require.Equal(t, map[string]int64{reloadStageLoad: 0, reloadStageRouterBuild: 0}, failures)
}

func TestReloadMetricsPrometheusExposition(t *testing.T) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	require.NoError(t, err)
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	previousProvider := otel.GetMeterProvider()
	previousInstruments := reloadInstrumentsRef.Load()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		reloadInstrumentsRef.Store(previousInstruments)
		otel.SetMeterProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	require.NoError(t, setupReloadMetrics())
	installed := reloadInstrumentsRef.Load()
	reloadInstrumentsRef.Store(&reloadInstruments{
		lastReload: installed.lastReload,
		failures:   installed.failures,
		clock:      func() time.Time { return time.Unix(1700000000, 0) },
	})

	recordConfigReloadSuccess(context.Background())
	recordConfigReloadFailure(context.Background(), reloadStageRouterBuild)

	families, err := registry.Gather()
	require.NoError(t, err)
	byName := map[string]float64{}
	for _, family := range families {
		if family.GetName() == "target_info" {
			continue
		}
		for _, metric := range family.Metric {
			stage := ""
			for _, label := range metric.Label {
				if label.GetName() == reloadStageLabel {
					stage = label.GetValue()
				}
			}
			switch {
			case metric.Gauge != nil:
				byName[family.GetName()] = metric.Gauge.GetValue()
			case metric.Counter != nil:
				byName[family.GetName()+"/"+stage] = metric.Counter.GetValue()
			}
		}
	}
	require.Equal(t, map[string]float64{
		"nvcf_vanity_gateway_last_successful_config_reload_timestamp_seconds": 1700000000,
		"nvcf_vanity_gateway_config_reload_failures_total/load":               0,
		"nvcf_vanity_gateway_config_reload_failures_total/router_build":       1,
	}, byName)
}

const reloadTestConfig = `
v2config:
  openai:
    chatCompletions:
      primary:
        modelName: facebook/opt-125m
        functionID: %s
`

func writeReloadTestConfig(t *testing.T, path, functionID string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(reloadTestConfig, functionID)), 0600))
}

func reloadTestServerConfig(privateModelPattern string) Config {
	return Config{
		NvcfApiEndpoint:              "http://127.0.0.1:1",
		PrivateModelNameRegexPattern: privateModelPattern,
	}
}

// startReloadPipeline loads a mapping file and runs the router swap loop the
// way NewNVCFGateway does. Callbacks from the file watcher stop recording once
// the test ends so a deleted temp dir cannot count as a failed reload.
func startReloadPipeline(t *testing.T, path string, serverConfig Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mappings, err := config.SetupConfigWithConfigPathAndTimeout(path, 0,
		reloadableconfig.WithReloadErrorFunc[config.GatewayConfig](func(err error) {
			if ctx.Err() == nil {
				recordConfigLoadFailure(err)
			}
		}))
	require.NoError(t, err)

	router, err := buildRouter(mappings.Get(), reloadTestServerConfig("^$"))
	require.NoError(t, err)
	swapRouterWhenNotified(ctx, mappings, serverConfig, router)
}

func TestReloadMetricsTrackFailedLoadAndSuccessfulSwap(t *testing.T) {
	reader, now := installReloadMetrics(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadTestConfig(t, path, "func-one")
	const first, second, third = 1000, 2000, 3000

	now.Store(first)
	startReloadPipeline(t, path, reloadTestServerConfig("^$"))
	require.Eventually(t, func() bool {
		lastReload, _ := collectReloadMetrics(t, reader)
		return lastReload != nil && *lastReload == first
	}, 2*time.Second, 10*time.Millisecond, "the initial load must be recorded as a success")

	// A rejected file counts as a load failure and leaves the gauge alone.
	now.Store(second)
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, os.WriteFile(path, []byte("v2config: [not, a, mapping]\n"), 0600))
	require.Eventually(t, func() bool {
		_, failures := collectReloadMetrics(t, reader)
		return failures[reloadStageLoad] >= 1
	}, 2*time.Second, 10*time.Millisecond)
	lastReload, failures := collectReloadMetrics(t, reader)
	require.NotNil(t, lastReload)
	require.EqualValues(t, first, *lastReload, "a failed load must not advance the gauge")
	require.Zero(t, failures[reloadStageRouterBuild])

	// A valid file is swapped in and advances the gauge.
	now.Store(third)
	time.Sleep(20 * time.Millisecond)
	writeReloadTestConfig(t, path, "func-two")
	require.Eventually(t, func() bool {
		lastReload, _ := collectReloadMetrics(t, reader)
		return lastReload != nil && *lastReload == third
	}, 2*time.Second, 10*time.Millisecond, "a successful reload must advance the gauge")
	_, failures = collectReloadMetrics(t, reader)
	require.Zero(t, failures[reloadStageRouterBuild])
}

func TestReloadMetricsCountRouterBuildFailures(t *testing.T) {
	reader, now := installReloadMetrics(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadTestConfig(t, path, "func-one")
	now.Store(1000)

	// The mapping loads, but the router cannot be built from the server config.
	startReloadPipeline(t, path, reloadTestServerConfig("("))
	require.Eventually(t, func() bool {
		_, failures := collectReloadMetrics(t, reader)
		return failures[reloadStageRouterBuild] >= 1
	}, 2*time.Second, 10*time.Millisecond)
	before := func() int64 {
		_, failures := collectReloadMetrics(t, reader)
		return failures[reloadStageRouterBuild]
	}()

	time.Sleep(20 * time.Millisecond)
	writeReloadTestConfig(t, path, "func-two")
	require.Eventually(t, func() bool {
		_, failures := collectReloadMetrics(t, reader)
		return failures[reloadStageRouterBuild] > before
	}, 2*time.Second, 10*time.Millisecond)

	lastReload, failures := collectReloadMetrics(t, reader)
	require.Nil(t, lastReload, "a failed router build must not set the gauge")
	require.Zero(t, failures[reloadStageLoad])
}
