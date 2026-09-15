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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
)

func TestValidateNVCFBackendParams_WithOAuthConfig_VaultEnabled_ClientSecretKeyError(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()

	// Create required ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomAnnotationsConfigMapName,
			Namespace: "test-namespace",
		},
	}
	_, err := clientset.CoreV1().ConfigMaps("test-namespace").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-id",
					ClientSecretKey: "should-not-be-set", // Error: vault enabled but secret key set
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: true,
				},
			},
		},
	}

	err = bc.validateNVCFBackendParams(ctx, nb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault is enabled. ClientSecretKey cannot be specified")
}

func TestValidateNVCFBackendParams_WithOAuthConfig_VaultDisabled_EmptySecretKeyError(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()

	// Create required ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomAnnotationsConfigMapName,
			Namespace: "test-namespace",
		},
	}
	_, err := clientset.CoreV1().ConfigMaps("test-namespace").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-id",
					ClientSecretKey: "", // Error: vault disabled but secret key empty
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err = bc.validateNVCFBackendParams(ctx, nb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault-integration disabled. ClientSecretKey cannot be empty")
}

func TestValidateNVCFBackendParams_WithOAuthConfig_VaultDisabled_ValidConfig(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()

	// Create required ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomAnnotationsConfigMapName,
			Namespace: "test-namespace",
		},
	}
	_, err := clientset.CoreV1().ConfigMaps("test-namespace").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-id",
					ClientSecretKey: "test-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err = bc.validateNVCFBackendParams(ctx, nb)
	require.NoError(t, err)
}

func TestValidateNVCFBackendParams_WithOAuthConfig_VaultEnabled_ValidConfig(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()

	// Create required ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomAnnotationsConfigMapName,
			Namespace: "test-namespace",
		},
	}
	_, err := clientset.CoreV1().ConfigMaps("test-namespace").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-id",
					ClientSecretKey: "", // Valid when vault enabled
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: true,
				},
			},
		},
	}

	err = bc.validateNVCFBackendParams(ctx, nb)
	require.NoError(t, err)
}

func TestValidateNVCFBackendParams_EmptyOAuthConfig_NoError(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()

	// Create required ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomAnnotationsConfigMapName,
			Namespace: "test-namespace",
		},
	}
	_, err := clientset.CoreV1().ConfigMaps("test-namespace").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				OAuthConfig: nvidiaiov1.OAuthConfig{
					// Empty config - no validation errors
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err = bc.validateNVCFBackendParams(ctx, nb)
	require.NoError(t, err)
}

func TestValidateNVCFBackendParams_VaultEnabled_NoClientID_NoError(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()

	// Create required ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomAnnotationsConfigMapName,
			Namespace: "test-namespace",
		},
	}
	_, err := clientset.CoreV1().ConfigMaps("test-namespace").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "", // No client ID
					ClientSecretKey: "", // No secret key
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: true,
				},
			},
		},
	}

	// When vault is enabled and config is empty, validation passes
	err = bc.validateNVCFBackendParams(ctx, nb)
	require.NoError(t, err)
}
