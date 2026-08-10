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
)

// RecordAuthSuccess records a successful authentication request.
func RecordAuthSuccess(plugin, account string) {
	AuthRequestsTotal.WithLabelValues("success", plugin, account, "").Inc()
}

// RecordAuthFailure records a failed authentication request.
func RecordAuthFailure(plugin, account string, cause Cause) {
	AuthRequestsTotal.WithLabelValues("failure", plugin, account, string(cause)).Inc()
}
