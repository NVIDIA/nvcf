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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaischedulingv2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestNewRunAIQueueHealthCheck(t *testing.T) {
	tests := []struct {
		name           string
		queues         []kaischedulingv2.Queue
		expectedStatus nvcatypes.HealthStatus
		expectedErrors []string
	}{
		{
			name: "healthy default queue hierarchy",
			queues: []kaischedulingv2.Queue{
				validQueue("default-parent-queue", ""),
				validQueue("default-queue", "default-parent-queue"),
			},
			expectedStatus: nvcatypes.HealthStatusHealthy,
			expectedErrors: nil,
		},
		{
			name: "healthy with extra queues",
			queues: []kaischedulingv2.Queue{
				validQueue("default-parent-queue", ""),
				validQueue("default-queue", "default-parent-queue"),
				validQueue("extra-queue", "extra-parent-queue"),
				validQueue("extra-parent-queue", ""),
			},
			expectedStatus: nvcatypes.HealthStatusHealthy,
			expectedErrors: nil,
		},
		{
			name: "unhealthy - missing default queues",
			queues: []kaischedulingv2.Queue{
				validQueue("some-other-queue", ""),
			},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"Expected the two default queues"},
		},
		{
			name:           "unhealthy - no queues at all",
			queues:         []kaischedulingv2.Queue{},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"Expected the two default queues"},
		},
		{
			name: "unhealthy - only default-parent-queue present",
			queues: []kaischedulingv2.Queue{
				validQueue("default-parent-queue", ""),
			},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"Expected the two default queues"},
		},
		{
			name: "unhealthy - CPU resource violation",
			queues: []kaischedulingv2.Queue{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "default-parent-queue"},
					Spec: kaischedulingv2.QueueSpec{
						ParentQueue: "",
						Resources: &kaischedulingv2.QueueResources{
							CPU:    kaischedulingv2.QueueResource{Limit: 100, Quota: -1, OverQuotaWeight: 1},
							GPU:    kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
							Memory: kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
						},
					},
				},
				validQueue("default-queue", "default-parent-queue"),
			},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"CPU resource violation for queue "},
		},
		{
			name: "unhealthy - GPU resource violation",
			queues: []kaischedulingv2.Queue{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "default-parent-queue"},
					Spec: kaischedulingv2.QueueSpec{
						ParentQueue: "",
						Resources: &kaischedulingv2.QueueResources{
							CPU:    kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
							GPU:    kaischedulingv2.QueueResource{Limit: 10, Quota: -1, OverQuotaWeight: 1},
							Memory: kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
						},
					},
				},
				validQueue("default-queue", "default-parent-queue"),
			},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"GPU resource violation for queue "},
		},
		{
			name: "unhealthy - Memory resource violation",
			queues: []kaischedulingv2.Queue{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "default-parent-queue"},
					Spec: kaischedulingv2.QueueSpec{
						ParentQueue: "",
						Resources: &kaischedulingv2.QueueResources{
							CPU:    kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
							GPU:    kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
							Memory: kaischedulingv2.QueueResource{Limit: 1024, Quota: -1, OverQuotaWeight: 1},
						},
					},
				},
				validQueue("default-queue", "default-parent-queue"),
			},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"Memory resource violation for queue "},
		},
		{
			name: "unhealthy - no leaf queue (both are root queues)",
			queues: []kaischedulingv2.Queue{
				validQueue("default-parent-queue", ""),
				validQueue("default-queue", ""),
			},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"Queue hierarchy misconfigured"},
		},
		{
			name: "unhealthy - both queues are child queues",
			queues: []kaischedulingv2.Queue{
				validQueue("default-parent-queue", "grandparent-queue"),
				validQueue("default-queue", "default-parent-queue"),
			},
			expectedStatus: nvcatypes.HealthStatusUnhealthy,
			expectedErrors: []string{"Queue hierarchy misconfigured"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := kaischedulingv2.AddToScheme(scheme)
			require.NoError(t, err)

			// fake client for testing queues
			queueList := &kaischedulingv2.QueueList{
				Items: tt.queues,
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithLists(queueList).
				Build()
			healthCheck := NewRunAIQueueHealthCheck(k8sClient)
			ctx := context.Background()
			health, err := healthCheck.GetComponentStatus(ctx)
			require.NoError(t, err)

			require.Contains(t, health.Components, ComponentName)
			component := health.Components[ComponentName]

			assert.Equal(t, tt.expectedStatus, component.Status, "status mismatch")

			if tt.expectedErrors != nil {
				require.NotEmpty(t, component.Errors)
				for _, expectedErr := range tt.expectedErrors {
					found := false
					for _, actualErr := range component.Errors {
						if len(expectedErr) > 0 && len(actualErr) >= len(expectedErr) {
							if actualErr[:len(expectedErr)] == expectedErr {
								found = true
								break
							}
						}
					}
					assert.True(t, found, "expected error '%s' not found in %v", expectedErr, component.Errors)
				}
			}

			if tt.expectedStatus == nvcatypes.HealthStatusHealthy {
				assert.Equal(t, nvcatypes.StatusLevelWarn, component.StatusLevel)
			} else {
				assert.Equal(t, nvcatypes.StatusLevelError, component.StatusLevel)
			}
		})
	}
}

func TestNewRunAIQueueHealthCheck_ListError(t *testing.T) {
	scheme := runtime.NewScheme()
	err := kaischedulingv2.AddToScheme(scheme)
	require.NoError(t, err)

	k8sClient := &fakeFailingClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}
	healthCheck := NewRunAIQueueHealthCheck(k8sClient)
	ctx := context.Background()
	health, err := healthCheck.GetComponentStatus(ctx)
	require.NoError(t, err)
	require.Contains(t, health.Components, ComponentName)
	component := health.Components[ComponentName]

	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, component.Status)
	assert.Equal(t, nvcatypes.StatusLevelError, component.StatusLevel)
	assert.NotEmpty(t, component.Errors)
}

// fakeFailingClient wraps a fake client and makes List fail
type fakeFailingClient struct {
	client.Client
}

func (f *fakeFailingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return assert.AnError
}

// fakeCRDNotInstalledClient wraps a fake client and returns NoKindMatchError to simulate missing CRD
type fakeCRDNotInstalledClient struct {
	client.Client
}

func (f *fakeCRDNotInstalledClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return &meta.NoKindMatchError{
		GroupKind: schema.GroupKind{
			Group: "scheduling.run.ai",
			Kind:  "Queue",
		},
		SearchedVersions: []string{"v2"},
	}
}

// validQueue builds a Queue with all resources set to the expected healthy defaults.
func validQueue(name, parentQueue string) kaischedulingv2.Queue {
	return kaischedulingv2.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaischedulingv2.QueueSpec{
			ParentQueue: parentQueue,
			Resources: &kaischedulingv2.QueueResources{
				CPU:    kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
				GPU:    kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
				Memory: kaischedulingv2.QueueResource{Limit: -1, Quota: -1, OverQuotaWeight: 1},
			},
		},
	}
}

func TestNewRunAIQueueHealthCheck_CRDNotInstalled(t *testing.T) {
	scheme := runtime.NewScheme()
	err := kaischedulingv2.AddToScheme(scheme)
	require.NoError(t, err)

	k8sClient := &fakeCRDNotInstalledClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}
	healthCheck := NewRunAIQueueHealthCheck(k8sClient)
	ctx := context.Background()
	health, err := healthCheck.GetComponentStatus(ctx)
	require.NoError(t, err)
	require.Contains(t, health.Components, ComponentName)
	component := health.Components[ComponentName]

	// Verify status is unhealthy
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, component.Status)
	assert.Equal(t, nvcatypes.StatusLevelError, component.StatusLevel)
	require.NotEmpty(t, component.Errors)

	// Verify remediation message is present
	errorMsg := component.Errors[0]
	assert.Contains(t, errorMsg, "KAI Scheduler is not installed")
	assert.Contains(t, errorMsg, "KAIScheduler feature flag is enabled")
	assert.Contains(t, errorMsg, "https://github.com/NVIDIA/KAI-Scheduler")
	assert.Contains(t, errorMsg, "disable the KAIScheduler feature flag")
}
