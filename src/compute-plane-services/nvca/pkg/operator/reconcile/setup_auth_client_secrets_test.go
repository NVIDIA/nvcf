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

func TestSetupOAuthClientSecrets_NVCA251Plus_WithOAuthConfig(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-client-id",
					ClientSecretKey: "test-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify oauth-client-id secret was created
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))

	// Verify oauth-client-secret-key secret was created
	clientKeySecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-secret-key", string(clientKeySecret.Data[OAuthClientKeySecretDataKey]))
}

func TestSetupOAuthClientSecrets_NVCALessThan251_WithOAuthConfig(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.50.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-client-id",
					ClientSecretKey: "test-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify oauth-client-id secret was created.
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))

	// Verify oauth-client-secret-key secret was created.
	clientKeySecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-secret-key", string(clientKeySecret.Data[OAuthClientKeySecretDataKey]))
}

func TestSetupOAuthClientSecrets_VaultEnabled_SkipsCreation(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-client-id",
					ClientSecretKey: "test-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: true,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify no secrets were created
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	assert.Error(t, err)

	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	assert.Error(t, err)
}

func TestSetupOAuthClientSecrets_EmptyClientID_WithVaultEnabled_SkipsCreation(t *testing.T) {
	// When vault is enabled, the function returns early and doesn't validate ClientID
	// This is expected behavior - vault handles the secrets, so we skip validation
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "", // Empty ClientID
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: true,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err) // Vault enabled, so function returns early without error

	// Verify no secrets were created
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	assert.Error(t, err)
}

func TestSetupOAuthClientSecrets_OnlyClientID_NoSecretKey(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-client-id",
					ClientSecretKey: "", // No secret key
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify client ID secret was created
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))

	// Verify secret key secret was NOT created
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	assert.Error(t, err)
}

func TestSetupOAuthClientSecrets_OnlySecretKey_NoClientID_NoSecretsCreated(t *testing.T) {
	// When ClientID is empty, getOAuthConfig returns empty config (it checks ClientID first)
	// So even if ClientSecretKey is set, it won't be used because getOAuthConfig returns empty
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "", // No client ID - getOAuthConfig will return empty config
					ClientSecretKey: "test-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify no secrets were created because getOAuthConfig returns empty when ClientID is empty
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	assert.Error(t, err)

	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	assert.Error(t, err)
}

func TestSetupOAuthClientSecrets_EmptyVersion_UsesOldNaming(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "", // Empty version defaults to old behavior
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-client-id",
					ClientSecretKey: "test-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify old secret names were used (conservative default)
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))

	clientKeySecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-secret-key", string(clientKeySecret.Data[OAuthClientKeySecretDataKey]))
}

func TestSetupOAuthClientSecrets_UpdatesExistingSecret(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()

	// Create existing secret
	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      OAuthClientIDSecretName,
			Namespace: "nvca-system",
		},
		Data: map[string][]byte{
			OAuthClientIDSecretDataKey: []byte("old-client-id"),
		},
	}
	_, err := clientset.CoreV1().Secrets("nvca-system").Create(ctx, existingSecret, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "new-client-id",
					ClientSecretKey: "new-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err = bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify secret was updated
	updatedSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new-client-id", string(updatedSecret.Data[OAuthClientIDSecretDataKey]))
}

func TestSetupOAuthClientIDSecret_DelegatesToSetupOAuthClientSecrets(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-client-id",
					ClientSecretKey: "test-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	// Test deprecated function delegates correctly
	err := bc.setupOAuthClientIDSecret(ctx, nb)
	require.NoError(t, err)

	// Verify secrets were created
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
}
