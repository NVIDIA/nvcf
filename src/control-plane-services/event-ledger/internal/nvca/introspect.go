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

// Package nvca introspects NVCA's Kubernetes projected service-account token
// (PSAT) at SIS, for callers that do not hold an OpenBao-issued JWT. It
// mirrors ReVal's ICMS introspection authorizer so Event Ledger and ReVal
// verify NVCA's identity the same way.
package nvca

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
)

// MaxTokenSize bounds how much bearer token material the introspection client
// will send. Self-managed cluster PSATs are well under 2 KiB; larger tokens
// are treated as abuse.
const MaxTokenSize = 2048

// ErrTokenTooLarge is returned when the bearer token exceeds MaxTokenSize.
var ErrTokenTooLarge = errors.New("bearer token exceeds maximum size of 2048 bytes")

// maxCacheEntries bounds the introspection cache so high-cardinality token
// traffic can't grow it without limit. Realistic cardinality is one entry
// per distinct NVCA pod PSAT across all registered clusters, far under this.
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

// IntrospectRequest is the body sent to SIS's introspection endpoint.
type IntrospectRequest struct {
	Token string `json:"token"`
}

// IntrospectResult is the response from SIS's NVCA token introspection
// endpoint (RFC 7662 shape, plus an NVCF-specific resolved cluster
// identifier that is not part of RFC 7662).
type IntrospectResult struct {
	Active    bool   `json:"active"`
	Sub       string `json:"sub"`
	ClusterID string `json:"cluster_id"`
	Error     string `json:"error,omitempty"`
}

// Introspector verifies a bearer token by asking an external service whether
// it is currently valid. It is implemented by *Client and by test doubles.
type Introspector interface {
	Introspect(ctx context.Context, token string) (*IntrospectResult, error)
}

type cacheEntry struct {
	result    *IntrospectResult
	expiresAt time.Time
}

// Client calls SIS's POST /v1/nvca/tokens/introspect endpoint to verify
// NVCA's PSAT. Results are cached by a hash of the token (never the raw
// token) for cacheTTL, bounded by the token's own exp claim so a cached
// result never outlives the token it was computed for.
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
		return nil, fmt.Errorf("nvca: introspect url is required")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		introspectURL: introspectURL,
		httpClient: &http.Client{
			Timeout: timeout,
			// Can't reuse middleware.GetSharedHTTPClient here: internal/middleware
			// imports internal/nvca, so importing middleware back would cycle.
			Transport: otelhttp.NewTransport(http.DefaultTransport,
				otelhttp.WithSpanNameFormatter(func(_ string, _ *http.Request) string {
					return "nvca.introspect"
				}),
			),
		},
		cacheTTL: cacheTTL,
		cache:    make(map[string]cacheEntry),
	}, nil
}

// Introspect implements Introspector.
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

	// Only a complete, subject-valid result is cached. A token that comes
	// back inactive or with the wrong subject may pass moments later (clock
	// skew, an nbf window), so it must be re-checked rather than pinned. A
	// missing ClusterID must not be cached either: caching it would pin a
	// transient, incomplete SIS response as a 403 for the full TTL even
	// after SIS starts returning a complete one.
	if result.Active && IsValidNVCASubject(result.Sub) && result.ClusterID != "" {
		c.cacheStore(key, result, token)
	}

	return result, nil
}

func (c *Client) callIntrospect(ctx context.Context, token string) (*IntrospectResult, error) {
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read introspect response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspect returned status %d", resp.StatusCode)
	}

	var result IntrospectResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode introspect response: %w", err)
	}
	return &result, nil
}

// cacheKey returns a stable, non-reversible key for a token. Hashing keeps
// raw bearer material out of long-lived process memory.
func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenExpiry decodes (without verifying) a JWT payload and returns the exp
// claim. Used only as an upper bound on cache TTL - the security boundary is
// the introspection result at SIS, not this local, unverified parse.
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
	return entry.result, true
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
	c.cache[key] = cacheEntry{result: result, expiresAt: time.Now().Add(ttl)}
	c.cacheMu.Unlock()
}
