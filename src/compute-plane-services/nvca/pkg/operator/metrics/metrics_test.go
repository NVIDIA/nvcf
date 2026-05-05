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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsLifecycle(t *testing.T) {
	ctx := context.Background()
	expectedNCAID := "some-nca-id"
	expectedClusterName := "mars"
	expectedVersion := "1.0.0"
	defaultLabelValues := []string{expectedNCAID, expectedClusterName, expectedVersion}

	ctx = WithDefaultMetrics(ctx, "nvca", defaultLabelValues)
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	assert.NotNil(t, metrics)

	// Verifying some metrics are registered, not all of them
	require.NotNil(t, metrics.EventQueueLength)
	defaultLabels := metrics.WithDefaultLabelValues("some-event-kind")
	assert.Equal(t,
		append(defaultLabelValues, "some-event-kind"),
		defaultLabels)
	metrics.EventQueueLength.WithLabelValues(defaultLabels...).Set(float64(5))

	// Attempt to retrieve from a nil context
	assert.Nil(t, FromContext(nil))
	// Attempt to retrieve from an empty context
	assert.Nil(t, FromContext(context.Background()))

	// Test several cases with additionalLabelValues
	assert.Equal(t, metrics.defaultLabelValues, metrics.WithDefaultLabelValues())
	assert.Equal(t, append(metrics.defaultLabelValues, "foo", "bar"), metrics.WithDefaultLabelValues("foo", "bar"))
}
