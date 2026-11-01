/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package metrics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/kit/endpoint"
	"github.com/go-kit/kit/metrics"
	"github.com/stretchr/testify/assert"
)

var (
	testError              = errors.New("test-error")
	testMovingAvgTolerance = 1.05 // slim tolerance for checking moving average
)

func TestRequestDurationInstrumentor(t *testing.T) {
	// Test instrumentation for the success path
	testDuration := NewTestHistogram()
	ep := endpoint.Endpoint(testEndpoint)
	ep = RequestDurationInstrumentor(testDuration.With("method", "test-method"))(testEndpoint)
	_, err := ep(context.Background(), nil)
	assert.Nil(t, err)
	assert.LessOrEqual(t, testDuration.ApproximateMovingAverage(), testMovingAvgTolerance)
	assert.Equal(t, map[string]string{"method": "test-method", "success": "true"}, testDuration.LabelValues())

	// Test instrumentation for the error path
	testDuration = NewTestHistogram()
	epWithErr := endpoint.Endpoint(testEndpoint)
	epWithErr = RequestDurationInstrumentor(testDuration.With("method", "test-method"))(testEndpoint)
	_, err = epWithErr(context.Background(), testError)
	assert.Equal(t, testError, err)
	assert.LessOrEqual(t, testDuration.ApproximateMovingAverage(), testMovingAvgTolerance)
	assert.Equal(t, map[string]string{"method": "test-method", "success": "false"}, testDuration.LabelValues())
}

// testEndpoint is use to simulate an endpoint with a delay with provided error
func testEndpoint(_ context.Context, req interface{}) (interface{}, error) {
	time.Sleep(time.Second)
	// We are hacking this endpoint to use req as the error to return
	if req == nil {
		return struct{}{}, nil
	}
	return struct{}{}, req.(error)
}

// TestHistogram is an in-memory implementation of a Histogram. It only tracks
// an approximate moving average, so is likely too naïve for many use cases.
type TestHistogram struct {
	mtx sync.RWMutex
	lvs map[string]string
	avg float64
	n   uint64
}

// NewTestHistogram returns a TestHistogram, ready for observations.
func NewTestHistogram() *TestHistogram {
	return &TestHistogram{lvs: map[string]string{}}
}

// With implements Histogram.
func (h *TestHistogram) With(labelValues ...string) metrics.Histogram {
	if len(labelValues) == 0 {
		return h
	}
	if len(labelValues)%2 != 0 {
		labelValues = append(labelValues, "unknown")
	}
	h.lvs[labelValues[0]] = labelValues[1]
	for i := 1; i < len(labelValues)/2; i++ {
		h.lvs[labelValues[2*i-1]] = labelValues[2*i]
	}
	return h
}

// Observe implements Histogram.
func (h *TestHistogram) Observe(value float64) {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	h.n++
	h.avg -= h.avg / float64(h.n)
	h.avg += value / float64(h.n)
}

// ApproximateMovingAverage returns the approximate moving average of observations.
func (h *TestHistogram) ApproximateMovingAverage() float64 {
	h.mtx.RLock()
	defer h.mtx.RUnlock()
	return h.avg
}

// LabelValues returns the set of label values attached to the histogram.
func (h *TestHistogram) LabelValues() map[string]string {
	return h.lvs
}
