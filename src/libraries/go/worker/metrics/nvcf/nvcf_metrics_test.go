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

package nvcf

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
	"github.com/stretchr/testify/require"
)

func TestPackageMetricsRegistered(t *testing.T) {
	// The package-level promauto metrics must be non-nil and usable. Touching
	// them confirms the var initializers registered without panicking.
	require.NotNil(t, RequestCounter)
	require.NotNil(t, ResponseCounter)
	require.NotNil(t, ResponseBytesCounter)
	require.NotNil(t, WorkerThreadCountGauge)
	require.NotNil(t, WorkerThreadBusyTimeCounter)
	require.NotNil(t, NatsErrorCounter)
	require.NotNil(t, NatsReconnectCounter)
	require.NotNil(t, NatsLameDuckCounter)
	require.NotNil(t, NatsDisconnectCounter)
	require.NotNil(t, WorkerNatsServerGauge)
	require.NotNil(t, WorkerSubscriptionsConnectedPrimaryRegionGauge)
	require.NotNil(t, WorkerSubscriptionsConnectedSecondaryRegionsGauge)
	require.NotNil(t, HealthcheckCounter)
	require.NotNil(t, StatefulProxySuccessCounter)

	require.NotPanics(t, func() {
		RequestCounter.Inc()
		ResponseCounter.WithLabelValues("0").Inc()
		ResponseBytesCounter.Add(128)
		WorkerThreadCountGauge.Set(2)
		WorkerThreadBusyTimeCounter.Add(0.5)
		NatsErrorCounter.Inc()
		NatsReconnectCounter.Inc()
		NatsLameDuckCounter.Inc()
		NatsDisconnectCounter.Inc()
		WorkerNatsServerGauge.WithLabelValues("nats.example").Set(1)
		WorkerSubscriptionsConnectedPrimaryRegionGauge.WithLabelValues("us").Set(1)
		WorkerSubscriptionsConnectedSecondaryRegionsGauge.WithLabelValues("eu").Set(1)
		HealthcheckCounter.WithLabelValues("success").Inc()
		StatefulProxySuccessCounter.Inc()
	})
}

func TestNamespaceConstants(t *testing.T) {
	require.Equal(t, metrics.NvcfRootNamespace+"_response", ResponseNamespace)
	require.Equal(t, metrics.NvcfRootNamespace+"_worker_thread", WorkerThreadNamespace)
}
