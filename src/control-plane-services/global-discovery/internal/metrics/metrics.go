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

// Package metrics defines the nvcf_gds_* Prometheus collectors of gds-server.
// Counters are pre-initialised for every known label value so that they
// appear on the first scrape. gRPC server metrics come from the
// go-grpc-middleware Prometheus provider and are registered by
// internal/server.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Namespace prefixes every metric name.
const Namespace = "nvcf_gds"

// Watch kinds, the values of the kind label.
const (
	KindCluster       = "cluster"
	KindServiceImport = "service_import"
	KindPool          = "pool"
	KindRouter        = "router"
)

// Kinds lists every watch kind.
var Kinds = []string{KindCluster, KindServiceImport, KindPool, KindRouter}

// Watch event types, the values of the type label.
var EventTypes = []string{"snapshot_start", "upsert", "delete", "snapshot_end", "bookmark"}

// Watch resync reasons.
const (
	// ResyncLagged: the watcher's queue overflowed.
	ResyncLagged = "lagged"
	// ResyncRevisionUnknown: the resume revision is malformed or from another
	// view epoch.
	ResyncRevisionUnknown = "revision_unknown"
	// ResyncRevisionExpired: the resume revision is older than the history.
	ResyncRevisionExpired = "revision_expired"
)

// ResyncReasons lists every resync reason.
var ResyncReasons = []string{ResyncLagged, ResyncRevisionUnknown, ResyncRevisionExpired}

// Watch rejection reasons.
const (
	RejectTooManyWatchers = "too_many_watchers"
	RejectShuttingDown    = "shutting_down"
)

// Lease kinds of lease_expirations_total.
const (
	LeaseCluster = "cluster"
	LeasePool    = "pool"
)

// Record kinds of compaction_deleted_total.
const (
	RecordCluster     = "cluster"
	RecordExport      = "export"
	RecordEndpointSet = "endpoint_set"
	RecordPool        = "pool"
)

// Store operations of the store metrics.
var StoreOperations = []string{
	"put_cluster", "renew_cluster_lease", "get_cluster", "delete_cluster",
	"put_export", "delete_export", "put_endpoint_set", "delete_endpoint_set",
	"put_pool", "renew_pool_lease", "delete_pool",
	"list_cluster_sets", "load_cluster_set", "ping",
}

// Store results of store_request_duration_seconds: ok, conditional (the
// write's condition refused it, or the record was not found) and error.
var StoreResults = []string{"ok", "conditional", "error"}

// Conditional failure reasons of store_conditional_failures_total, keyed by
// the operations that can produce them.
var ConditionalFailures = map[string][]string{
	"renew_cluster_lease": {"lease_epoch_mismatch", "not_found"},
	"put_export":          {"stale_generation"},
	"put_endpoint_set":    {"stale_generation", "too_large"},
	"put_pool":            {"stale_generation", "pool_name_taken"},
	"renew_pool_lease":    {"lease_epoch_mismatch", "not_found"},
}

// Metrics holds every gds-server collector.
type Metrics struct {
	Clusters       *prometheus.GaugeVec
	Exports        *prometheus.GaugeVec
	Endpoints      *prometheus.GaugeVec
	ServiceImports *prometheus.GaugeVec
	Pools          *prometheus.GaugeVec
	Routers        *prometheus.GaugeVec

	LeaseExpirations *prometheus.CounterVec

	Watchers        *prometheus.GaugeVec
	WatchEventsSent *prometheus.CounterVec
	WatchResyncs    *prometheus.CounterVec
	WatchRejected   *prometheus.CounterVec

	ViewRefreshDuration prometheus.Histogram
	ViewRefreshFailures prometheus.Counter
	ViewAge             prometheus.Gauge

	StoreRequestDuration     *prometheus.HistogramVec
	StoreConditionalFailures *prometheus.CounterVec

	CompactionDeleted *prometheus.CounterVec

	BuildInfo *prometheus.GaugeVec
}

// New creates the collectors, registers them on reg and pre-initialises the
// counters.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Clusters: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "clusters",
			Help: "Registered clusters by lease state (active, expired).",
		}, []string{"clusterset", "state"}),
		Exports: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "exports",
			Help: "ServiceExport records.",
		}, []string{"clusterset"}),
		Endpoints: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "endpoints",
			Help: "Endpoints in stored endpoint sets by readiness (ready, not_ready).",
		}, []string{"clusterset", "condition"}),
		ServiceImports: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "service_imports",
			Help: "Merged ServiceImports by conflict (true, false).",
		}, []string{"clusterset", "conflict"}),
		Pools: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "pools",
			Help: "Pools by derived state.",
		}, []string{"clusterset", "state"}),
		Routers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "routers",
			Help: "Routers derived from exports labelled with the router role.",
		}, []string{"clusterset"}),
		LeaseExpirations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "lease_expirations_total",
			Help: "Leases seen expiring by this replica, by kind (cluster, pool).",
		}, []string{"kind"}),
		Watchers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "watchers",
			Help: "Open watch streams by kind.",
		}, []string{"kind"}),
		WatchEventsSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "watch_events_sent_total",
			Help: "Watch events sent by kind and event type.",
		}, []string{"kind", "type"}),
		WatchResyncs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "watch_resync_total",
			Help: "Watch snapshots sent instead of deltas, by kind and reason (lagged, revision_unknown, revision_expired).",
		}, []string{"kind", "reason"}),
		WatchRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "watch_rejected_total",
			Help: "Watch streams refused, by reason (too_many_watchers, shutting_down).",
		}, []string{"reason"}),
		ViewRefreshDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "view_refresh_duration_seconds",
			Help:    "Duration of one clusterset refresh from the store.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}),
		ViewRefreshFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace, Name: "view_refresh_failures_total",
			Help: "Failed clusterset refreshes and clusterset discoveries.",
		}),
		ViewAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "view_age_seconds",
			Help: "Age of the oldest clusterset view since its last successful refresh.",
		}),
		StoreRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "store_request_duration_seconds",
			Help:    "Store request duration by operation and result (ok, conditional, error).",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"operation", "result"}),
		StoreConditionalFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "store_conditional_failures_total",
			Help: "Store writes refused by their condition, by operation and reason.",
		}, []string{"operation", "reason"}),
		CompactionDeleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "compaction_deleted_total",
			Help: "Records removed by compaction, by kind (cluster, export, endpoint_set, pool).",
		}, []string{"kind"}),
		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "build_info",
			Help: "Build metadata of gds-server; always 1.",
		}, []string{"version", "git_hash"}),
	}
	reg.MustRegister(
		m.Clusters, m.Exports, m.Endpoints, m.ServiceImports, m.Pools, m.Routers,
		m.LeaseExpirations,
		m.Watchers, m.WatchEventsSent, m.WatchResyncs, m.WatchRejected,
		m.ViewRefreshDuration, m.ViewRefreshFailures, m.ViewAge,
		m.StoreRequestDuration, m.StoreConditionalFailures,
		m.CompactionDeleted, m.BuildInfo,
	)
	m.init()
	return m
}

func (m *Metrics) init() {
	for _, k := range []string{LeaseCluster, LeasePool} {
		m.LeaseExpirations.WithLabelValues(k)
	}
	for _, k := range Kinds {
		m.Watchers.WithLabelValues(k)
		for _, t := range EventTypes {
			m.WatchEventsSent.WithLabelValues(k, t)
		}
		for _, r := range ResyncReasons {
			m.WatchResyncs.WithLabelValues(k, r)
		}
	}
	for _, r := range []string{RejectTooManyWatchers, RejectShuttingDown} {
		m.WatchRejected.WithLabelValues(r)
	}
	for _, op := range StoreOperations {
		for _, res := range StoreResults {
			m.StoreRequestDuration.WithLabelValues(op, res)
		}
	}
	for op, reasons := range ConditionalFailures {
		for _, r := range reasons {
			m.StoreConditionalFailures.WithLabelValues(op, r)
		}
	}
	for _, k := range []string{RecordCluster, RecordExport, RecordEndpointSet, RecordPool} {
		m.CompactionDeleted.WithLabelValues(k)
	}
}

// SetBuildInfo sets the build_info series.
func (m *Metrics) SetBuildInfo(version, gitHash string) {
	m.BuildInfo.Reset()
	m.BuildInfo.WithLabelValues(version, gitHash).Set(1)
}

// ClusterSetGauges are the per-clusterset object counts.
type ClusterSetGauges struct {
	ActiveClusters, ExpiredClusters int
	Exports                         int
	ReadyEndpoints, NotReadyEnds    int
	Imports, ConflictedImports      int
	PoolsByState                    map[string]int
	Routers                         int
}

// PoolStates are the values of the state label of nvcf_gds_pools.
var PoolStates = []string{"REGISTERED", "ACTIVE", "DRAINING", "UNHEALTHY", "GONE"}

// SetClusterSet sets the gauges of one clusterset.
func (m *Metrics) SetClusterSet(cs string, g ClusterSetGauges) {
	m.Clusters.WithLabelValues(cs, "active").Set(float64(g.ActiveClusters))
	m.Clusters.WithLabelValues(cs, "expired").Set(float64(g.ExpiredClusters))
	m.Exports.WithLabelValues(cs).Set(float64(g.Exports))
	m.Endpoints.WithLabelValues(cs, "ready").Set(float64(g.ReadyEndpoints))
	m.Endpoints.WithLabelValues(cs, "not_ready").Set(float64(g.NotReadyEnds))
	m.ServiceImports.WithLabelValues(cs, "true").Set(float64(g.ConflictedImports))
	m.ServiceImports.WithLabelValues(cs, "false").Set(float64(g.Imports - g.ConflictedImports))
	for _, s := range PoolStates {
		m.Pools.WithLabelValues(cs, s).Set(float64(g.PoolsByState[s]))
	}
	m.Routers.WithLabelValues(cs).Set(float64(g.Routers))
}

// DeleteClusterSet removes the gauges of a clusterset the view dropped.
func (m *Metrics) DeleteClusterSet(cs string) {
	labels := prometheus.Labels{"clusterset": cs}
	for _, g := range []*prometheus.GaugeVec{m.Clusters, m.Exports, m.Endpoints, m.ServiceImports, m.Pools, m.Routers} {
		g.DeletePartialMatch(labels)
	}
}
