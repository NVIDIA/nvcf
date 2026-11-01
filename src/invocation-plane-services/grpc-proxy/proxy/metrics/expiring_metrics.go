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
	"time"

	"github.com/hashicorp/go-metrics"
	hashicorpprom "github.com/hashicorp/go-metrics/prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

// ExpiringMetrics provides expiring metrics using HashiCorp's go-metrics library
type ExpiringMetrics struct {
	sink *hashicorpprom.PrometheusSink
}

func NewExpiringMetrics(expiration time.Duration) (*ExpiringMetrics, error) {
	opts := hashicorpprom.PrometheusOpts{
		Expiration: expiration,
		Registerer: prometheus.DefaultRegisterer,
	}

	sink, err := hashicorpprom.NewPrometheusSinkFrom(opts)
	if err != nil {
		return nil, err
	}

	return &ExpiringMetrics{
		sink: sink,
	}, nil
}

// IncrFunctionRequest increments the function request counter with high-cardinality labels
func (h *ExpiringMetrics) IncrFunctionRequest(functionID, functionVersionID, ncaID string) {
	labels := []metrics.Label{
		{Name: "function_id", Value: functionID},
		{Name: "function_version_id", Value: functionVersionID},
		{Name: "nca_id", Value: ncaID},
	}

	h.sink.IncrCounterWithLabels([]string{"function_request_total"}, 1, labels)
}
