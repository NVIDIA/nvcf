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
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// inactiveTTLBufferPercent is added on top of FlushInterval to form the
// inactivity TTL, so an entry outlives its scheduled flush.
const inactiveTTLBufferPercent = 10

var (
	errNilInnerHandler      = errors.New("cache: inner DBHandlerV2 must not be nil")
	errInvalidMaxSize       = errors.New("cache: MaxSize must be greater than 0")
	errInvalidFlushInterval = errors.New("cache: FlushInterval must be greater than 0")
)

// key identifies one cached event stream.
type key struct {
	namespace string
	context   string
	eventName string
}

// entry is the cached state for a key.
type entry struct {
	// timestamp is the timestamp of the latest event seen for the key.
	timestamp time.Time
	// source and details are the payload of the latest event, needed to write
	// the events table. They are empty for the stats table, which stores
	// neither. details is shared between copies and must not be mutated.
	source  string
	details json.RawMessage
	// pending is true when the latest event has not yet been written to the database.
	pending bool
	// lastWritten is the time of the last successful database write.
	lastWritten time.Time
	// lastUpdated is the time the entry was last inserted or replaced.
	lastUpdated time.Time
}

// Config holds the cache tunables. Both fields must be positive. The service
// config owns the defaults.
type Config struct {
	// MaxSize is the maximum number of entries before LRU eviction.
	MaxSize int
	// FlushInterval is the delay between an entry becoming pending and its flush.
	FlushInterval time.Duration
}

// inactiveTTL is how long an entry may go without an update before it is
// eligible for eviction: FlushInterval plus a 10% buffer.
func (c Config) inactiveTTL() time.Duration {
	return c.FlushInterval + c.FlushInterval*inactiveTTLBufferPercent/100
}

// CachingDBHandler wraps a DBHandlerV2 with a local write cache. Methods that
// are not cached are served by the embedded handler.
type CachingDBHandler struct {
	data_access.DBHandlerV2

	cfg Config

	entriesMu sync.RWMutex
	entries   map[key]*entry
}

// isNilHandler reports whether handler is nil, including a nil pointer stored
// in a non-nil interface, which would panic on the first promoted call.
func isNilHandler(handler data_access.DBHandlerV2) bool {
	if handler == nil {
		return true
	}
	handlerValue := reflect.ValueOf(handler)
	switch handlerValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice:
		return handlerValue.IsNil()
	default:
		return false
	}
}

// NewCachingDBHandler wraps inner with an empty cache. It returns an error if
// inner is nil, including a typed nil, or cfg has a non-positive field.
func NewCachingDBHandler(inner data_access.DBHandlerV2, cfg Config) (*CachingDBHandler, error) {
	if isNilHandler(inner) {
		return nil, errNilInnerHandler
	}
	if cfg.MaxSize <= 0 {
		return nil, errInvalidMaxSize
	}
	if cfg.FlushInterval <= 0 {
		return nil, errInvalidFlushInterval
	}
	return &CachingDBHandler{
		DBHandlerV2: inner,
		cfg:         cfg,
		entries:     make(map[key]*entry),
	}, nil
}

// lookup returns a copy of the entry for k, and whether it exists. It returns
// a copy because the read lock is released on return; a pointer would let the
// caller read fields while a writer mutates them, which is a data race. The
// copy is cheap because details is a slice header, not a copy of the payload,
// and cheaper than holding a lock for the caller.
func (h *CachingDBHandler) lookup(k key) (entry, bool) {
	h.entriesMu.RLock()
	defer h.entriesMu.RUnlock()
	e, ok := h.entries[k]
	if !ok {
		return entry{}, false
	}
	return *e, true
}
