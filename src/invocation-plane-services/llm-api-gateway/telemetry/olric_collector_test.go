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

package telemetry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olric-data/olric"
	"github.com/olric-data/olric/stats"
	"github.com/stretchr/testify/require"
	otelapi "go.opentelemetry.io/otel"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

type fakeStatsProvider struct {
	stats stats.Stats
	err   error
}

func (f *fakeStatsProvider) Stats(_ context.Context, _ string, _ ...olric.StatsOption) (stats.Stats, error) {
	return f.stats, f.err
}

func setupMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otelapi.SetMeterProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otelapi.SetMeterProvider(noopmetric.NewMeterProvider())
	})
	return reader
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	out := make(map[string]metricdata.Metrics)
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func gaugeValue(t *testing.T, m metricdata.Metrics) int64 {
	t.Helper()
	g, ok := m.Data.(metricdata.Gauge[int64])
	require.True(t, ok, "expected Gauge[int64] for %s", m.Name)
	require.NotEmpty(t, g.DataPoints)
	return g.DataPoints[0].Value
}

func sumValue(t *testing.T, m metricdata.Metrics) int64 {
	t.Helper()
	s, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] for %s", m.Name)
	require.NotEmpty(t, s.DataPoints)
	return s.DataPoints[0].Value
}

func TestOlricCollector_ObservesStats(t *testing.T) {
	reader := setupMeter(t)

	provider := &fakeStatsProvider{
		stats: stats.Stats{
			UptimeSeconds: 42,
			ClusterMembers: map[stats.MemberID]stats.Member{
				1: {Name: "node-a", ID: 1},
				2: {Name: "node-b", ID: 2},
			},
			DMaps: stats.DMaps{
				EntriesTotal: 100,
				GetHits:      80,
				GetMisses:    20,
				DeleteHits:   5,
				DeleteMisses: 3,
				EvictedTotal: 2,
			},
			Network: stats.Network{
				CommandsTotal:      500,
				CurrentConnections: 4,
			},
			Partitions: map[stats.PartitionID]stats.Partition{
				0: {}, 1: {}, 2: {},
			},
			Backups: map[stats.PartitionID]stats.Partition{
				0: {},
			},
		},
	}

	collector, err := telemetry.NewOlricCollector(provider, "127.0.0.1:3320")
	require.NoError(t, err)
	t.Cleanup(collector.Stop)

	metrics := collectMetrics(t, reader)

	require.Contains(t, metrics, "olric.cluster.members")
	require.Contains(t, metrics, "olric.cluster.uptime_seconds")
	require.Contains(t, metrics, "olric.dmap.entries_total")
	require.Contains(t, metrics, "olric.dmap.get_hits")
	require.Contains(t, metrics, "olric.dmap.get_misses")
	require.Contains(t, metrics, "olric.dmap.delete_hits")
	require.Contains(t, metrics, "olric.dmap.delete_misses")
	require.Contains(t, metrics, "olric.dmap.evicted_total")
	require.Contains(t, metrics, "olric.network.commands_total")
	require.Contains(t, metrics, "olric.network.current_connections")
	require.Contains(t, metrics, "olric.partitions.primary_owned")
	require.Contains(t, metrics, "olric.partitions.backup_owned")

	require.Equal(t, int64(2), gaugeValue(t, metrics["olric.cluster.members"]))
	require.Equal(t, int64(42), gaugeValue(t, metrics["olric.cluster.uptime_seconds"]))
	require.Equal(t, int64(100), sumValue(t, metrics["olric.dmap.entries_total"]))
	require.Equal(t, int64(80), sumValue(t, metrics["olric.dmap.get_hits"]))
	require.Equal(t, int64(20), sumValue(t, metrics["olric.dmap.get_misses"]))
	require.Equal(t, int64(5), sumValue(t, metrics["olric.dmap.delete_hits"]))
	require.Equal(t, int64(3), sumValue(t, metrics["olric.dmap.delete_misses"]))
	require.Equal(t, int64(2), sumValue(t, metrics["olric.dmap.evicted_total"]))
	require.Equal(t, int64(500), sumValue(t, metrics["olric.network.commands_total"]))
	require.Equal(t, int64(4), gaugeValue(t, metrics["olric.network.current_connections"]))
	require.Equal(t, int64(3), gaugeValue(t, metrics["olric.partitions.primary_owned"]))
	require.Equal(t, int64(1), gaugeValue(t, metrics["olric.partitions.backup_owned"]))
}

func TestOlricCollector_StatsError_EmitsNoMetrics(t *testing.T) {
	reader := setupMeter(t)

	provider := &fakeStatsProvider{err: errors.New("node not ready")}

	collector, err := telemetry.NewOlricCollector(provider, "127.0.0.1:3320")
	require.NoError(t, err)
	t.Cleanup(collector.Stop)

	metrics := collectMetrics(t, reader)

	for name, m := range metrics {
		switch data := m.Data.(type) {
		case metricdata.Gauge[int64]:
			require.Empty(t, data.DataPoints, "expected no data points for %s when Stats() fails", name)
		case metricdata.Sum[int64]:
			require.Empty(t, data.DataPoints, "expected no data points for %s when Stats() fails", name)
		}
	}
}

func TestOlricCollector_StopIsIdempotent(t *testing.T) {
	reader := setupMeter(t)
	_ = reader

	provider := &fakeStatsProvider{stats: stats.Stats{}}

	collector, err := telemetry.NewOlricCollector(provider, "127.0.0.1:3320")
	require.NoError(t, err)

	collector.Stop()
	collector.Stop()
}

func TestOlricCollector_StopOnNilIsNoop(t *testing.T) {
	var collector *telemetry.OlricCollector
	collector.Stop()
}
