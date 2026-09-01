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
	// authCacheTTL bounds how long a positive auth response is reused;
	// expiry is measured from write, so hits never extend it.
	authCacheTTL = 60 * time.Second
	// authCacheMaxEntries bounds memory (~1KB per entry).
	authCacheMaxEntries = 1024
)

// authCacheKey must include every AuthLlmInvokeRequest field, or a cached
// response could be served for a request the upstream would answer
// differently.
type authCacheKey struct {
	// tokenHash keeps raw bearer tokens out of process memory dumps.
	tokenHash  [sha256.Size]byte
	routingKey string
}

// cachedClient caches positive AuthorizeInvocation responses; errors and
// denials are never cached. Concurrent misses for the same key collapse into
// one upstream call running under the triggering caller's context, so that
// caller's cancellation fails all waiters.
type cachedClient struct {
	inner Client
	cache *otter.Cache[authCacheKey, *InvocationAuthResponse]
}

// NewCachedClient wraps non-nil inner with the auth response cache.
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
	// The cached response is shared; never hand it out directly.
	return resp.clone(), nil
}

func (c *cachedClient) Close() error {
	return c.inner.Close()
}
