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

package operator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaopotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
)

func TestWithDDCSIPAllowList(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()
	cidrs := []string{"10.0.0.0/8", "192.168.0.0/16"}

	result := builder.WithDDCSIPAllowList(cidrs)

	assert.NotNil(t, result)
	assert.Equal(t, cidrs, result.BackendK8sCache.ddcsIPAllowList)
}

func TestWithOTelTracer(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()
	tracer := nvcaopotel.NewTracer()

	result := builder.WithOTelTracer(tracer)

	assert.NotNil(t, result)
	assert.Equal(t, tracer, result.tracer)
}

func TestWithEnvType(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()

	tests := []struct {
		name    string
		envType nvidiaiov1.EnvType
	}{
		{
			name:    "prod environment",
			envType: nvidiaiov1.EnvTypeProd,
		},
		{
			name:    "stage environment",
			envType: nvidiaiov1.EnvTypeStage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := builder.WithEnvType(tt.envType)
			assert.NotNil(t, result)
			assert.Equal(t, tt.envType, result.BackendK8sCache.envType)
		})
	}
}

func TestWithFunctionDeploymentStagesServiceURL(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()
	url := "https://function-deployment-stages.test.com"

	result := builder.WithFunctionDeploymentStagesServiceURL(url)

	assert.NotNil(t, result)
	assert.Equal(t, url, result.BackendK8sCache.functionDeploymentStagesServiceURL)
}

func TestWithDispatchReconcileClusterFunc(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()
	called := false
	mockFunc := func(ctx context.Context) {
		called = true
	}

	result := builder.WithDispatchReconcileClusterFunc(mockFunc)

	assert.NotNil(t, result)
	assert.NotNil(t, result.BackendK8sCache.dispatchReconcileClusterFunc)

	// Test that the function was set correctly by calling it
	result.BackendK8sCache.dispatchReconcileClusterFunc(context.Background())
	assert.True(t, called)
}

func TestWithGenerateImagePullSecret(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()

	tests := []struct {
		name   string
		enable bool
	}{
		{
			name:   "enabled",
			enable: true,
		},
		{
			name:   "disabled",
			enable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := builder.WithGenerateImagePullSecret(tt.enable)
			assert.NotNil(t, result)
			assert.Equal(t, tt.enable, result.BackendK8sCache.generateImagePullSecret)
		})
	}
}

func TestBackendK8sCacheBuilder_WithAdditionalImagePullSecrets(t *testing.T) {
	tests := []struct {
		name    string
		secrets []corev1.LocalObjectReference
	}{
		{
			name:    "empty list",
			secrets: []corev1.LocalObjectReference{},
		},
		{
			name:    "single secret",
			secrets: []corev1.LocalObjectReference{{Name: "my-secret"}},
		},
		{
			name:    "multiple secrets",
			secrets: []corev1.LocalObjectReference{{Name: "secret1"}, {Name: "secret2"}, {Name: "secret3"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewBackendK8sCacheBuilder().
				WithAdditionalImagePullSecrets(tt.secrets)

			assert.Equal(t, tt.secrets, result.BackendK8sCache.additionalImagePullSecrets)
		})
	}
}

func TestWithAgentConfig_NilResources(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()

	agentConfig := nvidiaiov1.AgentConfig{
		DeploymentConfig: nvidiaiov1.DeploymentConfig{
			PriorityClassName: "high-priority",
		},
		NVCFWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
			CacheMountOptionsEnabled: true,
		},
		// AgentResources and WebhookResources are nil
	}

	result := builder.WithAgentConfig(agentConfig)

	assert.NotNil(t, result)
	assert.Equal(t, agentConfig.DeploymentConfig, result.BackendK8sCache.deploymentConfig)
	assert.Equal(t, agentConfig.NVCFWorkerConfig, result.BackendK8sCache.nvcfWorkerConfig)
}

func TestWithWorkloadTolerations(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()
	tolerations := []corev1.Toleration{{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}}

	result := builder.WithWorkloadTolerations(tolerations)

	require.NotNil(t, result)
	assert.Equal(t, tolerations, result.BackendK8sCache.workloadTolerations)
}

func TestObjectsEqual(t *testing.T) {
	tests := []struct {
		name     string
		a        any
		b        any
		expected bool
	}{
		{
			name:     "equal strings",
			a:        "test",
			b:        "test",
			expected: true,
		},
		{
			name:     "different strings",
			a:        "test1",
			b:        "test2",
			expected: false,
		},
		{
			name:     "empty and nil slices are equal (with EquateEmpty)",
			a:        []string{},
			b:        []string{},
			expected: true,
		},
		{
			name:     "equal slices",
			a:        []string{"a", "b"},
			b:        []string{"a", "b"},
			expected: true,
		},
		{
			name:     "different slices",
			a:        []string{"a", "b"},
			b:        []string{"a", "c"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := objectsEqual(tt.a, tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldNVCARollout(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		checks   []func() bool
		expected bool
	}{
		{
			name: "all checks return false",
			checks: []func() bool{
				func() bool { return false },
				func() bool { return false },
			},
			expected: false,
		},
		{
			name: "first check returns true",
			checks: []func() bool{
				func() bool { return true },
				func() bool { return false },
			},
			expected: true,
		},
		{
			name: "last check returns true",
			checks: []func() bool{
				func() bool { return false },
				func() bool { return true },
			},
			expected: true,
		},
		{
			name:     "no checks",
			checks:   []func() bool{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldNVCARollout(ctx, tt.checks...)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSyncNVCFCurrentBackend(t *testing.T) {
	ctx := context.Background()

	t.Run("no backends present", func(t *testing.T) {
		clients := mockKubeClients()
		bc, _, err := NewBackendK8sCacheBuilder().
			WithClients(clients).
			WithSystemNamespace(NVCAOperatorNamespace).
			Start(ctx)
		require.NoError(t, err)

		// Give informer time to sync
		time.Sleep(50 * time.Millisecond)

		err = bc.SyncNVCFCurrentBackend(ctx, false)
		assert.NoError(t, err)
	})
}
