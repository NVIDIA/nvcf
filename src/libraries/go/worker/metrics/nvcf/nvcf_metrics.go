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

package nvcf

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
)

const (
	ResponseNamespace     = metrics.NvcfRootNamespace + "_response"
	WorkerThreadNamespace = metrics.NvcfRootNamespace + "_worker_thread"
)

// NVCF metrics shared between utils and niclls containers
var (
	RequestCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NvcfRootNamespace,
			Name:      "request_total",
			Help:      "total requests received by the worker",
		})

	ResponseCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metrics.NvcfRootNamespace,
			Name:      "response_total",
			Help:      "total responses sent by the worker",
		}, []string{"error_code"})

	ResponseBytesCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: ResponseNamespace,
			Name:      "bytes_total",
			Help:      "total size of all responses sent",
		})

	WorkerThreadCountGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: WorkerThreadNamespace,
			Name:      "count_total",
			Help:      "the number of threads handling work",
		})

	WorkerThreadBusyTimeCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: WorkerThreadNamespace,
			Name:      "busy_seconds_total",
			Help:      "total seconds spent being busy by thread",
		})

	NatsErrorCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NatsNamespace,
			Name:      "error_total",
			Help:      "total nats errors on a nats connection",
		})

	NatsReconnectCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NatsNamespace,
			Name:      "reconnect_total",
			Help:      "total nats reconnects on a nats connection",
		})

	NatsLameDuckCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NatsNamespace,
			Name:      "lame_duck_total",
			Help:      "total number of lame duck messages",
		})

	NatsDisconnectCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NatsNamespace,
			Name:      "disconnect_total",
			Help:      "total nats disconnects on a nats connection",
		})

	WorkerNatsServerGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metrics.NatsNamespace,
			Name:      "server_fqdn",
			Help:      "NATS server worker is connected to",
		}, []string{"nats_fqdn"})

	WorkerSubscriptionsConnectedPrimaryRegionGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metrics.NatsNamespace,
			Name:      "connected_pri_region",
			Help:      "primary region that the worker is connected to",
		}, []string{"region"})

	WorkerSubscriptionsConnectedSecondaryRegionsGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metrics.NatsNamespace,
			Name:      "connected_sec_regions",
			Help:      "secondary regions that the worker is connected to",
		}, []string{"regions"})

	HealthcheckCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metrics.NvcfRootNamespace,
			Name:      "healthcheck_total",
			Help:      "total healthchecks performed by the worker",
		}, []string{"result"})

	StatefulProxySuccessCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NvcfRootNamespace,
			Name:      "stateful_proxy_success_total",
			Help:      "total stateful proxy successes",
		})
)
