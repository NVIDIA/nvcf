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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func TestGetSystemNamespace(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom system namespace",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							SystemNamespace: "custom-system-ns",
						},
					},
				},
			},
			expected: "custom-system-ns",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultNVCASystemNamespace,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							SystemNamespace: "",
						},
					},
				},
			},
			expected: DefaultNVCASystemNamespace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getSystemNamespace(tt.nb)
			if result != tt.expected {
				t.Errorf("getSystemNamespace() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetRequestsNamespace(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom requests namespace",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							RequestsNamespace: "custom-requests-ns",
						},
					},
				},
			},
			expected: "custom-requests-ns",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultNVCARequestsNamespace,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							RequestsNamespace: "",
						},
					},
				},
			},
			expected: DefaultNVCARequestsNamespace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getRequestsNamespace(tt.nb)
			if result != tt.expected {
				t.Errorf("getRequestsNamespace() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetBackendType(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom backend type",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							BackendType: "BCP",
						},
					},
				},
			},
			expected: "BCP",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultBackendTypeK8s,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							BackendType: "",
						},
					},
				},
			},
			expected: DefaultBackendTypeK8s,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getBackendType(tt.nb)
			if result != tt.expected {
				t.Errorf("getBackendType() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetClusterLogLevel(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom log level",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							LogLevel: "debug",
						},
					},
				},
			},
			expected: "debug",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultLogLevel,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							LogLevel: "",
						},
					},
				},
			},
			expected: DefaultLogLevel,
		},
		{
			name: "preserve case sensitivity",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-ns",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							LogLevel: "INFO",
						},
					},
				},
			},
			expected: "INFO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getClusterLogLevel(tt.nb)
			if result != tt.expected {
				t.Errorf("getClusterLogLevel() = %v, want %v", result, tt.expected)
			}
		})
	}
}
