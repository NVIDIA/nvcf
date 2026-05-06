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

package cache

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
)

type CacheStats struct {
	Items    int
	Capacity int
	Hits     int64
	Misses   int64
}

type Cache[K comparable, V any] interface {
	Set(key K, value V, ttl time.Duration)
	Get(key K) (V, bool)
	Delete(key K)
	Name() string
	Stats() CacheStats
}

type CloseableCache interface {
	Close()
}

type StatfulCache interface {
	Stats() CacheStats
}

type CacheRegistry struct {
	caches sync.Map
}

func NewCacheRegistry() *CacheRegistry {
	return &CacheRegistry{}
}

func (r *CacheRegistry) Stats() map[string]CacheStats {
	stats := make(map[string]CacheStats)
	r.caches.Range(func(key any, value any) bool {
		stats[must.As[string](key)] = must.As[StatfulCache](value).Stats()
		return true
	})
	return stats
}

func (r *CacheRegistry) Close() {
	r.caches.Range(func(_ any, value any) bool {
		must.As[CloseableCache](value).Close()
		return true
	})
}

func (r *CacheRegistry) registerCaches(caches ...registerable) {
	for _, cache := range caches {
		zlog.Info().Str("cache", cache.Name()).Msg("registering cache")
		r.caches.Store(cache.Name(), cache)
	}
}

func (r *CacheRegistry) retrieveCache(name string) (any, bool) {
	return r.caches.Load(name)
}

type registerable interface {
	Name() string
	Stats() CacheStats
}

func retrieveCache[K comparable, V any](r *CacheRegistry) (Cache[K, V], bool) {
	name := typeName[K, V]()
	cache, found := r.retrieveCache(name)
	if !found {
		return nil, false
	}

	x, ok := cache.(Cache[K, V])
	return x, ok
}

func typeName[K comparable, V any]() string {
	return fmt.Sprintf(
		"%s-%s",
		reflect.TypeOf((*K)(nil)).Elem(),
		reflect.TypeOf((*V)(nil)).Elem(),
	)
}
