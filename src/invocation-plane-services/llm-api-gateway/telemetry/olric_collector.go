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
	"context"
	"sync"

	"github.com/olric-data/olric"
	"github.com/olric-data/olric/stats"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
)

// OlricStatsProvider is the subset of olric.EmbeddedClient that the collector
// needs. Defined as an interface so tests can supply a fake.
type OlricStatsProvider interface {
	Stats(ctx context.Context, address string, options ...olric.StatsOption) (stats.Stats, error)
}

// OlricCollector registers OTel async observable instruments that report
// embedded Olric cluster health on each SDK collection cycle. The callback
// calls Stats() on the local node — no network hop — and maps the result
// to gauges and counters.
type OlricCollector struct {
	reg  otelmetric.Registration
	once sync.Once
}

// NewOlricCollector creates instruments and registers a single callback that
// observes all of them from one Stats() call per collection interval.
func NewOlricCollector(provider OlricStatsProvider, selfAddr string) (*OlricCollector, error) {
	meter := Meter()

	clusterMembers := must.Get(meter.Int64ObservableGauge(
		"olric.cluster.members",
		otelmetric.WithDescription("Number of members in the Olric cluster"),
	))

	clusterUptime := must.Get(meter.Int64ObservableGauge(
		"olric.cluster.uptime_seconds",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Seconds since the Olric node started"),
	))

	dmapEntriesTotal := must.Get(meter.Int64ObservableCounter(
		"olric.dmap.entries_total",
		otelmetric.WithDescription("Cumulative entries stored in DMaps over the node lifetime"),
	))

	dmapGetHits := must.Get(meter.Int64ObservableCounter(
		"olric.dmap.get_hits",
		otelmetric.WithDescription("Cumulative DMap Get operations that found the key"),
	))

	dmapGetMisses := must.Get(meter.Int64ObservableCounter(
		"olric.dmap.get_misses",
		otelmetric.WithDescription("Cumulative DMap Get operations where the key was absent"),
	))

	dmapDeleteHits := must.Get(meter.Int64ObservableCounter(
		"olric.dmap.delete_hits",
		otelmetric.WithDescription("Cumulative DMap Delete operations that removed a key"),
	))

	dmapDeleteMisses := must.Get(meter.Int64ObservableCounter(
		"olric.dmap.delete_misses",
		otelmetric.WithDescription("Cumulative DMap Delete operations where the key was absent"),
	))

	dmapEvictedTotal := must.Get(meter.Int64ObservableCounter(
		"olric.dmap.evicted_total",
		otelmetric.WithDescription("Cumulative entries evicted from DMaps to free memory"),
	))

	networkCommandsTotal := must.Get(meter.Int64ObservableCounter(
		"olric.network.commands_total",
		otelmetric.WithDescription("Cumulative commands processed by the Olric node"),
	))

	networkCurrentConnections := must.Get(meter.Int64ObservableGauge(
		"olric.network.current_connections",
		otelmetric.WithDescription("Current open connections to the Olric node"),
	))

	partitionsPrimaryOwned := must.Get(meter.Int64ObservableGauge(
		"olric.partitions.primary_owned",
		otelmetric.WithDescription("Number of primary partitions owned by this node"),
	))

	partitionsBackupOwned := must.Get(meter.Int64ObservableGauge(
		"olric.partitions.backup_owned",
		otelmetric.WithDescription("Number of backup partitions owned by this node"),
	))

	reg, err := meter.RegisterCallback(
		func(ctx context.Context, o otelmetric.Observer) error {
			s, err := provider.Stats(ctx, selfAddr)
			if err != nil {
				Logger(ctx).Warn().Err(err).Msg("failed to collect olric stats")
				return nil
			}

			o.ObserveInt64(clusterMembers, int64(len(s.ClusterMembers)))
			o.ObserveInt64(clusterUptime, s.UptimeSeconds)

			o.ObserveInt64(dmapEntriesTotal, s.DMaps.EntriesTotal)
			o.ObserveInt64(dmapGetHits, s.DMaps.GetHits)
			o.ObserveInt64(dmapGetMisses, s.DMaps.GetMisses)
			o.ObserveInt64(dmapDeleteHits, s.DMaps.DeleteHits)
			o.ObserveInt64(dmapDeleteMisses, s.DMaps.DeleteMisses)
			o.ObserveInt64(dmapEvictedTotal, s.DMaps.EvictedTotal)

			o.ObserveInt64(networkCommandsTotal, s.Network.CommandsTotal)
			o.ObserveInt64(networkCurrentConnections, s.Network.CurrentConnections)

			o.ObserveInt64(partitionsPrimaryOwned, int64(len(s.Partitions)))
			o.ObserveInt64(partitionsBackupOwned, int64(len(s.Backups)))

			return nil
		},
		clusterMembers,
		clusterUptime,
		dmapEntriesTotal,
		dmapGetHits,
		dmapGetMisses,
		dmapDeleteHits,
		dmapDeleteMisses,
		dmapEvictedTotal,
		networkCommandsTotal,
		networkCurrentConnections,
		partitionsPrimaryOwned,
		partitionsBackupOwned,
	)
	if err != nil {
		return nil, err
	}

	return &OlricCollector{reg: reg}, nil
}

// Stop unregisters the callback. Safe to call on a nil receiver or multiple
// times.
func (c *OlricCollector) Stop() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		_ = c.reg.Unregister()
	})
}
