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
	"fmt"
	"strings"

	"github.com/gorilla/mux"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func AddMetricsRoute(r *mux.Router,
	errorLog promhttp.Logger,
	defaultLabels []*dto.LabelPair,
	metricsPrefix string,
) {
	gatherer := &mergedGatherer{
		gatherers: []prometheus.Gatherer{
			prometheus.DefaultGatherer,
			getFilteredControllerRuntimeMetrics(ctrlmetrics.Registry, defaultLabels, metricsPrefix),
		},
	}
	r.Path("/metrics").
		Handler(promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{ErrorLog: errorLog})).
		Methods("GET")
}

type mergedGatherer struct {
	gatherers []prometheus.Gatherer
}

func (mg *mergedGatherer) Gather() ([]*dto.MetricFamily, error) {
	var metricDTOs []*dto.MetricFamily
	var errs []error

	for _, g := range mg.gatherers {
		dtos, err := g.Gather()
		if err != nil {
			errs = append(errs, err)
		} else {
			metricDTOs = append(metricDTOs, dtos...)
		}
	}

	return metricDTOs, errors.Join(errs...)
}

func getFilteredControllerRuntimeMetrics(
	metricsGatherer prometheus.Gatherer,
	defaultLabels []*dto.LabelPair,
	metricsPrefix string,
) prometheus.Gatherer {
	return gathererFunc(func() ([]*dto.MetricFamily, error) {
		var metricDTOs []*dto.MetricFamily
		ctrlMetricDTOs, err := metricsGatherer.Gather()
		if err != nil {
			return nil, err
		}
		for _, ctrlMetric := range ctrlMetricDTOs {
			if ctrlMetric.Name != nil && strings.HasPrefix(*ctrlMetric.Name, "controller_runtime_") {
				// Append the default labels to the metric
				for i := range ctrlMetric.Metric {
					ctrlMetric.Metric[i].Label = append(ctrlMetric.Metric[i].Label, defaultLabels...)
				}
				// Add NVCA prefix
				newMetricName := fmt.Sprintf("%s_%s", metricsPrefix, *ctrlMetric.Name)
				ctrlMetric.Name = &newMetricName
				metricDTOs = append(metricDTOs, ctrlMetric)
			}
		}
		return metricDTOs, nil
	})
}

type gathererFunc func() ([]*dto.MetricFamily, error)

func (f gathererFunc) Gather() ([]*dto.MetricFamily, error) {
	return f()
}
