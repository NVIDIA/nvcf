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

	// evictionFlushTimeout bounds the database writes, with their retries, made for
	// the entries one eviction pass removes, or for one cache miss.
	evictionFlushTimeout = 10 * time.Second

	// evictionFlushAttempts is how many times the write of an evicted entry is
	// tried before the entry is dropped.
	evictionFlushAttempts = 4

	// evictionRetryBaseDelay is the wait before the first retry. Each wait is twice
	// the one before, so the waits for one entry total 700ms, which is how long a
	// request that triggers an eviction can be held up per entry when the database
	// is down.
	evictionRetryBaseDelay = 100 * time.Millisecond

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
	// caller must write it to the database now, with writeMiss.
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
// room is written before processEvent returns, with retries if the write fails.
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
		// A miss is written at once by the caller, which is why the entry starts clean.
		handler.addEntry(cachedKey, ev, now, false)
		return outcomeMiss, handler.evictOverflow()
	}
	if !ev.timestamp.After(cachedEntry.timestamp) {
		return outcomeStale, nil
	}
	if handler.replaceEntry(cachedKey, cachedEntry, ev, now) {
		return outcomeAlreadyPending, nil
	}
	return outcomeBecamePending, nil
}

// addEntry stores ev as a new entry for cachedKey, at the front of the recency
// list. The caller holds entriesMu.
func (handler *CachingDBHandler) addEntry(cachedKey key, ev event, now time.Time, pending bool) {
	cachedEntry := &cached{entry: entry{
		timestamp:   ev.timestamp,
		source:      ev.source,
		details:     ev.details,
		pending:     pending,
		lastUpdated: now,
	}}
	if !pending {
		cachedEntry.lastWritten = now
	}
	cachedEntry.recencyNode = handler.recency.PushFront(cachedKey)
	handler.entries[cachedKey] = cachedEntry
	if pending {
		handler.scheduleFlush(cachedKey)
	}
}

// replaceEntry makes ev the latest event of an existing entry and marks it
// pending, and reports whether it already was. The caller holds entriesMu.
func (handler *CachingDBHandler) replaceEntry(cachedKey key, cachedEntry *cached, ev event, now time.Time) bool {
	wasPending := cachedEntry.pending
	cachedEntry.timestamp = ev.timestamp
	cachedEntry.source = ev.source
	cachedEntry.details = ev.details
	cachedEntry.pending = true
	cachedEntry.lastUpdated = now
	handler.recency.MoveToFront(cachedEntry.recencyNode)
	if !wasPending {
		handler.scheduleFlush(cachedKey)
	}
	return wasPending
}

// scheduleFlush is called while entriesMu is held, as eviction unschedules under
// it, so a key is in the wheel only while its entry is pending. Scheduling after
// the lock was released could race with an eviction and leave a pending entry
// unscheduled.
func (handler *CachingDBHandler) scheduleFlush(cachedKey key) {
	handler.wheel.schedule(cachedKey, handler.cfg.FlushInterval)
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
	return recordOf(cachedKey, cachedEntry.entry), true
}

func recordOf(cachedKey key, cachedEntry entry) data_access.EventV3UpsertRecord {
	return data_access.EventV3UpsertRecord{
		Namespace: cachedKey.namespace,
		Context:   cachedKey.context,
		EventName: cachedKey.eventName,
		Source:    cachedEntry.source,
		Details:   cachedEntry.details,
		Timestamp: cachedEntry.timestamp,
	}
}

// flushEvicted writes recs to the database, retrying a failed write with
// exponential backoff. The entries are already gone from the cache, so an entry
// whose writes all fail is dropped, and that is counted as a failed flush and
// logged. The records share one timeout, so when the database is down or slow the
// later records get fewer tries, and none once the timeout has ended.
//
// The write is made without the lock held, so a newer event for the same key can
// reach the database before it, and a retry widens that window to the length of
// the timeout. The database upsert must therefore apply an event only if it is
// newer than the stored one.
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
		attempts, err := handler.flushWithRetry(ctx, rec)
		if err != nil {
			handler.metrics.recordFlush(flushFailed)
			message := "dropped an evicted cache entry that could not be written"
			if attempts == 0 {
				message = "dropped an evicted cache entry because the flush timeout ended before its write"
			}
			logger.ErrorContext(ctx, message,
				zap.Error(err),
				zap.Int("attempts", attempts),
				zap.String("namespace", rec.Namespace),
				zap.String("context", rec.Context),
				zap.String("event_name", rec.EventName))
			continue
		}
		handler.metrics.recordFlush(flushSucceeded)
	}
}

// flushWithRetry writes rec up to evictionFlushAttempts times, waiting twice as
// long after each failure. It returns how many writes it made, and the last error
// if none succeeded. It makes no write once ctx has ended, since the write would
// fail at once.
func (handler *CachingDBHandler) flushWithRetry(ctx context.Context, rec data_access.EventV3UpsertRecord) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for attempts := 1; ; attempts++ {
		err := handler.flush(ctx, rec)
		if err == nil || attempts == evictionFlushAttempts || ctx.Err() != nil {
			return attempts, err
		}
		delay := evictionRetryBaseDelay << (attempts - 1)
		logging.GetLogger(ctx).WarnContext(ctx, "failed to write an evicted cache entry, waiting to retry",
			zap.Error(err),
			zap.Int("attempt", attempts),
			zap.Duration("retry_in", delay),
			zap.String("namespace", rec.Namespace),
			zap.String("context", rec.Context),
			zap.String("event_name", rec.EventName))
		if !waitOrDone(ctx, delay) {
			return attempts, err
		}
	}
}

// waitOrDone waits for delay and reports false if ctx ended first.
func waitOrDone(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// writeMiss writes the event of a cache miss, which the cache stored as clean, so
// a failed write that is not dealt with would hide the event: a redelivery with
// the same timestamp is discarded as stale. The write is retried like an
// eviction write. If every try fails, the event is kept as a pending entry and
// scheduled for a later flush, so it is written even if the source never sends it
// again, and the write error is returned for the caller to report.
func (handler *CachingDBHandler) writeMiss(ctx context.Context, cachedKey key, ev event) error {
	ctx, cancel := context.WithTimeout(ctx, evictionFlushTimeout)
	defer cancel()
	attempts, err := handler.flushWithRetry(ctx, recordOf(cachedKey, entry{timestamp: ev.timestamp, source: ev.source, details: ev.details}))
	if err == nil {
		return nil
	}
	handler.flushEvicted(handler.keepPending(cachedKey, ev, time.Now()))
	logging.GetLogger(ctx).WarnContext(ctx, "kept a cache miss for a later flush after its write failed",
		zap.Int("attempts", attempts),
		zap.String("namespace", cachedKey.namespace),
		zap.String("context", cachedKey.context),
		zap.String("event_name", cachedKey.eventName))
	return err
}

// keepPending makes ev the pending entry for cachedKey and schedules its flush,
// unless a newer event is already stored, which is pending or is written by its
// own caller. The entry may be gone, evicted while the write was in flight, so it
// is created again, and the pending entries that makes room by evicting are
// returned for the caller to write once the lock is released.
func (handler *CachingDBHandler) keepPending(cachedKey key, ev event, now time.Time) []data_access.EventV3UpsertRecord {
	handler.entriesMu.Lock()
	defer handler.entriesMu.Unlock()

	cachedEntry, exists := handler.entries[cachedKey]
	if !exists {
		handler.addEntry(cachedKey, ev, now, true)
		return handler.evictOverflow()
	}
	if !ev.timestamp.Before(cachedEntry.timestamp) {
		handler.replaceEntry(cachedKey, cachedEntry, ev, now)
	}
	return nil
}
