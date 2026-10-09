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

package service

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/cache"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// testMeter is a meter that drops every measurement.
var testMeter = noop.NewMeterProvider().Meter("test")

// guardedDB is a database handler that also offers the guarded events write.
type guardedDB struct {
	passDBHandlerV2

	mu            sync.Mutex
	singleWrites  int
	guardedWrites []data_access.EventV3UpsertRecord
}

func (db *guardedDB) UpsertEventV3(context.Context, string, string, string, string, json.RawMessage, time.Time) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.singleWrites++
	return nil
}

func (db *guardedDB) UpsertEventsIfNewerV3(_ context.Context, events []data_access.EventV3UpsertRecord) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.guardedWrites = append(db.guardedWrites, events...)
	return nil
}

func (db *guardedDB) counts() (singleWrites, guardedWrites int) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.singleWrites, len(db.guardedWrites)
}

func enabledCache(maxSize, flushIntervalSeconds int) config.CacheConfig {
	return config.CacheConfig{Enabled: true, MaxSize: maxSize, FlushIntervalSeconds: flushIntervalSeconds}
}

func TestWrapWithCache_ADisabledCacheLeavesTheHandlerAsItIs(t *testing.T) {
	db := &guardedDB{}

	wrapped, err := wrapWithCache(db, config.CacheConfig{Enabled: false, MaxSize: 10, FlushIntervalSeconds: 5}, testMeter)

	require.NoError(t, err)
	assert.Same(t, db, wrapped)
	assert.NoError(t, Connections{DbHandlerV2: wrapped}.DrainWriteCache(context.Background()), "nothing to drain")
}

func TestWrapWithCache_AnEnabledCacheWrapsTheHandlerAndFlushesThroughTheGuardedWrite(t *testing.T) {
	db := &guardedDB{}
	wrapped, err := wrapWithCache(db, enabledCache(10, 60), testMeter)
	require.NoError(t, err)
	handler, isCache := wrapped.(*cache.CachingDBHandler)
	require.True(t, isCache, "the handler is wrapped in the cache")
	t.Cleanup(func() { _ = handler.Close() })

	write := func(second int) {
		require.NoError(t, wrapped.UpsertEventV3(context.Background(), "ns", "ctx", "downloading", "agent",
			json.RawMessage(`{"progress":`+strconv.Itoa(second)+`}`), time.Unix(int64(1000+second), 0)))
	}
	write(1) // a miss: written at once, with the guarded write
	write(2) // held by the cache
	write(3) // held by the cache, replacing the second

	singleWrites, guardedWrites := db.counts()
	assert.Zero(t, singleWrites, "the ordinary write is never used for a cached event")
	assert.Equal(t, 1, guardedWrites, "only the first event was written at once")

	require.NoError(t, Connections{DbHandlerV2: wrapped}.DrainWriteCache(context.Background()))

	_, guardedWrites = db.counts()
	require.Equal(t, 2, guardedWrites, "the held event was written by the drain, also with the guarded write")
	assert.Equal(t, time.Unix(1001, 0), db.guardedWrites[0].Timestamp, "the miss was written first")
	assert.Equal(t, time.Unix(1003, 0), db.guardedWrites[1].Timestamp, "and the drain wrote the latest one, with its own timestamp")
}

func TestWrapWithCache_ZeroSettingsGetTheDefaults(t *testing.T) {
	wrapped, err := wrapWithCache(&guardedDB{}, config.CacheConfig{Enabled: true}, testMeter)

	require.NoError(t, err)
	t.Cleanup(func() { _ = wrapped.Close() })
	assert.IsType(t, &cache.CachingDBHandler{}, wrapped)
}

func TestWrapWithCache_RejectsAHandlerWithoutAGuardedWrite(t *testing.T) {
	wrapped, err := wrapWithCache(&passDBHandlerV2{}, enabledCache(10, 60), testMeter)

	require.ErrorIs(t, err, errCacheNeedsGuardedWriter)
	assert.Nil(t, wrapped)
}

func TestWrapWithCache_RejectsAFlushIntervalThatIsTooLong(t *testing.T) {
	tests := []struct {
		name    string
		seconds int
	}{
		{name: "over the cache's limit", seconds: 3601},
		{name: "a value that would overflow a duration", seconds: math.MaxInt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped, err := wrapWithCache(&guardedDB{}, enabledCache(10, tt.seconds), testMeter)

			require.Error(t, err)
			assert.Nil(t, wrapped)
		})
	}
}

func TestDrainWriteCache_WithoutACacheDoesNothing(t *testing.T) {
	assert.NoError(t, Connections{DbHandlerV2: &passDBHandlerV2{}}.DrainWriteCache(context.Background()))
	assert.NoError(t, Connections{}.DrainWriteCache(context.Background()))
}
