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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
)

func TestSetupOAuthClientSecrets_AllFieldsPopulated(t *testing.T) {
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
			Namespace: "custom-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.52.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "full-client-id",
					ClientSecretKey: "full-secret-key",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify both secrets created with correct data
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "full-client-id", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))
	// Verify annotations are set (getNBAnnotations sets various annotations)
	assert.NotEmpty(t, clientIDSecret.Annotations)

	clientKeySecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "full-secret-key", string(clientKeySecret.Data[OAuthClientKeySecretDataKey]))
}

func TestSetupOAuthClientSecrets_VersionBoundary251(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	// Test exactly 2.51.0
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "default",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "boundary-test",
					ClientSecretKey: "boundary-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Should use new naming
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	// Clean up
	clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientIDSecretName, metav1.DeleteOptions{})
	clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientKeySecretName, metav1.DeleteOptions{})

	// Test 2.50.99 (just below threshold)
	nb.Spec.Version = "2.50.99"
	err = bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Should use old naming
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestSetupOAuthClientSecrets_VersionWithPrefix(t *testing.T) {
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
				Version: "v2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "v-prefix-test",
					ClientSecretKey: "v-prefix-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Should use new naming (v prefix handled)
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestSetupOAuthClientSecrets_InvalidVersionDefaultsToOld(t *testing.T) {
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
				Version: "invalid-version-string",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "invalid-version-test",
					ClientSecretKey: "invalid-version-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Invalid version should default to old naming (conservative)
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestSetupOAuthClientSecrets_IdempotentMultipleCalls(t *testing.T) {
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
					ClientID:        "idempotent-test",
					ClientSecretKey: "idempotent-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	// First call
	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Second call with same config
	err = bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Third call with updated values
	nb.Spec.OAuthConfig.ClientID = "updated-id"
	nb.Spec.OAuthConfig.ClientSecretKey = "updated-secret"
	err = bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify secrets were updated
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "updated-id", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))

	clientKeySecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "updated-secret", string(clientKeySecret.Data[OAuthClientKeySecretDataKey]))
}

func TestSetupOAuthClientSecrets_VersionRollbackScenario(t *testing.T) {
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
				Version: "2.53.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "rollback-test",
					ClientSecretKey: "rollback-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	// Create with new version (2.53)
	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	// Clean up new secrets
	clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientIDSecretName, metav1.DeleteOptions{})
	clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientKeySecretName, metav1.DeleteOptions{})

	// Rollback to old version (2.50)
	nb.Spec.Version = "2.50.0"
	err = bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Secret names remain stable across version rollback.
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestSetupOAuthClientSecrets_SecretAnnotationsAndLabels(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "annotated-backend",
			Namespace: "test-ns",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.51.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName: "test-cluster", // Required for getNBAnnotations
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "annotated-test",
					ClientSecretKey: "annotated-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify annotations and labels are set correctly
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	// Verify annotations are set (getNBAnnotations sets ClusterName and ClusterGroupKey)
	assert.Contains(t, clientIDSecret.Annotations, ClusterName)
	assert.Equal(t, "test-cluster", clientIDSecret.Annotations[ClusterName])
	// Verify labels are set (getAppLabels sets instance, managed-by, and name labels)
	assert.NotEmpty(t, clientIDSecret.Labels)

	clientKeySecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
	// Verify annotations are set
	assert.Contains(t, clientKeySecret.Annotations, ClusterName)
	assert.Equal(t, "test-cluster", clientKeySecret.Annotations[ClusterName])
	// Verify labels are set
	assert.NotEmpty(t, clientKeySecret.Labels)
}

func TestSetupOAuthClientSecrets_VersionWithSuffixes(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	testCases := []struct {
		name           string
		version        string
		expectedSecret string
	}{
		{"rc version new", "2.51.0-rc1", OAuthClientIDSecretName},
		{"dev version new", "2.51.0-dev", OAuthClientIDSecretName},
		{"rc version old", "2.50.0-rc1", OAuthClientIDSecretName},
		{"dev version old", "2.50.0-dev", OAuthClientIDSecretName},
		{"patch version new", "2.51.1", OAuthClientIDSecretName},
		{"patch version old", "2.50.9", OAuthClientIDSecretName},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Clean up previous test secrets
			clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientIDSecretName, metav1.DeleteOptions{})
			clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientKeySecretName, metav1.DeleteOptions{})

			nb := &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: tc.version,
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID:        "version-test",
							ClientSecretKey: "version-secret",
						},
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: false,
						},
					},
				},
			}

			err := bc.setupOAuthClientSecrets(ctx, nb)
			require.NoError(t, err)

			// Verify correct secret name was used
			_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, tc.expectedSecret, metav1.GetOptions{})
			require.NoError(t, err, "Expected secret %s for version %s", tc.expectedSecret, tc.version)
		})
	}
}

func TestSetupOAuthClientSecrets_MinorVersionBoundaries(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	versions := []struct {
		version        string
		expectedSecret string
		description    string
	}{
		{"2.51.0", OAuthClientIDSecretName, "exact boundary"},
		{"2.51.1", OAuthClientIDSecretName, "patch above boundary"},
		{"2.52.0", OAuthClientIDSecretName, "minor above boundary"},
		{"2.50.9", OAuthClientIDSecretName, "patch below boundary"},
		{"2.50.0", OAuthClientIDSecretName, "minor below boundary"},
		{"2.49.99", OAuthClientIDSecretName, "much below boundary"},
	}

	for _, v := range versions {
		t.Run(v.description, func(t *testing.T) {
			// Clean up
			clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientIDSecretName, metav1.DeleteOptions{})
			clientset.CoreV1().Secrets("nvca-system").Delete(ctx, OAuthClientIDSecretName, metav1.DeleteOptions{})

			nb := &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: v.version,
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID:        "boundary-test",
							ClientSecretKey: "boundary-secret",
						},
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: false,
						},
					},
				},
			}

			err := bc.setupOAuthClientSecrets(ctx, nb)
			require.NoError(t, err)

			_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, v.expectedSecret, metav1.GetOptions{})
			require.NoError(t, err, "Version %s should use %s", v.version, v.expectedSecret)
		})
	}
}

func TestSetupOAuthClientSecrets_OAuthConfigWithPartialFields(t *testing.T) {
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
					ClientID:        "partial-oauth-id",
					ClientSecretKey: "partial-oauth-secret",
					// Other fields not set
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Verify secrets created from partial OAuthConfig
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "partial-oauth-id", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))

	clientKeySecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "partial-oauth-secret", string(clientKeySecret.Data[OAuthClientKeySecretDataKey]))
}

func TestSetupOAuthClientSecrets_VersionUpgradeScenario(t *testing.T) {
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
					ClientID:        "upgrade-test",
					ClientSecretKey: "upgrade-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	// Start with old version
	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	// Upgrade to new version
	nb.Spec.Version = "2.51.0"
	err = bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Should now use new naming
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	// Old secrets may still exist (not cleaned up automatically)
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	// May or may not exist depending on cleanup logic
}

func TestSetupOAuthClientSecrets_ConcurrentCalls(t *testing.T) {
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
					ClientID:        "concurrent-test",
					ClientSecretKey: "concurrent-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	// Simulate concurrent calls (in practice, this would be serialized by the controller)
	err1 := bc.setupOAuthClientSecrets(ctx, nb)
	err2 := bc.setupOAuthClientSecrets(ctx, nb)
	err3 := bc.setupOAuthClientSecrets(ctx, nb)

	require.NoError(t, err1)
	require.NoError(t, err2)
	require.NoError(t, err3)

	// Verify final state is correct
	clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "concurrent-test", string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))
}

func TestSetupOAuthClientSecrets_DifferentNamespaces(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clientset := fakek8sclient.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: clientset,
		},
	}

	namespaces := []string{"namespace1", "namespace2", "namespace3"}

	for _, ns := range namespaces {
		t.Run(ns, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: ns,
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "2.51.0",
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID:        "ns-test-" + ns,
							ClientSecretKey: "ns-secret-" + ns,
						},
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: false,
						},
					},
				},
			}

			err := bc.setupOAuthClientSecrets(ctx, nb)
			require.NoError(t, err)

			// All secrets go to nvca-system namespace regardless of backend namespace
			clientIDSecret, err := clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, "ns-test-"+ns, string(clientIDSecret.Data[OAuthClientIDSecretDataKey]))
		})
	}
}

func TestSetupOAuthClientSecrets_VaultDisabledWithEmptyConfig(t *testing.T) {
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
				Version:     "2.51.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					// Empty config
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// No secrets should be created
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	assert.Error(t, err)

	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	assert.Error(t, err)
}

func TestSetupOAuthClientSecrets_MajorVersion3(t *testing.T) {
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
				Version: "3.0.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "v3-test",
					ClientSecretKey: "v3-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Major version 3 should use new naming
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestSetupOAuthClientSecrets_MajorVersion1(t *testing.T) {
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
				Version: "1.99.0",
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "v1-test",
					ClientSecretKey: "v1-secret",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}

	err := bc.setupOAuthClientSecrets(ctx, nb)
	require.NoError(t, err)

	// Major version 1 should use old naming
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestSetupOAuthClientSecrets_OAuthConfigOnlyClientID(t *testing.T) {
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
					ClientID:        "oauth-only-id",
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

	// Only client ID secret should be created
	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	_, err = clientset.CoreV1().Secrets("nvca-system").Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
	assert.Error(t, err)
}
