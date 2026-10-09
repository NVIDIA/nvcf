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
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
)

// flushDue writes the pending entries of the keys whose scheduled flush has come
// due, in one call. It runs on the timing wheel's goroutine. A key whose entry is
// gone or no longer pending is skipped: eviction writes and removes entries
// without waiting for the wheel, and a key can come due after that.
func (handler *CachingDBHandler) flushDue(dueKeys []key) {
	ctx := context.Background()
	records := handler.pendingRecords(dueKeys)
	if err := handler.writePending(ctx, records); err != nil {
		// completeFlush leaves the entries pending and schedules them again, so this
		// only reports the error.
		logging.GetLogger(ctx).ErrorContext(ctx, "failed to flush pending cache entries",
			append(batchFields(records), zap.Error(err))...)
	}
}

// pendingRecords returns the records of the entries among keys that are pending.
func (handler *CachingDBHandler) pendingRecords(keys []key) []data_access.EventV3UpsertRecord {
	handler.entriesMu.RLock()
	defer handler.entriesMu.RUnlock()

	records := make([]data_access.EventV3UpsertRecord, 0, len(keys))
	for _, pendingKey := range keys {
		if cachedEntry, ok := handler.entries[pendingKey]; ok && cachedEntry.pending {
			records = append(records, recordOf(pendingKey, cachedEntry.entry))
		}
	}
	return records
}

// oldestPendingKeys returns the keys of up to limit pending entries, least
// recently updated first.
func (handler *CachingDBHandler) oldestPendingKeys(limit int) []key {
	handler.entriesMu.RLock()
	defer handler.entriesMu.RUnlock()

	var keys []key
	for node := handler.recency.Back(); node != nil && len(keys) < limit; node = node.Prev() {
		pendingKey := node.Value.(key)
		if handler.entries[pendingKey].pending {
			keys = append(keys, pendingKey)
		}
	}
	return keys
}

// batchFields are the log fields that identify a batch of records: how many there
// are and the first one, which is enough to find the batch without logging every key.
func batchFields(records []data_access.EventV3UpsertRecord) []zap.Field {
	return []zap.Field{
		zap.Int("count", len(records)),
		zap.String("first_namespace", records[0].Namespace),
		zap.String("first_context", records[0].Context),
		zap.String("first_event_name", records[0].EventName),
	}
}

// recordFlushResult counts the records of a batch as written or failed.
func (handler *CachingDBHandler) recordFlushResult(records []data_access.EventV3UpsertRecord, err error) {
	if err != nil {
		handler.metrics.recordFlushes(flushFailed, len(records))
		return
	}
	handler.metrics.recordFlushes(flushSucceeded, len(records))
}

// writePending writes the records of pending entries to the database in one call,
// without the cache lock held, and then updates the entries to match the result.
// A write is bounded by flushTimeout, and its error is for the caller to report.
func (handler *CachingDBHandler) writePending(parent context.Context, records []data_access.EventV3UpsertRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, flushTimeout)
	defer cancel()
	err := handler.flush(ctx, records)
	handler.recordFlushResult(records, err)
	handler.completeFlush(records, err == nil)
	return err
}

// completeFlush marks written entries clean and schedules the still-pending ones
// again, because their key left the wheel when it came due. An entry is marked
// clean only if its timestamp is the one written, so a newer event that arrived
// during the write stays pending. An entry evicted and recreated with the same
// timestamp during the write cannot be told apart.
func (handler *CachingDBHandler) completeFlush(records []data_access.EventV3UpsertRecord, written bool) {
	handler.entriesMu.Lock()
	defer handler.entriesMu.Unlock()

	now := time.Now()
	for _, record := range records {
		recordKey := keyOf(record)
		cachedEntry, ok := handler.entries[recordKey]
		if !ok {
			continue
		}
		switch {
		case written && cachedEntry.timestamp.Equal(record.Timestamp):
			cachedEntry.pending = false
			cachedEntry.lastWritten = now
		case cachedEntry.pending:
			// The write failed, or a newer event arrived during it.
			handler.wheel.schedule(recordKey, handler.cfg.FlushInterval)
		}
	}
}

// Drain stops the timing wheel and writes every pending entry, a batch at a time,
// until none are left or ctx ends. It leaves the wrapped handler open, so it is
// what to call at shutdown while other components still use the database.
func (handler *CachingDBHandler) Drain(ctx context.Context) error {
	handler.wheel.stop()
	for {
		keys := handler.oldestPendingKeys(drainBatchSize)
		if len(keys) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("cache: stopped draining with entries still pending: %w", err)
		}
		// The entries can be flushed or evicted between the two lookups, so the
		// keys may yield no records while other entries are still pending.
		if err := handler.writePending(ctx, handler.pendingRecords(keys)); err != nil {
			return fmt.Errorf("cache: failed to drain pending entries: %w", err)
		}
	}
}

// Close drains the pending entries, within closeDrainTimeout, and then closes the
// wrapped handler.
func (handler *CachingDBHandler) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeDrainTimeout)
	defer cancel()
	drainErr := handler.Drain(ctx)
	return errors.Join(drainErr, handler.DBHandlerV2.Close())
}

// flushEvicted writes evicted entries with retry, and drops them if every try
// fails, because they are already out of the cache. The write is not under the
// lock and retries widen the window for a newer event to land first, so the upsert
// must apply only events newer than the stored one.
func (handler *CachingDBHandler) flushEvicted(records []data_access.EventV3UpsertRecord) {
	if len(records) == 0 {
		return
	}
	// The write flushes other keys' data, so it must not be tied to the context of
	// the request that triggered the eviction.
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	attempts, err := handler.flushWithRetry(ctx, records)
	handler.recordFlushResult(records, err)
	if err == nil {
		return
	}
	message := "dropped evicted cache entries that could not be written"
	if attempts == 0 {
		message = "dropped evicted cache entries because the flush timeout ended before their write"
	}
	logging.GetLogger(ctx).ErrorContext(ctx, message,
		append(batchFields(records), zap.Error(err), zap.Int("attempts", attempts))...)
}

// flushWithRetry writes records in one call up to evictionFlushAttempts times,
// waiting twice as long after each failure. It returns how many writes it made,
// and the last error if none succeeded. It makes no write once ctx has ended,
// since the write would fail at once.
func (handler *CachingDBHandler) flushWithRetry(ctx context.Context, records []data_access.EventV3UpsertRecord) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for attempts := 1; ; attempts++ {
		err := handler.flush(ctx, records)
		if err == nil || attempts == evictionFlushAttempts || ctx.Err() != nil {
			return attempts, err
		}
		delay := evictionRetryBaseDelay << (attempts - 1)
		logging.GetLogger(ctx).WarnContext(ctx, "failed to write cache entries, waiting to retry",
			append(batchFields(records), zap.Error(err), zap.Int("attempt", attempts), zap.Duration("retry_in", delay))...)
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

// writeMisses writes cache misses, with retries. A miss was stored as not pending,
// so if every try fails it is made pending for a later flush, or a redelivery with
// the same timestamp would be dropped as stale. The error is still returned.
func (handler *CachingDBHandler) writeMisses(ctx context.Context, records []data_access.EventV3UpsertRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()
	attempts, err := handler.flushWithRetry(ctx, records)
	if err != nil {
		handler.keepForLaterFlush(records)
		logging.GetLogger(ctx).WarnContext(ctx, "kept cache misses for a later flush after their write failed",
			append(batchFields(records), zap.Int("attempts", attempts))...)
	}
	return err
}

// keepForLaterFlush stores the records as pending entries and writes the pending
// entries evicted to make room.
func (handler *CachingDBHandler) keepForLaterFlush(records []data_access.EventV3UpsertRecord) {
	now := time.Now()
	var evicted []data_access.EventV3UpsertRecord
	for _, record := range records {
		evicted = append(evicted, handler.storeAsPending(record, now)...)
	}
	handler.flushEvicted(evicted)
}

// storeAsPending stores the record's event as the pending entry for its key and
// returns the pending entries evicted to make room. Unlike recordEvent, an equal
// timestamp replaces the entry, because a failed miss left a clean entry with that
// timestamp. A newer stored event is kept.
func (handler *CachingDBHandler) storeAsPending(record data_access.EventV3UpsertRecord, now time.Time) []data_access.EventV3UpsertRecord {
	cachedKey, ev := keyOf(record), eventOf(record)
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
