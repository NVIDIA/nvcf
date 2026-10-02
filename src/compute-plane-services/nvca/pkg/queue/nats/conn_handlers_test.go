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
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
)

// recordingObserver counts lifecycle callbacks. nats.go dispatches these on its
// own goroutine, so every field is mutex-guarded and read via Eventually.
type recordingObserver struct {
	mu         sync.Mutex
	states     []ConnState
	reconnects int
}

func (o *recordingObserver) ConnectionStateChanged(state ConnState) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.states = append(o.states, state)
}

func (o *recordingObserver) ReconnectSucceeded() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reconnects++
}

func (o *recordingObserver) seen(want ConnState) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, s := range o.states {
		if s == want {
			return true
		}
	}
	return false
}

func (o *recordingObserver) reconnectCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reconnects
}

// first reports the earliest observed state, and whether anything was observed
// at all. The bool is load-bearing: ConnState is a string, so an observer that
// was never called returns "", and a NotEqual against a real state passes for
// it. That is precisely the regression these tests exist to catch, so without
// the flag they would stay green for an observer wired to nothing.
func (o *recordingObserver) first() (ConnState, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.states) == 0 {
		return "", false
	}
	return o.states[0], true
}

func newObservedClient(t *testing.T, obs ConnectionObserver) (*client, func()) {
	t.Helper()
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher, obs)
	require.NoError(t, err)
	return qc.(*client), srv.Shutdown
}

// TestConnectionObserverSeedsConnectedState covers the state reported at
// construction, before any callback has had a chance to run. ConnectHandler
// also reports this connection, but asynchronously, so the seed is what gives
// the gauge a value from process start rather than only once the dispatcher
// gets to it.
func TestConnectionObserverSeedsConnectedState(t *testing.T) {
	obs := &recordingObserver{}
	cl, shutdown := newObservedClient(t, obs)
	t.Cleanup(cl.nc.Close)
	t.Cleanup(shutdown)

	got, ok := obs.first()
	require.True(t, ok, "observer recorded no state at all")
	assert.Equal(t, ConnStateConnected, got,
		"a client that connected must report connected before anything else happens")
}

// TestConnectionObserverReportsUnreachableBrokerAsNotConnected is the case that
// RetryOnFailedConnect creates. nats.Connect now returns a usable client before
// the first connection succeeds, so anything that infers "constructor returned,
// therefore connected" publishes a healthy connection that never connected.
// The state has to come from the connection, not from which call returned.
func TestConnectionObserverReportsUnreachableBrokerAsNotConnected(t *testing.T) {
	obs := &recordingObserver{}

	origURL := DefaultNATSURL
	DefaultNATSURL = "nats://127.0.0.1:1" // unreachable
	t.Cleanup(func() { DefaultNATSURL = origURL })

	qc, err := NewClientWithTokenFetcher(context.Background(), "cluster",
		&staticTokenFetcher{token: "my-jwt"}, obs)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(cl.nc.Close)

	got, ok := obs.first()
	require.True(t, ok, "observer recorded nothing, so the check below would pass vacuously")
	assert.NotEqual(t, ConnStateConnected, got,
		"a client that never reached the broker must not report connected")
	assert.False(t, cl.ConnectionClosed(),
		"still retrying, so this is not the state that warrants a restart")
}

// TestConnectionObserverReportsDelayedFirstConnect is the other half of
// RetryOnFailedConnect, and the case a ConnectHandler is required for. When the
// first connection is deferred, nats.go queues ConnectedCB rather than
// ReconnectedCB, because the latter is gated on !nc.initc
// (nats.go:3403-3405). With only disconnect/reconnect/closed registered, the
// observer's last word on this client is the state seeded while it was still
// dialling, so the gauge reads reconnecting forever while the client polls
// happily. Operators would chase a dead connection that is fine.
func TestConnectionObserverReportsDelayedFirstConnect(t *testing.T) {
	obs := &recordingObserver{}
	seed, pubKey := newUserSeed(t)

	// Reserve a port and free it, so the client has somewhere to fail toward
	// until the broker is started on it below.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close())

	origURL := DefaultNATSURL
	DefaultNATSURL = fmt.Sprintf("nats://127.0.0.1:%d", port)
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher, obs)
	require.NoError(t, err, "RetryOnFailedConnect must let construction succeed with no broker")
	cl := qc.(*client)
	t.Cleanup(cl.nc.Close)

	seeded, ok := obs.first()
	require.True(t, ok, "observer recorded nothing, so the check below would pass vacuously")
	require.NotEqual(t, ConnStateConnected, seeded,
		"must not claim connected before the broker exists")

	srv := natstest.RunServer(&server.Options{
		Port:      port,
		Host:      "127.0.0.1",
		JetStream: true,
		StoreDir:  t.TempDir(),
		Nkeys:     []*server.NkeyUser{{Nkey: pubKey}},
	})
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)

	assert.Eventually(t, func() bool {
		return obs.seen(ConnStateConnected)
	}, 30*time.Second, 50*time.Millisecond,
		"the deferred first connection must be reported as connected")

	// A first connection is not a flap. Counting it here would make every
	// cold start where NATS lags NVCA look like connection instability.
	assert.Zero(t, obs.reconnectCount(),
		"the initial connection must not count as a reconnect")
}

// TestConnectionObserverSeesDisconnect covers the signal that distinguishes a
// broker outage from a healthy idle queue. Before this there were no handlers
// at all, so a NATS drop was invisible in both logs and metrics.
func TestConnectionObserverSeesDisconnect(t *testing.T) {
	obs := &recordingObserver{}
	cl, shutdown := newObservedClient(t, obs)
	t.Cleanup(cl.nc.Close)

	shutdown()

	assert.Eventually(t, func() bool {
		return obs.seen(ConnStateReconnecting) || obs.seen(ConnStateDisconnected)
	}, 10*time.Second, 20*time.Millisecond, "losing the broker must notify the observer")

	// Still retrying rather than closed, so liveness must not fire. This is the
	// mass-restart scenario reduced to a no-op.
	assert.False(t, cl.ConnectionClosed(),
		"a disconnected but retrying client must not look closed")
}

// TestConnectionObserverSeesClose covers the terminal state. It is the one that
// warrants a restart, and with the queue removed from the poll-based liveness
// path it is also what the connection-state alert keys on.
func TestConnectionObserverSeesClose(t *testing.T) {
	obs := &recordingObserver{}
	cl, shutdown := newObservedClient(t, obs)
	t.Cleanup(shutdown)

	cl.nc.Close()

	assert.Eventually(t, func() bool {
		return obs.seen(ConnStateClosed)
	}, 10*time.Second, 20*time.Millisecond, "closing the connection must notify the observer")

	assert.True(t, cl.ConnectionClosed())
}

// TestConnectionHandlersWithoutObserver guards the nil path: the handlers still
// have to be installed so the log lines appear even when nothing is collecting
// metrics.
func TestConnectionHandlersWithoutObserver(t *testing.T) {
	cl, shutdown := newObservedClient(t, nil)
	t.Cleanup(cl.nc.Close)

	require.NotNil(t, cl.nc.Opts.ConnectedCB, "connect handler must be registered")
	require.NotNil(t, cl.nc.Opts.DisconnectedErrCB, "disconnect handler must be registered")
	require.NotNil(t, cl.nc.Opts.ReconnectedCB, "reconnect handler must be registered")
	require.NotNil(t, cl.nc.Opts.ClosedCB, "closed handler must be registered")

	// Must not panic with no observer attached.
	shutdown()
	assert.Eventually(t, cl.nc.IsReconnecting, 10*time.Second, 20*time.Millisecond)
}
