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

package mscontroller

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// gvkCache caches GroupVersionKind lookups by reflect.Type to avoid repeated scheme lookups.
// GVK for each Kubernetes type is static, so caching is safe and reduces allocations significantly.
type gvkCache struct {
	mu     sync.RWMutex
	cache  map[reflect.Type]schema.GroupVersionKind
	scheme *runtime.Scheme
}

// newGVKCache creates a new gvkCache with the given scheme.
func newGVKCache(scheme *runtime.Scheme) *gvkCache {
	return &gvkCache{
		cache:  make(map[reflect.Type]schema.GroupVersionKind),
		scheme: scheme,
	}
}

// Get returns the GVK for the given object, using a cached value if available.
// It first checks if the object already has a GVK set, then checks the cache,
// and finally falls back to a scheme lookup (caching the result).
func (c *gvkCache) Get(obj client.Object) (schema.GroupVersionKind, error) {
	if gvk := obj.GetObjectKind().GroupVersionKind(); gvk != (schema.GroupVersionKind{}) {
		return gvk, nil
	}

	t := reflect.TypeOf(obj)
	c.mu.RLock()
	if gvk, ok := c.cache[t]; ok {
		c.mu.RUnlock()
		return gvk, nil
	}
	c.mu.RUnlock()

	gvks, _, err := c.scheme.ObjectKinds(obj)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	if len(gvks) == 0 {
		return schema.GroupVersionKind{}, fmt.Errorf("no GVK found for type %T", obj)
	}

	c.mu.Lock()
	c.cache[t] = gvks[0]
	c.mu.Unlock()
	return gvks[0], nil
}

// PrePopulate adds a GVK mapping for the given object type to the cache.
// Use this to pre-warm the cache with known types at initialization.
func (c *gvkCache) PrePopulate(obj client.Object, gvk schema.GroupVersionKind) {
	t := reflect.TypeOf(obj)
	c.mu.Lock()
	c.cache[t] = gvk
	c.mu.Unlock()
}

type gvkCacheContextKey struct{}

// withGVKCache embeds gvkCache in the context for transparent GVK lookups.
func withGVKCache(ctx context.Context, cache *gvkCache) context.Context {
	return context.WithValue(ctx, gvkCacheContextKey{}, cache)
}

// getGVKCache retrieves the gvkCache from context. Returns nil if not present.
func getGVKCache(ctx context.Context) *gvkCache {
	if cache, ok := ctx.Value(gvkCacheContextKey{}).(*gvkCache); ok {
		return cache
	}
	return nil
}

var unknownGVK = schema.GroupVersionKind{Group: "unknown", Version: "unknown", Kind: "unknown"}

// getObjectGVK returns the GVK for an object. If a gvkCache is in the context,
// it will use the cache for lookup (populating it if needed).
// If no cache is in context, falls back to scheme.ObjectKinds.
func getObjectGVK(ctx context.Context, sch *runtime.Scheme, obj client.Object) (schema.GroupVersionKind, error) {
	if gvk := obj.GetObjectKind().GroupVersionKind(); gvk != (schema.GroupVersionKind{}) {
		return gvk, nil
	}

	if cache := getGVKCache(ctx); cache != nil {
		return cache.Get(obj)
	}

	gvks, _, err := sch.ObjectKinds(obj)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	if len(gvks) == 0 {
		return schema.GroupVersionKind{}, fmt.Errorf("no GVK found for type %T", obj)
	}
	return gvks[0], nil
}

// getObjectGVKOrUnknown returns the GVK for the object, or unknownGVK if it cannot be determined.
// Use this for logging/display purposes where GVK lookup failure should not halt execution.
func getObjectGVKOrUnknown(ctx context.Context, sch *runtime.Scheme, obj client.Object) schema.GroupVersionKind {
	gvk, err := getObjectGVK(ctx, sch, obj)
	if err != nil {
		return unknownGVK
	}
	return gvk
}
