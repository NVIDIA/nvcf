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
	"encoding/json"
	"errors"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// cacheable reports whether an event's payload is small enough to cache.
func cacheable(details json.RawMessage) bool {
	return len(details) <= maxCachedDetailsBytes
}

// UpsertEventV3 writes a cache miss to the database at once, with retries. A hit
// (a later event for a cached key) returns immediately and is flushed later. A
// large payload bypasses the cache and is written at once.
func (handler *CachingDBHandler) UpsertEventV3(traceCtx context.Context, namespace, eventContext, eventName, source string, details json.RawMessage, timestamp time.Time) error {
	if !cacheable(details) {
		return handler.DBHandlerV2.UpsertEventV3(traceCtx, namespace, eventContext, eventName, source, details, timestamp)
	}
	record := data_access.EventV3UpsertRecord{
		Namespace: namespace, Context: eventContext, EventName: eventName, Source: source, Details: details, Timestamp: timestamp,
	}
	if handler.processEvent(keyOf(record), eventOf(record), time.Now()) != outcomeMiss {
		return nil
	}
	return handler.writeMisses(traceCtx, []data_access.EventV3UpsertRecord{record})
}

// BulkUpsertEventsV3 writes events through the cache, like UpsertEventV3. The
// misses are written in one call with retries, and the events with a large
// payload in one call to the wrapped handler. If either fails, the error is
// returned, and the misses are kept for a later flush.
func (handler *CachingDBHandler) BulkUpsertEventsV3(traceCtx context.Context, events []data_access.EventV3UpsertRecord) error {
	now := time.Now()
	var uncached []data_access.EventV3UpsertRecord
	var misses []data_access.EventV3UpsertRecord
	for _, record := range events {
		if !cacheable(record.Details) {
			uncached = append(uncached, record)
			continue
		}
		if handler.processEvent(keyOf(record), eventOf(record), now) == outcomeMiss {
			misses = append(misses, record)
		}
	}
	missesErr := handler.writeMisses(traceCtx, misses)
	if len(uncached) == 0 {
		return missesErr
	}
	return errors.Join(missesErr, handler.DBHandlerV2.BulkUpsertEventsV3(traceCtx, uncached))
}
