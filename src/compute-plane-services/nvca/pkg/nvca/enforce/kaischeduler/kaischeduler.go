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

package kaischeduler

import (
	"context"
	"fmt"

	kaischedulingv2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// ComponentName for health checks.
	ComponentName = "kai-scheduler-queues"

	SchedulerName = "kai-scheduler"

	SchedulerQueueLabel = "kai.scheduler/queue"

	DefaultQueue = "default-queue"
)

// NewRunAIQueueHealthCheck creates a health check that validates Run.ai queue configuration.
func NewRunAIQueueHealthCheck(k8sClient client.Client) health.ComponentStatusGetter {
	return health.GetComponentStatusFunc(func(ctx context.Context) (hs nvcatypes.AgentHealth, err error) {
		ch := nvcatypes.ComponentHealth{
			Status:      nvcatypes.HealthStatusHealthy,
			StatusLevel: nvcatypes.StatusLevelWarn,
		}
		hs.Components = map[string]nvcatypes.ComponentHealth{}

		queueList := &kaischedulingv2.QueueList{}
		if err := k8sClient.List(ctx, queueList); err != nil {
			ch.Status = nvcatypes.HealthStatusUnhealthy
			ch.StatusLevel = nvcatypes.StatusLevelError

			if meta.IsNoMatchError(err) {
				ch.Errors = append(ch.Errors,
					"KAI Scheduler is not installed but the KAIScheduler feature flag is enabled. "+
						"Either install KAI Scheduler (see https://github.com/NVIDIA/KAI-Scheduler) "+
						"or disable the KAIScheduler feature flag")
			} else {
				ch.Errors = append(ch.Errors, fmt.Sprintf("Failed to list KAI Scheduler queues: %v", err))
			}
			hs.Components[ComponentName] = ch
			return hs, nil
		}

		const (
			defaultParentQueue = "default-parent-queue"
			notFoundMessage    = "Expected the two default queues " + defaultParentQueue + " and " + DefaultQueue +
				", but either one or both were not found. " +
				"See https://raw.githubusercontent.com/NVIDIA/KAI-Scheduler/refs/heads/main/docs/quickstart/default-queues.yaml " +
				"for setting up right hierarchy"
			incorrectQMessage = "Queue hierarchy misconfigured: '" + defaultParentQueue + "' must be a parent queue " +
				"and '" + DefaultQueue + "' must be a child of '" + defaultParentQueue + "'. " +
				"See https://raw.githubusercontent.com/NVIDIA/KAI-Scheduler/refs/heads/main/docs/quickstart/default-queues.yaml " +
				"for setting up the correct hierarchy"
		)
		var expectedQueues []kaischedulingv2.Queue
		for _, queue := range queueList.Items {
			switch queue.Name {
			case defaultParentQueue, DefaultQueue:
				expectedQueues = append(expectedQueues, queue)
			}
		}

		if len(expectedQueues) != 2 {
			ch.Status = nvcatypes.HealthStatusUnhealthy
			ch.Errors = append(ch.Errors, notFoundMessage)
			ch.StatusLevel = nvcatypes.StatusLevelError
			hs.Components[ComponentName] = ch
			return hs, nil
		}

		var singleChildQExists bool
		for _, queue := range expectedQueues {
			if queue.Spec.ParentQueue != "" {
				if queue.Name != DefaultQueue {
					singleChildQExists = false
					break
				}
				singleChildQExists = true
			}

			if queue.Spec.Resources.CPU.Limit != -1 ||
				queue.Spec.Resources.CPU.Quota != -1 || queue.Spec.Resources.CPU.OverQuotaWeight != 1 {
				ch.Status = nvcatypes.HealthStatusUnhealthy
				ch.Errors = append(ch.Errors, fmt.Sprintf(
					"CPU resource violation for queue %s: expected limit=-1, quota=-1, overQuotaWeight=1, got limit=%v, quota=%v, overQuotaWeight=%v",
					queue.Name, queue.Spec.Resources.CPU.Limit, queue.Spec.Resources.CPU.Quota, queue.Spec.Resources.CPU.OverQuotaWeight))
				ch.StatusLevel = nvcatypes.StatusLevelError
				hs.Components[ComponentName] = ch
				return hs, nil
			}

			if queue.Spec.Resources.GPU.Limit != -1 ||
				queue.Spec.Resources.GPU.Quota != -1 || queue.Spec.Resources.GPU.OverQuotaWeight != 1 {
				ch.Status = nvcatypes.HealthStatusUnhealthy
				ch.Errors = append(ch.Errors, fmt.Sprintf(
					"GPU resource violation for queue %s: expected limit=-1, quota=-1, overQuotaWeight=1, got limit=%v, quota=%v, overQuotaWeight=%v",
					queue.Name, queue.Spec.Resources.GPU.Limit, queue.Spec.Resources.GPU.Quota, queue.Spec.Resources.GPU.OverQuotaWeight))
				ch.StatusLevel = nvcatypes.StatusLevelError
				hs.Components[ComponentName] = ch
				return hs, nil
			}

			if queue.Spec.Resources.Memory.Limit != -1 ||
				queue.Spec.Resources.Memory.Quota != -1 || queue.Spec.Resources.Memory.OverQuotaWeight != 1 {
				ch.Status = nvcatypes.HealthStatusUnhealthy
				ch.Errors = append(ch.Errors, fmt.Sprintf(
					"Memory resource violation for queue %s: expected limit=-1, quota=-1, overQuotaWeight=1, got limit=%v, quota=%v, overQuotaWeight=%v",
					queue.Name, queue.Spec.Resources.Memory.Limit, queue.Spec.Resources.Memory.Quota, queue.Spec.Resources.Memory.OverQuotaWeight))
				ch.StatusLevel = nvcatypes.StatusLevelError
				hs.Components[ComponentName] = ch
				return hs, nil
			}
		}

		if !singleChildQExists {
			ch.Status = nvcatypes.HealthStatusUnhealthy
			ch.Errors = append(ch.Errors, incorrectQMessage)
			ch.StatusLevel = nvcatypes.StatusLevelError
			hs.Components[ComponentName] = ch
			return hs, nil
		}

		hs.Components[ComponentName] = ch
		return hs, nil
	})
}
