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
	"errors"
	"fmt"
	"math"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/cache"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

var (
	errCacheNeedsGuardedWriter = errors.New("the write cache needs a database handler that writes events only when they are newer than the stored row")
	errCacheFlushIntervalRange = errors.New("cache flush-interval-seconds is too large")
)

// maxFlushIntervalSeconds is the largest number of seconds that converts to a
// time.Duration without overflowing. The cache enforces its own, much smaller limit.
const maxFlushIntervalSeconds = math.MaxInt64 / int64(time.Second)

// wrapWithCache wraps dbV2 in the events write cache and starts its timing wheel,
// when the cache is enabled. Pending entries are flushed with the guarded write, so
// a delayed flush cannot replace a newer event.
func wrapWithCache(dbV2 data_access.DBHandlerV2, cacheConfig config.CacheConfig, meter metric.Meter) (data_access.DBHandlerV2, error) {
	if !cacheConfig.Enabled {
		return dbV2, nil
	}
	guardedWriter, ok := dbV2.(data_access.GuardedEventsWriter)
	if !ok {
		return nil, errCacheNeedsGuardedWriter
	}

	cacheConfig = cacheConfig.WithDefaults()
	if int64(cacheConfig.FlushIntervalSeconds) > maxFlushIntervalSeconds {
		return nil, errCacheFlushIntervalRange
	}
	handler, err := cache.NewCachingDBHandler(dbV2, cache.Config{
		MaxSize:       cacheConfig.MaxSize,
		FlushInterval: time.Duration(cacheConfig.FlushIntervalSeconds) * time.Second,
	}, guardedWriter.UpsertEventsIfNewerV3, meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create the write cache: %w", err)
	}
	handler.Start()
	return handler, nil
}

// DrainWriteCache writes the write cache's pending entries to the database, if the
// cache is in use, within ctx. It leaves the database handler open for other
// components that still use it. Call it once the server has stopped taking requests.
func (connections Connections) DrainWriteCache(ctx context.Context) error {
	drainer, ok := connections.DbHandlerV2.(interface{ Drain(context.Context) error })
	if !ok {
		return nil
	}
	return drainer.Drain(ctx)
}
