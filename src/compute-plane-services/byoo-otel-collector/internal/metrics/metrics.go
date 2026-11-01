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

package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
)

const (
	MetricsPort         = 19090
	StatusSuccess       = "success"
	StatusError         = "error"
	RunOtelCollector    = "RunOtelCollector"
	RunSecretsExtractor = "RunSecretsExtractor"
	RunSecretsCheckLoop = "RunSecretsCheckLoop"
	GenerateConfig      = "GenerateConfig"
	ValidateSecretFile  = "ValidateSecretFile"
)

var (
	// counter for service restarts
	serviceRestarts = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "byoo_service_restart",
			Help: "Total number of service restarts due to secrets file changes",
		},
	)

	// gauge for service status
	serviceUp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "byoo_service_up",
			Help: "Whether the BYOO service is up and running",
		},
	)

	// histogram for operation duration
	operationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "byoo_operation_duration_ms",
			Help:    "Duration of operations in milliseconds",
			Buckets: []float64{0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000},
		},
		[]string{"operator", "status"},
	)

	// counter for operation status
	operationStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "byoo_operation_status",
			Help: "Total number of operations by status",
		},
		[]string{"operator", "status"},
	)

	// gauge for latest operation status (0 for error, 1 for success)
	operationLatestStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "byoo_operation_latest_status",
			Help: "Latest status of operations (0=error, 1=success)",
		},
		[]string{"operator"},
	)
)

type MetricsService struct {
	server *http.Server
	port   int
}

func init() {
	// register all metrics
	prometheus.MustRegister(serviceRestarts)
	prometheus.MustRegister(serviceUp)
	prometheus.MustRegister(operationDuration)
	prometheus.MustRegister(operationStatus)
	prometheus.MustRegister(operationLatestStatus)
}

// NewMetricsService create a new metrics service
func NewMetricsService(port int) *MetricsService {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return &MetricsService{
		server: server,
		port:   port,
	}
}

// Start start the metrics service
func (m *MetricsService) Start(ctx context.Context) error {
	logger.Logger.Infof("Starting metrics service on port %d", m.port)

	// set service to running state
	serviceUp.Set(1)

	// start the server in a goroutine
	go func() {
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Logger.Errorf("Metrics server error: %v", err)
		}
	}()

	// wait for context to be done
	<-ctx.Done()

	// graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// set service to stopped state
	serviceUp.Set(0)

	logger.Logger.Info("Shutting down metrics service")
	return m.server.Shutdown(shutdownCtx)
}

// increment service restart
func IncrementServiceRestart() {
	serviceRestarts.Inc()
}

// set service up
func SetServiceUp(up bool) {
	if up {
		logger.Logger.Info("Setting service to up")
		serviceUp.Set(1)
	} else {
		logger.Logger.Info("Setting service to down")
		serviceUp.Set(0)
	}
}

// record operation duration in milliseconds
func RecordOperationDuration(operator, status string, duration time.Duration) {
	durationMs := float64(duration.Nanoseconds()) / 1000000.0
	operationDuration.WithLabelValues(operator, status).Observe(durationMs)
}

func SetOperationStatus(operator, status string) {
	if status == StatusSuccess {
		operationLatestStatus.WithLabelValues(operator).Set(1)
	} else {
		operationLatestStatus.WithLabelValues(operator).Set(0)
	}
}

// increment operation status counter
func IncrementOperationStatus(operator, status string) {
	operationStatus.WithLabelValues(operator, status).Inc()
	SetOperationStatus(operator, status)
}
