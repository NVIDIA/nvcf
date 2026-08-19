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
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
)

const (
	RootNamespace = "nvcf_grpc_proxy_service"
	NatsNamespace = RootNamespace + "_nats"
)

var (
	// ExpiringMetrics provides expiring metrics with built-in support for high-cardinality labels
	// that automatically expire when unused to prevent memory issues in Prometheus
	expiringMetrics *ExpiringMetrics = lo.Must(NewExpiringMetrics(6 * time.Hour))

	ActiveHttpRequestsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: RootNamespace,
			Name:      "active_http_requests_total",
			Help:      "total active client http requests",
		})

	ActiveClientConnectionsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: RootNamespace,
			Name:      "active_connections_total",
			Help:      "total active client tcp connections",
		})

	SessionInitTimeCounter = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: RootNamespace,
			Name:      "session_init_seconds_total",
			Help:      "total seconds spent initializing the session",
		}, []string{"is_reconnect"})

	NatsErrorCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: NatsNamespace,
			Name:      "error_total",
			Help:      "total nats errors on a nats connection",
		})

	NatsReconnectCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: NatsNamespace,
			Name:      "reconnect_total",
			Help:      "total nats reconnects on a nats connection",
		})

	NatsLameDuckCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: NatsNamespace,
			Name:      "lame_duck_total",
			Help:      "total number of lame duck messages",
		})

	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "out_bytes",
		Help:      "The number of output bytes for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().OutBytes)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "in_bytes",
		Help:      "The number of input bytes for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().InBytes)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "out_msgs",
		Help:      "The number of output messages for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().OutMsgs)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "in_msgs",
		Help:      "The number of input messages for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().InMsgs)
	})
	_ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: NatsNamespace,
		Name:      "reconnects",
		Help:      "The number of reconnect attempts for this nats connection.",
	}, func() float64 {
		nc := nc.Load()
		if nc == nil {
			return 0
		}
		return float64(nc.Stats().Reconnects)
	})

	// Worker tunnel lifecycle. The proxy closes worker tunnels for several
	// ordinary reasons (cache TTL expiry, the connection going inactive,
	// capacity eviction, shutdown drain). Until now the reason was computed on
	// every close and only written to a DEBUG log, so "why did the tunnel
	// close" could not be answered from production data.

	WorkerConnectionsActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: RootNamespace,
			Name:      "worker_connections_active",
			Help:      "worker tunnel connections currently held by this pod",
		})

	WorkerConnectionOpenedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: RootNamespace,
			Name:      "worker_connection_opened_total",
			Help:      "total worker tunnel connections opened",
		})

	WorkerConnectionClosedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: RootNamespace,
			Name:      "worker_connection_closed_total",
			Help:      "total worker tunnel connections closed, by reason",
		}, []string{"reason"})

	// Complements worker_connection_closed_total. That reports which side tore
	// the tunnel down; this reports what the transport said while it happened,
	// which is what distinguishes a deliberate peer close from an idle timeout
	// from a reset. Label values come from worker.CloseCodes and are bounded.
	WorkerConnectionCloseCodeTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: RootNamespace,
			Name:      "worker_connection_close_code_total",
			Help:      "total worker tunnel connections closed, by transport close code",
		}, []string{"code"})

	// Buckets deliberately straddle the two timers that can close a tunnel:
	// the worker-side QUIC idle timeout (8s) and this service's worker
	// connection cache TTL (consts.Timeout, 30s). A hard cliff at either
	// value indicates connections being killed by a timer rather than ending
	// naturally.
	WorkerConnectionDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: RootNamespace,
			Name:      "worker_connection_duration_seconds",
			Help:      "how long worker tunnel connections stayed open before being closed",
			Buckets:   []float64{1, 5, 8, 10, 15, 30, 45, 60, 120, 300, 600, 1800, 3600},
		})
)

// Close reasons reported when a worker tunnel goes away. The three "deleted"
// variants matter: a bare `deleted` cannot distinguish the client hanging up
// from the worker hanging up from a proxy shutdown, and which side went first
// is usually the question being asked during an incident.
const (
	// CloseReasonClientClosed means the client connection closed and the proxy
	// tore down the worker tunnel in response (worker/connections.go).
	CloseReasonClientClosed = "client_closed"
	// CloseReasonWorkerClosed means the worker tunnel itself closed and the
	// proxy reacted (worker/worker.go CloseFuncConn).
	CloseReasonWorkerClosed = "worker_closed"
	// CloseReasonShutdown means the proxy is draining and closed everything.
	CloseReasonShutdown = "shutdown"
	// CloseReasonTTLExpired means the worker connection cache entry expired
	// before the tunnel was ever dialled. Established tunnels are set to
	// NoTTL, so this can only happen pre-activation.
	CloseReasonTTLExpired = "ttl_expired"
	// CloseReasonCapacity means the cache evicted under capacity pressure.
	CloseReasonCapacity = "capacity_reached"
	// CloseReasonDeleted is the fallback when the entry was deleted but no
	// origin was recorded. Seeing this in production means a close path is
	// missing instrumentation.
	CloseReasonDeleted = "deleted"
	CloseReasonUnknown = "unknown"
)

// Transport-level close codes. Deliberately a small, bounded set so they are
// safe as a metric label; anything unrecognised lands on CloseCodeUnknown
// rather than growing the label space. Classification lives in
// worker.ClassifyCloseError; the constants live here alongside the other label
// values.
const (
	// CloseCodeNone means no transport error was seen. The connection closed
	// locally without the peer or the stack reporting a fault.
	CloseCodeNone = "none"
	// CloseCodeEOF is a clean peer close.
	CloseCodeEOF = "eof"
	// CloseCodeReset is a TCP RST or equivalent abrupt teardown.
	CloseCodeReset = "reset"
	// CloseCodeTimeout is a read or write deadline expiring.
	CloseCodeTimeout = "timeout"
	// CloseCodeClosedConn is use of an already-closed connection, normally our
	// own teardown racing the transport.
	CloseCodeClosedConn = "closed_conn"
	// CloseCodeContextCanceled is the session context ending.
	CloseCodeContextCanceled = "context_canceled"

	// CloseCodeQUICIdleTimeout is QUIC's own idle timer expiring.
	CloseCodeQUICIdleTimeout = "quic_idle_timeout"
	// CloseCodeQUICApplication is a CONNECTION_CLOSE carrying an application
	// error code. The informative one: it has both a code and a peer-supplied
	// reason string.
	CloseCodeQUICApplication = "quic_application"
	// CloseCodeQUICTransport is a CONNECTION_CLOSE at the transport layer.
	CloseCodeQUICTransport = "quic_transport"
	// CloseCodeQUICStreamReset is RESET_STREAM; the connection survives.
	CloseCodeQUICStreamReset = "quic_stream_reset"
	// CloseCodeQUICHandshakeTimeout is the handshake failing to complete.
	CloseCodeQUICHandshakeTimeout = "quic_handshake_timeout"
	// CloseCodeQUICStatelessReset means the peer lost connection state.
	CloseCodeQUICStatelessReset = "quic_stateless_reset"

	// CloseCodeH2GoAway is an HTTP/2 GOAWAY frame.
	CloseCodeH2GoAway = "h2_goaway"
	// CloseCodeH2Stream is an HTTP/2 stream-level error.
	CloseCodeH2Stream = "h2_stream"
	// CloseCodeH2Connection is an HTTP/2 connection-level error.
	CloseCodeH2Connection = "h2_connection"

	// CloseCodeUnknown is an error the classifier does not recognise. The
	// detail field still carries the original text.
	CloseCodeUnknown = "unknown"
)

// CloseCodes enumerates every value the classifier can return, so the metric
// can be pre-initialised and appear on the first scrape.
var CloseCodes = []string{
	CloseCodeNone,
	CloseCodeEOF,
	CloseCodeReset,
	CloseCodeTimeout,
	CloseCodeClosedConn,
	CloseCodeContextCanceled,
	CloseCodeQUICIdleTimeout,
	CloseCodeQUICApplication,
	CloseCodeQUICTransport,
	CloseCodeQUICStreamReset,
	CloseCodeQUICHandshakeTimeout,
	CloseCodeQUICStatelessReset,
	CloseCodeH2GoAway,
	CloseCodeH2Stream,
	CloseCodeH2Connection,
	CloseCodeUnknown,
}

// WorkerConnectionCloseReasons enumerates every reason the proxy reports when
// closing a worker tunnel. Kept here so the counter can be pre-initialised to
// zero for each one: uninitialised counters produce gaps in rate() and make
// absent() alerts misfire.
var WorkerConnectionCloseReasons = []string{
	CloseReasonClientClosed,
	CloseReasonWorkerClosed,
	CloseReasonShutdown,
	CloseReasonTTLExpired,
	CloseReasonCapacity,
	CloseReasonDeleted,
	CloseReasonUnknown,
}

// Outcomes of a worker CONNECT to /v1/proxy. Every terminal path in
// HijackHandler maps to exactly one of these.
const (
	ConnectAccepted            = "accepted"
	ConnectNotHijackable       = "rejected_not_hijackable"    // 500
	ConnectMissingAuth         = "rejected_missing_auth"      // 401
	ConnectMissingRequestID    = "rejected_missing_requestid" // 400
	ConnectInvalidRequestID    = "rejected_invalid_requestid" // 400
	ConnectTokenExpired        = "rejected_token_expired"     // 403, token was issued but has aged out
	ConnectTokenUnknown        = "rejected_token_unknown"     // 403, token was never issued by this pod
	ConnectRequestIDMismatch   = "rejected_requestid_mismatch"// 403, token valid but bound to another request
	ConnectHijackFailed        = "rejected_hijack_failed"     // 500
)

var ConnectResults = []string{
	ConnectAccepted,
	ConnectNotHijackable,
	ConnectMissingAuth,
	ConnectMissingRequestID,
	ConnectInvalidRequestID,
	ConnectTokenExpired,
	ConnectTokenUnknown,
	ConnectRequestIDMismatch,
	ConnectHijackFailed,
}

var (
	// WorkerConnectTotal counts every CONNECT attempt by outcome. Splitting
	// the 403s into expired / unknown / mismatch is the point: they are
	// indistinguishable in the response and in the log today.
	WorkerConnectTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: RootNamespace,
			Name:      "worker_connect_total",
			Help:      "worker CONNECT attempts to /v1/proxy, by outcome",
		}, []string{"result"})

	// WorkerConnectTokenAgeSeconds is how old the worker token was when the
	// CONNECT arrived. The token lifetime is consts.Timeout (30s), so this
	// shows directly how much headroom real traffic has. Buckets are dense
	// below 30s because that is where the cliff is.
	WorkerConnectTokenAgeSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: RootNamespace,
			Name:      "worker_connect_token_age_seconds",
			Help:      "age of the worker token when the CONNECT was processed",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 15, 20, 25, 30, 45, 60, 120, 300},
		}, []string{"result"})

	// WorkerTokenIssuedTotal counts tokens minted, so issuance can be
	// compared against accepted CONNECTs. A growing gap means tokens are
	// being minted and never used.
	WorkerTokenIssuedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: RootNamespace,
			Name:      "worker_token_issued_total",
			Help:      "worker CONNECT tokens minted",
		})

	// ClientConnectionOpenedTotal and ClientConnectionDurationSeconds cover
	// the client side of the proxy. Only an active gauge existed before, so
	// churn and lifetime were invisible.
	ClientConnectionOpenedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: RootNamespace,
			Name:      "client_connection_opened_total",
			Help:      "client connections accepted",
		})

	ClientConnectionDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: RootNamespace,
			Name:      "client_connection_duration_seconds",
			Help:      "how long client connections stayed open",
			Buckets:   []float64{1, 5, 8, 10, 15, 30, 45, 60, 120, 300, 600, 1800, 3600},
		})

	// ClientConnectionWorkerTunnelsAtClose records how many worker tunnels a
	// client connection was still holding when it closed. Anything above zero
	// means that close tore down live tunnels.
	ClientConnectionWorkerTunnelsAtClose = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: RootNamespace,
			Name:      "client_connection_worker_tunnels_at_close",
			Help:      "worker tunnels still attached to a client connection when it closed",
			Buckets:   []float64{0, 1, 2, 3, 5, 10, 25, 50, 100},
		})
)

func init() {
	// Set up OpenTelemetry metrics with Prometheus exporter
	exporter := lo.Must(otelprom.New())
	provider := metric.NewMeterProvider(metric.WithReader(exporter))
	otel.SetMeterProvider(provider)

	// Pre-initialise every close reason and CONNECT outcome so the series
	// exist on the first scrape rather than appearing only once each first
	// occurs. Uninitialised counters leave gaps in rate() and make absent()
	// alerts misfire.
	for _, reason := range WorkerConnectionCloseReasons {
		WorkerConnectionClosedTotal.WithLabelValues(reason)
	}
	for _, result := range ConnectResults {
		WorkerConnectTotal.WithLabelValues(result)
	}
	for _, code := range CloseCodes {
		WorkerConnectionCloseCodeTotal.WithLabelValues(code)
	}
}

var nc atomic.Pointer[nats.Conn]

func SetNatsStatsConnection(newNc *nats.Conn) {
	nc.Store(newNc)
}

func IncrFunctionRequest(functionID, functionVersionID, ncaID string) {
	expiringMetrics.IncrFunctionRequest(functionID, functionVersionID, ncaID)
}
