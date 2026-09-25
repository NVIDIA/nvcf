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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticTokenFetcher returns the same token on every call. Used to verify
// the happy-path auth-callout envelope construction.
type staticTokenFetcher struct {
	token string
	calls int32
}

func (f *staticTokenFetcher) FetchToken(context.Context) (string, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.token, nil
}

// rotatingTokenFetcher returns a different value on each call, simulating
// kubelet PSAT rotation between NATS reconnects.
type rotatingTokenFetcher struct {
	tokens []string
	idx    int32
}

func (f *rotatingTokenFetcher) FetchToken(context.Context) (string, error) {
	i := atomic.AddInt32(&f.idx, 1) - 1
	if int(i) >= len(f.tokens) {
		i = int32(len(f.tokens)) - 1
	}
	return f.tokens[i], nil
}

// errTokenFetcher always fails. Used to verify the NATS TokenHandler returns
// an empty string (rather than panicking) when the fetcher can't produce a token.
type errTokenFetcher struct{ err error }

func (f *errTokenFetcher) FetchToken(context.Context) (string, error) {
	return "", f.err
}

// ctxAwareFetcher returns ctx.Err() when the caller's context is done. Used
// to verify the constructor's pre-flight fetch honors caller cancellation.
type ctxAwareFetcher struct{}

func (f *ctxAwareFetcher) FetchToken(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "ok", nil
}

func TestBuildAuthCalloutToken(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.payload.sig"
	token := buildAuthCalloutToken("APP", "oidc", jwt)

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)

	var req authCalloutRequest
	require.NoError(t, json.Unmarshal(decoded, &req))

	assert.Equal(t, "APP", req.Account)
	assert.Equal(t, "oidc", req.PluginName)
	assert.Equal(t, jwt, req.Payload)
}

func TestBuildAuthCalloutToken_EmptyJWT(t *testing.T) {
	token := buildAuthCalloutToken("APP", "oidc", "")

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)

	var req authCalloutRequest
	require.NoError(t, json.Unmarshal(decoded, &req))

	assert.Equal(t, "APP", req.Account)
	assert.Equal(t, "oidc", req.PluginName)
	assert.Empty(t, req.Payload)
}

func TestNewClientWithTokenFetcher_NilFetcher(t *testing.T) {
	_, err := NewClientWithTokenFetcher(context.Background(), "cluster", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token fetcher is required")
}

func TestNewClientWithTokenFetcher_PreFlightFailure(t *testing.T) {
	// A broken fetcher surfaces a precise error at construction, before nats.Connect
	// is invoked. Guards the fail-fast contract that the constructor uses the
	// caller's ctx for its one pre-flight FetchToken.
	_, err := NewClientWithTokenFetcher(context.Background(), "cluster",
		&errTokenFetcher{err: errors.New("file missing")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-flight token fetch")
	assert.Contains(t, err.Error(), "file missing")
}

func TestNewClientWithTokenFetcher_PreFlightRespectsCancelledCtx(t *testing.T) {
	// A cancelled ctx passed to the constructor must short-circuit the pre-flight
	// fetch rather than waste a NATS connect attempt. This exercises the "honest
	// use of the caller's ctx" contract (different from the bounded
	// reconnect-callback ctx used later in the lifecycle).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClientWithTokenFetcher(ctx, "cluster", &ctxAwareFetcher{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-flight token fetch")
}

func TestNewClientWithTokenFetcher_NatsUnreachable(t *testing.T) {
	// An unreachable broker must no longer fail construction. This test
	// previously asserted the opposite, and that was the crash-loop: the error
	// propagated out of Agent.Start, the pod restarted, and CrashLoopBackOff
	// caps at five minutes, so recovery could lag NATS returning by that long.
	//
	// With RetryOnFailedConnect the client is created and keeps retrying.
	// Readiness reports not-ready until a poll succeeds, and liveness stays OK
	// because a retrying connection is not a closed one. A genuine misconfig is
	// still caught early on this path by the pre-flight token fetch above.
	origURL := DefaultNATSURL
	DefaultNATSURL = "nats://127.0.0.1:1" // unreachable
	defer func() { DefaultNATSURL = origURL }()

	qc, err := NewClientWithTokenFetcher(context.Background(), "cluster",
		&staticTokenFetcher{token: "my-jwt"})
	require.NoError(t, err, "an unreachable broker must not fail startup")
	require.NotNil(t, qc)

	cl := qc.(*client)
	t.Cleanup(cl.nc.Close)

	assert.False(t, cl.ConnectionClosed(),
		"the client is retrying, so liveness must not restart the pod")
}

func TestFetcherHappyPath_ProducesValidEnvelope(t *testing.T) {
	// Simulate what the nats.TokenHandler does: fetch, then wrap.
	fetcher := &staticTokenFetcher{token: "my-jwt-token"}

	jwt, err := fetcher.FetchToken(context.Background())
	require.NoError(t, err)
	token := buildAuthCalloutToken("APP", "oidc", jwt)

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	var req authCalloutRequest
	require.NoError(t, json.Unmarshal(decoded, &req))
	assert.Equal(t, "my-jwt-token", req.Payload)
}

func TestFetcherRotation_ProducesDifferentTokensOnReconnect(t *testing.T) {
	// Kubelet rotates the PSAT; nats.go calls the handler again on reconnect.
	// Verify each fetcher call can yield a distinct auth-callout envelope.
	fetcher := &rotatingTokenFetcher{tokens: []string{"token-v1", "token-v2"}}

	j1, err := fetcher.FetchToken(context.Background())
	require.NoError(t, err)
	j2, err := fetcher.FetchToken(context.Background())
	require.NoError(t, err)

	t1 := buildAuthCalloutToken("APP", "oidc", j1)
	t2 := buildAuthCalloutToken("APP", "oidc", j2)
	assert.NotEqual(t, t1, t2, "rotated JWTs must produce distinct envelopes")
}

func TestFetcherFailure_SurfacedViaEmptyString(t *testing.T) {
	// If the underlying fetcher errors (file missing, API down, etc.), the
	// handler must fall back to returning an empty string so nats.Connect
	// can surface a clean auth failure rather than hanging.
	fetcher := &errTokenFetcher{err: errors.New("file missing")}
	jwt, err := fetcher.FetchToken(context.Background())
	require.Error(t, err)
	assert.Empty(t, jwt)
}

// flakyTokenFetcher can be switched between working and failing at runtime, so
// a test can break the token source underneath a live connection.
type flakyTokenFetcher struct {
	mu       sync.Mutex
	jwt      string
	err      error
	failures int
}

func (f *flakyTokenFetcher) FetchToken(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		f.failures++
		return "", f.err
	}
	return f.jwt, nil
}

func (f *flakyTokenFetcher) failureCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failures
}

func (f *flakyTokenFetcher) breakSource(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *flakyTokenFetcher) fixSource() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = nil
}

// TestPSATTokenFetchFailureDuringReconnectRecovers is Kristina's item 2 on the
// production auth path rather than a synthetic one.
//
// When a token fetch fails the handler returns "", and the server rejects that
// as an authorization violation, not a network error. processAuthError
// (nats.go:3955) latches nc.ar when the same auth error repeats on a server and
// doReconnect (nats.go:3150) then closes the connection regardless of
// MaxReconnects, and nats.go never reopens a closed connection. So a token
// source that blips across a reconnect would take the queue down until the pod
// restarted. IgnoreAuthErrorAbort is what prevents that, and this proves it:
// the connection survives repeated auth rejections and the same client
// reconnects once the token source returns.
//
// This is the live 53-minute outage in plans/repro-1590 reduced to a unit test;
// there the auth callout service was down and NVCA logged 96 authorization
// violations without ever closing the connection.
func TestPSATTokenFetchFailureDuringReconnectRecovers(t *testing.T) {
	const jwt = "psat-jwt-under-test"
	// The server accepts exactly the envelope a working fetcher produces, so a
	// failed fetch (empty token) is rejected as an auth violation.
	validToken := buildAuthCalloutToken("APP", "oidc", jwt)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close())

	opts := func() *server.Options {
		return &server.Options{
			Port:          port,
			Host:          "127.0.0.1",
			JetStream:     true,
			StoreDir:      t.TempDir(),
			Authorization: validToken,
		}
	}

	srv := natstest.RunServer(opts())
	require.True(t, srv.ReadyForConnections(10*time.Second))

	origURL := DefaultNATSURL
	DefaultNATSURL = fmt.Sprintf("nats://127.0.0.1:%d", port)
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := &flakyTokenFetcher{jwt: jwt}
	obs := &recordingObserver{}

	qc, err := NewClientWithTokenFetcher(context.Background(), "cluster", fetcher, obs)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(cl.nc.Close)
	require.True(t, cl.nc.IsConnected(), "must be connected while the token source works")

	// Break the token source, then drop the connection. The broker comes back
	// immediately so it is auth, not the network, that rejects each reconnect —
	// which is the case that used to close the connection for good.
	fetcher.breakSource(errors.New("projected service account token unreadable"))
	srv.Shutdown()
	srv2 := natstest.RunServer(opts())
	require.True(t, srv2.ReadyForConnections(10*time.Second))
	t.Cleanup(srv2.Shutdown)

	require.Eventually(t, func() bool {
		return !cl.nc.IsConnected()
	}, 15*time.Second, 20*time.Millisecond,
		"the connection must actually drop, or the rest of this proves nothing")

	// The reconnect loop has to actually be rejected on auth, more than once,
	// since it is the repeat that latches nc.ar. Without asserting this the test
	// cannot tell a rejected reconnect from a successful one and would pass
	// against a server that ignored the token entirely.
	require.Eventually(t, func() bool {
		return fetcher.failureCount() >= 2
	}, 20*time.Second, 50*time.Millisecond,
		"the token handler must be called and fail on each reconnect attempt")

	require.False(t, cl.nc.IsConnected(),
		"an empty token must not be accepted; if this connects the server is not "+
			"enforcing auth and the test proves nothing")
	require.Equal(t, nats.RECONNECTING, cl.nc.Status(),
		"the client must still be retrying, not closed or connected")
	require.False(t, cl.ConnectionClosed(),
		"repeated auth rejections must not close the connection permanently")

	// Token source returns. The same client must recover on its own.
	fetcher.fixSource()

	assert.Eventually(t, func() bool {
		return cl.nc.IsConnected()
	}, 30*time.Second, 50*time.Millisecond,
		"the same client must reconnect once the token source recovers, with no restart")

	assert.False(t, cl.ConnectionClosed())
	assert.True(t, obs.seen(ConnStateConnected))
}
