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
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

// tokenFetchTimeout bounds the time the nats reconnect callback is willing to
// wait on the TokenFetcher. Short enough that a hung fetcher can't stall the
// reconnect loop forever; long enough to tolerate a single disk read plus
// occasional token caching work upstream.
const tokenFetchTimeout = 5 * time.Second

// TokenFetcher is the subset of the agent's auth.TokenFetcher interface that
// this package needs to mint per-connect JWTs for NATS auth_callout. It's
// declared here (rather than imported from internal/auth) so the queue package
// doesn't depend on the agent's auth module — any type implementing
// {@code FetchToken(ctx) (string, error)} can be plugged in, which keeps the
// package independently testable with a trivial fake.
type TokenFetcher interface {
	FetchToken(ctx context.Context) (string, error)
}

// authCalloutRequest is the JSON envelope expected by the NATS auth callout service.
// The auth callout decodes the NATS token as base64url JSON and routes to the named plugin.
type authCalloutRequest struct {
	Account    string `json:"account"`
	PluginName string `json:"pluginName"`
	Payload    string `json:"payload"`
}

// buildAuthCalloutToken wraps a JWT in the base64url-encoded JSON envelope
// expected by the NATS auth callout service.
func buildAuthCalloutToken(account, pluginName, jwt string) string {
	req := authCalloutRequest{
		Account:    account,
		PluginName: pluginName,
		Payload:    jwt,
	}
	jsonBytes, _ := json.Marshal(req)
	return base64.RawURLEncoding.EncodeToString(jsonBytes)
}

// NewClientWithTokenFetcher creates a JetStream-backed queue client that
// authenticates via NATS auth_callout using JWTs produced by the given
// TokenFetcher. The fetcher is invoked on every NATS connect/reconnect so
// rotated projected-SA tokens are picked up automatically without an agent
// restart.
//
// `ctx` is used for the one-shot pre-flight token fetch during construction
// (mirrors how NewClient fetches NATS secrets up front) — caller cancellation
// aborts startup cleanly. The nats.go reconnect callback runs async for the
// lifetime of the connection, so it uses its own bounded context internally
// rather than closing over the caller's ctx (which may outlive / be canceled
// independently of the long-lived NATS session).
func NewClientWithTokenFetcher(ctx context.Context, clusterID string, fetcher TokenFetcher) (queue.Client, error) {
	return NewClientWithTokenFetcherURL(ctx, "", clusterID, fetcher)
}

// NewClientWithTokenFetcherURL creates a JetStream-backed queue client for a configured NATS URL.
func NewClientWithTokenFetcherURL(ctx context.Context, natsURL, clusterID string, fetcher TokenFetcher) (queue.Client, error) {
	if fetcher == nil {
		return nil, fmt.Errorf("token fetcher is required")
	}

	// Pre-flight: verify the fetcher can produce a token before we dial NATS.
	// Catches misconfiguration (missing file, unreachable source) with a
	// precise error instead of a less-helpful "nats: authorization violation"
	// on the subsequent Connect.
	if _, err := fetcher.FetchToken(ctx); err != nil {
		return nil, fmt.Errorf("pre-flight token fetch: %w", err)
	}

	tokenHandler := nats.TokenHandler(func() string {
		// Reconnect callback. Bounded fresh context so a slow/hung fetcher
		// can't stall the nats.go reconnect loop, and so the callback keeps
		// working after the construction ctx (above) has been cancelled.
		// Returning an empty string lets nats.Connect surface a clean auth
		// failure; the reconnect loop will try again.
		fctx, cancel := context.WithTimeout(context.Background(), tokenFetchTimeout)
		defer cancel()
		jwt, err := fetcher.FetchToken(fctx)
		if err != nil {
			log.Errorf("Failed to fetch PSAT token for NATS auth: %v", err)
			return ""
		}
		return buildAuthCalloutToken("APP", "oidc", jwt)
	})

	nc, err := nats.Connect(natsURLOrDefault(natsURL),
		tokenHandler,
		nats.Name(fmt.Sprintf("nvca-queue-client/%s", clusterID)))
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		_ = nc.Drain()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}

	return &client{
		clusterID: clusterID,
		nc:        nc,
		js:        js,
		consumers: map[string]jetstream.Consumer{},
	}, nil
}
