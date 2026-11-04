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
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	threadBusyTimeInterval time.Duration = 5 * time.Second
)

type RequestTracker struct {
	requestStartTime        time.Time
	requestEndTime          time.Time
	workStartTime           time.Time
	workEndTime             time.Time
	TotalWorkTime           float64
	lastThreadBusyTime      time.Time
	ticker                  *time.Ticker
	threadBusyContext       context.Context
	finalizeThreadBusyTime  context.CancelFunc
	preWorkTimeCounter      prometheus.Counter
	postWorkTimeCounter     prometheus.Counter
	totalWorkTimeCounter    prometheus.Counter
	requestLatencyHistogram prometheus.Histogram
}

func NewRequestTracker(ctx context.Context, preWorkTimeCounter prometheus.Counter, postWorkTimeCounter prometheus.Counter, totalWorkTimeCounter prometheus.Counter, requestLatencyHistogram prometheus.Histogram) *RequestTracker {
	RequestCounter.Inc()

	threadBusyContext, cancel := context.WithCancel(ctx)
	now := time.Now()

	rt := &RequestTracker{
		requestStartTime:        now,
		lastThreadBusyTime:      now,
		threadBusyContext:       threadBusyContext,
		finalizeThreadBusyTime:  cancel,
		ticker:                  time.NewTicker(threadBusyTimeInterval),
		preWorkTimeCounter:      preWorkTimeCounter,
		postWorkTimeCounter:     postWorkTimeCounter,
		totalWorkTimeCounter:    totalWorkTimeCounter,
		requestLatencyHistogram: requestLatencyHistogram,
	}

	go startMonitoringThreadBusyTime(rt)

	return rt
}

func startMonitoringThreadBusyTime(rt *RequestTracker) {
	for {
		select {
		case <-rt.threadBusyContext.Done():
			rt.ticker.Stop()
			WorkerThreadBusyTimeCounter.Add(time.Since(rt.lastThreadBusyTime).Seconds())
			return
		case <-rt.ticker.C:
			WorkerThreadBusyTimeCounter.Add(time.Since(rt.lastThreadBusyTime).Seconds())
			rt.lastThreadBusyTime = time.Now()
		}
	}
}

func (rt *RequestTracker) EndRequest() {
	rt.requestEndTime = time.Now()

	rt.finalizeThreadBusyTime()

	if !rt.workStartTime.IsZero() {
		rt.preWorkTimeCounter.Add(rt.workStartTime.Sub(rt.requestStartTime).Seconds())
	}

	if !rt.workEndTime.IsZero() {
		rt.postWorkTimeCounter.Add(rt.requestEndTime.Sub(rt.workEndTime).Seconds())
	}

	rt.requestLatencyHistogram.Observe(rt.requestEndTime.Sub(rt.requestStartTime).Seconds())
}

func (rt *RequestTracker) StartWork() {
	rt.workStartTime = time.Now()
}

func (rt *RequestTracker) EndWork() {
	rt.workEndTime = time.Now()
	// Calculating inference time now as other components use it (like metering).
	rt.TotalWorkTime = rt.workEndTime.Sub(rt.workStartTime).Seconds()
	rt.totalWorkTimeCounter.Add(rt.TotalWorkTime)
}
