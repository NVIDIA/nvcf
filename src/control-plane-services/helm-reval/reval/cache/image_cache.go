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

package cache

import (
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"go.uber.org/zap"
)

type ImageCache interface {
	Get(in ImageCacheInput) bool
	Put(in ImageCacheInput)
	Delete(in ImageCacheInput)
}

type ImageCacheInput struct {
	ImageTag string
	Public   bool
}

const (
	// Cache entries are ~100 bytes max so 1MB should be enough space.
	cacheSize = 10_000
	// Public images should be cached for a long time.
	publicImageCacheTTL = 7 * 24 * time.Hour
)

func NewImageCache(logger *zap.Logger) ImageCache {
	ic := &memoryImageCache{
		logger:      logger,
		publicCache: expirable.NewLRU[string, struct{}](cacheSize, nil, publicImageCacheTTL),
	}
	return ic
}

type memoryImageCache struct {
	logger      *zap.Logger
	publicCache *expirable.LRU[string, struct{}]
}

func (c *memoryImageCache) Get(in ImageCacheInput) bool {
	if in.Public {
		cacheKey := in.ImageTag
		_, ok := c.publicCache.Get(cacheKey)
		return ok
	}
	return false
}

func (c *memoryImageCache) Put(in ImageCacheInput) {
	if in.Public {
		cacheKey := in.ImageTag
		c.publicCache.Add(cacheKey, struct{}{})
		return
	}
}

func (c *memoryImageCache) Delete(in ImageCacheInput) {
	if in.Public {
		cacheKey := in.ImageTag
		c.publicCache.Remove(cacheKey)
		return
	}
}
