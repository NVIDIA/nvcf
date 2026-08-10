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
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/progress"
)

const (
	AssetsNamespace          = metrics.NvcfRootNamespace + "_assets"
	InferenceNamespace       = metrics.NvcfRootNamespace + "_inference"
	LargeResponseNamespace   = metrics.NvcfRootNamespace + "_large_response"
	ProgressMonitorNamespace = metrics.NvcfRootNamespace + "_progress_monitor"
)

var (
	AssetDownloadCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: AssetsNamespace,
			Name:      "downloads_total",
			Help:      "total assets downloaded",
		})

	AssetDownloadFailureCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: AssetsNamespace,
			Name:      "download_failures_total",
			Help:      "total asset download failures",
		})

	AssetBytesCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: AssetsNamespace,
			Name:      "bytes_total",
			Help:      "total size of all assets downloaded",
		})

	AssetDownloadTimeCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: AssetsNamespace,
			Name:      "download_time_seconds_total",
			Help:      "total time downloading assets",
		})

	LargeResponseCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: LargeResponseNamespace,
			Name:      "uploads_total",
			Help:      "total large response uploads",
		})

	LargeResponseFailureCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: LargeResponseNamespace,
			Name:      "failure_total",
			Help:      "total large response failures",
		})

	LargeResponseBytesCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: LargeResponseNamespace,
			Name:      "bytes_total",
			Help:      "total size of all uploads",
		})

	LargeResponseTimeCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: LargeResponseNamespace,
			Name:      "upload_time_seconds_total",
			Help:      "total time uploading responses",
		})

	InferenceRequestTimeCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: InferenceNamespace,
			Name:      "request_time_seconds_total",
			Help:      "total inference request time",
		})

	PreInferenceTimeCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NvcfRootNamespace,
			Name:      "pre_inference_seconds_total",
			Help:      "total seconds spent on request before making inference request",
		})

	PostInferenceTimeCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.NvcfRootNamespace,
			Name:      "post_inference_seconds_total",
			Help:      "total seconds spent on request after making inference request",
		})

	InferenceRequestLatencyHistogram = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metrics.NvcfRootNamespace,
			Name:      "request_latency_seconds",
			Help:      "histogram of request latency in seconds from start to completion",
			Buckets:   []float64{.1, .25, 1, 5, 10, 30, 100, 500, 1000, 5000},
		})
)

var initProgressMonitorOnce sync.Once

func InitProgressMonitorMetrics(progressMonitor *progress.Monitor) {
	initProgressMonitorOnce.Do(func() {
		promauto.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: ProgressMonitorNamespace,
				Name:      "watches",
				Help:      "current count of inotify watches",
			}, progressMonitor.WatchGaugeCallback)
	})
}
