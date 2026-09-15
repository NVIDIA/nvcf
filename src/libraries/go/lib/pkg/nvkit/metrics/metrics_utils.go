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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	metricsNamespace = "nvkit"
)

// CreateCounterVec creates a CounterVec for tracking multiple counters with distinct label sets.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateCounterVec(registerer prometheus.Registerer, metricName string, labelNames []string, metricsSubsystem string) *prometheus.CounterVec {
	return promauto.With(registerer).NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName},
		labelNames)
}

// CreateCounter creates a single Counter for tracking a cumulative metric.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateCounter(registerer prometheus.Registerer, metricName string, metricsSubsystem string) prometheus.Counter {
	return promauto.With(registerer).NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName})
}

// CreateHistogram creates a Histogram for sampling observations over specified buckets.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateHistogram(registerer prometheus.Registerer, metricName string, metricsSubsystem string) prometheus.Histogram {
	return promauto.With(registerer).NewHistogram(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName,
		Buckets:   []float64{1, 2, 5, 10, 20, 60},
	})
}

// CreateHistogramVec creates a HistogramVec for tracking multiple histograms with distinct label sets.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateHistogramVec(registerer prometheus.Registerer, metricName string, labelNames []string, metricsSubsystem string) *prometheus.HistogramVec {
	return promauto.With(registerer).NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName,
		Buckets:   []float64{1, 2, 5, 10, 20, 60},
	}, labelNames)
}

// CreateGauge creates a Gauge for representing a single numerical value that can go up and down.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateGauge(registerer prometheus.Registerer, metricName string, metricsSubsystem string) prometheus.Gauge {
	return promauto.With(registerer).NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName,
	})
}

// CreateGaugeVec creates a GaugeVec for tracking multiple gauges with distinct label sets.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateGaugeVec(registerer prometheus.Registerer, metricName string, labelNames []string, metricsSubsystem string) *prometheus.GaugeVec {
	return promauto.With(registerer).NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName,
	}, labelNames)
}

// CreateSummary creates a Summary for tracking the size and number of events.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateSummary(registerer prometheus.Registerer, metricName string, metricsSubsystem string) prometheus.Summary {
	return promauto.With(registerer).NewSummary(prometheus.SummaryOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName,
	})
}

// CreateSummaryVec creates a SummaryVec for tracking multiple summaries with distinct label sets.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func CreateSummaryVec(registerer prometheus.Registerer, metricName string, labelNames []string, metricsSubsystem string) *prometheus.SummaryVec {
	return promauto.With(registerer).NewSummaryVec(prometheus.SummaryOpts{
		Namespace: metricsNamespace,
		Subsystem: metricsSubsystem,
		Name:      metricName,
	}, labelNames)
}
