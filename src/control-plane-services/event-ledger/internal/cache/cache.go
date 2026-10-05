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
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
)

const (
	// inactiveTTLBufferPercent is added on top of FlushInterval to form the
	// inactivity TTL, so an entry outlives its scheduled flush.
	inactiveTTLBufferPercent = 10

	// evictionFlushTimeout bounds the database writes made for the entries one
	// eviction pass removes.
	evictionFlushTimeout = 10 * time.Second

	// wheelTick is how long the timing wheel spends on each slot. The wheel has one
	// slot per tick of FlushInterval, so a flush is due when the wheel has made one
	// full rotation.
	wheelTick = time.Second

	// maxFlushInterval bounds the timing wheel, which holds one slot for every
	// wheelTick of FlushInterval and builds them all up front.
	maxFlushInterval = time.Hour
)

var (
	errNilInnerHandler      = errors.New("cache: inner DBHandlerV2 must not be nil")
	errNilFlush             = errors.New("cache: flush function must not be nil")
	errNilMeter             = errors.New("cache: meter must not be nil")
	errInvalidMaxSize       = errors.New("cache: MaxSize must be greater than 0")
	errInvalidFlushInterval = errors.New("cache: FlushInterval must be greater than 0")
	errFlushIntervalTooLong = errors.New("cache: FlushInterval must not exceed one hour")
)

// scheduler tracks when the flush of each pending key is due. The timing wheel
// implements it.
type scheduler interface {
	// schedule makes key due after delay, unless it is already scheduled.
	schedule(scheduledKey key, delay time.Duration) bool
	// unschedule removes key so it never comes due.
	unschedule(scheduledKey key) bool
	start()
	stop()
}

// FlushFunc writes one pending event to the database.
type FlushFunc func(ctx context.Context, rec data_access.EventV3UpsertRecord) error

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

// cached is an entry together with its node in the handler's recency list. The
// node lets an update move the key to the front of the list, and eviction
// remove the least recently updated key, without scanning the map.
type cached struct {
	entry
	recencyNode *list.Element
}

// Config holds the cache tunables. Both fields must be positive, and
// FlushInterval at most one hour. The service config owns the defaults.
type Config struct {
	// MaxSize is the maximum number of entries before the least recently
	// updated one is evicted.
	MaxSize int
	// FlushInterval is the delay between an entry becoming pending and its flush.
	FlushInterval time.Duration
}

// inactiveTTL is how long an entry may go without an update before it is
// eligible for eviction: FlushInterval plus a 10% buffer.
func (cfg Config) inactiveTTL() time.Duration {
	return cfg.FlushInterval + cfg.FlushInterval*inactiveTTLBufferPercent/100
}

// CachingDBHandler wraps a DBHandlerV2 with a local write cache. Methods that
// are not cached are served by the embedded handler.
type CachingDBHandler struct {
	data_access.DBHandlerV2

	cfg     Config
	flush   FlushFunc
	metrics *cacheMetrics
	// wheel schedules the flush of each pending entry. It is called with entriesMu
	// held, so its own lock is always taken after entriesMu, never before, and it
	// must never call into the cache while holding that lock.
	wheel scheduler

	entriesMu sync.RWMutex
	entries   map[key]*cached
	// recency lists keys by last update, most recent first. Its values are keys.
	recency *list.List
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

// NewCachingDBHandler wraps inner with an empty cache. flush writes the pending
// entries that eviction removes, and the cache's metrics are created on meter.
// It returns an error if inner, including a typed nil, flush, or meter is nil,
// cfg has a non-positive field, or FlushInterval is over one hour. The handler's
// timing wheel does not run until Start is called.
func NewCachingDBHandler(inner data_access.DBHandlerV2, cfg Config, flush FlushFunc, meter metric.Meter) (*CachingDBHandler, error) {
	if isNilHandler(inner) {
		return nil, errNilInnerHandler
	}
	if flush == nil {
		return nil, errNilFlush
	}
	if meter == nil {
		return nil, errNilMeter
	}
	if cfg.MaxSize <= 0 {
		return nil, errInvalidMaxSize
	}
	if cfg.FlushInterval <= 0 {
		return nil, errInvalidFlushInterval
	}
	if cfg.FlushInterval > maxFlushInterval {
		return nil, errFlushIntervalTooLong
	}
	handler := &CachingDBHandler{
		DBHandlerV2: inner,
		cfg:         cfg,
		flush:       flush,
		entries:     make(map[key]*cached),
		recency:     list.New(),
	}
	// One slot per tick of FlushInterval, rounded up.
	slotCount := int(cfg.FlushInterval / wheelTick)
	if cfg.FlushInterval%wheelTick > 0 {
		slotCount++
	}
	wheel, err := newTimingWheel(slotCount, wheelTick, handler.flushDue)
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create the timing wheel: %w", err)
	}
	handler.wheel = wheel
	handler.metrics, err = newCacheMetrics(meter, handler.entryCount)
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// entryCount returns the number of entries in the cache.
func (handler *CachingDBHandler) entryCount() int64 {
	handler.entriesMu.RLock()
	defer handler.entriesMu.RUnlock()
	return int64(len(handler.entries))
}

// Start begins advancing the timing wheel in a background goroutine.
func (handler *CachingDBHandler) Start() {
	handler.wheel.start()
}

// Close stops the timing wheel and then closes the wrapped handler. Pending
// entries are not written; what to do with them at shutdown is decided when the
// cache is wired into the service.
func (handler *CachingDBHandler) Close() error {
	handler.wheel.stop()
	return handler.DBHandlerV2.Close()
}

// flushDue receives the keys whose scheduled flush has come due. Writing them to
// the database is added when the cache is wired into the service, so for now a due
// key only leaves the wheel, and its entry stays pending without a scheduled flush.
// The entry may also be gone by the time this runs, because eviction can remove it
// between the wheel handing over the key and this call.
func (handler *CachingDBHandler) flushDue(dueKeys []key) {}

// lookup returns a copy of the entry for cachedKey, and whether it exists. It returns
// a copy because the read lock is released on return; a pointer would let the
// caller read fields while a writer mutates them, which is a data race. The
// copy is cheap because details is a slice header, not a copy of the payload,
// and cheaper than holding a lock for the caller.
func (handler *CachingDBHandler) lookup(cachedKey key) (entry, bool) {
	handler.entriesMu.RLock()
	defer handler.entriesMu.RUnlock()
	cachedEntry, ok := handler.entries[cachedKey]
	if !ok {
		return entry{}, false
	}
	return cachedEntry.entry, true
}

// event is an incoming event to record in the cache. processEvent stores details
// without copying it, so the caller must not mutate it afterwards.
type event struct {
	timestamp time.Time
	source    string
	details   json.RawMessage
}

// outcome reports what processEvent did with an incoming event, and so what the
// caller must do next.
type outcome int

const (
	// outcomeMiss means no entry existed. The event was stored as clean, so the
	// caller must write it to the database now.
	outcomeMiss outcome = iota
	// outcomeBecamePending means a newer event replaced a clean entry. A flush
	// has been scheduled on the timing wheel.
	outcomeBecamePending
	// outcomeAlreadyPending means a newer event replaced a pending entry. A flush
	// is already scheduled and will pick up the latest value.
	outcomeAlreadyPending
	// outcomeStale means the event was not newer than the cached one and was
	// discarded.
	outcomeStale
)

// processEvent records ev for cachedKey and reports what the caller must do. It compares and
// updates under one write lock so concurrent events for a key cannot both
// decide they are the newest. When the entry becomes pending, it schedules the
// flush on the timing wheel. It does not write ev itself to the database, but
// a miss can push the cache over MaxSize, and the pending entry evicted to make
// room is written before processEvent returns.
func (handler *CachingDBHandler) processEvent(cachedKey key, ev event, now time.Time) outcome {
	out, evicted := handler.recordEvent(cachedKey, ev, now)
	handler.metrics.recordOutcome(out)
	handler.flushEvicted(evicted)
	return out
}

// recordEvent updates the cache under the write lock. It returns the pending
// entries it evicted, which the caller writes once the lock is released.
func (handler *CachingDBHandler) recordEvent(cachedKey key, ev event, now time.Time) (outcome, []data_access.EventV3UpsertRecord) {
	handler.entriesMu.Lock()
	defer handler.entriesMu.Unlock()

	cachedEntry, ok := handler.entries[cachedKey]
	if !ok {
		cachedEntry = &cached{entry: entry{
			timestamp:   ev.timestamp,
			source:      ev.source,
			details:     ev.details,
			lastWritten: now,
			lastUpdated: now,
		}}
		cachedEntry.recencyNode = handler.recency.PushFront(cachedKey)
		handler.entries[cachedKey] = cachedEntry
		// Cache miss causes an immediate flush to the database. This will be implemented in a later PR
		return outcomeMiss, handler.evictOverflow()
	}
	if !ev.timestamp.After(cachedEntry.timestamp) {
		return outcomeStale, nil
	}

	wasPending := cachedEntry.pending
	cachedEntry.timestamp = ev.timestamp
	cachedEntry.source = ev.source
	cachedEntry.details = ev.details
	cachedEntry.pending = true
	cachedEntry.lastUpdated = now
	handler.recency.MoveToFront(cachedEntry.recencyNode)
	if wasPending {
		return outcomeAlreadyPending, nil
	}
	// Scheduled while entriesMu is held, as eviction unschedules under it, so a key
	// is in the wheel only while its entry is pending. Scheduling after the lock was
	// released could race with an eviction and leave a pending entry unscheduled.
	handler.wheel.schedule(cachedKey, handler.cfg.FlushInterval)
	return outcomeBecamePending, nil
}

// evictOverflow removes the least recently updated entries until the cache fits
// in MaxSize and returns the pending ones. The caller holds entriesMu.
func (handler *CachingDBHandler) evictOverflow() []data_access.EventV3UpsertRecord {
	var pending []data_access.EventV3UpsertRecord
	for len(handler.entries) > handler.cfg.MaxSize {
		if rec, ok := handler.remove(handler.recency.Back(), evictedForSize); ok {
			pending = append(pending, rec)
		}
	}
	return pending
}

// evictInactive removes the entries that have not been updated within the
// inactivity TTL as of now, writes the pending ones, and returns how many it
// evicted. Nothing calls it yet. A background goroutine must call it on a ticker,
// not in a busy loop. The wheel's fire function only runs for slots that have
// keys, so it cannot drive this. A pass with nothing expired is cheap because the
// list is ordered by last update.
func (handler *CachingDBHandler) evictInactive(now time.Time) int {
	evicted, pending := handler.removeInactive(now)
	handler.flushEvicted(pending)
	return evicted
}

// removeInactive removes the expired entries under the write lock and returns
// how many there were along with the pending ones. The recency list is ordered
// by last update, so it stops at the first entry that has not expired.
func (handler *CachingDBHandler) removeInactive(now time.Time) (int, []data_access.EventV3UpsertRecord) {
	handler.entriesMu.Lock()
	defer handler.entriesMu.Unlock()

	ttl := handler.cfg.inactiveTTL()
	evicted := 0
	var pending []data_access.EventV3UpsertRecord
	for node := handler.recency.Back(); node != nil; node = handler.recency.Back() {
		if now.Sub(handler.entries[node.Value.(key)].lastUpdated) <= ttl {
			break
		}
		if rec, ok := handler.remove(node, evictedForInactive); ok {
			pending = append(pending, rec)
		}
		evicted++
	}
	return evicted, pending
}

// remove deletes the entry at node. It reports the entry as a record only when
// it is pending, since a clean entry is already in the database. The caller
// holds entriesMu.
func (handler *CachingDBHandler) remove(node *list.Element, reason evictionReason) (data_access.EventV3UpsertRecord, bool) {
	cachedKey := handler.recency.Remove(node).(key)
	cachedEntry := handler.entries[cachedKey]
	delete(handler.entries, cachedKey)
	// A pending entry has a scheduled flush, which an eviction replaces by writing
	// the entry now.
	handler.wheel.unschedule(cachedKey)
	handler.metrics.recordEviction(reason)
	if !cachedEntry.pending {
		return data_access.EventV3UpsertRecord{}, false
	}
	return data_access.EventV3UpsertRecord{
		Namespace: cachedKey.namespace,
		Context:   cachedKey.context,
		EventName: cachedKey.eventName,
		Source:    cachedEntry.source,
		Details:   cachedEntry.details,
		Timestamp: cachedEntry.timestamp,
	}, true
}

// flushEvicted writes recs to the database. The entries are already gone from
// the cache, so a failed write is logged and not retried. The write is made
// without the lock held, and a newer event for the same key can reach the
// database before it.
func (handler *CachingDBHandler) flushEvicted(recs []data_access.EventV3UpsertRecord) {
	if len(recs) == 0 {
		return
	}
	// The write flushes another key's data, so it must not be tied to the
	// context of the request that triggered the eviction.
	ctx, cancel := context.WithTimeout(context.Background(), evictionFlushTimeout)
	defer cancel()
	logger := logging.GetLogger(ctx)
	for _, rec := range recs {
		if err := handler.flush(ctx, rec); err != nil {
			handler.metrics.recordFlush(flushFailed)
			logger.ErrorContext(ctx, "failed to flush evicted cache entry",
				zap.Error(err),
				zap.String("namespace", rec.Namespace),
				zap.String("context", rec.Context),
				zap.String("event_name", rec.EventName))
			continue
		}
		handler.metrics.recordFlush(flushSucceeded)
	}
}
