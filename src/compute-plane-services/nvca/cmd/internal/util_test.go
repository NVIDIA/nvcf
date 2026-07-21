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

package internalutil

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func TestNewContextFlag(t *testing.T) {
	flag := NewContextFlag()
	assert.NotNil(t, flag)
	assert.Equal(t, "", *flag)
}

func TestNewK8sClient(t *testing.T) {
	tests := []struct {
		name         string
		kcPath       string
		currContext  string
		setupEnv     func()
		cleanupEnv   func()
		expectError  bool
		expectClient bool
	}{
		{
			name:        "empty kubeconfig path and context",
			kcPath:      "",
			currContext: "",
			setupEnv: func() {
				// Point at a path that cannot exist rather than "": an empty
				// KUBECONFIG makes client-go fall back to ~/.kube/config, so on
				// a machine with a real kubeconfig the client unexpectedly
				// builds and the test fails. A nonexistent path keeps the
				// intent (no usable kubeconfig) hermetic everywhere.
				os.Setenv(clientcmd.RecommendedConfigPathEnvVar, "/nonexistent/kubeconfig")
			},
			cleanupEnv: func() {
				os.Unsetenv(clientcmd.RecommendedConfigPathEnvVar)
			},
			expectError:  true,
			expectClient: false,
		},
		{
			name:        "with kubeconfig path",
			kcPath:      "/tmp/kubeconfig",
			currContext: "test-context",
			setupEnv: func() {
				os.Setenv(clientcmd.RecommendedConfigPathEnvVar, "/tmp/kubeconfig")
			},
			cleanupEnv: func() {
				os.Unsetenv(clientcmd.RecommendedConfigPathEnvVar)
			},
			expectError:  true, // Will fail because file doesn't exist
			expectClient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupEnv != nil {
				tt.setupEnv()
			}
			if tt.cleanupEnv != nil {
				defer tt.cleanupEnv()
			}

			client, config, err := NewK8sClient(context.Background(), tt.currContext)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, client)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				if tt.expectClient {
					assert.NotNil(t, client)
					assert.NotNil(t, config)
					assert.IsType(t, &kubernetes.Clientset{}, client)
					assert.IsType(t, &rest.Config{}, config)
				}
			}
		})
	}
}

func TestGetRESTConfig(t *testing.T) {
	tests := []struct {
		name        string
		kcPath      string
		currContext string
		setupEnv    func()
		cleanupEnv  func()
		expectError bool
	}{
		{
			name:        "empty kubeconfig path",
			kcPath:      "",
			currContext: "",
			setupEnv:    func() {},
			cleanupEnv:  func() {},
			expectError: true, // Will fail because we're not in a cluster
		},
		{
			name:        "invalid kubeconfig path",
			kcPath:      "/nonexistent/path",
			currContext: "test-context",
			setupEnv:    func() {},
			cleanupEnv:  func() {},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupEnv != nil {
				tt.setupEnv()
			}
			if tt.cleanupEnv != nil {
				defer tt.cleanupEnv()
			}

			config, err := getRESTConfig(context.Background(), tt.kcPath, tt.currContext)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
				assert.IsType(t, &rest.Config{}, config)
			}
		})
	}
}

// TestGetRESTConfigWithMockKubeconfig tests getRESTConfig with a mock kubeconfig file
func TestGetRESTConfigWithMockKubeconfig(t *testing.T) {
	// Create a temporary kubeconfig file
	tmpFile, err := os.CreateTemp("", "kubeconfig-*")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	// Write mock kubeconfig content
	mockKubeconfig := `
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test-server:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: test-token
`
	err = os.WriteFile(tmpFile.Name(), []byte(mockKubeconfig), 0644)
	require.NoError(t, err)

	// Test with the mock kubeconfig
	config, err := getRESTConfig(context.Background(), tmpFile.Name(), "test-context")
	assert.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, "https://test-server:6443", config.Host)
}
