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

// Package metrics provides Prometheus instrumentation for the NvSnap agent and server.
package metrics

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "nvsnap"

// Agent metrics — per-node checkpoint/restore operations.
var (
	CheckpointTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "checkpoint_total",
		Help:      "Total checkpoint operations by status and workload type.",
	}, []string{"status", "workload_type", "namespace"})

	CheckpointDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "checkpoint_duration_seconds",
		Help:      "Checkpoint operation duration in seconds.",
		Buckets:   []float64{5, 10, 30, 60, 120, 300, 600},
	}, []string{"workload_type"})

	CheckpointSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "checkpoint_size_bytes",
		Help:      "Checkpoint size in bytes.",
		Buckets:   []float64{1e8, 5e8, 1e9, 5e9, 1e10, 5e10, 1e11}, // 100MB to 100GB
	}, []string{"workload_type"})

	RestoreTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "restore_total",
		Help:      "Total restore operations by status and workload type.",
	}, []string{"status", "workload_type", "namespace"})

	RestoreDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "restore_duration_seconds",
		Help:      "Restore operation duration in seconds.",
		Buckets:   []float64{5, 10, 30, 60, 120, 300, 600},
	}, []string{"workload_type"})

	ActiveOperations = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "active_operations",
		Help:      "Currently active operations.",
	}, []string{"operation"})

	CRIUDumpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "criu_dump_duration_seconds",
		Help:      "CRIU dump phase duration in seconds (subset of total checkpoint time).",
		Buckets:   []float64{1, 5, 10, 30, 60, 120, 300},
	}, []string{"workload_type"})

	GPUProcessesDiscovered = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "gpu_processes_discovered",
		Help:      "Number of GPU processes discovered on this node.",
	})
)

// Server metrics — API and cluster-wide.
var (
	APIRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "api_requests_total",
		Help:      "Total API requests by method, path, and status code.",
	}, []string{"method", "route", "status_code"})

	APIRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "api_request_duration_seconds",
		Help:      "API request duration in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route"})

	CheckpointsStored = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "checkpoints_stored",
		Help:      "Number of checkpoints stored by namespace and node.",
	}, []string{"namespace", "node_name"})

	WebSocketConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "websocket_connections",
		Help:      "Active WebSocket connections.",
	})
)

var (
	agentOnce  sync.Once
	serverOnce sync.Once
)

// RegisterAgent registers agent-side metrics with the default Prometheus registry.
func RegisterAgent() {
	agentOnce.Do(func() {
		prometheus.MustRegister(
			CheckpointTotal,
			CheckpointDuration,
			CheckpointSize,
			RestoreTotal,
			RestoreDuration,
			ActiveOperations,
			CRIUDumpDuration,
			GPUProcessesDiscovered,
		)
	})
}

// RegisterServer registers server-side metrics with the default Prometheus registry.
func RegisterServer() {
	serverOnce.Do(func() {
		prometheus.MustRegister(
			APIRequestsTotal,
			APIRequestDuration,
			CheckpointsStored,
			WebSocketConnections,
		)
	})
}

// Handler returns the Prometheus HTTP handler for /metrics.
func Handler() http.Handler {
	return promhttp.Handler()
}

// InstrumentRoute returns a mux middleware that records per-route request metrics.
func InstrumentRoute() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := mux.CurrentRoute(r)
			var routePattern string
			if route != nil {
				routePattern, _ = route.GetPathTemplate()
			}
			if routePattern == "" {
				routePattern = "unknown"
			}

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rw, r)
			duration := time.Since(start).Seconds()

			APIRequestsTotal.WithLabelValues(r.Method, routePattern, strconv.Itoa(rw.statusCode)).Inc()
			APIRequestDuration.WithLabelValues(r.Method, routePattern).Observe(duration)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Hijack supports WebSocket upgrade by delegating to the underlying ResponseWriter.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack not supported")
}
