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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func TestRequestDurationHistogram(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := RequestDurationHistogram(reg, "test_ns", "my-service")
	assert.NotNil(t, h)
	h.With("method", "GET", "success", "true").Observe(1.5)
}

func TestCreateCounterVec(t *testing.T) {
	reg := prometheus.NewRegistry()
	v := CreateCounterVec(reg, "test_counter_vec", []string{"label1"}, "subsys")
	assert.NotNil(t, v)
	v.WithLabelValues("val1").Inc()
}

func TestCreateCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := CreateCounter(reg, "test_counter", "subsys")
	assert.NotNil(t, c)
	c.Inc()
}

func TestCreateHistogram(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := CreateHistogram(reg, "test_histogram", "subsys")
	assert.NotNil(t, h)
	h.Observe(1.0)
}

func TestCreateHistogramVec(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := CreateHistogramVec(reg, "test_histogram_vec", []string{"label1"}, "subsys")
	assert.NotNil(t, h)
	h.WithLabelValues("val1").Observe(1.0)
}

func TestCreateGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := CreateGauge(reg, "test_gauge", "subsys")
	assert.NotNil(t, g)
	g.Set(42.0)
}

func TestCreateGaugeVec(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := CreateGaugeVec(reg, "test_gauge_vec", []string{"label1"}, "subsys")
	assert.NotNil(t, g)
	g.WithLabelValues("val1").Set(1.0)
}

func TestCreateSummary(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := CreateSummary(reg, "test_summary", "subsys")
	assert.NotNil(t, s)
	s.Observe(1.0)
}

func TestCreateSummaryVec(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := CreateSummaryVec(reg, "test_summary_vec", []string{"label1"}, "subsys")
	assert.NotNil(t, s)
	s.WithLabelValues("val1").Observe(1.0)
}
