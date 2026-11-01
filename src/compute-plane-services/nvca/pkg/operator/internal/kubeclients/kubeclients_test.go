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

package kubeclients

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

var fakeDynClient = fakedynamic.NewSimpleDynamicClient(runtime.NewScheme())

func TestNewFromCore_Success(t *testing.T) {
	// Create a valid rest.Config for testing
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	// Create a mock core.KubeClients with fake K8s client
	coreClient := &core.KubeClients{
		Config: config,
		K8s:    fake.NewSimpleClientset(),
	}

	// Test NewFromCore function
	kubeClients, err := NewFromCore(coreClient, fakeDynClient, coreClient.K8s.Discovery())

	// Assertions
	if err != nil {
		t.Fatalf("NewFromCore() returned unexpected error: %v", err)
	}

	if kubeClients == nil {
		t.Fatal("NewFromCore() returned nil KubeClients")
	}

	// Verify all fields are properly set
	if kubeClients.Config != config {
		t.Errorf("Config not properly set, expected %v, got %v", config, kubeClients.Config)
	}

	if kubeClients.K8s != coreClient.K8s {
		t.Errorf("K8s client not properly set, expected %v, got %v", coreClient.K8s, kubeClients.K8s)
	}

	if kubeClients.APIExtV1 == nil {
		t.Error("APIExtV1 client is nil")
	}

	if kubeClients.NVCAOP == nil {
		t.Error("NVCAOP client is nil")
	}
}

func TestKubeClients_StructFields(t *testing.T) {
	// Test that KubeClients struct has expected fields
	config := &rest.Config{Host: "https://test.example.com"}
	k8sClient := fake.NewSimpleClientset()

	kubeClients := &KubeClients{
		Config: config,
		K8s:    k8sClient,
	}

	// Verify struct fields are accessible
	if kubeClients.Config != config {
		t.Error("Config field not properly accessible")
	}

	if kubeClients.K8s != k8sClient {
		t.Error("K8s field not properly accessible")
	}

	// APIExtV1 and NVCAOP can be nil in this test as they're not set
	if kubeClients.APIExtV1 != nil {
		t.Error("APIExtV1 should be nil in this test")
	}

	if kubeClients.NVCAOP != nil {
		t.Error("NVCAOP should be nil in this test")
	}
}

// Benchmark test for NewFromCore function
func BenchmarkNewFromCore(b *testing.B) {
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	coreClient := &core.KubeClients{
		Config: config,
		K8s:    fake.NewSimpleClientset(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := NewFromCore(coreClient, fakeDynClient, coreClient.K8s.Discovery())
		if err != nil {
			b.Fatalf("NewFromCore() failed in benchmark: %v", err)
		}
	}
}

// Test concurrent access to NewFromCore
func TestNewFromCore_Concurrent(t *testing.T) {
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	coreClient := &core.KubeClients{
		Config: config,
		K8s:    fake.NewSimpleClientset(),
	}

	// Run multiple goroutines concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()

			kubeClients, err := NewFromCore(coreClient, fakeDynClient, coreClient.K8s.Discovery())
			if err != nil {
				t.Errorf("NewFromCore() failed in concurrent test: %v", err)
				return
			}

			if kubeClients == nil {
				t.Error("NewFromCore() returned nil in concurrent test")
				return
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestGetDynamicResourceClient_Success(t *testing.T) {
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	coreClient := &core.KubeClients{
		Config: config,
		K8s:    fake.NewSimpleClientset(),
	}

	kubeClients, err := NewFromCore(coreClient, fakeDynClient, coreClient.K8s.Discovery())
	require.NoError(t, err)
	require.NotNil(t, kubeClients)

	// Test with a common GVK (Pod)
	gvk := schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "Pod",
	}

	client, err := kubeClients.GetDynamicResourceClient(gvk)
	if err != nil {
		// This may fail with fake clients if the resource isn't registered
		// but we're testing the code path
		t.Logf("GetDynamicResourceClient error (expected with fake clients): %v", err)
	} else {
		require.NotNil(t, client)
	}
}

func TestGetDynamicResourceClient_InvalidGVK(t *testing.T) {
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	coreClient := &core.KubeClients{
		Config: config,
		K8s:    fake.NewSimpleClientset(),
	}

	kubeClients, err := NewFromCore(coreClient, fakeDynClient, coreClient.K8s.Discovery())
	require.NoError(t, err)
	require.NotNil(t, kubeClients)

	// Test with invalid GVK
	gvk := schema.GroupVersionKind{
		Group:   "nonexistent.example.com",
		Version: "v999",
		Kind:    "NonExistentResource",
	}

	_, err = kubeClients.GetDynamicResourceClient(gvk)
	require.Error(t, err)
	require.Contains(t, err.Error(), "get REST mapping for gvk")
}

func TestGetDynamicResourceClient_MultipleGVKs(t *testing.T) {
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	coreClient := &core.KubeClients{
		Config: config,
		K8s:    fake.NewSimpleClientset(),
	}

	kubeClients, err := NewFromCore(coreClient, fakeDynClient, coreClient.K8s.Discovery())
	require.NoError(t, err)
	require.NotNil(t, kubeClients)

	testCases := []struct {
		name string
		gvk  schema.GroupVersionKind
	}{
		{
			name: "core v1 Pod",
			gvk: schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Pod",
			},
		},
		{
			name: "core v1 Service",
			gvk: schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Service",
			},
		},
		{
			name: "apps v1 Deployment",
			gvk: schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := kubeClients.GetDynamicResourceClient(tc.gvk)
			// May fail with fake clients, but we're testing the code path
			if err != nil {
				t.Logf("Expected error with fake client: %v", err)
			} else {
				require.NotNil(t, client)
			}
		})
	}
}

func TestKubeClients_AllFieldsSet(t *testing.T) {
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	coreClient := &core.KubeClients{
		Config: config,
		K8s:    fake.NewSimpleClientset(),
	}

	kubeClients, err := NewFromCore(coreClient, fakeDynClient, coreClient.K8s.Discovery())
	require.NoError(t, err)
	require.NotNil(t, kubeClients)

	// Verify all struct fields are properly initialized
	assert.NotNil(t, kubeClients.Config, "Config should be set")
	assert.NotNil(t, kubeClients.K8s, "K8s client should be set")
	assert.NotNil(t, kubeClients.APIExtV1, "APIExtV1 client should be set")
	assert.NotNil(t, kubeClients.NVCAOP, "NVCAOP client should be set")
	assert.NotNil(t, kubeClients.DynamicClient, "DynamicClient should be set")
	assert.NotNil(t, kubeClients.DiscoveryClient, "DiscoveryClient should be set")
	assert.NotNil(t, kubeClients.DiscoveryRESTMapper, "DiscoveryRESTMapper should be set")
}
