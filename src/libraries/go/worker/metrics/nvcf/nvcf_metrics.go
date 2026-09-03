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
	QuicNamespace         = metrics.NvcfRootNamespace + "_quic"
)

// Reasons a QUIC dial failed. The label separates the two tunnel failures that
// share the log message "quic connection attempt failed" and can only be told
// apart by the error: a network timeout is flow poisoning, a 403 is the
// saturated backlog wedge. Keep this list short; it is a metric label.
const (
	DialFailureTimeout = "timeout"
	DialFailureAuth    = "auth"
	DialFailureOther   = "other"
)

// Reasons a dial result was declined for rotation. Each of these is a silent
// return in the dial path, and a silent return that reads as "nothing wrong"
// is what made the original defect hard to find. Counting them separates
// "rotation never fired" from "rotation fired and did not help".
//
// stale_transport covers successful dials as well as failed ones: the
// staleness guard runs before the error is examined, so a dial that succeeded
// on a superseded socket lands here too.
const (
	DialSkipStaleTransport = "stale_transport"
	DialSkipCtxCancelled   = "ctx_cancelled"
	DialSkipNotTimeout     = "not_timeout"
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

	// QuicDialCounter and QuicDialFailureCounter are the denominator and
	// numerator of the dial failure rate. Failures alone are not actionable:
	// a routine proxy scale-down produces a large, harmless burst.
	QuicDialCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: QuicNamespace,
			Name:      "dial_total",
			Help:      "total quic dial attempts made by the worker",
		})

	QuicDialFailureCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: QuicNamespace,
			Name:      "dial_failure_total",
			Help:      "total quic dial attempts that failed, by reason",
		}, []string{"reason"})

	// QuicTransportRotationCounter rising alongside dial failures means the
	// worker is recovering on its own. Dial failures rising while this stays
	// flat means it is not, which is the condition that needs an operator.
	QuicTransportRotationCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: QuicNamespace,
			Name:      "transport_rotation_total",
			Help:      "total quic transport rotations after consecutive dial failures",
		})

	QuicDialSkipCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: QuicNamespace,
			Name:      "dial_skip_total",
			Help:      "dial results that did not count toward rotation, by reason",
		}, []string{"reason"})

	QuicTunnelGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: QuicNamespace,
			Name:      "tunnel_active",
			Help:      "quic tunnels currently held open by the worker",
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

// Pre-initialize the dial failure reasons so every series exists at zero on
// the first scrape. Without this, absent() alerts misfire and rate() has gaps
// until the first failure of each kind actually occurs.
func init() {
	for _, reason := range []string{DialFailureTimeout, DialFailureAuth, DialFailureOther} {
		QuicDialFailureCounter.WithLabelValues(reason)
	}
	for _, reason := range []string{DialSkipStaleTransport, DialSkipCtxCancelled, DialSkipNotTimeout} {
		QuicDialSkipCounter.WithLabelValues(reason)
	}
}
