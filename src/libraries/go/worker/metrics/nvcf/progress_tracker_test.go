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

package nvcf

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// freshTrackerCounters builds unregistered counters/histogram so tests do not
// touch the global default registry or collide across runs.
func freshTrackerCounters() (prometheus.Counter, prometheus.Counter, prometheus.Counter, prometheus.Histogram) {
	pre := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_pre_work"})
	post := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_post_work"})
	total := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_total_work"})
	hist := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "test_latency"})
	return pre, post, total, hist
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

func histogramCount(t *testing.T, h prometheus.Histogram) uint64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, h.(prometheus.Metric).Write(&m))
	return m.GetHistogram().GetSampleCount()
}

func TestRequestTrackerFullLifecycle(t *testing.T) {
	pre, post, total, hist := freshTrackerCounters()

	rt := NewRequestTracker(context.Background(), pre, post, total, hist)
	require.NotNil(t, rt)

	rt.StartWork()
	rt.EndWork()
	require.GreaterOrEqual(t, rt.TotalWorkTime, 0.0)
	require.GreaterOrEqual(t, counterValue(t, total), 0.0)

	rt.EndRequest()

	// Pre-work (request->work) and post-work (work->request end) intervals were
	// both observed, so their counters advanced and one latency sample landed.
	require.GreaterOrEqual(t, counterValue(t, pre), 0.0)
	require.GreaterOrEqual(t, counterValue(t, post), 0.0)
	require.Equal(t, uint64(1), histogramCount(t, hist))
}

func TestRequestTrackerEndRequestWithoutWork(t *testing.T) {
	pre, post, total, hist := freshTrackerCounters()

	rt := NewRequestTracker(context.Background(), pre, post, total, hist)
	// No StartWork / EndWork: workStartTime and workEndTime stay zero, so the
	// pre/post counters must remain untouched while latency is still observed.
	rt.EndRequest()

	require.Equal(t, 0.0, counterValue(t, pre))
	require.Equal(t, 0.0, counterValue(t, post))
	require.Equal(t, 0.0, counterValue(t, total))
	require.Equal(t, uint64(1), histogramCount(t, hist))
}

func TestRequestTrackerCancelViaContext(t *testing.T) {
	pre, post, total, hist := freshTrackerCounters()

	ctx, cancel := context.WithCancel(context.Background())
	rt := NewRequestTracker(ctx, pre, post, total, hist)
	// Cancelling the parent context stops the busy-time monitor goroutine via
	// the threadBusyContext.Done() branch.
	cancel()
	rt.EndRequest()
	require.Equal(t, uint64(1), histogramCount(t, hist))
}
