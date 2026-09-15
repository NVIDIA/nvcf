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

package nvca

import (
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func TestGetDefaultWorkloadContainerNamesToWatch(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{
			name: "default",
			want: []string{
				"init",
				"ess",
				"ess-init",
				common.UtilsContainerName,
				nvcastorage.SMBServerContainerName,
				function.LLMWorkerContainerName,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetDefaultWorkloadContainerNamesToWatch()
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestReportPodCrashAndRestartMetrics(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = nvcametrics.WithDefaultMetrics(ctx,
		"my-nca-id", "my-cluster", "my-cluster", "1.0.0",
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithRegisterer(reg))
	metrics := nvcametrics.FromContext(ctx)

	oldContainerStatuses := []corev1.ContainerStatus{
		{
			Name:         common.UtilsContainerName,
			RestartCount: 1,
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 0,
				},
			},
		},
	}

	newContainerStatuses := []corev1.ContainerStatus{
		{
			Name:         common.UtilsContainerName,
			RestartCount: 3,
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1,
				},
			},
		},
	}

	podNamespacedName := "namespace/pod"

	t.Run("Test container restart and crash", func(t *testing.T) {
		metrics.ContainerCrashTotal.Reset()
		metrics.ContainerRestartTotal.Reset()
		reportPodCrashAndRestartMetrics(ctx, oldContainerStatuses, newContainerStatuses, podNamespacedName)
		assert.Equal(t, 1, promtestutil.CollectAndCount(metrics.ContainerCrashTotal))
		assert.Equal(t, 1, promtestutil.CollectAndCount(metrics.ContainerRestartTotal))
	})

	t.Run("Test no restart or crash", func(t *testing.T) {
		metrics.ContainerCrashTotal.Reset()
		metrics.ContainerRestartTotal.Reset()
		newContainerStatuses[0].RestartCount = 1
		newContainerStatuses[0].LastTerminationState.Terminated.ExitCode = 0
		reportPodCrashAndRestartMetrics(ctx, oldContainerStatuses, newContainerStatuses, podNamespacedName)
		assert.Equal(t, 0, promtestutil.CollectAndCount(metrics.ContainerCrashTotal))
		assert.Equal(t, 0, promtestutil.CollectAndCount(metrics.ContainerRestartTotal))
	})
}
