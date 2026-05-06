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
	"time"

	"github.com/maypok86/otter"
)

type CacheKey interface {
	comparable
	fmt.Stringer
}

type CacheWrapper[K CacheKey, V any] struct {
	cache *otter.CacheWithVariableTTL[K, V]
	name  string
}

func NewCacheWrapper[K CacheKey, V any](c *otter.CacheWithVariableTTL[K, V]) *CacheWrapper[K, V] {
	return &CacheWrapper[K, V]{
		cache: c,
		name: fmt.Sprintf(
			"%s-%s",
			reflect.TypeOf((*K)(nil)).Elem(),
			reflect.TypeOf((*V)(nil)).Elem(),
		),
	}
}

func (w *CacheWrapper[K, V]) Set(key K, value V, ttl time.Duration) {
	w.cache.Set(key, value, ttl)
}

func (w *CacheWrapper[K, V]) Get(key K) (V, bool) {
	return w.cache.Get(key)
}

func (w *CacheWrapper[K, V]) Delete(key K) {
	w.cache.Delete(key)
}

func (w *CacheWrapper[K, V]) Stats() CacheStats {
	stats := w.cache.Stats()
	return CacheStats{
		Items:    w.cache.Size(),
		Capacity: w.cache.Capacity(),
		Hits:     stats.Hits(),
		Misses:   stats.Misses(),
	}
}

func (w *CacheWrapper[K, V]) Close() {
	w.cache.Close()
}

func (w *CacheWrapper[K, V]) Name() string {
	return w.name
}

func WrapCache[K CacheKey, V any](maxSize int) Cache[K, V] {
	//nolint:errcheck
	c, _ := otter.MustBuilder[K, V](maxSize).
		CollectStats().
		WithVariableTTL().
		Build()

	return NewCacheWrapper(&c)
}
