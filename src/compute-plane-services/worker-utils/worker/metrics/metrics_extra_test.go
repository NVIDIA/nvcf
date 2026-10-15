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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/progress"
)

func TestMetricsRegistered(t *testing.T) {
	// promauto-registered collectors must be non-nil after package init.
	assert.NotNil(t, AssetDownloadCounter)
	assert.NotNil(t, AssetDownloadFailureCounter)
	assert.NotNil(t, AssetBytesCounter)
	assert.NotNil(t, AssetDownloadTimeCounter)
	assert.NotNil(t, LargeResponseCounter)
	assert.NotNil(t, LargeResponseFailureCounter)
	assert.NotNil(t, LargeResponseBytesCounter)
	assert.NotNil(t, LargeResponseTimeCounter)
	assert.NotNil(t, InferenceRequestTimeCounter)
	assert.NotNil(t, PreInferenceTimeCounter)
	assert.NotNil(t, PostInferenceTimeCounter)
	assert.NotNil(t, InferenceRequestLatencyHistogram)

	// Smoke: counters can be observed without panicking.
	assert.NotPanics(t, func() {
		AssetDownloadCounter.Inc()
		AssetBytesCounter.Add(1024)
		InferenceRequestLatencyHistogram.Observe(0.5)
	})
}

func TestNamespaceConstants(t *testing.T) {
	assert.Contains(t, AssetsNamespace, "_assets")
	assert.Contains(t, InferenceNamespace, "_inference")
	assert.Contains(t, LargeResponseNamespace, "_large_response")
	assert.Contains(t, ProgressMonitorNamespace, "_progress_monitor")
}

func TestInitProgressMonitorMetrics(t *testing.T) {
	monitor := progress.New(t.TempDir())
	// Registers a GaugeFunc exactly once; a second call must be a safe no-op
	// (sync.Once) and must not panic with a duplicate-registration error.
	assert.NotPanics(t, func() {
		InitProgressMonitorMetrics(monitor)
		InitProgressMonitorMetrics(monitor)
	})
}
