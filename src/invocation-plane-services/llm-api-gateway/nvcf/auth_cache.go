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

package nvcf

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// authCacheTTL bounds how long a positive auth response is reused. Token
	// revocation, function unpublish, and model spec changes take up to this
	// long to be observed.
	authCacheTTL = 60 * time.Second
	// authCacheMaxEntries bounds memory, not hit rate. Eviction past the cap
	// removes expired entries first, then arbitrary ones.
	authCacheMaxEntries = 1024
)

// authCacheKey must include every AuthLlmInvokeRequest field. If the RPC
// gains an input, it must join this key or a cached response could be served
// for a request the upstream would answer differently.
type authCacheKey struct {
	// tokenHash keeps raw bearer tokens out of process memory dumps.
	tokenHash  [sha256.Size]byte
	routingKey string
}

type authCacheEntry struct {
	resp      *InvocationAuthResponse
	expiresAt time.Time
}

// cachedClient caches positive AuthorizeInvocation responses. Errors and
// denials are never cached: they propagate to the caller and the next request
// retries upstream. There is no explicit invalidation; entries expire on TTL
// only, matching the invocation auth cache in http-invocation.
type cachedClient struct {
	inner      Client
	ttl        time.Duration
	maxEntries int

	// group collapses concurrent misses for the same key into one upstream
	// call. The first caller's context governs the shared call, so its
	// cancellation fails all waiters; they retry on their next request.
	group singleflight.Group

	mu      sync.Mutex
	entries map[authCacheKey]authCacheEntry
}

// NewCachedClient wraps inner with the auth response cache. inner must be
// non-nil.
func NewCachedClient(inner Client) Client {
	return newCachedClient(inner, authCacheTTL, authCacheMaxEntries)
}

func newCachedClient(inner Client, ttl time.Duration, maxEntries int) Client {
	if ttl <= 0 || maxEntries <= 0 {
		return inner
	}
	return &cachedClient{
		inner:      inner,
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[authCacheKey]authCacheEntry),
	}
}

func (c *cachedClient) AuthorizeInvocation(
	ctx context.Context,
	clientAuthorizationToken string,
	functionID string,
) (*InvocationAuthResponse, error) {
	key := authCacheKey{
		tokenHash:  sha256.Sum256([]byte(clientAuthorizationToken)),
		routingKey: functionID,
	}

	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.resp.clone(), nil
	}

	flightKey := string(key.tokenHash[:]) + key.routingKey
	value, err, _ := c.group.Do(flightKey, func() (any, error) {
		resp, err := c.inner.AuthorizeInvocation(ctx, clientAuthorizationToken, functionID)
		if err != nil {
			return nil, err
		}
		c.store(key, resp)
		return resp, nil
	})
	if err != nil {
		return nil, err
	}
	// Clone for every caller, including the one that ran the upstream call:
	// the stored response is shared and must never be handed out directly.
	return value.(*InvocationAuthResponse).clone(), nil
}

func (c *cachedClient) store(key authCacheKey, resp *InvocationAuthResponse) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.maxEntries {
		for k, e := range c.entries {
			if !now.Before(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < c.maxEntries {
				break
			}
			delete(c.entries, k)
		}
	}

	c.entries[key] = authCacheEntry{resp: resp, expiresAt: now.Add(c.ttl)}
}

func (c *cachedClient) Close() error {
	return c.inner.Close()
}
