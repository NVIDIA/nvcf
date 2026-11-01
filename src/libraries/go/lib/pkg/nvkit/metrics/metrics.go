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
	"strings"

	gokitmetrics "github.com/go-kit/kit/metrics"
	kitprometheus "github.com/go-kit/kit/metrics/prometheus"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RequestDurationHistogram returns a standard request duration histogram.
// Pass prometheus.DefaultRegisterer for production use; inject a fresh prometheus.NewRegistry() in tests.
func RequestDurationHistogram(registerer stdprometheus.Registerer, namespace string, serviceName string) gokitmetrics.Histogram {
	serviceName = strings.ReplaceAll(serviceName, "-", "_")
	hvec := promauto.With(registerer).NewHistogramVec(stdprometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: serviceName,
		Name:      "request_duration_seconds",
		Help:      "Request duration in seconds.",
		Buckets:   stdprometheus.DefBuckets,
	}, []string{"method", "success"})
	return kitprometheus.NewHistogram(hvec)
}
