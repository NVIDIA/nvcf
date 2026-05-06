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

package api

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
)

const (
	contextKeyRequestContext = "nvcf.request_context"
	contextKeyRequestID      = "nvcf.request_id"
	contextKeyRateLimitCtx   = "nvcf.rate_limit_context"
)

type GatewayContext struct {
	echo.Context

	userContext context.Context
	store       *ContextStore
}

func NewGatewayContext(ectx echo.Context) *GatewayContext {
	return &GatewayContext{
		Context:     ectx,
		userContext: ectx.Request().Context(),
		store: &ContextStore{
			requestStart: time.Now(),
		},
	}
}

func (c *GatewayContext) Bind(i any) error {
	return c.Echo().Binder.Bind(i, c)
}

func (c *GatewayContext) UserContext() context.Context {
	return c.userContext
}

func (c *GatewayContext) SetUserContext(ctx context.Context) {
	c.userContext = ctx
}

func (c *GatewayContext) RequestContext() *requestctx.RequestContext {
	return c.store.RequestContext()
}

func (c *GatewayContext) RequestID() string {
	return c.store.RequestID()
}

func (c *GatewayContext) RequestStart() time.Time {
	return c.store.requestStart
}

func (c *GatewayContext) RateLimitContext() *RateLimitContext {
	return c.store.RateLimitContext()
}

func (c *GatewayContext) SetRateLimitContext(rlc *RateLimitContext) {
	c.store.SetRateLimitContext(rlc)
}

type ContextStore struct {
	requestStart time.Time
	subRequestID atomic.Uint64
	lock         sync.RWMutex
	store        map[string]any
}

func (c *ContextStore) RequestContext() *requestctx.RequestContext {
	return getContextValue[*requestctx.RequestContext](c, contextKeyRequestContext)
}

func (c *ContextStore) RequestID() string {
	return getContextValue[string](c, contextKeyRequestID)
}

func (c *ContextStore) RateLimitContext() *RateLimitContext {
	return getContextValue[*RateLimitContext](c, contextKeyRateLimitCtx)
}

func (c *ContextStore) SetRateLimitContext(rlc *RateLimitContext) {
	c.Set(contextKeyRateLimitCtx, rlc)
}

func (c *ContextStore) NextSubRequestID() string {
	requestID := c.RequestID()
	if requestID == "" {
		return ""
	}
	return requestID + "_" + fmtUint64(c.subRequestID.Add(1)-1)
}

func (c *ContextStore) Set(key string, value any) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.store == nil {
		c.store = make(map[string]any)
	}

	c.store[key] = value
}

func (c *ContextStore) Get(key string) any {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.store[key]
}

func getContextValue[T any](store *ContextStore, key string) T {
	store.lock.RLock()
	defer store.lock.RUnlock()

	value, ok := store.store[key]
	if !ok {
		var zero T
		return zero
	}

	cast, ok := value.(T)
	if !ok {
		var zero T
		return zero
	}

	return cast
}

func fmtUint64(value uint64) string {
	return strconv.FormatUint(value, 10)
}
