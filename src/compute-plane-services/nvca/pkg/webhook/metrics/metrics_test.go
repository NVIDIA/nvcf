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

package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsLifecycle(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetrics(WithRegisterer(reg))
	t.Cleanup(m.destroy)

	require.NotNil(t, m.RequestLatency)
	require.NotNil(t, m.RequestTotal)
	require.NotNil(t, m.RequestInFlight)
}

func TestInstrumentedHook(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetrics(WithRegisterer(reg))
	t.Cleanup(m.destroy)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Return a 400 status code to check new code label value.
		w.WriteHeader(http.StatusBadRequest)
	})

	handler := m.InstrumentedHook("/test-webhook", inner)
	require.NotNil(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/test-webhook", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	families, err := reg.Gather()
	require.NoError(t, err)

	getLabelStrings := func(metric *io_prometheus_client.Metric) []string {
		labels := make([]string, 0, len(metric.Label))
		for _, label := range metric.Label {
			labels = append(labels, *label.Name, *label.Value)
		}
		return labels
	}

	var foundLatency, foundTotal, foundInFlight bool
	for _, mf := range families {
		switch *mf.Name {
		case requestLatencyMetricName:
			foundLatency = true
			if assert.Equal(t, 1, len(mf.Metric)) {
				assert.Equal(t, getLabelStrings(mf.Metric[0]), []string{webhookLabel, "/test-webhook"})
			}
		case requestTotalMetricName:
			foundTotal = true
			if assert.Equal(t, 3, len(mf.Metric)) {
				assert.Equal(t, getLabelStrings(mf.Metric[0]), []string{
					httpCodeLabel, "200",
					webhookLabel, "/test-webhook",
				})
				assert.Equal(t, 0.0, *mf.Metric[0].Counter.Value)

				assert.Equal(t, getLabelStrings(mf.Metric[1]), []string{
					httpCodeLabel, "400",
					webhookLabel, "/test-webhook",
				})
				assert.Equal(t, 1.0, *mf.Metric[1].Counter.Value)

				assert.Equal(t, getLabelStrings(mf.Metric[2]), []string{
					httpCodeLabel, "500",
					webhookLabel, "/test-webhook",
				})
				assert.Equal(t, 0.0, *mf.Metric[2].Counter.Value)
			}
		case requestInFlightMetricName:
			foundInFlight = true
			if assert.Equal(t, 1, len(mf.Metric)) {
				assert.Equal(t, getLabelStrings(mf.Metric[0]), []string{webhookLabel, "/test-webhook"})
			}
		}
	}
	assert.True(t, foundLatency, "latency metric should be gathered")
	assert.True(t, foundTotal, "request total metric should be gathered")
	assert.True(t, foundInFlight, "in-flight metric should be gathered")
}

func TestInstrumentedHook_500(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetrics(WithRegisterer(reg))
	t.Cleanup(m.destroy)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	handler := m.InstrumentedHook("/error-webhook", inner)
	req := httptest.NewRequest(http.MethodPost, "/error-webhook", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
