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

package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Cause string

const (
	CauseClient Cause = "client"
	CauseServer Cause = "server"
)

var (
	AuthRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auth_requests_total",
		Help: "Total number of auth requests received",
	}, []string{"status", "plugin", "account", "cause"})

	AuthRequestDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "auth_request_duration_seconds",
		Help:    "Duration of auth request processing",
		Buckets: prometheus.DefBuckets,
	})

	NATSConnectionStatus      = newNATSConnectionStatus()
	NATSConnectionEventsTotal = newNATSConnectionEventsTotal()
)

// newNATSConnectionStatus creates every status series up front so dashboards
// and absent() alerts see them on the first scrape.
func newNATSConnectionStatus() *prometheus.GaugeVec {
	gauge := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvcf_nats_auth_callout_nats_connection_status",
		Help: "Current NATS connection status: 1 for the current status, 0 for every other status",
	}, []string{"status"})
	for _, status := range connectionStatuses {
		gauge.WithLabelValues(statusLabel(status))
	}
	return gauge
}

func newNATSConnectionEventsTotal() *prometheus.CounterVec {
	counter := promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nvcf_nats_auth_callout_nats_connection_events_total",
		Help: "NATS connection lifecycle events by event and failure reason",
	}, []string{"event", "reason"})
	for _, event := range connectionEvents {
		for _, reason := range connectionReasons {
			counter.WithLabelValues(event, reason)
		}
	}
	return counter
}

// RecordAuthSuccess records a successful authentication request.
func RecordAuthSuccess(plugin, account string) {
	AuthRequestsTotal.WithLabelValues("success", plugin, account, "").Inc()
}

// RecordAuthFailure records a failed authentication request.
func RecordAuthFailure(plugin, account string, cause Cause) {
	AuthRequestsTotal.WithLabelValues("failure", plugin, account, string(cause)).Inc()
}
