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
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	hitsMetricName      = "nvcf_event_ledger_cache_hits_total"
	missesMetricName    = "nvcf_event_ledger_cache_misses_total"
	evictionsMetricName = "nvcf_event_ledger_cache_evictions_total"
	flushesMetricName   = "nvcf_event_ledger_cache_flushes_total"
	entriesMetricName   = "nvcf_event_ledger_cache_entries"
)

// evictionReason says why an entry was evicted. It is a metric label, so it must
// stay a small fixed set.
type evictionReason string

const (
	evictedForSize     evictionReason = "max_size"
	evictedForInactive evictionReason = "ttl"
)

// flushResult says how a flush of an evicted entry ended. It is a metric label,
// so it must stay a small fixed set.
type flushResult string

const (
	flushSucceeded flushResult = "success"
	flushFailed    flushResult = "failure"
)

var (
	allEvictionReasons = []evictionReason{evictedForSize, evictedForInactive}
	allFlushResults    = []flushResult{flushSucceeded, flushFailed}
)

// cacheMetrics holds the cache's OpenTelemetry instruments. The labels are the
// eviction reason and the flush result only, and never the key, so the number of
// series stays fixed however many keys the cache sees.
type cacheMetrics struct {
	hits      metric.Int64Counter
	misses    metric.Int64Counter
	evictions metric.Int64Counter
	flushes   metric.Int64Counter

	reasonOptions map[evictionReason]metric.AddOption
	resultOptions map[flushResult]metric.AddOption
}

// newCacheMetrics creates the instruments on meter. entryCount is read each time
// the entries gauge is collected, so the gauge always shows the current size. Every
// counter is set to zero for every label value, so it appears on the first scrape.
func newCacheMetrics(meter metric.Meter, entryCount func() int64) (*cacheMetrics, error) {
	hits, err := meter.Int64Counter(hitsMetricName,
		metric.WithDescription("Events that found an existing entry in the cache"))
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create %s: %w", hitsMetricName, err)
	}
	misses, err := meter.Int64Counter(missesMetricName,
		metric.WithDescription("Events with no entry in the cache, which are stored as written and left for the caller to write to the database"))
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create %s: %w", missesMetricName, err)
	}
	evictions, err := meter.Int64Counter(evictionsMetricName,
		metric.WithDescription("Entries evicted from the cache, by reason"))
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create %s: %w", evictionsMetricName, err)
	}
	flushes, err := meter.Int64Counter(flushesMetricName,
		metric.WithDescription("Flushes of evicted pending entries to the database, by result. A failure means every retry failed and the entry was dropped"))
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create %s: %w", flushesMetricName, err)
	}
	_, err = meter.Int64ObservableGauge(entriesMetricName,
		metric.WithDescription("Entries currently in the cache"),
		metric.WithUnit("{entry}"),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			observer.Observe(entryCount())
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create %s: %w", entriesMetricName, err)
	}

	cacheMetrics := &cacheMetrics{
		hits:          hits,
		misses:        misses,
		evictions:     evictions,
		flushes:       flushes,
		reasonOptions: make(map[evictionReason]metric.AddOption, len(allEvictionReasons)),
		resultOptions: make(map[flushResult]metric.AddOption, len(allFlushResults)),
	}
	for _, reason := range allEvictionReasons {
		cacheMetrics.reasonOptions[reason] = metric.WithAttributeSet(attribute.NewSet(attribute.String("reason", string(reason))))
	}
	for _, result := range allFlushResults {
		cacheMetrics.resultOptions[result] = metric.WithAttributeSet(attribute.NewSet(attribute.String("result", string(result))))
	}

	ctx := context.Background()
	hits.Add(ctx, 0)
	misses.Add(ctx, 0)
	for _, reason := range allEvictionReasons {
		evictions.Add(ctx, 0, cacheMetrics.reasonOptions[reason])
	}
	for _, result := range allFlushResults {
		flushes.Add(ctx, 0, cacheMetrics.resultOptions[result])
	}
	return cacheMetrics, nil
}

// recordOutcome counts an event as a miss when it found no entry, and as a hit
// otherwise, whether or not the event was newer than the cached one.
func (cacheMetrics *cacheMetrics) recordOutcome(eventOutcome outcome) {
	if eventOutcome == outcomeMiss {
		cacheMetrics.misses.Add(context.Background(), 1)
		return
	}
	cacheMetrics.hits.Add(context.Background(), 1)
}

func (cacheMetrics *cacheMetrics) recordEviction(reason evictionReason) {
	cacheMetrics.evictions.Add(context.Background(), 1, cacheMetrics.reasonOptions[reason])
}

func (cacheMetrics *cacheMetrics) recordFlush(result flushResult) {
	cacheMetrics.flushes.Add(context.Background(), 1, cacheMetrics.resultOptions[result])
}
