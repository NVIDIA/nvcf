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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
)

var (
	WorkerInitThreadCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.WorkerInitRootNamespace,
			Name:      "thread_count_total",
			Help:      "number of threads to use for downloading artifacts",
		})

	WorkerInitArtifactCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.WorkerInitRootNamespace,
			Name:      "artifacts_count_total",
			Help:      "number of models to download",
		})

	WorkerInitArtifactCachedCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.WorkerInitRootNamespace,
			Name:      "cached_artifacts_total",
			Help:      "number of artifacts in cache",
		})

	WorkerInitArtifactBytesCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.WorkerInitRootNamespace,
			Name:      "download_bytes_total",
			Help:      "total size of download bytes",
		})

	WorkerInitArtifactDownloadFailCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metrics.WorkerInitRootNamespace,
			Name:      "artifacts_download_fail_total",
			Help:      "number of artifacts that failed download",
		})
)
