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

// Package cache provides a local write cache that collapses high-frequency
// stats events per key before they reach the database.
package cache

import (
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

const (
	defaultMaxSize       = 100_000
	defaultFlushInterval = 60 * time.Second

	// inactiveTTLBufferPercent is added on top of FlushInterval to form the
	// inactivity TTL, so an entry outlives its scheduled flush.
	inactiveTTLBufferPercent = 10
)

// key identifies one cached event stream.
type key struct {
	namespace string
	context   string
	eventName string
}

// entry is the cached state for a key.
type entry struct {
	// timestamp is the timestamp of the latest event seen for the key. The
	// stats table stores nothing else, so no payload is cached.
	timestamp time.Time
	// pending is true when timestamp has not yet been written to the database.
	pending bool
	// lastWritten is the time of the last successful database write.
	lastWritten time.Time
	// lastUpdated is the time the entry was last inserted or replaced.
	lastUpdated time.Time
}

// Config holds the cache tunables. Non-positive fields fall back to defaults.
type Config struct {
	// MaxSize is the maximum number of entries before LRU eviction.
	MaxSize int
	// FlushInterval is the delay between an entry becoming pending and its flush.
	FlushInterval time.Duration
}

// DefaultConfig returns a Config populated with the documented defaults.
func DefaultConfig() Config {
	return Config{
		MaxSize:       defaultMaxSize,
		FlushInterval: defaultFlushInterval,
	}
}

// inactiveTTL is how long an entry may go without an update before it is
// eligible for eviction: FlushInterval plus a 10% buffer.
func (c Config) inactiveTTL() time.Duration {
	return c.FlushInterval + c.FlushInterval*inactiveTTLBufferPercent/100
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MaxSize <= 0 {
		c.MaxSize = d.MaxSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = d.FlushInterval
	}
	return c
}

// CachingDBHandler wraps a DBHandlerV2 with a local write cache. Methods that
// are not cached are served by the embedded handler.
type CachingDBHandler struct {
	data_access.DBHandlerV2

	cfg Config

	entriesMu sync.RWMutex
	entries   map[key]*entry
}

// NewCachingDBHandler wraps inner with an empty cache.
func NewCachingDBHandler(inner data_access.DBHandlerV2, cfg Config) *CachingDBHandler {
	return &CachingDBHandler{
		DBHandlerV2: inner,
		cfg:         cfg.withDefaults(),
		entries:     make(map[key]*entry),
	}
}

// lookup returns a copy of the entry for k, and whether it exists. It returns
// a copy because the read lock is released on return; a pointer would let the
// caller read fields while a writer mutates them, which is a data race. The
// entry is small, so the copy is cheaper than holding a lock for the caller.
func (h *CachingDBHandler) lookup(k key) (entry, bool) {
	h.entriesMu.RLock()
	defer h.entriesMu.RUnlock()
	e, ok := h.entries[k]
	if !ok {
		return entry{}, false
	}
	return *e, true
}
