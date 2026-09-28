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

// Package nvcaintrospect verifies NVCA's Kubernetes projected service-account
// token (PSAT) or SPIFFE SVID against a remote RFC 7662-shaped introspection
// endpoint, for callers that hold neither an OpenBao-issued JWT nor a
// Starfleet SSA token. It is the shared primitive behind ReVal's and Event
// Ledger's own introspection clients; service-specific authorization
// wiring (ReVal's Authorizer adapter, Event Ledger's cluster binding) stays
// in each service.
package nvcaintrospect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const instrumentationName = "nvcaintrospect"

// MaxTokenSize bounds how much bearer token material the client will send.
// Self-managed cluster PSATs are well under 2 KiB; larger tokens are treated
// as abuse.
const MaxTokenSize = 2048

// ErrTokenTooLarge is returned when the bearer token exceeds MaxTokenSize.
var ErrTokenTooLarge = errors.New("bearer token exceeds maximum size of 2048 bytes")

// maxResponseSize bounds how much of the introspection response body the
// client will read. The response is a handful of short string fields; a
// faulty or compromised endpoint could otherwise stream an unbounded body.
const maxResponseSize = 64 * 1024

// errResponseTooLarge is returned when the introspection response exceeds
// maxResponseSize.
var errResponseTooLarge = errors.New("introspect response exceeds maximum size")

// maxCacheEntries bounds the introspection cache so high-cardinality token
// traffic can't grow it without limit.
const maxCacheEntries = 1024

const (
	// psatSubjectPrefix matches Kubernetes service-account token subjects.
	psatSubjectPrefix = "system:serviceaccount:"
	// expectedPSATServiceAccountName is the only ServiceAccount name accepted
	// for PSAT subjects; the namespace is customer-configurable but the SA
	// name is always `nvca`.
	expectedPSATServiceAccountName = "nvca"
	// spiffeSubjectPrefix matches SPIFFE SVID subjects.
	spiffeSubjectPrefix = "spiffe://"
	// spiffeNVCASegment must be the terminal path segment of an accepted
	// SPIFFE SVID (matched with suffix so trailing-path attacks fail).
	spiffeNVCASegment = "/nvca"
)

// IsValidNVCASubject anchors identity to the NVCA workload rather than any
// service account that happens to run in the cluster: PSAT callers must be
// `system:serviceaccount:<any-ns>:nvca`, SPIFFE callers must end with
// `/nvca`. Any other subject is rejected.
func IsValidNVCASubject(sub string) bool {
	if strings.HasPrefix(sub, psatSubjectPrefix) {
		parts := strings.SplitN(sub, ":", 4)
		return len(parts) == 4 && parts[3] == expectedPSATServiceAccountName
	}
	if strings.HasPrefix(sub, spiffeSubjectPrefix) {
		return strings.HasSuffix(sub, spiffeNVCASegment)
	}
	return false
}

// IntrospectRequest is the body sent to the introspection endpoint.
type IntrospectRequest struct {
	Token string `json:"token"`
}

// IntrospectResult is the response from a token introspection endpoint
// (RFC 7662 shape, plus an NVCF-specific resolved cluster identifier that is
// not part of RFC 7662).
type IntrospectResult struct {
	Active    bool   `json:"active"`
	Sub       string `json:"sub"`
	Aud       string `json:"aud,omitempty"`
	Iss       string `json:"iss,omitempty"`
	ClusterID string `json:"cluster_id"`
	TokenType string `json:"token_type,omitempty"`
	Error     string `json:"error,omitempty"`
}

// cacheEntry stores an introspection result (allow or deny) with its
// expiration wall-clock.
type cacheEntry struct {
	result    *IntrospectResult
	expiresAt time.Time
}

// Client calls a remote NVCA token introspection endpoint. Results are
// cached by a hash of the token (never the raw token) for cacheTTL, bounded
// by the token's own exp claim so a cached result never outlives the token
// it was computed for.
//
// Both an active token with a valid NVCA subject and an active token with an
// invalid subject are cached: a token's subject is immutable once issued, so
// either verdict for a specific token is safe to reuse. An inactive result is
// never cached, since clock skew or an nbf window can make the same token
// valid moments later.
type Client struct {
	introspectURL string
	httpClient    *http.Client
	cacheTTL      time.Duration
	cacheMu       sync.RWMutex
	cache         map[string]cacheEntry
}

// NewClient builds an introspection client. introspectURL is required. A
// cacheTTL of 0 disables caching.
func NewClient(introspectURL string, timeout, cacheTTL time.Duration) (*Client, error) {
	if strings.TrimSpace(introspectURL) == "" {
		return nil, fmt.Errorf("nvcaintrospect: introspect url is required")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	// Clone DefaultTransport to include the HTTP proxy env vars. Fall back to
	// DefaultTransport as-is if it's been replaced with a non-*http.Transport
	// RoundTripper (e.g. by an httpmock-style test helper), since the single-
	// value assertion would otherwise panic.
	var transport http.RoundTripper = http.DefaultTransport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
	}
	return &Client{
		introspectURL: introspectURL,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: otelhttp.NewTransport(transport,
				otelhttp.WithSpanNameFormatter(func(_ string, _ *http.Request) string {
					return "nvcaintrospect.introspect"
				}),
			),
			// The introspection endpoint should never redirect. Following one
			// would resend the bearer token (via the transport's Authorization
			// header, if the caller sets one) to whatever host the redirect
			// names, so refuse rather than follow.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		cacheTTL: cacheTTL,
		cache:    make(map[string]cacheEntry),
	}, nil
}

// Introspect verifies token against the configured endpoint, using the cache
// when enabled.
func (c *Client) Introspect(ctx context.Context, token string) (*IntrospectResult, error) {
	if len(token) > MaxTokenSize {
		return nil, ErrTokenTooLarge
	}

	key := cacheKey(token)
	if cached, ok := c.cacheLookup(key); ok {
		return cached, nil
	}

	result, err := c.callIntrospect(ctx, token)
	if err != nil {
		return nil, err
	}

	if shouldCache(result) {
		c.cacheStore(key, result, token)
	}

	return result, nil
}

// shouldCache reports whether an introspection result is safe to reuse for a
// token's remaining lifetime.
//
// An inactive result is never cached: clock skew or an nbf window can make
// the same token valid moments later. A result with an empty Sub is never
// cached either: that's an incomplete response from the introspection
// endpoint, not evidence of an invalid identity. A non-empty, invalid-subject
// result is cached regardless of ClusterID: a token's subject can't change
// once issued, so that denial is permanent. A valid-subject result with an
// empty ClusterID is not cached: that's also an incomplete response (a
// caller requiring ClusterID would reject it), and caching it would pin that
// rejection for the full TTL even after the endpoint starts returning a
// complete response.
func shouldCache(result *IntrospectResult) bool {
	if !result.Active {
		return false
	}
	if result.Sub == "" {
		return false
	}
	if !IsValidNVCASubject(result.Sub) {
		return true
	}
	return result.ClusterID != ""
}

func (c *Client) callIntrospect(ctx context.Context, token string) (result *IntrospectResult, err error) {
	ctx, span := otel.Tracer(instrumentationName).Start(ctx, "nvcaintrospect.call")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	body, err := json.Marshal(IntrospectRequest{Token: token})
	if err != nil {
		return nil, fmt.Errorf("marshal introspect request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.introspectURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build introspect request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call introspect endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read introspect response: %w", err)
	}
	if len(respBody) > maxResponseSize {
		return nil, errResponseTooLarge
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspect returned status %d", resp.StatusCode)
	}

	var parsed IntrospectResult
	if err = json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode introspect response: %w", err)
	}
	return &parsed, nil
}

// cacheKey returns a stable, non-reversible key for a token. Hashing keeps
// raw bearer material out of long-lived process memory.
func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenExpiry decodes (without verifying) a JWT payload and returns the exp
// claim. Used only as an upper bound on cache TTL - the security boundary is
// the introspection result at the remote endpoint, not this local,
// unverified parse.
func tokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

func (c *Client) cacheLookup(key string) (*IntrospectResult, bool) {
	if c.cacheTTL <= 0 {
		return nil, false
	}
	c.cacheMu.RLock()
	entry, ok := c.cache[key]
	c.cacheMu.RUnlock()
	if !ok || !time.Now().Before(entry.expiresAt) {
		return nil, false
	}
	cp := *entry.result
	return &cp, true
}

func (c *Client) cacheStore(key string, result *IntrospectResult, token string) {
	if c.cacheTTL <= 0 {
		return
	}
	ttl := c.cacheTTL
	if exp, ok := tokenExpiry(token); ok {
		if remaining := time.Until(exp); remaining < ttl {
			ttl = remaining
		}
	}
	if ttl <= 0 {
		return
	}
	stored := *result
	c.cacheMu.Lock()
	if _, exists := c.cache[key]; !exists && len(c.cache) >= maxCacheEntries {
		// At capacity: evict one entry rather than scanning the whole map.
		// Go's range order is randomized, so this is an arbitrary eviction,
		// not LRU - acceptable since entries are already TTL-bounded.
		for k := range c.cache {
			delete(c.cache, k)
			break
		}
	}
	c.cache[key] = cacheEntry{result: &stored, expiresAt: time.Now().Add(ttl)}
	c.cacheMu.Unlock()
}
