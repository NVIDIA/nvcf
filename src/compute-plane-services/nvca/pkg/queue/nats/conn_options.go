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

package nats

import (
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	log "github.com/sirupsen/logrus"
)

// ConnState is the connection's state at the moment of a transition. It is a
// string rather than nats.Status so consumers do not have to import nats.go,
// and because nats.Status' own numbering is not the one the metric uses.
type ConnState string

const (
	ConnStateDisconnected ConnState = "disconnected"
	ConnStateConnected    ConnState = "connected"
	ConnStateReconnecting ConnState = "reconnecting"
	ConnStateClosed       ConnState = "closed"
	ConnStateConnecting   ConnState = "connecting"
)

func connState(nc *nats.Conn) ConnState {
	switch nc.Status() {
	case nats.CONNECTED:
		return ConnStateConnected
	case nats.RECONNECTING:
		return ConnStateReconnecting
	case nats.CLOSED:
		return ConnStateClosed
	case nats.CONNECTING:
		return ConnStateConnecting
	default:
		return ConnStateDisconnected
	}
}

// ConnectionObserver is notified of NATS connection lifecycle transitions. It
// is an interface supplied by the caller so this package stays independent of
// NVCA's metrics; pass nil when only the log lines are wanted.
//
// State is reported rather than inferred from which callback fired. Inferring
// it gets the RetryOnFailedConnect case wrong: nats.Connect returns a usable
// client before the first connection succeeds, so a caller that assumes
// "constructor returned, therefore connected" publishes a healthy connection
// that has never connected.
type ConnectionObserver interface {
	// ConnectionStateChanged fires on construction and on every subsequent
	// transition, with the connection's actual state at that moment.
	ConnectionStateChanged(state ConnState)
	// ReconnectSucceeded fires only when a reconnect completes, so a caller can
	// tell repeated flapping from one long outage.
	ReconnectSucceeded()
}

// firstObserver unwraps the variadic observer argument the constructors accept.
// Returns nil when none was supplied, which leaves the handlers logging only.
func firstObserver(observer []ConnectionObserver) ConnectionObserver {
	if len(observer) == 0 {
		return nil
	}
	return observer[0]
}

// connectionHandlers wires the lifecycle callbacks. These were absent entirely,
// which meant a NATS drop produced no log line at all: the connection would go
// away, polls would fail, and nothing said why. Logging is unconditional; the
// observer is optional and drives metrics.
func connectionHandlers(obs ConnectionObserver) []nats.Option {
	return []nats.Option{
		// Required by RetryOnFailedConnect, not merely symmetrical. When the
		// first connection is deferred, nats.go queues ConnectedCB and not
		// ReconnectedCB, because the latter is gated on !nc.initc
		// (nats.go:3403-3405). Without this handler the only report of that
		// connection is the state seeded in connect() below, which was captured
		// while the client was still dialling, so the gauge would sit at
		// reconnecting for a client that is connected and polling fine.
		//
		// Deliberately does not call ReconnectSucceeded: this is the first
		// connection, and counting it would make a clean start look like a flap.
		nats.ConnectHandler(func(nc *nats.Conn) {
			log.WithField("url", nc.ConnectedUrl()).Info("NATS connection established")
			if obs != nil {
				obs.ConnectionStateChanged(connState(nc))
			}
		}),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			log.WithError(err).Warn("NATS connection lost; client is reconnecting")
			if obs != nil {
				obs.ConnectionStateChanged(connState(nc))
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.WithField("url", nc.ConnectedUrl()).Info("NATS connection re-established")
			if obs != nil {
				obs.ConnectionStateChanged(connState(nc))
				obs.ReconnectSucceeded()
			}
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			// Terminal. nats.go never reopens a closed connection, so this is
			// the state that needs a restart and the one to alert on.
			log.WithError(nc.LastError()).Error("NATS connection closed permanently; queue processing cannot recover without a restart")
			if obs != nil {
				obs.ConnectionStateChanged(ConnStateClosed)
			}
		}),
	}
}

// connect builds the full option set and dials NATS. Both auth modes go through
// here so neither can pick up an option the other misses; authOption is the only
// thing that legitimately differs between them.
func connect(natsURL, natsHostOverride, clusterID string, authOption nats.Option, obs ConnectionObserver) (*client, error) {
	opts := []nats.Option{
		authOption,
		nats.Name(fmt.Sprintf("nvca-queue-client/%s", clusterID)),
	}
	opts = append(opts, resilienceOptions()...)
	opts = append(opts, connectionHandlers(obs)...)
	opts = append(opts, natsHostOverrideOptions(natsURL, natsHostOverride)...)

	nc, err := nats.Connect(natsURLOrDefault(natsURL), opts...)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		_ = nc.Drain()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}

	// Seed the observer with the state we actually reached, so the gauge has a
	// value from process start rather than only after the first transition.
	// ConnectHandler above reports the connection itself, but it is dispatched
	// asynchronously (nats.go:1875-1879), and with RetryOnFailedConnect it may
	// not fire for a long time, so without this seed the metric would be absent
	// exactly while a client is failing to connect. Reports the observed state,
	// never an assumed one: with RetryOnFailedConnect this is reached while
	// still dialling, so publishing "connected" here would be a lie.
	if obs != nil {
		obs.ConnectionStateChanged(connState(nc))
	}

	return &client{
		clusterID: clusterID,
		nc:        nc,
		js:        js,
		consumers: map[string]jetstream.Consumer{},
	}, nil
}

// resilienceOptions returns the connection options every NATS client here must
// share. It exists so the nkey path (NewClientWithURLAndHostOverride) and the
// PSAT path (NewPSATClient...) cannot drift: they build their option slices
// independently, and an option added to only one of them is a bug that shows up
// solely in whichever auth mode production happens to run.
//
// Line references are to the vendored nats.go v1.51.0.
func resilienceOptions() []nats.Option {
	return []nats.Option{
		// Never give up reconnecting. The default is 60 attempts per server,
		// after which selectNextServer (nats.go:1949) drops the server from the
		// pool; once the pool empties the reconnect loop ends by closing the
		// connection with ErrNoServers, and nats.go never reopens a closed
		// connection, so only a process restart recovers. A negative limit makes
		// selectNextServer retain every server, so the pool cannot empty.
		nats.MaxReconnects(-1),

		// Infinite reconnects alone does not close the hole. processAuthError
		// (nats.go:3955) sets nc.ar when the same auth error repeats on a
		// server, and doReconnect (nats.go:3150) then aborts and closes the
		// connection regardless of MaxReconnects. That path is live for us: the
		// PSAT token handler returns "" when a token fetch fails, which the
		// server reports as an authorization violation rather than a network
		// error, so a token-source blip overlapping a reconnect would close the
		// connection for good.
		nats.IgnoreAuthErrorAbort(),

		// Let the client start before NATS is reachable. Without this, NATS
		// being down when NVCA starts makes nats.Connect fail, Agent.Start
		// returns an error, and the pod crash-loops with backoff capped at five
		// minutes, so recovery can lag NATS returning by that much. With it the
		// agent starts, reports not-ready through the queue readiness component,
		// and converges on its own.
		//
		// Tradeoff: a genuine misconfiguration (wrong URL) becomes not-ready
		// indefinitely instead of a fast crash. Readiness surfaces it, and the
		// PSAT path still fails fast on an unusable token source via its
		// pre-flight fetch.
		nats.RetryOnFailedConnect(true),
	}
}
