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

package oauth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	defaultTokenExpiryMargin = 15 * time.Minute
	defaultBackOffBase       = 100 * time.Millisecond
	defaultBackOffMax        = 5 * time.Minute
)

type tokenFetcher interface {
	FetchToken(ctx context.Context) (string, error)
	RefreshClient()
}

type tokenVerifier interface {
	// VerifyToken verifies that the current token is valid.
	// Returns true if valid, false otherwise. An error is returned
	// only when the validity of the token could not be determined.
	VerifyToken(ctx context.Context, token string) (bool, error)
}

type JWTCache struct {
	// Followings are immutable configurations.

	fetcher tokenFetcher
	// nowFunc returns current time, inject as dependency for testing.
	nowFunc      func() time.Time
	expiryMargin time.Duration
	jwtVerifier  tokenVerifier

	// sync.Mutex protects followings mutable states
	sync.Mutex
	token  string
	expiry *time.Time

	// Exponential back-off configs
	backOffBase time.Duration
	backOffMax  time.Duration
	// Exponential back-off states
	backOff        time.Duration
	lastFailedTime *time.Time
}

func NewJWTCache() *JWTCache {
	return &JWTCache{
		nowFunc:      time.Now,
		expiryMargin: defaultTokenExpiryMargin,
		backOffBase:  defaultBackOffBase,
		backOffMax:   defaultBackOffMax,
	}
}

func copyTimePointer(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	out := *t
	return &out
}

func (c *JWTCache) Copy() *JWTCache {
	out := &JWTCache{}

	c.Lock()
	defer c.Unlock()
	out.fetcher = c.fetcher
	out.nowFunc = c.nowFunc
	out.expiryMargin = c.expiryMargin
	out.token = c.token
	out.expiry = copyTimePointer(c.expiry)
	out.backOffBase = c.backOffBase
	out.backOffMax = c.backOffMax
	out.backOff = c.backOff
	out.jwtVerifier = c.jwtVerifier
	out.lastFailedTime = copyTimePointer(c.lastFailedTime)
	return out
}

func (c *JWTCache) WithFetcher(fetcher tokenFetcher) *JWTCache {
	next := c.Copy()
	next.fetcher = fetcher
	return next
}

func (c *JWTCache) WithVerifier(jwtVerifier tokenVerifier) *JWTCache {
	next := c.Copy()
	next.jwtVerifier = jwtVerifier
	return next
}

func (c *JWTCache) WithNowFunc(f func() time.Time) *JWTCache {
	next := c.Copy()
	next.nowFunc = f
	return next
}

func (c *JWTCache) WithExpiryMargin(margin time.Duration) *JWTCache {
	next := c.Copy()
	next.expiryMargin = margin
	return next
}

func (c *JWTCache) WithBackOffBase(backOffBase time.Duration) *JWTCache {
	next := c.Copy()
	next.backOffBase = backOffBase
	return next
}

func (c *JWTCache) WithBackOffMax(backOffMax time.Duration) *JWTCache {
	next := c.Copy()
	next.backOffMax = backOffMax
	return next
}

func (c *JWTCache) RefreshClient() {
	c.fetcher.RefreshClient()
}

// FetchToken returns the token in the cache if it think the token is
// valid, otherwise, it tries to fetch a new token and update cache
// first.
func (c *JWTCache) FetchToken(ctx context.Context) (string, error) {
	c.Lock()
	defer c.Unlock()

	log := core.GetLogger(ctx)
	expiry, token := c.expiry, c.token

	if expiry != nil {
		now := c.nowFunc()
		deadline := expiry.Add(-c.expiryMargin)
		tsStr := fmt.Sprintf("now: %v, deadline: %v, expiry: %v, expiryMargin: %v", now, deadline, *expiry, c.expiryMargin)
		// Verify the JWT to see if it has expired remotely through
		// non-expiry margins
		// TODO(mcamp) add some wait in-between fetches to ensure we don't
		// constantly hammer the server, perhaps do some of this is
		// in an expiry chain mechanism and put this verification AFTER
		// the deadline expiration check
		tokenVerified := true
		if c.jwtVerifier != nil {
			log.Debug("Verify the cached token")
			v, err := c.jwtVerifier.VerifyToken(ctx, token)
			if err != nil {
				log.Errorf("failed to verify token, error: %v", err)
				return "", err
			}
			tokenVerified = v
		}
		log.Debugf("Token verify result=%t", tokenVerified)
		if now.Before(deadline) && tokenVerified {
			log.Debugf("Token in cache is still valid, return. %s", tsStr)
			return token, nil
		}
		if !tokenVerified {
			log.Debug("Token in cache failed verification, refreshing.")
		} else {
			log.Infof("Token in cache is about to expire, refreshing. %s", tsStr)
		}
	} else {
		log.Info("Token in cache is not valid, refreshing.")
	}

	lastFailedTime := c.lastFailedTime
	backOffBase, backOffMax, backOff := c.backOffBase, c.backOffMax, c.backOff
	if lastFailedTime != nil {
		now := c.nowFunc()
		nextAvailable := lastFailedTime.Add(backOff)
		if now.Before(nextAvailable) {
			msg := fmt.Sprintf("client retry rate limited, now: %v, nextAvailable: %v,"+
				" backOff: %v, backOffBase: %v, backOffMax: %v",
				now, nextAvailable, backOff, backOffBase, backOffMax)
			log.Info(msg)
			return "", errors.New(msg)
		}
		log.Debug("next retry is available, proceed")
	} else {
		log.Debug("lastFailedTime is nil, proceed")
	}

	token, err := c.forceNewToken(ctx)
	if err != nil {
		now := c.nowFunc()
		c.lastFailedTime, c.backOff = &now, nextExpBackOff(backOff, backOffBase, backOffMax)
		return "", fmt.Errorf("failed to FetchToken(), err: %w", err)
	}

	// forceNewToken was successful, reset back off
	c.lastFailedTime, c.backOff = nil, time.Duration(0)
	return token, nil
}

func nextExpBackOff(current, base, maxBackoff time.Duration) time.Duration {
	var next time.Duration
	if current == time.Duration(0) {
		next = base
	} else {
		next = time.Duration(2) * current
	}
	if next > maxBackoff {
		next = maxBackoff
	}
	return next
}

// ForceNewToken forces to retrieve a new device token. Caller is not
// expect to use ForceNewToken() directly but use FetchToken() most of
// time.
func (c *JWTCache) ForceNewToken(ctx context.Context) (string, error) {
	c.Lock()
	defer c.Unlock()
	return c.forceNewToken(ctx)
}

// forceNewToken is not thread safe, caller needs to make sure it holds
// lock.
func (c *JWTCache) forceNewToken(ctx context.Context) (string, error) {
	log := core.GetLogger(ctx)

	token, err := c.fetcher.FetchToken(ctx)
	if err != nil {
		return "", err
	}

	if token == "" {
		return "", fmt.Errorf("fetched token is empty")
	}

	// We parse the token just to extract the exp field as a hint for
	// caching, we don't verify its authenticity and claims here.
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{
		jose.EdDSA,
		jose.HS256,
		jose.HS384,
		jose.HS512,
		jose.RS256,
		jose.RS384,
		jose.RS512,
		jose.ES256,
		jose.ES384,
		jose.PS256,
		jose.PS384,
		jose.PS512,
	})
	if err != nil {
		return "", fmt.Errorf("failed to parse token, err: %w", err)
	}

	claims := struct {
		Expiry int64 `json:"exp"`
	}{}

	err = parsed.UnsafeClaimsWithoutVerification(&claims)
	if err != nil {
		return "", fmt.Errorf("failed to parse claims, err: %w", err)
	}

	expiry := time.Unix(claims.Expiry, 0)
	log.Debugf("Refreshed token expiry: %v", expiry)

	now := c.nowFunc()
	if now.After(expiry) {
		return "", fmt.Errorf("refreshed token is already expired, now: %v, expiry: %v", now, expiry)
	}

	c.expiry, c.token = &expiry, token
	log.Infof("Updated token in cache with expiry: %v", c.expiry)
	return c.token, nil
}
