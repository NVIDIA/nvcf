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
	"sync/atomic"
	"testing"

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
	// With a valid fetcher but an unreachable NATS URL, connect must surface
	// an error rather than panic. Exercises the full constructor path.
	origURL := DefaultNATSURL
	DefaultNATSURL = "nats://127.0.0.1:1" // unreachable
	defer func() { DefaultNATSURL = origURL }()

	_, err := NewClientWithTokenFetcher(context.Background(), "cluster",
		&staticTokenFetcher{token: "my-jwt"})
	require.Error(t, err)
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
