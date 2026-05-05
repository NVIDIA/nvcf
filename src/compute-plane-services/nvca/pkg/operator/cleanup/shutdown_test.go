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

package cleanup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	fakenvcaop "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
)

// newTestDynamicClient creates a fake dynamic client with ICMSRequest list kind registered
func newTestDynamicClient() *fakedynamic.FakeDynamicClient {
	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}
	return fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			icmsGVR: "ICMSRequestList",
		})
}

func TestNewShutdownHandler_NoSentinelDeletion(t *testing.T) {
	ctx := context.Background()

	// Create sentinel ConfigMap without deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ShutdownSentinelConfigMapName,
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	nvcaClient := fakenvcaop.NewSimpleClientset()
	dynamicClient := newTestDynamicClient()

	var gracefulShutdown bool
	opts := ShutdownHandlerOptions{
		K8sClient:     k8sClient,
		NVCAClient:    nvcaClient,
		DynamicClient: dynamicClient,
		Namespace:     "test-namespace",
		PollTimeout:   100 * time.Millisecond, // Very short timeout for test
		SetGracefulShutdown: func(shutdown bool) {
			gracefulShutdown = shutdown
		},
	}

	handler := NewShutdownHandler(ctx, opts)

	// Make request
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)

	var resp ShutdownResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.False(t, resp.Cleanup, "should not trigger cleanup when sentinel is not being deleted")
	assert.Contains(t, resp.Message, "no deletion detected")
	assert.False(t, gracefulShutdown, "graceful shutdown flag should be reset after no deletion detected")
}

func TestNewShutdownHandler_SentinelNotFound(t *testing.T) {
	ctx := context.Background()

	// No sentinel ConfigMap - means it's already deleted, should trigger cleanup
	k8sClient := fake.NewSimpleClientset()
	nvcaClient := fakenvcaop.NewSimpleClientset()
	dynamicClient := newTestDynamicClient()

	var gracefulShutdown bool
	var shutdownCalled bool
	opts := ShutdownHandlerOptions{
		K8sClient:     k8sClient,
		NVCAClient:    nvcaClient,
		DynamicClient: dynamicClient,
		Namespace:     "test-namespace",
		PollTimeout:   100 * time.Millisecond,
		OnShutdown: func(ctx context.Context) {
			shutdownCalled = true
		},
		SetGracefulShutdown: func(shutdown bool) {
			gracefulShutdown = shutdown
		},
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	// Verify response - sentinel not found means we should trigger cleanup
	assert.Equal(t, http.StatusOK, w.Code)

	var resp ShutdownResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.True(t, resp.Cleanup, "should trigger cleanup when sentinel is not found")
	assert.True(t, shutdownCalled, "OnShutdown callback should be called")
	assert.True(t, gracefulShutdown, "graceful shutdown flag should remain true after cleanup")
}

func TestNewShutdownHandler_SentinelBeingDeleted(t *testing.T) {
	ctx := context.Background()

	// Create sentinel ConfigMap WITH deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	nvcaClient := fakenvcaop.NewSimpleClientset()
	dynamicClient := newTestDynamicClient()

	var shutdownCalled bool
	opts := ShutdownHandlerOptions{
		K8sClient:     k8sClient,
		NVCAClient:    nvcaClient,
		DynamicClient: dynamicClient,
		Namespace:     "test-namespace",
		PollTimeout:   100 * time.Millisecond,
		OnShutdown: func(ctx context.Context) {
			shutdownCalled = true
		},
		SetGracefulShutdown: func(shutdown bool) {},
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)

	var resp ShutdownResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.True(t, resp.Cleanup, "should trigger cleanup when sentinel has deletion timestamp")
	assert.True(t, shutdownCalled, "OnShutdown callback should be called")
}

func TestRunShutdownCleanup_RemovesManagedResources(t *testing.T) {
	ctx := context.Background()

	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}
	systemNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCASystemNamespace}}
	requestsNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCARequestsNamespace}}
	modelCacheInitNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultModelCacheInitNamespace}}
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-operator",
			Finalizers: []string{SentinelFinalizer},
		},
	}
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-operator",
			Finalizers: []string{SentinelFinalizer},
		},
	}
	backend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-backend",
			Namespace:  "test-namespace",
			Finalizers: []string{NVCAOperatorFinalizer},
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel, systemNS, requestsNS, modelCacheInitNS, clusterRole, clusterRoleBinding)
	nvcaClient := fakenvcaop.NewSimpleClientset(backend)
	dynamicClient := newTestDynamicClient()

	var shutdownCalled bool
	resp := RunShutdownCleanup(ctx, ShutdownHandlerOptions{
		K8sClient:              k8sClient,
		NVCAClient:             nvcaClient,
		DynamicClient:          dynamicClient,
		Namespace:              "test-namespace",
		ClusterRoleName:        "test-operator",
		ClusterRoleBindingName: "test-operator",
		OnShutdown: func(ctx context.Context) {
			shutdownCalled = true
		},
		SetGracefulShutdown: func(shutdown bool) {},
	})

	require.True(t, resp.Cleanup)
	require.Empty(t, resp.Error)
	assert.Equal(t, "cleanup complete", resp.Message)
	assert.True(t, shutdownCalled, "OnShutdown callback should be called")

	_, err := k8sClient.CoreV1().Namespaces().Get(ctx, DefaultNVCASystemNamespace, metav1.GetOptions{})
	assert.Error(t, err, "system namespace should be deleted")
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, DefaultNVCARequestsNamespace, metav1.GetOptions{})
	assert.Error(t, err, "requests namespace should be deleted")
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, DefaultModelCacheInitNamespace, metav1.GetOptions{})
	assert.Error(t, err, "model cache init namespace should be deleted")

	cm, err := k8sClient.CoreV1().ConfigMaps("test-namespace").Get(ctx, ShutdownSentinelConfigMapName, metav1.GetOptions{})
	if !k8serrors.IsNotFound(err) {
		require.NoError(t, err)
		assert.NotContains(t, cm.Finalizers, SentinelFinalizer)
	}

	cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, "test-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, cr.Finalizers, SentinelFinalizer)

	crb, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, "test-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, crb.Finalizers, SentinelFinalizer)
}

func TestNewShutdownHandler_DefaultTimeouts(t *testing.T) {
	ctx := context.Background()

	// Create sentinel without deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ShutdownSentinelConfigMapName,
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	nvcaClient := fakenvcaop.NewSimpleClientset()
	dynamicClient := newTestDynamicClient()

	// Test with zero timeouts to verify defaults are applied
	opts := ShutdownHandlerOptions{
		K8sClient:           k8sClient,
		NVCAClient:          nvcaClient,
		DynamicClient:       dynamicClient,
		Namespace:           "test-namespace",
		PollTimeout:         0, // Should default
		DrainTimeout:        0, // Should default
		RolloutTimeout:      0, // Should default
		SetGracefulShutdown: func(shutdown bool) {},
	}

	// This just verifies the handler can be created with zero timeouts
	handler := NewShutdownHandler(ctx, opts)
	assert.NotNil(t, handler)
}

func TestShutdownResponse_JSONEncoding(t *testing.T) {
	tests := []struct {
		name     string
		response ShutdownResponse
		expected string
	}{
		{
			name: "cleanup complete",
			response: ShutdownResponse{
				Cleanup: true,
				Message: "cleanup complete",
			},
			expected: `{"cleanup":true,"message":"cleanup complete"}`,
		},
		{
			name: "no cleanup with error",
			response: ShutdownResponse{
				Cleanup: false,
				Message: "failed to list NVCFBackends",
				Error:   "connection refused",
			},
			expected: `{"cleanup":false,"message":"failed to list NVCFBackends","error":"connection refused"}`,
		},
		{
			name: "normal restart",
			response: ShutdownResponse{
				Cleanup: false,
				Message: "no deletion detected, normal restart",
			},
			expected: `{"cleanup":false,"message":"no deletion detected, normal restart"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.response)
			require.NoError(t, err)
			assert.JSONEq(t, tt.expected, string(data))
		})
	}
}

func TestNewShutdownHandler_WithNVCFBackend(t *testing.T) {
	ctx := context.Background()

	// Create sentinel with deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	// Create NVCFBackend
	backend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-backend",
			Namespace:  "test-namespace",
			Finalizers: []string{NVCAOperatorFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	nvcaClient := fakenvcaop.NewSimpleClientset(backend)
	dynamicClient := newTestDynamicClient()

	var shutdownCalled bool
	opts := ShutdownHandlerOptions{
		K8sClient:     k8sClient,
		NVCAClient:    nvcaClient,
		DynamicClient: dynamicClient,
		Namespace:     "test-namespace",
		PollTimeout:   100 * time.Millisecond,
		OnShutdown: func(ctx context.Context) {
			shutdownCalled = true
		},
		SetGracefulShutdown: func(shutdown bool) {},
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ShutdownResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.True(t, resp.Cleanup, "should trigger cleanup")
	assert.True(t, shutdownCalled, "OnShutdown callback should be called")
}

func TestNewShutdownHandler_WithRBACCleanup(t *testing.T) {
	ctx := context.Background()

	// Create sentinel with deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	// Create RBAC resources with finalizers
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-operator",
			Finalizers: []string{SentinelFinalizer},
		},
	}
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-operator",
			Finalizers: []string{SentinelFinalizer},
		},
	}
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-operator",
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel, clusterRole, clusterRoleBinding, serviceAccount)
	nvcaClient := fakenvcaop.NewSimpleClientset()
	dynamicClient := newTestDynamicClient()

	opts := ShutdownHandlerOptions{
		K8sClient:              k8sClient,
		NVCAClient:             nvcaClient,
		DynamicClient:          dynamicClient,
		Namespace:              "test-namespace",
		PollTimeout:            100 * time.Millisecond,
		ClusterRoleName:        "test-operator",
		ClusterRoleBindingName: "test-operator",
		ServiceAccountName:     "test-operator",
		SetGracefulShutdown:    func(shutdown bool) {},
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify RBAC finalizers were removed
	cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, "test-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, cr.Finalizers, SentinelFinalizer)

	crb, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, "test-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, crb.Finalizers, SentinelFinalizer)

	sa, err := k8sClient.CoreV1().ServiceAccounts("test-namespace").Get(ctx, "test-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, sa.Finalizers, SentinelFinalizer)
}

func TestNewShutdownHandler_NilCallbacks(t *testing.T) {
	ctx := context.Background()

	// Create sentinel with deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	nvcaClient := fakenvcaop.NewSimpleClientset()
	dynamicClient := newTestDynamicClient()

	// Test with nil callbacks - should not panic
	opts := ShutdownHandlerOptions{
		K8sClient:           k8sClient,
		NVCAClient:          nvcaClient,
		DynamicClient:       dynamicClient,
		Namespace:           "test-namespace",
		PollTimeout:         100 * time.Millisecond,
		OnShutdown:          nil, // nil
		SetGracefulShutdown: nil, // nil
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		handler(w, req)
	})

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestNewShutdownHandler_WithV1ICMSRequests(t *testing.T) {
	ctx := context.Background()

	// Create sentinel with deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	// Create NVCFBackend
	backend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-backend",
			Namespace:  "test-namespace",
			Finalizers: []string{NVCAOperatorFinalizer},
		},
	}

	// Create agent-config ConfigMap for maintenance mode patching
	agentConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-config",
			Namespace: DefaultNVCASystemNamespace,
		},
		Data: map[string]string{
			"config.yaml": `agent:
  featureFlags: []`,
		},
	}

	// Create NVCA deployment for rollout
	replicas := int32(1)
	nvcaDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      NVCAModuleName,
			Namespace: DefaultNVCASystemNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{},
		},
		Status: appsv1.DeploymentStatus{
			UpdatedReplicas:     1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel, agentConfig, nvcaDeployment)
	nvcaClient := fakenvcaop.NewSimpleClientset(backend)

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}
	icmsRequest := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
			"kind":       "ICMSRequest",
			"metadata": map[string]interface{}{
				"name":      "sr-1",
				"namespace": DefaultNVCARequestsNamespace,
			},
		},
	}
	dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			icmsGVR: "ICMSRequestList",
		}, icmsRequest)

	opts := ShutdownHandlerOptions{
		K8sClient:           k8sClient,
		NVCAClient:          nvcaClient,
		DynamicClient:       dynamicClient,
		Namespace:           "test-namespace",
		PollTimeout:         100 * time.Millisecond,
		DrainTimeout:        100 * time.Millisecond, // Very short for test
		RolloutTimeout:      100 * time.Millisecond,
		SetGracefulShutdown: func(shutdown bool) {},
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ShutdownResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.True(t, resp.Cleanup, "should trigger cleanup even with ICMS requests present")
}

func TestNewShutdownHandler_MultipleBackends(t *testing.T) {
	ctx := context.Background()

	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	backend1 := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "backend-1",
			Namespace:  "test-namespace",
			Finalizers: []string{NVCAOperatorFinalizer},
		},
	}
	backend2 := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "backend-2",
			Namespace:  "test-namespace",
			Finalizers: []string{NVCAOperatorFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	nvcaClient := fakenvcaop.NewSimpleClientset(backend1, backend2)
	dynamicClient := newTestDynamicClient()

	opts := ShutdownHandlerOptions{
		K8sClient:           k8sClient,
		NVCAClient:          nvcaClient,
		DynamicClient:       dynamicClient,
		Namespace:           "test-namespace",
		PollTimeout:         100 * time.Millisecond,
		SetGracefulShutdown: func(shutdown bool) {},
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ShutdownResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Should still complete cleanup (processing only the first backend)
	assert.True(t, resp.Cleanup, "should complete cleanup even with multiple backends")
}

func TestNewShutdownHandler_NoBackendsToCleanup(t *testing.T) {
	ctx := context.Background()

	// Create sentinel with deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	nvcaClient := fakenvcaop.NewSimpleClientset() // No backends
	dynamicClient := newTestDynamicClient()

	opts := ShutdownHandlerOptions{
		K8sClient:           k8sClient,
		NVCAClient:          nvcaClient,
		DynamicClient:       dynamicClient,
		Namespace:           "test-namespace",
		PollTimeout:         100 * time.Millisecond,
		SetGracefulShutdown: func(shutdown bool) {},
	}

	handler := NewShutdownHandler(ctx, opts)

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ShutdownResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.True(t, resp.Cleanup, "should complete cleanup when no backends exist")
}
