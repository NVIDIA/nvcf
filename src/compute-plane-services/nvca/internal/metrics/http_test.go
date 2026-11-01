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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddMetricsRoute(t *testing.T) {
	r := mux.NewRouter()
	AddMetricsRoute(r, nil, nil, "test")

	s := httptest.NewServer(r)
	t.Cleanup(func() { s.Close() })

	c := &http.Client{
		Timeout: 1 * time.Second,
	}

	resp, err := c.Get(s.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// parse the returned metrics
	parser := expfmt.NewTextParser(model.UTF8Validation)
	metricsFamilies, err := parser.TextToMetricFamilies(resp.Body)
	assert.NoError(t, err)
	assert.NotEmpty(t, metricsFamilies)

	// log for debugging in case the test fails
	t.Logf("metricsFamilies=%+v", metricsFamilies)
	assert.NotEmpty(t, metricsFamilies)
}

func Test_mergedGatherer_Gather(t *testing.T) {
	// 1. test with happy path
	gatherer := &mergedGatherer{
		gatherers: []prometheus.Gatherer{
			gathererFunc(func() ([]*dto.MetricFamily, error) {
				return []*dto.MetricFamily{
					{Name: &[]string{"some-metric"}[0]},
				}, nil
			}),
			gathererFunc(func() ([]*dto.MetricFamily, error) {
				return []*dto.MetricFamily{
					{Name: &[]string{"some-other-metric"}[0]},
				}, nil
			}),
		},
	}

	result, err := gatherer.Gather()
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, "some-metric", *result[0].Name)
	assert.Equal(t, "some-other-metric", *result[1].Name)

	// 2. test with failure of one gatherer
	gatherer = &mergedGatherer{
		gatherers: []prometheus.Gatherer{
			gathererFunc(func() ([]*dto.MetricFamily, error) {
				return []*dto.MetricFamily{
					{Name: &[]string{"some-metric"}[0]},
				}, nil
			}),
			gathererFunc(func() ([]*dto.MetricFamily, error) {
				return []*dto.MetricFamily{
					{Name: &[]string{"some-other-metric"}[0]},
				}, nil
			}),
			gathererFunc(func() ([]*dto.MetricFamily, error) {
				return nil, errors.New("failed to get metrics")
			}),
		},
	}
	_, err = gatherer.Gather()
	assert.Error(t, err)
}

func Test_getFilteredControllerRuntimeMetrics(t *testing.T) {
	// test happy path
	gatherer := getFilteredControllerRuntimeMetrics(gathererFunc(func() ([]*dto.MetricFamily, error) {
		return []*dto.MetricFamily{
			{Name: &[]string{"controller_runtime_some_other_value"}[0], Metric: []*dto.Metric{{}}},
			{Name: &[]string{"some-metric"}[0]},
			{Name: &[]string{"controller_runtime_some_value"}[0], Metric: []*dto.Metric{{}}},
			{Name: &[]string{"controller_runtime_some_value_1_1"}[0], Metric: []*dto.Metric{{}}},
		}, nil
	}), []*dto.LabelPair{{
		Name:  &[]string{"foo"}[0],
		Value: &[]string{"bar"}[0],
	}}, "nvcatest")
	result, err := gatherer.Gather()
	require.NoError(t, err)
	require.Len(t, result, 3)
	require.Len(t, result[0].Metric[0].Label, 1)
	assert.Equal(t, "foo", *result[0].Metric[0].Label[0].Name)
	assert.Equal(t, "bar", *result[0].Metric[0].Label[0].Value)
	assert.Equal(t, "nvcatest_controller_runtime_some_other_value", *result[0].Name)

	// test failure
	gatherer = getFilteredControllerRuntimeMetrics(gathererFunc(func() ([]*dto.MetricFamily, error) {
		return nil, errors.New("some error")
	}), []*dto.LabelPair{{
		Name:  &[]string{"foo"}[0],
		Value: &[]string{"bar"}[0],
	}}, "nvcatest")
	result, err = gatherer.Gather()
	require.Error(t, err)
	assert.Nil(t, result)
}
