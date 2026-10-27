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

// Package metrics defines the nvcf_gds_mcs_controller_* Prometheus
// collectors of gds-mcs-controller. Counters and labelled gauges are
// pre-initialised for every known label value so that they appear on the
// first scrape. The GDS client adds nvcf_gds_client_* and controller-runtime
// its own controller_runtime_* metrics.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Namespace prefixes every metric name.
const Namespace = "nvcf_gds_mcs_controller"

// Controllers, the values of the controller label.
const (
	ControllerServiceExport = "serviceexport"
	ControllerAutoExport    = "autoexport"
	ControllerServiceImport = "serviceimport"
	ControllerPoolExport    = "poolexport"
)

// Controllers lists every controller.
var Controllers = []string{ControllerServiceExport, ControllerAutoExport, ControllerServiceImport, ControllerPoolExport}

// Reconcile results.
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

// Results lists the reconcile results.
var Results = []string{ResultSuccess, ResultError}

// Export states, the values of the state label of exports.
const (
	// ExportExported: spec and endpoints are in GDS.
	ExportExported = "exported"
	// ExportPending: not written yet, for example while the cluster is not
	// registered.
	ExportPending = "pending"
	// ExportInvalid: Valid=False (no Service or an ExternalName Service).
	ExportInvalid = "invalid"
	// ExportFailed: the last GDS write failed.
	ExportFailed = "failed"
)

// ExportStates lists every export state.
var ExportStates = []string{ExportExported, ExportPending, ExportInvalid, ExportFailed}

// GDS write operations and results.
const (
	OpPutExport    = "put_export"
	OpDeleteExport = "delete_export"

	WriteSuccess         = "success"
	WriteStaleGeneration = "stale_generation"
	WriteNotRegistered   = "not_registered"
	WriteError           = "error"
)

// WriteOps lists the export write operations.
var WriteOps = []string{OpPutExport, OpDeleteExport}

// WriteResults lists the GDS write results.
var WriteResults = []string{WriteSuccess, WriteStaleGeneration, WriteNotRegistered, WriteError}

// Import watch event types.
var EventTypes = []string{"snapshot_start", "upsert", "delete", "snapshot_end", "bookmark"}

// Full resync reasons.
const (
	// ResyncStartup: the leader's first resync.
	ResyncStartup = "startup"
	// ResyncReconnect: the GDS client reconnected.
	ResyncReconnect = "reconnect"
	// ResyncReregister: the cluster was registered again after a lost lease.
	ResyncReregister = "reregister"
	// ResyncSnapshot: the import watch received a new snapshot it could not
	// resume.
	ResyncSnapshot = "snapshot"
)

// ResyncReasons lists every resync reason.
var ResyncReasons = []string{ResyncStartup, ResyncReconnect, ResyncReregister, ResyncSnapshot}

// Kinds of objects deleted by garbage collection.
const (
	GCExport        = "gds_export"
	GCServiceImport = "service_import"
	GCService       = "service"
	GCEndpointSlice = "endpoint_slice"
)

// GCKinds lists every garbage-collected kind.
var GCKinds = []string{GCExport, GCServiceImport, GCService, GCEndpointSlice}

// Heartbeat failure reasons.
const (
	HeartbeatEpochMismatch = "epoch_mismatch"
	HeartbeatNotRegistered = "not_registered"
	HeartbeatError         = "error"
)

// HeartbeatReasons lists every heartbeat failure reason.
var HeartbeatReasons = []string{HeartbeatEpochMismatch, HeartbeatNotRegistered, HeartbeatError}

// Metrics holds every gds-mcs-controller collector.
type Metrics struct {
	Exports                 *prometheus.GaugeVec
	ReconcileDuration       *prometheus.HistogramVec
	Reconciles              *prometheus.CounterVec
	ExportWrites            *prometheus.CounterVec
	EndpointReplacements    *prometheus.CounterVec
	ExportedEndpoints       prometheus.Gauge
	Imports                 prometheus.Gauge
	ImportEvents            *prometheus.CounterVec
	ImportsPendingNamespace prometheus.Gauge
	StreamReconnects        prometheus.Counter
	Resyncs                 *prometheus.CounterVec
	GCDeletions             *prometheus.CounterVec
	HeartbeatFailures       *prometheus.CounterVec
	Registrations           *prometheus.CounterVec
	ClusterRegistered       prometheus.Gauge
	PoolExports             prometheus.Gauge
}

// New creates the collectors with every label value pre-initialised.
func New() *Metrics {
	m := &Metrics{
		Exports: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "exports",
			Help: "ServiceExports handled by this controller by state (exported, pending, invalid, failed).",
		}, []string{"state"}),
		ReconcileDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "reconcile_duration_seconds",
			Help:    "Duration of one reconcile by controller.",
			Buckets: prometheus.DefBuckets,
		}, []string{"controller"}),
		Reconciles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "reconciles_total",
			Help: "Reconciles by controller and result (success, error).",
		}, []string{"controller", "result"}),
		ExportWrites: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "export_writes_total",
			Help: "PutServiceExport and DeleteServiceExport calls by operation and result.",
		}, []string{"op", "result"}),
		EndpointReplacements: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "endpoint_replacements_total",
			Help: "ReplaceEndpoints calls by result.",
		}, []string{"result"}),
		ExportedEndpoints: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "exported_endpoints",
			Help: "Endpoints of all exports as last written to GDS.",
		}),
		Imports: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "imports",
			Help: "ServiceImports in the controller's view of GDS.",
		}),
		ImportEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "import_events_total",
			Help: "WatchServiceImports events received by type.",
		}, []string{"type"}),
		ImportsPendingNamespace: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "imports_pending_namespace",
			Help: "Imports held back because their namespace does not exist.",
		}),
		StreamReconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace, Name: "stream_reconnects_total",
			Help: "Reconnects of the GDS client after its first connection.",
		}),
		Resyncs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "resyncs_total",
			Help: "Full resyncs by reason (startup, reconnect, reregister, snapshot).",
		}, []string{"reason"}),
		GCDeletions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "gc_deletions_total",
			Help: "Objects deleted by garbage collection by kind.",
		}, []string{"kind"}),
		HeartbeatFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "heartbeat_failures_total",
			Help: "Failed cluster heartbeats by reason.",
		}, []string{"reason"}),
		Registrations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "registrations_total",
			Help: "RegisterCluster calls by result (success, error).",
		}, []string{"result"}),
		ClusterRegistered: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "cluster_registered",
			Help: "1 while this replica holds a registered cluster lease, else 0.",
		}),
		PoolExports: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "pool_exports",
			Help: "ServiceExports the controller maintains for API-registered pools.",
		}),
	}
	for _, s := range ExportStates {
		m.Exports.WithLabelValues(s)
	}
	for _, c := range Controllers {
		m.ReconcileDuration.WithLabelValues(c)
		for _, r := range Results {
			m.Reconciles.WithLabelValues(c, r)
		}
	}
	for _, op := range WriteOps {
		for _, r := range WriteResults {
			m.ExportWrites.WithLabelValues(op, r)
		}
	}
	for _, r := range WriteResults {
		m.EndpointReplacements.WithLabelValues(r)
	}
	for _, t := range EventTypes {
		m.ImportEvents.WithLabelValues(t)
	}
	for _, r := range ResyncReasons {
		m.Resyncs.WithLabelValues(r)
	}
	for _, k := range GCKinds {
		m.GCDeletions.WithLabelValues(k)
	}
	for _, r := range HeartbeatReasons {
		m.HeartbeatFailures.WithLabelValues(r)
	}
	for _, r := range Results {
		m.Registrations.WithLabelValues(r)
	}
	return m
}

// Register registers every collector on reg.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		m.Exports, m.ReconcileDuration, m.Reconciles, m.ExportWrites, m.EndpointReplacements,
		m.ExportedEndpoints, m.Imports, m.ImportEvents, m.ImportsPendingNamespace, m.StreamReconnects,
		m.Resyncs, m.GCDeletions, m.HeartbeatFailures, m.Registrations, m.ClusterRegistered, m.PoolExports,
	} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// ObserveReconcile records one reconcile of controller that started at
// start and ended with err.
func (m *Metrics) ObserveReconcile(controller string, start time.Time, err error) {
	m.ReconcileDuration.WithLabelValues(controller).Observe(time.Since(start).Seconds())
	result := ResultSuccess
	if err != nil {
		result = ResultError
	}
	m.Reconciles.WithLabelValues(controller, result).Inc()
}
