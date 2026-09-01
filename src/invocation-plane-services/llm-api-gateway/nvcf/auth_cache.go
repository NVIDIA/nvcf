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
	"time"

	"github.com/maypok86/otter/v2"
)

const (
	// authCacheTTL bounds how long a positive auth response is reused. Token
	// revocation, function unpublish, and model spec changes take up to this
	// long to be observed. Expiry is measured from write, never from access:
	// a continuously hit entry must not outlive the revocation bound.
	authCacheTTL = 60 * time.Second
	// authCacheMaxEntries bounds memory (~1KB per entry), not hit rate. The
	// bound is required: the key space is request-derived, so an unbounded
	// cache would grow with every distinct valid token/function pair.
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

// cachedClient caches positive AuthorizeInvocation responses. Errors and
// denials are never cached: otter's loader propagates them to all waiters
// and stores nothing, so the next request retries upstream. There is no
// explicit invalidation; entries expire on the write TTL only, matching the
// invocation auth cache in http-invocation.
//
// Concurrent misses for the same key collapse into one upstream call. The
// loader runs under the context of the caller that triggered it, so that
// caller's cancellation fails all waiters; they retry on their next request.
type cachedClient struct {
	inner Client
	cache *otter.Cache[authCacheKey, *InvocationAuthResponse]
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
		inner: inner,
		cache: otter.Must(&otter.Options[authCacheKey, *InvocationAuthResponse]{
			MaximumSize:      maxEntries,
			ExpiryCalculator: otter.ExpiryWriting[authCacheKey, *InvocationAuthResponse](ttl),
		}),
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

	resp, err := c.cache.Get(ctx, key,
		otter.LoaderFunc[authCacheKey, *InvocationAuthResponse](
			func(ctx context.Context, _ authCacheKey) (*InvocationAuthResponse, error) {
				return c.inner.AuthorizeInvocation(ctx, clientAuthorizationToken, functionID)
			},
		),
	)
	if err != nil {
		return nil, err
	}
	// Clone for every caller: the cached response is shared and must never
	// be handed out directly.
	return resp.clone(), nil
}

func (c *cachedClient) Close() error {
	return c.inner.Close()
}
