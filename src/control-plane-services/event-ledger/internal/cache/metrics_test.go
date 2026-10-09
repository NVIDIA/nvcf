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
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var (
	sizeEviction     = attribute.String("reason", "max_size")
	inactiveEviction = attribute.String("reason", "ttl")
	successfulFlush  = attribute.String("result", "success")
	failedFlush      = attribute.String("result", "failure")
)

// discardMeter returns a meter that drops every measurement, for tests that do
// not look at metrics.
func discardMeter() metric.Meter {
	return noop.NewMeterProvider().Meter("cache-test")
}

// newMetricsHandler returns a handler whose metrics the returned reader collects.
func newMetricsHandler(t *testing.T, cfg Config) (*CachingDBHandler, *flushRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	recorder := &flushRecorder{}
	handler, err := NewCachingDBHandler(&fakeDB{}, cfg, recorder.flush, provider.Meter("cache-test"))
	require.NoError(t, err)
	return handler, recorder, reader
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	return collected
}

// counterValue returns the value of the counter series with exactly the given
// attributes, and whether that series exists.
func counterValue(t *testing.T, reader *sdkmetric.ManualReader, name string, attributes ...attribute.KeyValue) (int64, bool) {
	t.Helper()
	wanted := attribute.NewSet(attributes...)
	for _, scope := range collectMetrics(t, reader).ScopeMetrics {
		for _, collectedMetric := range scope.Metrics {
			if collectedMetric.Name != name {
				continue
			}
			sum, isSum := collectedMetric.Data.(metricdata.Sum[int64])
			require.True(t, isSum, "%s should be a counter", name)
			for _, dataPoint := range sum.DataPoints {
				if dataPoint.Attributes.Equals(&wanted) {
					return dataPoint.Value, true
				}
			}
		}
	}
	return 0, false
}

func requireCounter(t *testing.T, reader *sdkmetric.ManualReader, want int64, name string, attributes ...attribute.KeyValue) {
	t.Helper()
	got, exists := counterValue(t, reader, name, attributes...)
	require.True(t, exists, "%s %v has no series", name, attributes)
	assert.Equal(t, want, got, "%s %v", name, attributes)
}

func entriesGauge(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	for _, scope := range collectMetrics(t, reader).ScopeMetrics {
		for _, collectedMetric := range scope.Metrics {
			if collectedMetric.Name != entriesMetricName {
				continue
			}
			gauge, isGauge := collectedMetric.Data.(metricdata.Gauge[int64])
			require.True(t, isGauge, "%s should be a gauge", entriesMetricName)
			require.Len(t, gauge.DataPoints, 1)
			return gauge.DataPoints[0].Value
		}
	}
	t.Fatalf("%s was not collected", entriesMetricName)
	return 0
}

func TestNewCachingDBHandler_NilMeterFails(t *testing.T) {
	handler, err := NewCachingDBHandler(&fakeDB{}, testConfig(), discardFlush, nil)

	require.ErrorIs(t, err, errNilMeter)
	assert.Nil(t, handler)
}

func TestMetrics_AreZeroBeforeAnyEvent(t *testing.T) {
	_, _, reader := newMetricsHandler(t, evictionConfig(10))

	requireCounter(t, reader, 0, hitsMetricName)
	requireCounter(t, reader, 0, missesMetricName)
	requireCounter(t, reader, 0, evictionsMetricName, sizeEviction)
	requireCounter(t, reader, 0, evictionsMetricName, inactiveEviction)
	requireCounter(t, reader, 0, flushesMetricName, successfulFlush)
	requireCounter(t, reader, 0, flushesMetricName, failedFlush)
	assert.Zero(t, entriesGauge(t, reader))
}

func TestMetrics_HitsAndMissesFollowTheOutcome(t *testing.T) {
	tests := []struct {
		name       string
		events     []struct{ keyIndex, second int }
		wantHits   int64
		wantMisses int64
	}{
		{name: "one new key is a miss", events: []struct{ keyIndex, second int }{{1, 1}}, wantHits: 0, wantMisses: 1},
		{name: "a newer event on a clean entry is a hit", events: []struct{ keyIndex, second int }{{1, 1}, {1, 2}}, wantHits: 1, wantMisses: 1},
		{name: "a newer event on a pending entry is a hit", events: []struct{ keyIndex, second int }{{1, 1}, {1, 2}, {1, 3}}, wantHits: 2, wantMisses: 1},
		{name: "a stale event is a hit", events: []struct{ keyIndex, second int }{{1, 5}, {1, 1}}, wantHits: 1, wantMisses: 1},
		{name: "different keys are separate misses", events: []struct{ keyIndex, second int }{{1, 1}, {2, 1}, {3, 1}}, wantHits: 0, wantMisses: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _, reader := newMetricsHandler(t, evictionConfig(10))

			for _, step := range tt.events {
				handler.processEvent(keyN(step.keyIndex), event{timestamp: at(step.second)}, at(step.second))
			}

			requireCounter(t, reader, tt.wantHits, hitsMetricName)
			requireCounter(t, reader, tt.wantMisses, missesMetricName)
		})
	}
}

func TestMetrics_EvictionsAreLabelledByReason(t *testing.T) {
	t.Run("size eviction", func(t *testing.T) {
		handler, _, reader := newMetricsHandler(t, evictionConfig(1))
		handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

		handler.processEvent(keyN(2), event{timestamp: at(2)}, at(2))

		requireCounter(t, reader, 1, evictionsMetricName, sizeEviction)
		requireCounter(t, reader, 0, evictionsMetricName, inactiveEviction)
	})

	t.Run("inactivity eviction", func(t *testing.T) {
		handler, _, reader := newMetricsHandler(t, evictionConfig(10))
		handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))
		handler.processEvent(keyN(2), event{timestamp: at(1)}, at(1))

		handler.evictInactive(at(1000))

		requireCounter(t, reader, 0, evictionsMetricName, sizeEviction)
		requireCounter(t, reader, 2, evictionsMetricName, inactiveEviction)
	})

	t.Run("nothing evicted", func(t *testing.T) {
		handler, _, reader := newMetricsHandler(t, evictionConfig(10))
		handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

		handler.evictInactive(at(2))

		requireCounter(t, reader, 0, evictionsMetricName, sizeEviction)
		requireCounter(t, reader, 0, evictionsMetricName, inactiveEviction)
	})
}

func TestMetrics_EntriesGaugeFollowsInsertsAndEvictions(t *testing.T) {
	handler, _, reader := newMetricsHandler(t, evictionConfig(3))
	assert.EqualValues(t, 0, entriesGauge(t, reader))

	for entryCount := 1; entryCount <= 3; entryCount++ {
		handler.processEvent(keyN(entryCount), event{timestamp: at(entryCount)}, at(entryCount))
		assert.EqualValues(t, entryCount, entriesGauge(t, reader), "after insert %d", entryCount)
	}

	handler.processEvent(keyN(4), event{timestamp: at(4)}, at(4))
	assert.EqualValues(t, 3, entriesGauge(t, reader), "an insert at the limit evicts one, so the size stays")

	handler.evictInactive(at(1000))
	assert.EqualValues(t, 0, entriesGauge(t, reader), "after every entry expired")
}

func TestMetrics_FlushesAreCountedByResult(t *testing.T) {
	t.Run("a successful flush of an evicted pending entry", func(t *testing.T) {
		handler, _, reader := newMetricsHandler(t, evictionConfig(1))
		makePending(handler, keyN(1), 1)

		handler.processEvent(keyN(2), event{timestamp: at(10)}, at(10))

		requireCounter(t, reader, 1, flushesMetricName, successfulFlush)
		requireCounter(t, reader, 0, flushesMetricName, failedFlush)
	})

	t.Run("a flush that fails every retry is counted once and the entry is still evicted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			handler, flushed, reader := newMetricsHandler(t, evictionConfig(1))
			flushed.err = errors.New("database unavailable")
			makePending(handler, keyN(1), 1)

			handler.processEvent(keyN(2), event{timestamp: at(10)}, at(10))

			requireCounter(t, reader, 0, flushesMetricName, successfulFlush)
			requireCounter(t, reader, 1, flushesMetricName, failedFlush)
			requireCounter(t, reader, 1, evictionsMetricName, sizeEviction)
		})
	})

	t.Run("a flush that succeeds on a retry counts as one success", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			handler, flushed, reader := newMetricsHandler(t, evictionConfig(1))
			flushed.failFirst = 2
			makePending(handler, keyN(1), 1)

			handler.processEvent(keyN(2), event{timestamp: at(10)}, at(10))

			requireCounter(t, reader, 1, flushesMetricName, successfulFlush)
			requireCounter(t, reader, 0, flushesMetricName, failedFlush)
		})
	})

	t.Run("evicting a clean entry flushes nothing", func(t *testing.T) {
		handler, _, reader := newMetricsHandler(t, evictionConfig(1))
		handler.processEvent(keyN(1), event{timestamp: at(1)}, at(1))

		handler.processEvent(keyN(2), event{timestamp: at(2)}, at(2))

		requireCounter(t, reader, 0, flushesMetricName, successfulFlush)
		requireCounter(t, reader, 0, flushesMetricName, failedFlush)
		requireCounter(t, reader, 1, evictionsMetricName, sizeEviction)
	})

	t.Run("inactivity eviction flushes pending entries", func(t *testing.T) {
		handler, _, reader := newMetricsHandler(t, evictionConfig(10))
		makePending(handler, keyN(1), 1)

		handler.evictInactive(at(1000))

		requireCounter(t, reader, 1, flushesMetricName, successfulFlush)
	})
}

// Run with -race. The reader collects while events arrive and entries are
// evicted. Collecting reads the entry count under the cache lock, so it must not
// deadlock with the writers, and every event must be counted exactly once.
func TestMetrics_ConcurrentEventsAreAllCounted(t *testing.T) {
	const (
		writers         = 8
		eventsPerWriter = 200
	)
	handler, _, reader := newMetricsHandler(t, evictionConfig(5))
	var clock atomic.Int64

	writersDone := make(chan struct{})
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			select {
			case <-writersDone:
				return
			default:
				var collected metricdata.ResourceMetrics
				assert.NoError(t, reader.Collect(context.Background(), &collected))
			}
		}
	}()

	var wg sync.WaitGroup
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for eventIndex := range eventsPerWriter {
				sequence := int(clock.Add(1))
				handler.processEvent(keyN((writer+eventIndex)%12), event{timestamp: at(sequence)}, at(sequence))
				if eventIndex%9 == 0 {
					handler.evictInactive(at(sequence))
				}
			}
		}()
	}
	wg.Wait()
	close(writersDone)
	select {
	case <-collectorDone:
	case <-time.After(30 * time.Second):
		t.Fatal("collecting metrics deadlocked with the cache")
	}

	hits, _ := counterValue(t, reader, hitsMetricName)
	misses, _ := counterValue(t, reader, missesMetricName)
	assert.EqualValues(t, writers*eventsPerWriter, hits+misses)
	assert.LessOrEqual(t, entriesGauge(t, reader), int64(5))
}

// failingMeter is a meter that cannot create one named instrument.
type failingMeter struct {
	metric.Meter
	failOn string
	err    error
}

func (meter failingMeter) Int64Counter(name string, options ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	if name == meter.failOn {
		return nil, meter.err
	}
	return meter.Meter.Int64Counter(name, options...)
}

func (meter failingMeter) Int64ObservableGauge(name string, options ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	if name == meter.failOn {
		return nil, meter.err
	}
	return meter.Meter.Int64ObservableGauge(name, options...)
}

func TestNewCachingDBHandler_ReturnsAnErrorWhenAnInstrumentCannotBeCreated(t *testing.T) {
	instrumentErr := errors.New("instrument rejected")
	for _, instrumentName := range []string{hitsMetricName, missesMetricName, evictionsMetricName, flushesMetricName, entriesMetricName} {
		t.Run(instrumentName, func(t *testing.T) {
			meter := failingMeter{Meter: discardMeter(), failOn: instrumentName, err: instrumentErr}

			handler, err := NewCachingDBHandler(&fakeDB{}, testConfig(), discardFlush, meter)

			require.ErrorIs(t, err, instrumentErr)
			assert.ErrorContains(t, err, instrumentName)
			assert.Nil(t, handler)
		})
	}
}
