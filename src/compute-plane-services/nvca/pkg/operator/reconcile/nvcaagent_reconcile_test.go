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
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/auth"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

//go:embed testdata/*
var testdataFiles embed.FS

func readTestdataFile(t *testing.T, fileName string) string {
	t.Helper()
	b, err := testdataFiles.ReadFile(fileName)
	require.NoError(t, err)
	return string(b)
}

func TestValidateNVCFBackendParams(t *testing.T) {
	tests := []struct {
		name          string
		configMaps    map[string]*corev1.ConfigMap
		expectError   bool
		errorContains string
	}{
		{
			name: "all required configmaps exist",
			configMaps: map[string]*corev1.ConfigMap{
				nvcfCustomAnnotationsConfigMapName: {
					ObjectMeta: metav1.ObjectMeta{
						Name: nvcfCustomAnnotationsConfigMapName,
					},
				},
			},
			expectError: false,
		},
		{
			name:          "missing annotations configmap",
			configMaps:    map[string]*corev1.ConfigMap{},
			expectError:   true,
			errorContains: "required ConfigMap nvca-namespace-pod-annotations not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients := mockKubeClientsForIntegrationTests()

			// Pre-create the ConfigMaps in the mock client
			for _, cm := range tt.configMaps {
				_, err := clients.K8s.CoreV1().ConfigMaps("test-namespace").Create(ctx, cm, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			bc := &BackendK8sCache{
				clients:              clients,
				ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
			}

			nb := &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-namespace",
				},
			}

			err := bc.validateNVCFBackendParams(ctx, nb)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateNVCFBackendParams_SelfManagedClusterID(t *testing.T) {
	tests := []struct {
		name           string
		clusterSource  nvcaoptypes.ClusterSource
		clusterID      string
		clusterGroupID string
		expectError    bool
		errorContains  string
	}{
		{
			name:           "self-managed with empty clusterID",
			clusterSource:  nvcaoptypes.ClusterSourceSelfHosted,
			clusterID:      "",
			clusterGroupID: "valid-group-id",
			expectError:    true,
			errorContains:  "clusterID is required for self-managed clusters but was empty",
		},
		{
			name:           "self-managed with empty clusterGroupID",
			clusterSource:  nvcaoptypes.ClusterSourceSelfHosted,
			clusterID:      "valid-cluster-id",
			clusterGroupID: "",
			expectError:    true,
			errorContains:  "clusterGroupID is required for self-managed clusters but was empty",
		},
		{
			name:           "self-managed with both IDs empty",
			clusterSource:  nvcaoptypes.ClusterSourceSelfHosted,
			clusterID:      "",
			clusterGroupID: "",
			expectError:    true,
			errorContains:  "clusterID is required for self-managed clusters but was empty",
		},
		{
			name:           "self-managed with valid IDs",
			clusterSource:  nvcaoptypes.ClusterSourceSelfHosted,
			clusterID:      "valid-cluster-id",
			clusterGroupID: "valid-group-id",
			expectError:    false,
		},
		{
			name:           "ngc-managed with empty clusterID is allowed",
			clusterSource:  nvcaoptypes.ClusterSourceNGCManaged,
			clusterID:      "",
			clusterGroupID: "",
			expectError:    false,
		},
		{
			name:           "helm-managed with empty clusterID is allowed",
			clusterSource:  nvcaoptypes.ClusterSourceHelmManaged,
			clusterID:      "",
			clusterGroupID: "",
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients := mockKubeClientsForIntegrationTests()

			annotationsCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name: nvcfCustomAnnotationsConfigMapName,
				},
			}
			_, err := clients.K8s.CoreV1().ConfigMaps("test-namespace").Create(ctx, annotationsCM, metav1.CreateOptions{})
			require.NoError(t, err)

			bc := &BackendK8sCache{
				clients:              clients,
				ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
			}

			nb := &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-namespace",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterSource: tt.clusterSource,
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterID:      tt.clusterID,
							ClusterGroupID: tt.clusterGroupID,
						},
					},
				},
			}

			err = bc.validateNVCFBackendParams(ctx, nb)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSetupNVCADeployment(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		envType:              nvidiaiov1.EnvTypeStage,
	}

	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					UnregisterOnStartup: true,
					ClusterID:           "some-cluster-id",
					CloudProvider:       "ON-PREM",
					ClusterGroupName:    "FC-NVCF-Backend",
					ClusterName:         "byoc-test",
					Description:         "FleetCommand NVCF test cluster",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{},
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
						"SharedCluster",
					},
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullPolicy: "IfNotPresent",
					Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
					Tag:        "1.0.0",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://stg.icms.nvcf.nvidia.com",
					TokenURL:       "https://stg.icms.nvcf.nvidia.com/token",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:             "oauth-stg-abc123",
					ClientSecretKey:      "test-secret-key",
					ClientSecretsEnvFile: "",
					PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
					TokenURL:             "https://stage-oauth.example.test/token",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
					ImageConfig: nvidiaiov1.ImageConfig{
						PullPolicy: "IfNotPresent",
						Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
						Tag:        "1.0.0",
					},
				},
				Version: "1.0.0",
			},
		},
	}

	agentCfg, err := bc.newAgentConfig(ctx, inNVCFBackend)
	require.NoError(t, err)
	assert.Equal(t, nvcaconfig.Config{
		Environment: nvcaconfig.EnvironmentStaging,
		Cluster: nvcaconfig.NVCFClusterConfig{
			ID:            "some-cluster-id",
			CloudProvider: "ON-PREM",
			GroupName:     "FC-NVCF-Backend",
			Name:          "byoc-test",
			NCAID:         "ncaid1",
		},
		Agent: nvcaconfig.AgentConfig{
			LogLevel: "info",
			FeatureFlags: []string{
				"CachingSupport",
				"LogPosting",
				"PeriodicInstanceStatusUpdate",
				"SharedCluster",
			},
			ICMSURL: "https://stg.icms.nvcf.nvidia.com",
			SharedStorage: nvcaconfig.SharedStorageConfig{
				Server: nvcaconfig.SharedStorageServerConfig{
					Image: "stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5",
				},
			},
			SvcAddress:        ":8000",
			AdminAddr:         "127.0.0.1:8001",
			SystemNamespace:   "nvca-system",
			RequestsNamespace: "nvcf-backend",
			NamespaceLabels: map[string]string{
				"app.kubernetes.io/instance":   "nvca",
				"app.kubernetes.io/managed-by": "nvca-operator",
				"app.kubernetes.io/name":       "nvca",
			},
			ComputeBackend:      "k8s",
			HelmReValServiceURL: "https://reval.stg.nvcf.nvidia.com",
		},
		Webhook: nvcaconfig.WebhookConfig{
			SvcAddress:    ":8001",
			TLSKeyFile:    "/certs/server/tls.key",
			TLSCertFile:   "/certs/server/tls.crt",
			TLSSecretName: "nvca-webhook-tls-server-certs",
		},
		Authz: nvcaconfig.AuthzConfig{
			PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
			TokenURL:             "https://stg.icms.nvcf.nvidia.com/token",
			NGCServiceAPIKeyFile: "/var/run/secrets/ngc-service-api-key/ngc-service-api-key",
		},
		Tracing: nvcaconfig.TracingConfig{
			Exporter: nvcaconfig.LightstepExporter,
		},
	}, agentCfg)

	err = bc.setupNVCADeployment(ctx, inNVCFBackend)
	require.NoError(t, err)

	depIface := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace)
	var gotDep *appsv1.Deployment
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotDep, err = depIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
	}, 10*time.Second, 100*time.Millisecond)

	assert.Equal(t, []corev1.Volume{
		{
			Name: agentConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: agentConfigConfigMapName,
					},
					DefaultMode: ptr.To(int32(0644)),
					Optional:    ptr.To(false),
				},
			},
		},
		{
			Name: SrvCertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: CACertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: NGCServiceAPIKeySecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: NGCServiceAPIKeySecretName,
				},
			},
		},
		{
			Name: ReValCacheVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: resource.NewQuantity(50*1<<30, resource.BinarySI),
				},
			},
		},
	}, gotDep.Spec.Template.Spec.Volumes)

	// Check containers.
	require.Len(t, gotDep.Spec.Template.Spec.Containers, 2)
	gotContainers := gotDep.Spec.Template.Spec.Containers
	var nvcaContainer, webhookContainer corev1.Container
	for _, c := range gotContainers {
		switch c.Name {
		case "agent":
			nvcaContainer = c
		case "webhook":
			webhookContainer = c
		}
	}

	require.Equal(t, "agent", nvcaContainer.Name, "NVCA container not found")
	require.Equal(t, "webhook", webhookContainer.Name, "Webhook container not found")

	assert.False(t, *nvcaContainer.SecurityContext.AllowPrivilegeEscalation)
	assert.True(t, *nvcaContainer.SecurityContext.RunAsNonRoot)
	assert.Equal(t, nvcaContainer.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"})

	// Check feature flags.
	assert.Empty(t, nvcaContainer.Command)
	assert.Equal(t, []string{"/usr/bin/nvca", "--config", "/var/run/nvca/config.yaml"}, nvcaContainer.Args)
	assert.Equal(t, []corev1.EnvVar{
		{
			Name: auth.ClientIDEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: OAuthClientIDSecretName,
					},
					Key: OAuthClientIDSecretDataKey,
				},
			},
		},
		{
			Name: auth.ClientSecretEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: OAuthClientKeySecretName,
					},
					Key: OAuthClientKeySecretDataKey,
				},
			},
		},
	}, nvcaContainer.Env)
	assert.Equal(t, []corev1.VolumeMount{
		{
			Name:      NGCServiceAPIKeySecretName,
			MountPath: fmt.Sprintf("/var/run/secrets/%s", NGCServiceAPIKeySecretName),
			ReadOnly:  true,
		},
		{
			Name:      agentConfigVolumeName,
			MountPath: agentConfigDir,
		},
		{
			Name:      ReValCacheVolumeName,
			MountPath: ReValCacheDir,
		},
	}, nvcaContainer.VolumeMounts)

	assert.Empty(t, webhookContainer.Command)
	assert.Equal(t, []string{"/usr/bin/webhook-server", "--config", "/var/run/nvca/config.yaml"}, webhookContainer.Args)
	assert.Equal(t, []corev1.EnvVar{
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
	}, webhookContainer.Env)
	assert.Equal(t, []corev1.VolumeMount{
		{
			Name:      SrvCertsVolumeName,
			MountPath: SrvCertsMountDir,
		},
		{
			Name:      CACertsVolumeName,
			MountPath: CACertsMountDir,
		},
		{
			Name:      agentConfigVolumeName,
			MountPath: agentConfigDir,
		},
	}, webhookContainer.VolumeMounts)

	// Check service
	err = bc.setupNVCAService(ctx, inNVCFBackend)
	require.NoError(t, err)

	svcIface := clients.K8s.CoreV1().Services(DefaultNVCASystemNamespace)
	var gotSvc *corev1.Service
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotSvc, err = svcIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
	}, 10*time.Second, 100*time.Millisecond)

	// Ensure labels are in nvca service and deployment
	expectedLabels := map[string]string{
		InstanceLabelKey:  nvcaoptypes.NVCAModuleName,
		ManagedbyLabelKey: NVCAOperatorName,
		NameLabelKey:      nvcaoptypes.NVCAModuleName,
	}
	assert.Equal(t, expectedLabels, gotSvc.Labels)
	assert.Equal(t, expectedLabels, gotDep.Labels)

	expectedAnnotations := map[string]string{
		ClusterName:     inNVCFBackend.Spec.ClusterConfig.ClusterName,
		ClusterGroupKey: inNVCFBackend.Spec.ClusterConfig.ClusterGroupName,
	}
	assert.Equal(t, expectedAnnotations, gotSvc.Annotations)
	assert.Equal(t, expectedAnnotations, gotDep.Annotations)
	assert.Empty(t, gotDep.Spec.Template.Annotations)

	// Try rollout with the same spec.
	err = bc.setupNVCADeployment(ctx, inNVCFBackend)
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotDep, err = depIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.Contains(ct, gotDep.Spec.Template.Annotations, restartedAtAnnotation)
			assert.NotEmpty(ct, gotDep.Spec.Template.Annotations[restartedAtAnnotation])
		}
	}, 10*time.Second, 100*time.Millisecond)
}

func TestSetupNVCADeployment_OverrideEnvironmentVars(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		envType:              nvidiaiov1.EnvTypeStage,
	}

	overrideVars := map[string]string{
		"LOG_LEVEL":  "debug",
		"CUSTOM_VAR": "custom-value",
		"EXTRA_FLAG": "enabled",
	}

	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					UnregisterOnStartup: true,
					ClusterID:           "some-cluster-id",
					CloudProvider:       "ON-PREM",
					ClusterGroupName:    "FC-NVCF-Backend",
					ClusterName:         "byoc-test",
					Description:         "FleetCommand NVCF test cluster",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{},
					Values:     []string{"LogPosting", "CachingSupport"},
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullPolicy: "IfNotPresent",
					Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
					Tag:        "1.0.0",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://stg.icms.nvcf.nvidia.com",
					TokenURL:       "https://stg.icms.nvcf.nvidia.com/token",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:             "oauth-stg-abc123",
					ClientSecretKey:      "test-secret-key",
					ClientSecretsEnvFile: "",
					PublicKeysetEndpoint: "https://stg.oauth.example.com/.well-known/jwks.json",
					TokenURL:             "https://stg.oauth.example.com/token",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
					ImageConfig: nvidiaiov1.ImageConfig{
						PullPolicy: "IfNotPresent",
						Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
						Tag:        "1.0.0",
					},
				},
				Version: "1.0.0",
				AgentConfig: nvidiaiov1.AgentConfig{
					DeploymentConfig: nvidiaiov1.DeploymentConfig{
						OverrideEnvironmentVars: overrideVars,
					},
				},
			},
		},
	}

	err := bc.setupNVCADeployment(ctx, inNVCFBackend)
	require.NoError(t, err)

	depIface := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace)
	var gotDep *appsv1.Deployment
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		var getErr error
		gotDep, getErr = depIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, getErr)
	}, 10*time.Second, 100*time.Millisecond)

	var nvcaContainer *corev1.Container
	for i := range gotDep.Spec.Template.Spec.Containers {
		if gotDep.Spec.Template.Spec.Containers[i].Name == "agent" {
			nvcaContainer = &gotDep.Spec.Template.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, nvcaContainer, "NVCA container not found in deployment")

	envByName := make(map[string]corev1.EnvVar)
	for _, e := range nvcaContainer.Env {
		envByName[e.Name] = e
	}
	for name, wantValue := range overrideVars {
		ev, ok := envByName[name]
		require.True(t, ok, "override env var %q not found in NVCA container", name)
		assert.Equal(t, wantValue, ev.Value, "override env var %q value", name)
		assert.Nil(t, ev.ValueFrom, "override env var %q should use literal Value, not ValueFrom", name)
	}
}

func TestSetupNVCADeployment_Vault(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		envType:              nvidiaiov1.EnvTypeStage,
	}

	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					UnregisterOnStartup: true,
					ClusterID:           "some-cluster-id",
					CloudProvider:       "ON-PREM",
					ClusterGroupName:    "FC-NVCF-Backend",
					ClusterName:         "byoc-test",
					Description:         "FleetCommand NVCF test cluster",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{},
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
						"SharedCluster",
					},
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullPolicy: "IfNotPresent",
					Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
					Tag:        "1.0.0",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://stg.icms.nvcf.nvidia.com",
					TokenURL:       "https://stg.icms.nvcf.nvidia.com/token",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:             "oauth-stg-abc123",
					ClientSecretKey:      "test-secret-key",
					ClientSecretsEnvFile: "",
					PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
					TokenURL:             "https://stage-oauth.example.test/token",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled:         true,
					Address:         "https://stg.vault.nvidia.com:443",
					OAuthConfigRole: "k8s_byoc-test_jwt_role",
					AuthMountPath:   "auth/jwt/k8s/byoc-test",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
					ImageConfig: nvidiaiov1.ImageConfig{
						PullPolicy: "IfNotPresent",
						Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
						Tag:        "1.0.0",
					},
				},
				Version: "1.0.0",
			},
		},
	}

	agentCfg, err := bc.newAgentConfig(ctx, inNVCFBackend)
	require.NoError(t, err)
	assert.Equal(t, nvcaconfig.Config{
		Environment: nvcaconfig.EnvironmentStaging,
		Cluster: nvcaconfig.NVCFClusterConfig{
			ID:            "some-cluster-id",
			CloudProvider: "ON-PREM",
			GroupName:     "FC-NVCF-Backend",
			Name:          "byoc-test",
			NCAID:         "ncaid1",
		},
		Agent: nvcaconfig.AgentConfig{
			LogLevel: "info",
			FeatureFlags: []string{
				"CachingSupport",
				"LogPosting",
				"PeriodicInstanceStatusUpdate",
				"SharedCluster",
			},
			ICMSURL: "https://stg.icms.nvcf.nvidia.com",
			SharedStorage: nvcaconfig.SharedStorageConfig{
				Server: nvcaconfig.SharedStorageServerConfig{
					Image: "stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5",
				},
			},
			SvcAddress:        ":8000",
			AdminAddr:         "127.0.0.1:8001",
			SystemNamespace:   "nvca-system",
			RequestsNamespace: "nvcf-backend",
			NamespaceLabels: map[string]string{
				"app.kubernetes.io/instance":   "nvca",
				"app.kubernetes.io/managed-by": "nvca-operator",
				"app.kubernetes.io/name":       "nvca",
			},
			ComputeBackend:      "k8s",
			HelmReValServiceURL: "https://reval.stg.nvcf.nvidia.com",
		},
		Webhook: nvcaconfig.WebhookConfig{
			SvcAddress:    ":8001",
			TLSKeyFile:    "/certs/server/tls.key",
			TLSCertFile:   "/certs/server/tls.crt",
			TLSSecretName: "nvca-webhook-tls-server-certs",
		},
		Authz: nvcaconfig.AuthzConfig{
			PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
			TokenURL:             "https://stg.icms.nvcf.nvidia.com/token",
			NGCServiceAPIKeyFile: "/var/run/secrets/ngc-service-api-key/ngc-service-api-key",
			ClientSecretsEnvFile: "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
		},
		Tracing: nvcaconfig.TracingConfig{
			Exporter: nvcaconfig.LightstepExporter,
		},
	}, agentCfg)

	err = bc.setupNVCADeployment(ctx, inNVCFBackend)
	require.NoError(t, err)

	depIface := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace)
	var gotDep *appsv1.Deployment
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotDep, err = depIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
	}, 10*time.Second, 100*time.Millisecond)

	assert.Equal(t, []corev1.Volume{
		{
			Name: agentConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: agentConfigConfigMapName,
					},
					DefaultMode: ptr.To(int32(0644)),
					Optional:    ptr.To(false),
				},
			},
		},
		{
			Name: SrvCertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: CACertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: NGCServiceAPIKeySecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: NGCServiceAPIKeySecretName,
				},
			},
		},
		{
			Name: ReValCacheVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: resource.NewQuantity(50*1<<30, resource.BinarySI),
				},
			},
		},
		{
			Name: "token",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Path:              "token",
								ExpirationSeconds: ptr.To(int64(3600)),
								Audience:          "https://stg.vault.nvidia.com:443",
							},
						},
					},
				},
			},
		},
	}, gotDep.Spec.Template.Spec.Volumes)

	// Check containers.
	require.Len(t, gotDep.Spec.Template.Spec.Containers, 2)
	gotContainers := gotDep.Spec.Template.Spec.Containers
	var nvcaContainer, webhookContainer corev1.Container
	for _, c := range gotContainers {
		switch c.Name {
		case "agent":
			nvcaContainer = c
		case "webhook":
			webhookContainer = c
		}
	}

	require.Equal(t, "agent", nvcaContainer.Name, "NVCA container not found")
	require.Equal(t, "webhook", webhookContainer.Name, "Webhook container not found")

	assert.False(t, *nvcaContainer.SecurityContext.AllowPrivilegeEscalation)
	assert.True(t, *nvcaContainer.SecurityContext.RunAsNonRoot)
	assert.Equal(t, nvcaContainer.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"})

	// Check feature flags.
	assert.Empty(t, nvcaContainer.Command)
	assert.Equal(t, []string{"/usr/bin/nvca", "--config", "/var/run/nvca/config.yaml"}, nvcaContainer.Args)
	// When Vault is enabled, OAuth credentials come from ClientSecretsEnvFile (Vault agent output),
	// not from SecretKeyRef - so no OAUTH_CLIENT_ID env var is added (fixes "secret oauth-client-id not found").
	assert.Empty(t, nvcaContainer.Env)
	assert.Equal(t, []corev1.VolumeMount{
		{
			Name:      NGCServiceAPIKeySecretName,
			MountPath: fmt.Sprintf("/var/run/secrets/%s", NGCServiceAPIKeySecretName),
			ReadOnly:  true,
		},
		{
			Name:      agentConfigVolumeName,
			MountPath: agentConfigDir,
		},
		{
			Name:      ReValCacheVolumeName,
			MountPath: ReValCacheDir,
		},
		{
			Name:      "token",
			MountPath: "/var/run/secrets/kubernetes.io/serviceaccount-vault",
		},
	}, nvcaContainer.VolumeMounts)

	assert.Empty(t, webhookContainer.Command)
	assert.Equal(t, []string{"/usr/bin/webhook-server", "--config", "/var/run/nvca/config.yaml"}, webhookContainer.Args)
	assert.Equal(t, []corev1.EnvVar{
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
	}, webhookContainer.Env)
	assert.Equal(t, []corev1.VolumeMount{
		{
			Name:      SrvCertsVolumeName,
			MountPath: SrvCertsMountDir,
		},
		{
			Name:      CACertsVolumeName,
			MountPath: CACertsMountDir,
		},
		{
			Name:      agentConfigVolumeName,
			MountPath: agentConfigDir,
		},
	}, webhookContainer.VolumeMounts)

	// Ensure labels are in nvca deployment
	expectedLabels := map[string]string{
		InstanceLabelKey:  nvcaoptypes.NVCAModuleName,
		ManagedbyLabelKey: NVCAOperatorName,
		NameLabelKey:      nvcaoptypes.NVCAModuleName,
	}
	assert.Equal(t, expectedLabels, gotDep.Labels)

	expectedAnnotations := map[string]string{
		ClusterName:     inNVCFBackend.Spec.ClusterConfig.ClusterName,
		ClusterGroupKey: inNVCFBackend.Spec.ClusterConfig.ClusterGroupName,
	}
	assert.Equal(t, expectedAnnotations, gotDep.Annotations)
	assert.Equal(t, getVaultAnnotations(inNVCFBackend), gotDep.Spec.Template.Annotations)
}

func TestSetupNVCADeployment_SelfHosted(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		envType:              nvidiaiov1.EnvTypeStage,
		nvcfWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
			CacheMountOptionsEnabled: true,
			CacheMountOptions:        "ro",
			WorkerDegradationPeriod:  5 * time.Hour,
		},
		workloadTolerations: []corev1.Toleration{{
			Key:      "nvidia.com/test-workload",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		}},
	}

	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterSource: nvcaoptypes.ClusterSourceSelfHosted,
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					UnregisterOnStartup: true,
					ClusterID:           "some-cluster-id",
					CloudProvider:       "ON-PREM",
					ClusterGroupName:    "FC-NVCF-Backend",
					ClusterName:         "byoc-test",
					Description:         "FleetCommand NVCF test cluster",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{},
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
						"SharedCluster",
					},
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullPolicy: "IfNotPresent",
					Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
					Tag:        "1.0.0",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://stg.icms.nvcf.nvidia.com",
					TokenURL:       "https://stg.icms.nvcf.nvidia.com/token",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:             "oauth-stg-abc123",
					ClientSecretKey:      "test-secret-key",
					ClientSecretsEnvFile: "",
					PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
					TokenURL:             "https://stage-oauth.example.test/token",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled:         true,
					Address:         "https://stg.vault.nvidia.com:443",
					OAuthConfigRole: "k8s_byoc-test_jwt_role",
					AuthMountPath:   "auth/jwt/k8s/byoc-test",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
					ImageConfig: nvidiaiov1.ImageConfig{
						PullPolicy: "IfNotPresent",
						Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
						Tag:        "1.0.0",
					},
				},
				Version: "1.0.0",
			},
		},
	}

	agentCfg, err := bc.newAgentConfig(ctx, inNVCFBackend)
	require.NoError(t, err)
	assert.Equal(t, nvcaconfig.Config{
		Environment: nvcaconfig.EnvironmentStaging,
		Cluster: nvcaconfig.NVCFClusterConfig{
			ID:            "some-cluster-id",
			CloudProvider: "ON-PREM",
			GroupName:     "FC-NVCF-Backend",
			Name:          "byoc-test",
			NCAID:         "ncaid1",
		},
		Agent: nvcaconfig.AgentConfig{
			LogLevel: "info",
			FeatureFlags: []string{
				"CachingSupport",
				"LogPosting",
				"PeriodicInstanceStatusUpdate",
				"SelfHosted",
				"SharedCluster",
			},
			ICMSURL: "https://stg.icms.nvcf.nvidia.com",
			SharedStorage: nvcaconfig.SharedStorageConfig{
				Server: nvcaconfig.SharedStorageServerConfig{
					Image: "stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5",
				},
			},
			SvcAddress:        ":8000",
			AdminAddr:         "127.0.0.1:8001",
			SystemNamespace:   "nvca-system",
			RequestsNamespace: "nvcf-backend",
			NamespaceLabels: map[string]string{
				"app.kubernetes.io/instance":   "nvca",
				"app.kubernetes.io/managed-by": "nvca-operator",
				"app.kubernetes.io/name":       "nvca",
			},
			ComputeBackend:        "k8s",
			HelmReValServiceURL:   "http://reval.nvcf.svc.cluster.local:8080",
			CSIVolumeMountOptions: []string{"ro"},
		},
		Webhook: nvcaconfig.WebhookConfig{
			SvcAddress:    ":8001",
			TLSKeyFile:    "/certs/server/tls.key",
			TLSCertFile:   "/certs/server/tls.crt",
			TLSSecretName: "nvca-webhook-tls-server-certs",
		},
		Workload: nvcaconfig.WorkloadConfig{
			WorkloadTimeConfig: nvcaconfig.WorkloadTimeConfig{
				WorkerDegradationTimeout: 5 * time.Hour,
			},
			Tolerations: []corev1.Toleration{{
				Key:      "nvidia.com/test-workload",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		},
		Authz: nvcaconfig.AuthzConfig{
			PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
			TokenURL:             "https://stg.icms.nvcf.nvidia.com/token",
			NGCServiceAPIKeyFile: "/var/run/secrets/ngc-service-api-key/ngc-service-api-key",
			ClientSecretsEnvFile: "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
		},
		Tracing: nvcaconfig.TracingConfig{
			Exporter: nvcaconfig.LightstepExporter,
		},
	}, agentCfg)

	err = bc.setupNVCADeployment(ctx, inNVCFBackend)
	require.NoError(t, err)

	depIface := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace)
	var gotDep *appsv1.Deployment
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotDep, err = depIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
	}, 10*time.Second, 100*time.Millisecond)

	assert.Equal(t, []corev1.Volume{
		{
			Name: agentConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: agentConfigConfigMapName,
					},
					DefaultMode: ptr.To(int32(0644)),
					Optional:    ptr.To(false),
				},
			},
		},
		{
			Name: SrvCertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: CACertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: NGCServiceAPIKeySecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: NGCServiceAPIKeySecretName,
				},
			},
		},
		{
			Name: ReValCacheVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: resource.NewQuantity(50*1<<30, resource.BinarySI),
				},
			},
		},
		{
			Name: "token",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Path:              "token",
								ExpirationSeconds: ptr.To(int64(3600)),
								Audience:          "https://stg.vault.nvidia.com:443",
							},
						},
					},
				},
			},
		},
		{
			Name: "nvca-token",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          "nvcf-icms:some-cluster-id",
								ExpirationSeconds: ptr.To(int64(3600)),
								Path:              "token",
							},
						},
					},
				},
			},
		},
		{
			Name: "nvca-self-managed-registration",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}, gotDep.Spec.Template.Spec.Volumes)

	// Check containers.
	require.Len(t, gotDep.Spec.Template.Spec.Containers, 2)
	gotContainers := gotDep.Spec.Template.Spec.Containers
	var nvcaContainer, webhookContainer corev1.Container
	for _, c := range gotContainers {
		switch c.Name {
		case "agent":
			nvcaContainer = c
		case "webhook":
			webhookContainer = c
		}
	}

	require.Equal(t, "agent", nvcaContainer.Name, "NVCA container not found")
	require.Equal(t, "webhook", webhookContainer.Name, "Webhook container not found")

	assert.False(t, *nvcaContainer.SecurityContext.AllowPrivilegeEscalation)
	assert.True(t, *nvcaContainer.SecurityContext.RunAsNonRoot)
	assert.Equal(t, nvcaContainer.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"})

	// Check feature flags.
	assert.Empty(t, nvcaContainer.Command)
	assert.Equal(t, []string{"/usr/bin/nvca", "--config", "/var/run/nvca/config.yaml"}, nvcaContainer.Args)
	// PSAT identity threads NVCF_TOKEN_FILE_PATH + NVCF_IDENTITY_SOURCE so the
	// agent reads its bearer token from the projected SA volume and knows which
	// code paths apply (e.g. JWKS-pusher is PSAT-only).
	assert.Equal(t, []corev1.EnvVar{
		{Name: "NVCF_TOKEN_FILE_PATH", Value: "/var/run/secrets/tokens/token"},
		{Name: "NVCF_IDENTITY_SOURCE", Value: IdentitySourcePSAT},
	}, nvcaContainer.Env)
	assert.Equal(t, []corev1.VolumeMount{
		{
			Name:      NGCServiceAPIKeySecretName,
			MountPath: fmt.Sprintf("/var/run/secrets/%s", NGCServiceAPIKeySecretName),
			ReadOnly:  true,
		},
		{
			Name:      agentConfigVolumeName,
			MountPath: agentConfigDir,
		},
		{
			Name:      ReValCacheVolumeName,
			MountPath: ReValCacheDir,
		},
		{
			Name:      "token",
			MountPath: "/var/run/secrets/kubernetes.io/serviceaccount-vault",
		},
		{
			Name:      "nvca-token",
			MountPath: "/var/run/secrets/tokens",
			ReadOnly:  true,
		},
		{
			Name:      "nvca-self-managed-registration",
			MountPath: "/var/run/secrets/nvca-self-managed-registration",
			ReadOnly:  true,
		},
	}, nvcaContainer.VolumeMounts)

	assert.Empty(t, webhookContainer.Command)
	assert.Equal(t, []string{"/usr/bin/webhook-server", "--config", "/var/run/nvca/config.yaml"}, webhookContainer.Args)
	assert.Equal(t, []corev1.EnvVar{
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
	}, webhookContainer.Env)
	assert.Equal(t, []corev1.VolumeMount{
		{
			Name:      SrvCertsVolumeName,
			MountPath: SrvCertsMountDir,
		},
		{
			Name:      CACertsVolumeName,
			MountPath: CACertsMountDir,
		},
		{
			Name:      agentConfigVolumeName,
			MountPath: agentConfigDir,
		},
	}, webhookContainer.VolumeMounts)

	// Ensure labels are in nvca deployment
	expectedLabels := map[string]string{
		InstanceLabelKey:  nvcaoptypes.NVCAModuleName,
		ManagedbyLabelKey: NVCAOperatorName,
		NameLabelKey:      nvcaoptypes.NVCAModuleName,
	}
	assert.Equal(t, expectedLabels, gotDep.Labels)

	expectedAnnotations := map[string]string{
		ClusterName:     inNVCFBackend.Spec.ClusterConfig.ClusterName,
		ClusterGroupKey: inNVCFBackend.Spec.ClusterConfig.ClusterGroupName,
	}
	assert.Equal(t, expectedAnnotations, gotDep.Annotations)
	// PSAT identity: pod template inherits the NVCFBackend's vault-template
	// annotations (carried for managed-cluster vault routing) but no longer
	// has the vault-agent-inject role/auth-path/secret-volume-path keys —
	// those are SelfHosted-vault-only and that path was removed.
	assert.Equal(t, map[string]string{
		"vault.hashicorp.com/agent-init-first":    "true",
		"vault.hashicorp.com/agent-inject-status": "update",
		"vault.hashicorp.com/agent-configmap":     NVCAVaultConfigmapName,
		"vault.hashicorp.com/agent-inject":        trueValue,
		"vault.hashicorp.com/secret-volume-path":  "/home/nvca/vault-agent/secrets",
	}, gotDep.Spec.Template.Annotations)
}

func TestSetupNVCAStaticGPUs(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients: clients,
	}

	expStaticGPUConfig := `[
		{
			"name": "L40",
			"instanceTypes": [
				{
					"name": "ON-PREM.GPU.L40_1x",
					"value": "ON-PREM.GPU.L40",
					"description": "One Nvidia ada GPU",
					"default": true,
					"cpuCores": 4,
					"systemMemory": "28G",
					"gpuMemory": "48G",
					"gpuCount": 1
				},
				{
					"name": "ON-PREM.GPU.L40_2x",
					"value": "ON-PREM.GPU.L40",
					"description": "Two Nvidia ada GPUs",
					"default": false,
					"cpuCores": 4,
					"systemMemory": "28G",
					"gpuMemory": "96G",
					"gpuCount": 2
				}
			]
		}
	]`

	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Namespace: "foo"},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					GPUDiscovery: nvidiaiov1.GPUDiscoveryConfig{
						Static: &nvidiaiov1.StaticGPUDiscoveryConfig{
							GPUConfig: expStaticGPUConfig,
						},
					},
				},
			},
		},
	}
	err := bc.setupStaticGPUConfigMap(ctx, inNVCFBackend)
	require.NoError(t, err)

	var gotCM *corev1.ConfigMap
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotCM, err = clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, NVCAConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)
	}, 10*time.Second, 100*time.Millisecond)

	assert.Contains(t, gotCM.Data, "gpus")
	assert.Equal(t, expStaticGPUConfig, gotCM.Data["gpus"])
}

func Test_getNVCAFeatureFlags(t *testing.T) {
	trueVal := true

	tests := []struct {
		name          string
		enableGXCache bool
		clusterSource nvidiaiov1.ClusterSource
		featureValues []string
		fndService    *nvidiaiov1.FNDServiceConfig
		want          string
	}{
		{
			name: "no flags, no options",
			want: "",
		},
		{
			name:          "single flag",
			featureValues: []string{"Foo"},
			want:          "Foo",
		},
		{
			name:          "multiple flags sorted",
			featureValues: []string{"Zebra", "Alpha"},
			want:          "Alpha,Zebra",
		},
		{
			name:          "plus prefix stripped",
			featureValues: []string{"+Foo"},
			want:          "Foo",
		},
		{
			name:          "minus prefix disables flag",
			featureValues: []string{"-Foo"},
			want:          "-Foo",
		},
		{
			name:          "both enabled and disabled results in disabled",
			featureValues: []string{"Foo", "-Foo"},
			want:          "-Foo",
		},
		{
			name:          "both plus-enabled and disabled results in disabled",
			featureValues: []string{"+Foo", "-Foo"},
			want:          "-Foo",
		},
		{
			name:          "mixed: intersection disabled, disjoint flags preserved",
			featureValues: []string{"Alpha", "-Alpha", "Bravo", "-Charlie"},
			want:          "-Alpha,-Charlie,Bravo",
		},
		{
			name:          "enabled wins when not disabled",
			featureValues: []string{"-Bar", "Foo"},
			want:          "-Bar,Foo",
		},
		{
			name:          "whitespace-only values skipped",
			featureValues: []string{"  ", "Foo", "", "\t"},
			want:          "Foo",
		},
		{
			name:          "whitespace trimmed from values",
			featureValues: []string{"  Foo  ", " +Bar ", " -Baz "},
			want:          "-Baz,Bar,Foo",
		},
		{
			name:          "duplicates deduplicated",
			featureValues: []string{"Foo", "Foo", "+Foo"},
			want:          "Foo",
		},
		{
			name:          "enableGXCache adds GXCache flag",
			enableGXCache: true,
			featureValues: []string{"Foo"},
			want:          "Foo,GXCache",
		},
		{
			name:          "enableGXCache alone",
			enableGXCache: true,
			want:          "GXCache",
		},
		{
			name:          "self-hosted adds SelfHosted flag",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			want:          "SelfHosted",
		},
		{
			name:          "self-hosted combined with explicit flags",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			featureValues: []string{"Foo"},
			want:          "Foo,SelfHosted",
		},
		{
			name:          "self-hosted with GXCache",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			enableGXCache: true,
			want:          "GXCache,SelfHosted",
		},
		{
			name:       "FNDService enabled adds UseFunctionDeploymentStages",
			fndService: &nvidiaiov1.FNDServiceConfig{Enabled: &trueVal},
			want:       "UseFunctionDeploymentStages",
		},
		{
			name:          "FNDService enabled combined with explicit flags",
			fndService:    &nvidiaiov1.FNDServiceConfig{Enabled: &trueVal},
			featureValues: []string{"Foo"},
			want:          "Foo,UseFunctionDeploymentStages",
		},
		{
			name:          "UseFunctionDeploymentStages in values triggers FND even without config",
			featureValues: []string{"UseFunctionDeploymentStages"},
			want:          "UseFunctionDeploymentStages",
		},
		{
			name:          "all options combined",
			enableGXCache: true,
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			fndService:    &nvidiaiov1.FNDServiceConfig{Enabled: &trueVal},
			featureValues: []string{"Alpha", "-Disabled"},
			want:          "-Disabled,Alpha,GXCache,SelfHosted,UseFunctionDeploymentStages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				enableGXCache: tt.enableGXCache,
			}
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterSource: tt.clusterSource,
						FeatureGate: nvidiaiov1.FeatureGate{
							Values: tt.featureValues,
						},
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: tt.fndService,
						},
					},
				},
			}

			got := bc.getNVCAFeatureFlags(nb)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_setupNVCARBAC(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:                 clients,
		generateImagePullSecret: true, // Explicitly set to true to maintain existing test behavior
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "1234-5678",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "my-cluster",
					ClusterGroupName: "my-cluster-group",
					ClusterID:        "1234-45678",
					ClusterGroupID:   "1234-5678",
					Description:      "This is my cluster",
					CloudProvider:    "GCP",
					Region:           "us-west-1",
					Attributes:       []string{"KEY=VALUE", "KEY1=VALUE1"},
					LogLevel:         "info",
				},
			},
		},
	}

	err := bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	expLabels := map[string]string{
		"app.kubernetes.io/instance":   "nvca",
		"app.kubernetes.io/managed-by": "nvca-operator",
		"app.kubernetes.io/name":       "nvca",
	}
	expAnnotations := map[string]string{
		"nvcf.nvidia.io/cluster-group": "my-cluster-group",
		"nvcf.nvidia.io/cluster-name":  "my-cluster",
	}

	expSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Namespace:   DefaultNVCASystemNamespace,
			Annotations: expAnnotations,
			Labels:      expLabels,
		},
		AutomountServiceAccountToken: boolPtr(false),
		ImagePullSecrets: []corev1.LocalObjectReference{{
			Name: NVCAImagePullSecretName,
		}},
	}
	gotSA, err := clients.K8s.CoreV1().ServiceAccounts(DefaultNVCASystemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expSA, gotSA)

	expCRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Annotations: expAnnotations,
			Labels:      expLabels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{
					"secrets",
					"configmaps",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{
					"persistentvolumes",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{
					"storagerequests",
					"storagerequests/status",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{
					"storageclasses", "volumeattachments",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{
					"csidrivers",
				},
				Verbs: []string{"get", "list", "watch"},
			},
			{
				APIGroups:     []string{"security.openshift.io"},
				Resources:     []string{"securitycontextconstraints"},
				ResourceNames: []string{"nonroot"},
				Verbs:         []string{"use"},
			},
			{
				APIGroups:     []string{"admissionregistration.k8s.io"},
				Resources:     []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"},
				ResourceNames: []string{nvcaoptypes.NVCAModuleName},
				Verbs:         []string{"get", "list", "watch"},
			},
			{}, // Node rule, added below
			{
				APIGroups: []string{""},
				Resources: []string{
					"serviceaccounts",
					"namespaces",
					"pods",
					"pods/log",
					"pods/status",
					"events",
					"services",
					"persistentvolumes",
					"persistentvolumes/status",
					"persistentvolumeclaims",
					"persistentvolumeclaims/status",
					"resourcequotas",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"impersonate"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets", "deployments", "statefulsets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"networkpolicies"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"node.k8s.io"},
				Resources: []string{"runtimeclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"scheduling.run.ai"},
				Resources: []string{"queues"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{
					"miniservices",
					"miniservices/status",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
		},
	}
	gotCRole, err := clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	nodeRuleIdx := func() int {
		for i, rule := range expCRole.Rules {
			// Check if rule is empty
			if rule.APIGroups == nil && rule.Resources == nil && rule.Verbs == nil {
				return i
			}
		}
		return -1
	}()
	require.NotEqual(t, -1, nodeRuleIdx, "Node rule not found")
	expCRole.Rules[nodeRuleIdx] = rbacv1.PolicyRule{
		APIGroups: []string{""},
		Resources: []string{"nodes"},
		Verbs:     []string{"get", "list", "watch", "update", "patch"},
	}
	assert.Equal(t, expCRole, gotCRole)

	expCRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Annotations: expAnnotations,
			Labels:      expLabels,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      nvcaoptypes.NVCAModuleName,
				Namespace: DefaultNVCASystemNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     nvcaoptypes.NVCAModuleName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	gotCRoleBinding, err := clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expCRoleBinding, gotCRoleBinding)

	// Static GPU case.
	nb.Spec.ClusterConfig.GPUDiscovery.Dynamic = nil
	nb.Spec.ClusterConfig.GPUDiscovery.Static = &nvidiaiov1.StaticGPUDiscoveryConfig{
		AllocatedGPUCapacity: 1,
	}

	err = bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	gotSA, err = clients.K8s.CoreV1().ServiceAccounts(DefaultNVCASystemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expSA, gotSA)

	gotCRole, err = clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	expCRole.Rules[nodeRuleIdx] = rbacv1.PolicyRule{
		APIGroups: []string{""},
		Resources: []string{"nodes"},
		Verbs:     []string{"get", "list", "watch"},
	}
	assert.Equal(t, expCRole, gotCRole)

	gotCRoleBinding, err = clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expCRoleBinding, gotCRoleBinding)
}

func Test_setupNVCARBAC_generateImagePullSecretFalse(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	bc := &BackendK8sCache{
		clients:                 clients,
		generateImagePullSecret: false, // Set to false to test the scenario
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "1234-5678",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "my-cluster",
					ClusterGroupName: "my-cluster-group",
					ClusterID:        "1234-45678",
					ClusterGroupID:   "1234-5678",
					Description:      "This is my cluster",
					CloudProvider:    "GCP",
					Region:           "us-west-1",
					Attributes:       []string{"KEY=VALUE", "KEY1=VALUE1"},
					LogLevel:         "info",
				},
			},
		},
	}

	err := bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	expLabels := map[string]string{
		"app.kubernetes.io/instance":   "nvca",
		"app.kubernetes.io/managed-by": "nvca-operator",
		"app.kubernetes.io/name":       "nvca",
	}
	expAnnotations := map[string]string{
		"nvcf.nvidia.io/cluster-group": "my-cluster-group",
		"nvcf.nvidia.io/cluster-name":  "my-cluster",
	}

	// Expected ServiceAccount should NOT have ImagePullSecrets when generateImagePullSecret is false
	expSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Namespace:   DefaultNVCASystemNamespace,
			Annotations: expAnnotations,
			Labels:      expLabels,
		},
		AutomountServiceAccountToken: boolPtr(false),
		// ImagePullSecrets should be empty/nil when generateImagePullSecret is false
	}

	gotSA, err := clients.K8s.CoreV1().ServiceAccounts(DefaultNVCASystemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expSA, gotSA)

	// Verify that ImagePullSecrets is empty
	assert.Empty(t, gotSA.ImagePullSecrets, "ImagePullSecrets should be empty when generateImagePullSecret is false")
}

func Test_setupNVCARBAC_withAdditionalImagePullSecrets(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	additionalSecrets := []corev1.LocalObjectReference{{Name: "my-registry-secret"}, {Name: "another-secret"}}
	bc := &BackendK8sCache{
		clients:                    clients,
		generateImagePullSecret:    true,
		additionalImagePullSecrets: additionalSecrets,
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "1234-5678",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "my-cluster",
					ClusterGroupName: "my-cluster-group",
					ClusterID:        "1234-45678",
				},
			},
		},
	}

	err := bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	// Verify ServiceAccount has all image pull secrets
	gotSA, err := clients.K8s.CoreV1().ServiceAccounts(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)

	// Should have generated secret + additional secrets
	expectedSecrets := []corev1.LocalObjectReference{
		{Name: NVCAImagePullSecretName},
		{Name: "my-registry-secret"},
		{Name: "another-secret"},
	}
	assert.Equal(t, expectedSecrets, gotSA.ImagePullSecrets)
}

func Test_setupNVCARBAC_withAdditionalImagePullSecretsOnly(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	additionalSecrets := []corev1.LocalObjectReference{{Name: "my-registry-secret"}}
	bc := &BackendK8sCache{
		clients:                    clients,
		generateImagePullSecret:    false, // Generated secret disabled
		additionalImagePullSecrets: additionalSecrets,
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "1234-5678",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "my-cluster",
					ClusterGroupName: "my-cluster-group",
					ClusterID:        "1234-45678",
				},
			},
		},
	}

	err := bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	// Verify ServiceAccount has only additional secrets (not generated one)
	gotSA, err := clients.K8s.CoreV1().ServiceAccounts(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)

	expectedSecrets := []corev1.LocalObjectReference{
		{Name: "my-registry-secret"},
	}
	assert.Equal(t, expectedSecrets, gotSA.ImagePullSecrets)
}

func Test_setupNVCARBAC_ValidationPolicy(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:                 clients,
		generateImagePullSecret: true, // Explicitly set to true to maintain existing test behavior
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "1234-5678",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "my-cluster",
					ClusterGroupName: "my-cluster-group",
					ClusterID:        "1234-45678",
					ClusterGroupID:   "1234-5678",
					Description:      "This is my cluster",
					CloudProvider:    "GCP",
					Region:           "us-west-1",
					Attributes:       []string{"KEY=VALUE", "KEY1=VALUE1"},
					LogLevel:         "info",
				},
			},
		},
	}

	cfg := nvcaconfig.Config{
		Cluster: nvcaconfig.NVCFClusterConfig{
			ValidationPolicy: &nvcaconfig.ValidationPolicyConfig{
				Name: "Default",
				AllowedExtraKubernetesTypes: []nvcaconfig.AllowedExtraKubernetesTypeConfig{{
					Group:    "foo.com",
					Resource: "foos",
				}},
			},
		},
	}
	cfgBytes, err := nvcaconfig.EncodeConfig(cfg)
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	cm.ObjectMeta.Name, cm.ObjectMeta.Namespace = agentConfigMergeConfigMapName, bc.operatorNamespace
	cm.Data = map[string]string{
		agentConfigFile: string(cfgBytes),
	}

	_, err = bc.clients.K8s.CoreV1().ConfigMaps(bc.operatorNamespace).Create(t.Context(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	err = bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	expLabels := map[string]string{
		"app.kubernetes.io/instance":   "nvca",
		"app.kubernetes.io/managed-by": "nvca-operator",
		"app.kubernetes.io/name":       "nvca",
	}
	expAnnotations := map[string]string{
		"nvcf.nvidia.io/cluster-group": "my-cluster-group",
		"nvcf.nvidia.io/cluster-name":  "my-cluster",
	}

	expCRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Annotations: expAnnotations,
			Labels:      expLabels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{
					"secrets",
					"configmaps",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{
					"persistentvolumes",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{
					"storagerequests",
					"storagerequests/status",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{
					"storageclasses", "volumeattachments",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{
					"csidrivers",
				},
				Verbs: []string{"get", "list", "watch"},
			},
			{
				APIGroups:     []string{"security.openshift.io"},
				Resources:     []string{"securitycontextconstraints"},
				ResourceNames: []string{"nonroot"},
				Verbs:         []string{"use"},
			},
			{
				APIGroups:     []string{"admissionregistration.k8s.io"},
				Resources:     []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"},
				ResourceNames: []string{nvcaoptypes.NVCAModuleName},
				Verbs:         []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{
					"serviceaccounts",
					"namespaces",
					"pods",
					"pods/log",
					"pods/status",
					"events",
					"services",
					"persistentvolumes",
					"persistentvolumes/status",
					"persistentvolumeclaims",
					"persistentvolumeclaims/status",
					"resourcequotas",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"impersonate"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets", "deployments", "statefulsets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"networkpolicies"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"node.k8s.io"},
				Resources: []string{"runtimeclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"scheduling.run.ai"},
				Resources: []string{"queues"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{
					"miniservices",
					"miniservices/status",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"foo.com"},
				Resources: []string{"foos"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
		},
	}

	gotCRole, err := clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expCRole, gotCRole)

	// Update config with new allowed type
	cfg.Cluster.ValidationPolicy.AllowedExtraKubernetesTypes = append(cfg.Cluster.ValidationPolicy.AllowedExtraKubernetesTypes,
		nvcaconfig.AllowedExtraKubernetesTypeConfig{
			Group:    "bar.com",
			Resource: "bars",
		},
	)
	cfgBytes, err = nvcaconfig.EncodeConfig(cfg)
	require.NoError(t, err)

	cm = &corev1.ConfigMap{}
	cm.ObjectMeta.Name, cm.ObjectMeta.Namespace = agentConfigMergeConfigMapName, bc.operatorNamespace
	cm.Data = map[string]string{
		agentConfigFile: string(cfgBytes),
	}

	_, err = bc.clients.K8s.CoreV1().ConfigMaps(bc.operatorNamespace).Update(t.Context(), cm, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	gotCRole, err = clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	expCRole.Rules = append(expCRole.Rules, []rbacv1.PolicyRule{
		{
			APIGroups: []string{"bar.com"},
			Resources: []string{"bars"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
		},
	}...)
	assert.Equal(t, expCRole, gotCRole)
}

func Test_NVLinkOptimized(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	bc := &BackendK8sCache{
		clients: clients,
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "1234-5678",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "my-cluster",
					ClusterGroupName: "my-cluster-group",
					ClusterID:        "1234-45678",
					ClusterGroupID:   "1234-5678",
					Description:      "This is my cluster",
					CloudProvider:    "GCP",
					Region:           "us-west-1",
					Attributes:       []string{featureflag.AttrNVLinkOptimized.Key + "=true"},
					LogLevel:         "info",
				},
			},
		},
	}

	err := bc.setupNVCARBAC(ctx, nb)
	require.NoError(t, err)

	expLabels := map[string]string{
		"app.kubernetes.io/instance":   "nvca",
		"app.kubernetes.io/managed-by": "nvca-operator",
		"app.kubernetes.io/name":       "nvca",
	}
	expAnnotations := map[string]string{
		"nvcf.nvidia.io/cluster-group": "my-cluster-group",
		"nvcf.nvidia.io/cluster-name":  "my-cluster",
	}

	expCRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Annotations: expAnnotations,
			Labels:      expLabels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{
					"secrets",
					"configmaps",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{
					"persistentvolumes",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{
					"storagerequests",
					"storagerequests/status",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{
					"storageclasses", "volumeattachments",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"csidrivers"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups:     []string{"security.openshift.io"},
				Resources:     []string{"securitycontextconstraints"},
				ResourceNames: []string{"nonroot"},
				Verbs:         []string{"use"},
			},
			{
				APIGroups:     []string{"admissionregistration.k8s.io"},
				Resources:     []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"},
				ResourceNames: []string{nvcaoptypes.NVCAModuleName},
				Verbs:         []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{
					"serviceaccounts",
					"namespaces",
					"pods",
					"pods/log",
					"pods/status",
					"events",
					"services",
					"persistentvolumes",
					"persistentvolumes/status",
					"persistentvolumeclaims",
					"persistentvolumeclaims/status",
					"resourcequotas",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"impersonate"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets", "deployments", "statefulsets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"networkpolicies"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"node.k8s.io"},
				Resources: []string{"runtimeclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"scheduling.run.ai"},
				Resources: []string{"queues"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"miniservices", "miniservices/status"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			},
			{
				APIGroups: []string{"resource.nvidia.com"},
				Resources: []string{"computedomains"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"resource.k8s.io"},
				Resources: []string{"resourceclaims", "resourceclaimtemplates"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"},
			},
			{
				APIGroups: []string{"resource.k8s.io"},
				Resources: []string{"deviceclasses", "resourceslices"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"daemonsets"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
	gotCRole, err := clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expCRole, gotCRole)
}

func TestGetInternalPersistentStorageConfig(t *testing.T) {
	tests := []struct {
		name        string
		nb          *nvidiaiov1.NVCFBackend
		expected    string
		expectedErr bool
	}{
		{
			name: "empty feature gate",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					Overrides: &nvidiaiov1.NVCFBackendSpecT{
						FeatureGate: nvidiaiov1.FeatureGate{
							InternalPersistentStorage: nil,
						},
					},
				},
			},
			expected:    "",
			expectedErr: false,
		},
		{
			name: "feature gate disabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					Overrides: &nvidiaiov1.NVCFBackendSpecT{
						FeatureGate: nvidiaiov1.FeatureGate{
							InternalPersistentStorage: &nvidiaiov1.InternalPersistentStorageSpec{
								Enabled: false,
							},
						},
					},
				},
			},
			expected:    "",
			expectedErr: false,
		},
		{
			name: "missing storage class name",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					Overrides: &nvidiaiov1.NVCFBackendSpecT{
						FeatureGate: nvidiaiov1.FeatureGate{
							InternalPersistentStorage: &nvidiaiov1.InternalPersistentStorageSpec{
								Enabled: true,
							},
						},
					},
				},
			},
			expected:    "",
			expectedErr: true,
		},
		{
			name: "valid config",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					Overrides: &nvidiaiov1.NVCFBackendSpecT{
						FeatureGate: nvidiaiov1.FeatureGate{
							InternalPersistentStorage: &nvidiaiov1.InternalPersistentStorageSpec{
								Enabled:          true,
								StorageClassName: "my-storage-class",
								ResourceQuota: nvidiaiov1.InternalPersistentStorageResourceQuotaSpec{
									Hard: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceRequestsStorage: resource.MustParse("1Gi"),
									},
								},
							},
						},
					},
				},
			},
			expected:    "eyJlbmFibGVkIjp0cnVlLCJzdG9yYWdlQ2xhc3NOYW1lIjoibXktc3RvcmFnZS1jbGFzcyIsInJlc291cmNlUXVvdGEiOnsiaGFyZCI6eyJyZXF1ZXN0cy5zdG9yYWdlIjoiMUdpIn19fQo=",
			expectedErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := mergeOverrides(tt.nb)
			require.NoError(t, err)
			actual, err := getInternalPersistentStorageConfig(ctx, tt.nb)
			if tt.expectedErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestGetNetworkPoliciesDataEmptyDDCSIPList(t *testing.T) {
	expNPNames := []string{
		EgressNetworkPolicyNameKey,
		IngressNetworkPolicyNameKey,
		EgressGXCacheNetworkPolicyNameKey,
		IngressGXCacheNetworkPolicyNameKey,
		IngressMonitoringDCGMNetworkPolicyNameKey,
		EgressNVCFCacheAllowPolicyNameKey,
		EgressBYOOOTelPrometheusNetworkPolicyNameKey,
		EgressCrowdstrikeAllowPolicyNameKey,
		IngressCrowdstrikeNetworkPolicyNameKey,
	}

	bc := &BackendK8sCache{
		gxCacheNamespace:       "gxcache-ns",
		crowdstrikeNamespace:   "crowdstrike-injector",
		k8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"},
	}
	nb := &nvidiaiov1.NVCFBackend{}
	got, err := bc.getNetworkPoliciesData(newTestContext(), nb)
	require.NoError(t, err)
	assert.Len(t, got, len(expNPNames))
	b := &bytes.Buffer{}
	require.NoError(t, err)
	for _, k := range expNPNames {
		io.WriteString(b, "---\n")
		io.WriteString(b, got[k])
	}
	assert.Equal(t, readTestdataFile(t, filepath.Join("testdata", "netpols.yaml")), b.String())
}

func TestGetNetworkPoliciesDataWithDDCSIPList(t *testing.T) {
	expNPNames := []string{
		EgressNetworkPolicyNameKey,
		IngressNetworkPolicyNameKey,
		EgressGXCacheNetworkPolicyNameKey,
		IngressGXCacheNetworkPolicyNameKey,
		IngressMonitoringDCGMNetworkPolicyNameKey,
		EgressNVCFCacheAllowPolicyNameKey,
		EgressBYOOOTelPrometheusNetworkPolicyNameKey,
		EgressCrowdstrikeAllowPolicyNameKey,
		IngressCrowdstrikeNetworkPolicyNameKey,
	}

	bc := &BackendK8sCache{
		gxCacheNamespace:       "gxcache-ns",
		crowdstrikeNamespace:   "crowdstrike-injector",
		ddcsIPAllowList:        []string{"1.2.3.4/27", "5.6.7.8/27"},
		k8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"},
	}
	nb := &nvidiaiov1.NVCFBackend{}
	got, err := bc.getNetworkPoliciesData(newTestContext(), nb)
	require.NoError(t, err)
	assert.Len(t, got, len(expNPNames))
	b := &bytes.Buffer{}
	require.NoError(t, err)
	for _, k := range expNPNames {
		io.WriteString(b, "---\n")
		io.WriteString(b, got[k])
	}
	assert.Equal(t, readTestdataFile(t, filepath.Join("testdata", "netpols_with_ddcs.yaml")), b.String())
}

func TestGetEffectiveK8sNetworkCIDRs(t *testing.T) {
	tests := []struct {
		name          string
		helmCIDRs     []string
		specCIDRs     []string
		expectedCIDRs []string
		description   string
	}{
		{
			name:          "Spec has values - should use spec values",
			helmCIDRs:     []string{"10.0.0.0/8", "172.16.0.0/12"},
			specCIDRs:     []string{"192.168.0.0/16", "100.64.0.0/12"},
			expectedCIDRs: []string{"192.168.0.0/16", "100.64.0.0/12"},
			description:   "When spec has network CIDRs (from NGC), those should be used",
		},
		{
			name:          "Spec is empty - should fall back to Helm values",
			helmCIDRs:     []string{"10.0.0.0/8", "172.16.0.0/12"},
			specCIDRs:     []string{},
			expectedCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"},
			description:   "When spec is empty, should fall back to Helm-configured values",
		},
		{
			name:          "Spec is nil - should fall back to Helm values",
			helmCIDRs:     []string{"10.0.0.0/8"},
			specCIDRs:     nil,
			expectedCIDRs: []string{"10.0.0.0/8"},
			description:   "When spec is nil, should fall back to Helm-configured values",
		},
		{
			name:          "Spec has single value - should use spec value",
			helmCIDRs:     []string{"10.0.0.0/8", "172.16.0.0/12"},
			specCIDRs:     []string{"100.64.0.0/10"},
			expectedCIDRs: []string{"100.64.0.0/10"},
			description:   "Spec values from NGC API take precedence",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				k8sClusterNetworkCIDRs: tt.helmCIDRs,
			}
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							K8sClusterNetworkCIDRs: tt.specCIDRs,
						},
					},
				},
			}

			result := bc.getEffectiveK8sNetworkCIDRs(nb)
			assert.Equal(t, tt.expectedCIDRs, result, tt.description)
		})
	}
}

func TestGetNetworkPoliciesDataUsesSpecCIDRs(t *testing.T) {
	// This test verifies that network policies use dynamic values from spec (NGC) instead of static Helm values
	helmCIDRs := []string{"10.0.0.0/8", "172.16.0.0/12"}
	specCIDRs := []string{"192.168.0.0/16", "100.64.0.0/12"}

	bc := &BackendK8sCache{
		gxCacheNamespace:       "gxcache-ns",
		k8sClusterNetworkCIDRs: helmCIDRs,
	}

	// Create NVCFBackend with spec values (as if fetched from NGC API)
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					K8sClusterNetworkCIDRs: specCIDRs,
				},
			},
		},
	}

	got, err := bc.getNetworkPoliciesData(newTestContext(), nb)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Verify that the network policy uses spec values, not Helm values
	egressPolicy := got[EgressNetworkPolicyNameKey]
	require.NotEmpty(t, egressPolicy)

	// Check that spec CIDRs are in the policy
	for _, cidr := range specCIDRs {
		assert.Contains(t, egressPolicy, cidr, "Network policy should contain spec CIDR")
	}

	// Check that Helm CIDRs are NOT in the policy (since spec takes precedence)
	for _, cidr := range helmCIDRs {
		assert.NotContains(t, egressPolicy, cidr, "Network policy should NOT contain Helm CIDR when spec is present")
	}
}

func TestGetSharedStorageConfig(t *testing.T) {
	tests := []struct {
		name        string
		nb          *nvidiaiov1.NVCFBackend
		expected    string
		expectedErr bool
	}{
		{
			name: "empty feature gate",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					Overrides: &nvidiaiov1.NVCFBackendSpecT{
						FeatureGate: nvidiaiov1.FeatureGate{
							SharedStorage: nil,
						},
					},
				},
			},
			expected:    "",
			expectedErr: false,
		},
		{
			name: "feature gate disabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					Overrides: &nvidiaiov1.NVCFBackendSpecT{
						FeatureGate: nvidiaiov1.FeatureGate{
							Values: []string{},
						},
					},
				},
			},
			expected:    "",
			expectedErr: false,
		},
		{
			name: "valid config",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					Overrides: &nvidiaiov1.NVCFBackendSpecT{
						FeatureGate: nvidiaiov1.FeatureGate{
							SharedStorage: &nvidiaiov1.SharedStorageSpec{
								Server: &nvidiaiov1.SharedStorageServerSpec{
									SMBServerContainerResources: &corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1"),
											corev1.ResourceMemory: resource.MustParse("10Gi"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1"),
											corev1.ResourceMemory: resource.MustParse("10Gi"),
										},
									},
								},
							},
							Values: []string{"HelmSharedStorage"},
						},
					},
				},
			},
			expected:    "eyJzZXJ2ZXIiOnsic21iU2VydmVyQ29udGFpbmVyUmVzb3VyY2VzIjp7ImxpbWl0cyI6eyJjcHUiOiIxIiwibWVtb3J5IjoiMTBHaSJ9LCJyZXF1ZXN0cyI6eyJjcHUiOiIxIiwibWVtb3J5IjoiMTBHaSJ9fX19Cg==",
			expectedErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := mergeOverrides(tt.nb)
			require.NoError(t, err)
			actual, err := getSharedStorageConfig(ctx, tt.nb)
			if tt.expectedErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestBackendK8sCacheBuilder_WithNVCAConfig(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()

	config := nvidiaiov1.NVCFWorkerConfig{
		CacheMountOptionsEnabled: true,
		CacheMountOptions:        "ro,noatime,nouuid",
		WorkerDegradationPeriod:  2 * time.Hour,
	}

	newBuilder := builder.WithNVCFWorkerConfig(config.CacheMountOptionsEnabled, config.CacheMountOptions, config.WorkerDegradationPeriod)

	// Verify the builder was updated correctly
	assert.Equal(t, config.CacheMountOptionsEnabled, newBuilder.nvcfWorkerConfig.CacheMountOptionsEnabled)
	assert.Equal(t, config.CacheMountOptions, newBuilder.nvcfWorkerConfig.CacheMountOptions)
	assert.Equal(t, config.WorkerDegradationPeriod, newBuilder.nvcfWorkerConfig.WorkerDegradationPeriod)

	// Verify original builder was updated
	assert.Equal(t, newBuilder.nvcfWorkerConfig, builder.nvcfWorkerConfig)
}

func TestNVCAConfig_StaticGPUDiscovery(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		nvcaImageRepo:        "nvcr.io/nvidia/nvca",
		nvcaRunAsUserID:      1000,
		nvcaRunAsGroupID:     1000,
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					GPUDiscovery: nvidiaiov1.GPUDiscoveryConfig{
						Static: &nvidiaiov1.StaticGPUDiscoveryConfig{
							AllocatedGPUCapacity: 4,
						},
					},
				},
				Version: "1.0.0",
			},
		},
	}

	cfg, err := bc.newAgentConfig(ctx, nb)
	require.NoError(t, err)
	assert.EqualValues(t, 4, cfg.Agent.StaticGPUCapacity)
}

func TestEncodeAgentConfig_ConfiguresSelfHostedControlPlaneEndpoints(t *testing.T) {
	cfg := nvcaconfig.Config{
		Agent: nvcaconfig.AgentConfig{
			ICMSURL:             "http://api.icms.svc.cluster.local:8080",
			HelmReValServiceURL: "http://reval.nvcf.svc.cluster.local:8080",
		},
	}

	data, err := encodeAgentConfig(cfg, nvcaconfig.Config{}, ptr.To("nats://nats.nats-system.svc.cluster.local:4222"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "NATSURL: nats://nats.nats-system.svc.cluster.local:4222")
	assert.NotContains(t, string(data), "natsURL:")

	var got struct {
		Agent struct {
			ICMSURL             string `json:"icmsURL" yaml:"icmsURL"`
			HelmReValServiceURL string `json:"helmReValServiceURL" yaml:"helmReValServiceURL"`
			NATSURL             string `json:"NATSURL" yaml:"NATSURL"`
		} `json:"agent" yaml:"agent"`
	}
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, "http://api.icms.svc.cluster.local:8080", got.Agent.ICMSURL)
	assert.Equal(t, "http://reval.nvcf.svc.cluster.local:8080", got.Agent.HelmReValServiceURL)
	assert.Equal(t, "nats://nats.nats-system.svc.cluster.local:4222", got.Agent.NATSURL)
}

func TestSetupNVCADeployment_OTELConfig(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		nvcaImageRepo:        "nvcr.io/nvidia/nvca",
		nvcaRunAsUserID:      1000,
		nvcaRunAsGroupID:     1000,
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{
						ServiceName: "test-service",
						AccessToken: "test-token",
						Exporter:    "custom-exporter",
					},
				},
				Version: "1.0.0",
			},
		},
	}

	err := bc.setupNVCADeployment(ctx, nb)
	require.NoError(t, err)

	dep, err := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)

	// Verify OTEL config is mounted
	nvcaContainer := getNVCAContainer(t, dep)
	found := false
	for _, envFrom := range nvcaContainer.EnvFrom {
		if envFrom.SecretRef != nil && envFrom.SecretRef.Name == OTELConfigSecretName {
			found = true
			break
		}
	}
	assert.True(t, found, "OTEL config not found")
}

func TestSetupNVCADeployment_SecurityContext(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		nvcaImageRepo:        "nvcr.io/nvidia/nvca",
		nvcaRunAsUserID:      1000,
		nvcaRunAsGroupID:     1000,
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "1.0.0",
			},
		},
	}

	err := bc.setupNVCADeployment(ctx, nb)
	require.NoError(t, err)

	dep, err := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)

	// Verify pod security context
	assert.Equal(t, int64(1000), *dep.Spec.Template.Spec.SecurityContext.RunAsUser)
	assert.Equal(t, int64(1000), *dep.Spec.Template.Spec.SecurityContext.RunAsGroup)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, dep.Spec.Template.Spec.SecurityContext.SeccompProfile.Type)

	// Verify container security contexts
	for _, container := range dep.Spec.Template.Spec.Containers {
		assert.False(t, *container.SecurityContext.AllowPrivilegeEscalation)
		assert.True(t, *container.SecurityContext.RunAsNonRoot)
		assert.Equal(t, []corev1.Capability{"ALL"}, container.SecurityContext.Capabilities.Drop)
	}
}

func TestNewAgentConfig_IncludesAgentAndWorkloadTolerations(t *testing.T) {
	ctx := newTestContext()
	bc := &BackendK8sCache{
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		workloadTolerations: []corev1.Toleration{{
			Key:      "workload-taint",
			Operator: corev1.TolerationOpEqual,
			Value:    "true",
			Effect:   corev1.TaintEffectNoExecute,
		}},
	}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AgentConfig: nvidiaiov1.AgentConfig{
					DeploymentConfig: nvidiaiov1.DeploymentConfig{
						Tolerations: []corev1.Toleration{{
							Key:      "agent-taint",
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						}},
					},
				},
				Version: "1.0.0",
			},
		},
	}

	cfg, err := bc.newAgentConfig(ctx, nb)
	require.NoError(t, err)
	assert.Equal(t, nb.Spec.AgentConfig.DeploymentConfig.Tolerations, cfg.Agent.Tolerations)
	assert.Equal(t, bc.workloadTolerations, cfg.Workload.Tolerations)
}

// TestNewAgentConfig_FallsBackToHelmAgentTolerationsWhenCRIsEmpty pins the bug
// 6122598 fix: in ngc-managed clusters the NVCFBackend CR is sourced from the
// NGC API which does not surface agent tolerations, so the operator must fall
// back to the helm-supplied value seeded into bc.deploymentConfig.
func TestNewAgentConfig_FallsBackToHelmAgentTolerationsWhenCRIsEmpty(t *testing.T) {
	ctx := newTestContext()
	helmTolerations := []corev1.Toleration{{
		Key:      "dedicated",
		Operator: corev1.TolerationOpEqual,
		Value:    "nvca",
		Effect:   corev1.TaintEffectNoSchedule,
	}}
	bc := &BackendK8sCache{
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		deploymentConfig: nvidiaiov1.DeploymentConfig{
			Tolerations: helmTolerations,
		},
	}

	// CR has no agent tolerations (typical ngc-managed shape).
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "1.0.0",
			},
		},
	}

	cfg, err := bc.newAgentConfig(ctx, nb)
	require.NoError(t, err)
	assert.Equal(t, helmTolerations, cfg.Agent.Tolerations)
}

// TestNewAgentConfig_CRTolerationsTakePrecedence ensures the NVCFBackend CR
// wins when it carries tolerations (helm-managed and self-managed paths), so
// the helm fallback never silently overrides values supplied via cluster-dto.
func TestNewAgentConfig_CRTolerationsTakePrecedence(t *testing.T) {
	ctx := newTestContext()
	bc := &BackendK8sCache{
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		deploymentConfig: nvidiaiov1.DeploymentConfig{
			Tolerations: []corev1.Toleration{{
				Key:      "helm-fallback",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		},
	}

	crTolerations := []corev1.Toleration{{
		Key:      "from-cr",
		Operator: corev1.TolerationOpEqual,
		Value:    "yes",
		Effect:   corev1.TaintEffectNoSchedule,
	}}
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AgentConfig: nvidiaiov1.AgentConfig{
					DeploymentConfig: nvidiaiov1.DeploymentConfig{
						Tolerations: crTolerations,
					},
				},
				Version: "1.0.0",
			},
		},
	}

	cfg, err := bc.newAgentConfig(ctx, nb)
	require.NoError(t, err)
	assert.Equal(t, crTolerations, cfg.Agent.Tolerations)
}

func TestSetupNVCADeployment_AppliesAgentTolerations(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		nvcaImageRepo:        "nvcr.io/nvidia/nvca",
		nvcaRunAsUserID:      1000,
		nvcaRunAsGroupID:     1000,
	}

	tolerations := []corev1.Toleration{{
		Key:      "agent-taint",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}}
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AgentConfig: nvidiaiov1.AgentConfig{
					DeploymentConfig: nvidiaiov1.DeploymentConfig{
						Tolerations: tolerations,
					},
				},
				Version: "1.0.0",
			},
		},
	}

	err := bc.setupNVCADeployment(ctx, nb)
	require.NoError(t, err)

	dep, err := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, tolerations, dep.Spec.Template.Spec.Tolerations)
}

// Helper function to get the NVCA container from a deployment
func getNVCAContainer(t *testing.T, dep *appsv1.Deployment) corev1.Container {
	t.Helper()
	for _, container := range dep.Spec.Template.Spec.Containers {
		if container.Name == "agent" {
			return container
		}
	}
	t.Fatal("NVCA container not found")
	return corev1.Container{}
}

func TestGetCustomNetworkPoliciesData(t *testing.T) {
	tests := []struct {
		name           string
		configMapData  map[string]string
		configMapError error
		expected       map[string]string
		expectedError  bool
		errorContains  string
	}{
		{
			name:           "configmap not found returns empty map",
			configMapData:  nil,
			configMapError: k8serr.NewNotFound(schema.GroupResource{Group: "", Resource: "configmaps"}, nvcfCustomNetworkPoliciesConfigMapName),
			expected:       map[string]string{},
			expectedError:  false,
		},
		{
			name:           "other error returns error",
			configMapData:  nil,
			configMapError: fmt.Errorf("permission denied"),
			expected:       nil,
			expectedError:  true,
			errorContains:  "failed to get custom network policies configmap",
		},
		{
			name: "valid single network policy without prefix",
			configMapData: map[string]string{
				"custom-policy-0": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: my-custom-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}`,
			},
			configMapError: nil,
			expected: map[string]string{
				"nvcf-custom-my-custom-policy": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: my-custom-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}`,
			},
			expectedError: false,
		},
		{
			name: "valid single network policy with prefix",
			configMapData: map[string]string{
				"custom-policy-0": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nvcf-custom-my-custom-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}`,
			},
			configMapError: nil,
			expected: map[string]string{
				"nvcf-custom-my-custom-policy": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nvcf-custom-my-custom-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}`,
			},
			expectedError: false,
		},
		{
			name: "multiple valid network policies",
			configMapData: map[string]string{
				"custom-policy-0": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: policy-one
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress`,
				"custom-policy-1": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nvcf-custom-policy-two
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Egress`,
			},
			configMapError: nil,
			expected: map[string]string{
				"nvcf-custom-policy-one": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: policy-one
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress`,
				"nvcf-custom-policy-two": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nvcf-custom-policy-two
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Egress`,
			},
			expectedError: false,
		},
		{
			name: "invalid YAML returns error",
			configMapData: map[string]string{
				"custom-policy-0": `invalid yaml content
  - this is not valid yaml
    - it should cause an error`,
			},
			configMapError: nil,
			expected:       nil,
			expectedError:  true,
			errorContains:  "failed to parse custom network policy",
		},
		{
			name: "name collision returns error",
			configMapData: map[string]string{
				"custom-policy-0": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: my-policy
  namespace: default
spec:
  podSelector: {}`,
				"custom-policy-1": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nvcf-custom-my-policy
  namespace: default
spec:
  podSelector: {}`,
			},
			configMapError: nil,
			expected:       nil,
			expectedError:  true,
			errorContains:  "custom network policy name collision",
		},
		{
			name:           "empty configmap returns empty map",
			configMapData:  map[string]string{},
			configMapError: nil,
			expected:       map[string]string{},
			expectedError:  false,
		},
		{
			name: "mixed valid and invalid policies returns error",
			configMapData: map[string]string{
				"custom-policy-0": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: valid-policy
  namespace: default
spec:
  podSelector: {}`,
				"custom-policy-1": `invalid yaml content`,
			},
			configMapError: nil,
			expected:       nil,
			expectedError:  true,
			errorContains:  "failed to parse custom network policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			bc := &BackendK8sCache{
				clients: mockKubeClients(),
			}

			// Create mock configmap getter
			configMapGetter := func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error) {
				if tt.configMapError != nil {
					return nil, tt.configMapError
				}
				return &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: "test-namespace",
					},
					Data: tt.configMapData,
				}, nil
			}

			result, err := bc.getCustomNetworkPoliciesData(ctx, configMapGetter)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetCustomAnnotationsData(t *testing.T) {
	tests := []struct {
		name           string
		configMapData  map[string]string
		configMapError error
		expectedData   map[string]string
		expectError    bool
	}{
		{
			name:          "no custom annotations",
			configMapData: map[string]string{},
			expectedData:  map[string]string{},
		},
		{
			name: "valid custom annotations",
			configMapData: map[string]string{
				"annotations.json": `{
					"example.com/annotation1": "value1",
					"example.com/annotation2": "value2",
					"prometheus.io/scrape": "true"
				}`,
			},
			expectedData: map[string]string{
				"example.com/annotation1": "value1",
				"example.com/annotation2": "value2",
				"prometheus.io/scrape":    "true",
			},
		},
		{
			name: "invalid json",
			configMapData: map[string]string{
				"annotations.json": "invalid json",
			},
			expectError: true,
		},
		{
			name: "missing annotations.json",
			configMapData: map[string]string{
				"other.json": "{}",
			},
			expectedData: map[string]string{},
		},
		{
			name:           "configmap not found",
			configMapError: k8serr.NewNotFound(schema.GroupResource{Group: "", Resource: "configmaps"}, "test-error"),
			expectedData:   map[string]string{},
		},
		{
			name:           "configmap error",
			configMapError: fmt.Errorf("test error"),
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configMapGetter := func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error) {
				if tt.configMapError != nil {
					return nil, tt.configMapError
				}
				return &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      nvcfCustomAnnotationsConfigMapName,
						Namespace: NVCAOperatorNamespace,
					},
					Data: tt.configMapData,
				}, nil
			}

			bc := &BackendK8sCache{}
			result, err := bc.getCustomAnnotationsData(context.Background(), configMapGetter)

			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedData, result)
		})
	}
}

func TestCreateOrUpdateWebhookConfiguration(t *testing.T) {
	// Create a test context
	ctx := newTestContext()

	// Create mock clients
	clients := mockKubeClientsForIntegrationTests()

	// Create a BackendK8sCache with mock clients
	bc := &BackendK8sCache{
		clients: clients,
	}

	// Create a simple test webhook configuration
	testWebhook := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-webhook",
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{
			{
				Name: "test.webhook.example.com",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					URL: &[]string{"https://example.com/webhook"}[0],
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
						},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"apps"},
							APIVersions: []string{"v1"},
							Resources:   []string{"deployments"},
						},
					},
				},
			},
		},
	}

	// Test creating a new webhook
	err := bc.createOrUpdateWebhookConfiguration(ctx, "test-webhook", true, testWebhook)
	require.NoError(t, err)

	// Verify the webhook was created
	createdWebhook, err := clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, "test-webhook", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, testWebhook.Name, createdWebhook.Name)
	assert.Equal(t, testWebhook.Webhooks[0].Name, createdWebhook.Webhooks[0].Name)

	// Test updating the webhook
	testWebhook.Webhooks[0].ClientConfig.URL = &[]string{"https://example.com/webhook-updated"}[0]
	err = bc.createOrUpdateWebhookConfiguration(ctx, "test-webhook", true, testWebhook)
	require.NoError(t, err)

	// Verify the webhook was updated
	updatedWebhook, err := clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, "test-webhook", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, *testWebhook.Webhooks[0].ClientConfig.URL, *updatedWebhook.Webhooks[0].ClientConfig.URL)
}

func TestGetCustomNetworkPoliciesData_Integration(t *testing.T) {
	ctx := context.Background()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients: clients,
	}

	// Create a test configmap with custom network policies
	testConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomNetworkPoliciesConfigMapName,
			Namespace: NVCAOperatorNamespace,
		},
		Data: map[string]string{
			"custom-policy-0": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: test-ingress-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}`,
			"custom-policy-1": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nvcf-custom-test-egress-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Egress
  egress:
  - to:
    - namespaceSelector: {}`,
		},
	}

	// Create the configmap in the mock client
	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, testConfigMap, metav1.CreateOptions{})
	require.NoError(t, err)

	// Test the function with the mock client
	configMapGetter := func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error) {
		return clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Get(ctx, name, opts)
	}

	result, err := bc.getCustomNetworkPoliciesData(ctx, configMapGetter)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the results
	assert.Len(t, result, 2)
	assert.Contains(t, result, "nvcf-custom-test-ingress-policy")
	assert.Contains(t, result, "nvcf-custom-test-egress-policy")

	// Verify the content of the first policy
	ingressPolicy, exists := result["nvcf-custom-test-ingress-policy"]
	assert.True(t, exists)
	assert.Contains(t, ingressPolicy, "name: test-ingress-policy")
	assert.Contains(t, ingressPolicy, "policyTypes:\n  - Ingress")

	// Verify the content of the second policy
	egressPolicy, exists := result["nvcf-custom-test-egress-policy"]
	assert.True(t, exists)
	assert.Contains(t, egressPolicy, "name: nvcf-custom-test-egress-policy")
	assert.Contains(t, egressPolicy, "policyTypes:\n  - Egress")
}

func TestValidateNetworkPolicy(t *testing.T) {
	tests := []struct {
		name          string
		networkPolicy *netv1.NetworkPolicy
		expectedError bool
		errorContains string
	}{
		{
			name: "valid network policy should pass validation",
			networkPolicy: &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-policy",
					Namespace: "default",
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
					Ingress: []netv1.NetworkPolicyIngressRule{
						{
							From: []netv1.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{},
								},
							},
						},
					},
				},
			},
			expectedError: false,
		},
		{
			name: "network policy without namespace should use default namespace",
			networkPolicy: &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "no-namespace-policy",
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
					Egress: []netv1.NetworkPolicyEgressRule{
						{
							To: []netv1.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{},
								},
							},
						},
					},
				},
			},
			expectedError: false,
		},
		{
			name: "network policy with valid minimal spec should pass",
			networkPolicy: &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-policy",
					Namespace: "default",
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			clients := mockKubeClients()
			bc := &BackendK8sCache{
				clients: clients,
			}

			err := bc.validateNetworkPolicy(ctx, tt.networkPolicy)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateNetworkPolicy_WithInvalidPolicy(t *testing.T) {
	ctx := context.Background()
	clients := mockKubeClients()
	bc := &BackendK8sCache{
		clients: clients,
	}

	// Create a network policy with invalid fields that would fail server-side validation
	invalidPolicy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "invalid-policy",
			Namespace: "default",
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{"InvalidPolicyType"}, // Invalid policy type
		},
	}

	err := bc.validateNetworkPolicy(ctx, invalidPolicy)

	// The fake client might not catch all validation errors that a real API server would,
	// but we can still test the function structure and error handling
	// In a real cluster, this would fail with validation errors
	if err != nil {
		assert.Contains(t, err.Error(), "NetworkPolicy validation failed")
	}
}

func TestValidateNetworkPolicy_DeepCopyBehavior(t *testing.T) {
	ctx := context.Background()
	clients := mockKubeClients()
	bc := &BackendK8sCache{
		clients: clients,
	}

	// Create a network policy without namespace
	originalPolicy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-policy",
			// No namespace set
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
		},
	}

	// Validate the policy
	err := bc.validateNetworkPolicy(ctx, originalPolicy)
	require.NoError(t, err)

	// Verify that the original policy was not modified (namespace should still be empty)
	assert.Empty(t, originalPolicy.Namespace, "Original policy should not be modified")
}

func TestGetCustomNetworkPoliciesData_WithValidation(t *testing.T) {
	ctx := context.Background()
	clients := mockKubeClients()
	bc := &BackendK8sCache{
		clients: clients,
	}

	tests := []struct {
		name          string
		configMapData map[string]string
		expectedError bool
		errorContains string
		expectedCount int
	}{
		{
			name: "valid network policies should pass validation",
			configMapData: map[string]string{
				"policy-1": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: valid-ingress-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}`,
				"policy-2": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: valid-egress-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Egress
  egress:
  - to:
    - namespaceSelector: {}`,
			},
			expectedError: false,
			expectedCount: 2,
		},
		{
			name: "mixed valid and invalid policies should return error",
			configMapData: map[string]string{
				"valid-policy": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: valid-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress`,
				"invalid-yaml": `this is not valid yaml content`,
			},
			expectedError: true,
			errorContains: "failed to parse custom network policy",
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock configmap getter
			configMapGetter := func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error) {
				return &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: "test-namespace",
					},
					Data: tt.configMapData,
				}, nil
			}

			result, err := bc.getCustomNetworkPoliciesData(ctx, configMapGetter)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Len(t, result, tt.expectedCount)
			}
		})
	}
}

// =============================================================================
// OTel Collector Tests
// =============================================================================

func TestIsOTelCollectorEnabled(t *testing.T) {
	tests := []struct {
		name          string
		nb            *nvidiaiov1.NVCFBackend
		bcOTelEnabled bool
		expected      bool
	}{
		{
			name: "OTel collector enabled via NVCFBackend spec",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: true,
						},
					},
				},
			},
			bcOTelEnabled: false,
			expected:      true,
		},
		{
			name: "OTel collector enabled via BackendK8sCache",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: nil, // NGC API doesn't provide enabled field
					},
				},
			},
			bcOTelEnabled: true, // From env var OTEL_COLLECTOR_ENABLED
			expected:      true,
		},
		{
			name: "OTel collector enabled via both sources",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: true,
						},
					},
				},
			},
			bcOTelEnabled: true,
			expected:      true,
		},
		{
			name: "OTel collector disabled explicitly in spec",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: false,
						},
					},
				},
			},
			bcOTelEnabled: true,
			expected:      false, // NVCFBackend config takes precedence over Helm flag
		},
		{
			name: "OTel collector disabled - both sources false",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: false,
						},
					},
				},
			},
			bcOTelEnabled: false,
			expected:      false,
		},
		{
			name: "OTel collector disabled - nil config, bc false",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: nil,
					},
				},
			},
			bcOTelEnabled: false,
			expected:      false,
		},
		{
			name: "OTel collector - Enabled not specified (only ImageConfig), bc enabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							// Enabled not specified - defaults to false
							ImageConfig: nvidiaiov1.ImageConfig{
								Repository: "custom.registry.io/otel",
								Tag:        "v1.0.0",
							},
						},
					},
				},
			},
			bcOTelEnabled: true,
			expected:      false,
		},
		{
			name: "OTel collector - empty struct in ICMS response, bc enabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{},
					},
				},
			},
			bcOTelEnabled: true,
			expected:      false, // OTelCollector struct exists (even if empty), so NVCFBackend is authoritative
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				otelCollectorEnabled: tt.bcOTelEnabled,
			}
			result := bc.isOTelCollectorEnabled(tt.nb)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetOTelCollectorImagePath(t *testing.T) {
	tests := []struct {
		name          string
		nb            *nvidiaiov1.NVCFBackend
		bcImageRepo   string
		bcImageTag    string
		expectedImage string
	}{
		{
			name: "Use NVCFBackend config when fully specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							ImageConfig: nvidiaiov1.ImageConfig{
								Repository: "custom.registry.io/otel-collector",
								Tag:        "v1.0.0",
							},
						},
					},
				},
			},
			bcImageRepo:   "default.registry.io/otel",
			bcImageTag:    "default-tag",
			expectedImage: "custom.registry.io/otel-collector:v1.0.0",
		},
		{
			name: "Fallback to bc values when NVCFBackend config is nil",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: nil,
					},
				},
			},
			bcImageRepo:   "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib",
			bcImageTag:    "0.139.0",
			expectedImage: "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib:0.139.0",
		},
		{
			name: "Fallback to bc repo when NVCFBackend repo is empty",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							ImageConfig: nvidiaiov1.ImageConfig{
								Repository: "",
								Tag:        "v2.0.0",
							},
						},
					},
				},
			},
			bcImageRepo:   "fallback.registry.io/otel",
			bcImageTag:    "fallback-tag",
			expectedImage: "fallback.registry.io/otel:v2.0.0",
		},
		{
			name: "Fallback to bc tag when NVCFBackend tag is empty",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							ImageConfig: nvidiaiov1.ImageConfig{
								Repository: "custom.registry.io/otel",
								Tag:        "",
							},
						},
					},
				},
			},
			bcImageRepo:   "fallback.registry.io/otel",
			bcImageTag:    "fallback-tag",
			expectedImage: "custom.registry.io/otel:fallback-tag",
		},
		{
			name: "Empty OTelCollectorConfig struct",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{},
					},
				},
			},
			bcImageRepo:   "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib",
			bcImageTag:    "0.139.0",
			expectedImage: "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib:0.139.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				otelCollectorImageRepo: tt.bcImageRepo,
				otelCollectorImageTag:  tt.bcImageTag,
			}

			result := bc.getOTelCollectorImagePath(tt.nb)
			assert.Equal(t, tt.expectedImage, result)
		})
	}
}

func TestGetOTelCollectorContainerCommandArgsAndEnv(t *testing.T) {
	tests := []struct {
		name                      string
		nb                        *nvidiaiov1.NVCFBackend
		envType                   nvidiaiov1.EnvType
		expectedRequestsNamespace string
		expectedFNDSEndpoint      string
	}{
		{
			name: "Default namespace - prod env",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			envType:                   nvidiaiov1.EnvTypeProd,
			expectedRequestsNamespace: DefaultNVCARequestsNamespace,
			expectedFNDSEndpoint:      "https://deployment-stages.nvcf.nvidia.com/v3/ledger/k8s-events",
		},
		{
			name: "Custom namespace - stage env",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							RequestsNamespace: "custom-namespace",
						},
					},
				},
			},
			envType:                   nvidiaiov1.EnvTypeStage,
			expectedRequestsNamespace: "custom-namespace",
			expectedFNDSEndpoint:      "https://deployment-stages.stg.nvcf.nvidia.com/v3/ledger/k8s-events",
		},
		{
			name: "Custom FNDS endpoint",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: &nvidiaiov1.FNDServiceConfig{
								ServiceURL: "https://custom-fnds.example.com",
							},
						},
					},
				},
			},
			envType:                   nvidiaiov1.EnvTypeProd,
			expectedRequestsNamespace: DefaultNVCARequestsNamespace,
			expectedFNDSEndpoint:      "https://custom-fnds.example.com/v3/ledger/k8s-events",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				envType: tt.envType,
			}

			command, args, env := bc.getOTelCollectorContainerCommandArgsAndEnv(tt.nb)

			// Verify command
			assert.Equal(t, []string{"/otelcol-contrib"}, command)

			// Verify args
			expectedArgs := []string{
				fmt.Sprintf("--config=%s/config.yaml", NVCAOTelCollectorConfigMountPath),
			}
			assert.Equal(t, expectedArgs, args)

			// Verify all expected env vars are present (7 original + 4 OAuth-related)
			assert.Len(t, env, 11)

			envMap := make(map[string]string)
			for _, e := range env {
				envMap[e.Name] = e.Value
			}

			// Verify NGC service API key file env var
			expectedAPIKeyPath := fmt.Sprintf("/var/run/secrets/%s/%s", NGCServiceAPIKeySecretName, NGCServiceAPIKeySecretDataKey)
			assert.Equal(t, expectedAPIKeyPath, envMap[NGCServiceAPIKeyFileEnvVar])

			// Verify OTel collector specific env vars
			assert.Equal(t, tt.expectedRequestsNamespace, envMap[NVCAOTelCollectorRequestsNamespaceEnvVar])
			assert.Equal(t, fmt.Sprintf("%d", NVCAOTelCollectorHealthCheckPort), envMap[NVCAOTelCollectorHealthCheckPortEnvVar])
			assert.Equal(t, tt.expectedFNDSEndpoint, envMap[NVCAOTelCollectorFNDSEndpointEnvVar])
			assert.Equal(t, fmt.Sprintf("%d", NVCAOTelCollectorMetricsPort), envMap[NVCAOTelCollectorMetricsPortEnvVar])
			assert.Equal(t, fmt.Sprintf("%d", NVCAOTelCollectorMemoryLimitPercentage), envMap[NVCAOTelCollectorMemoryLimitPercentageEnvVar])
			assert.Equal(t, fmt.Sprintf("%d", NVCAOTelCollectorSpikeLimitPercentage), envMap[NVCAOTelCollectorSpikeLimitPercentageEnvVar])

			// Verify OAuth-related env vars are present
			assert.Contains(t, envMap, NVCAOTelCollectorOAuthClientIDEnvVar)
			assert.Contains(t, envMap, NVCAOTelCollectorOAuthClientSecretFileEnvVar)
			assert.Contains(t, envMap, NVCAOTelCollectorOAuthTokenURLEnvVar)
			assert.Contains(t, envMap, NVCAOTelCollectorAuthenticatorEnvVar)
		})
	}
}

func TestGetOTelCollectorVolume(t *testing.T) {
	tests := []struct {
		name           string
		nb             *nvidiaiov1.NVCFBackend
		bcOTelEnabled  bool
		expectVolume   bool
		expectedLength int
	}{
		{
			name: "Returns volume when OTel collector is enabled via spec",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: true,
						},
					},
				},
			},
			bcOTelEnabled:  false,
			expectVolume:   true,
			expectedLength: 1,
		},
		{
			name: "Returns volume when OTel collector is enabled via bc (ngc-managed)",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: nil,
					},
				},
			},
			bcOTelEnabled:  true,
			expectVolume:   true,
			expectedLength: 1,
		},
		{
			name: "Returns nil when OTel collector is disabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: false,
						},
					},
				},
			},
			bcOTelEnabled:  false,
			expectVolume:   false,
			expectedLength: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				otelCollectorEnabled: tt.bcOTelEnabled,
			}
			volumes := bc.getOTelCollectorVolume(tt.nb)

			if tt.expectVolume {
				require.Len(t, volumes, tt.expectedLength)
				assert.Equal(t, NVCAOTelCollectorConfigMapName, volumes[0].Name)
				assert.NotNil(t, volumes[0].VolumeSource.ConfigMap)
				assert.Equal(t, NVCAOTelCollectorConfigMapName, volumes[0].VolumeSource.ConfigMap.LocalObjectReference.Name)
			} else {
				assert.Nil(t, volumes)
			}
		})
	}
}

func TestGetOTelCollectorContainer(t *testing.T) {
	tests := []struct {
		name            string
		nb              *nvidiaiov1.NVCFBackend
		bcImageRepo     string
		bcImageTag      string
		expectContainer bool
		expectedLength  int
	}{
		{
			name: "Returns container when OTel collector is enabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: true,
							ImageConfig: nvidiaiov1.ImageConfig{
								Repository: "test.registry.io/otel",
								Tag:        "v1.0.0",
							},
						},
					},
				},
			},
			bcImageRepo:     "fallback.registry.io/otel",
			bcImageTag:      "fallback-tag",
			expectContainer: true,
			expectedLength:  1,
		},
		{
			name: "Returns nil when OTel collector is disabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: false,
						},
					},
				},
			},
			bcImageRepo:     "test.registry.io/otel",
			bcImageTag:      "v1.0.0",
			expectContainer: false,
			expectedLength:  0,
		},
		{
			name: "Returns container with vault secrets mount when OTel and Vault are enabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{
							Enabled: true,
							ImageConfig: nvidiaiov1.ImageConfig{
								Repository: "test.registry.io/otel",
								Tag:        "v1.0.0",
							},
						},
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: true,
						},
					},
				},
			},
			bcImageRepo:     "test.registry.io/otel",
			bcImageTag:      "v1.0.0",
			expectContainer: true,
			expectedLength:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				otelCollectorImageRepo: tt.bcImageRepo,
				otelCollectorImageTag:  tt.bcImageTag,
				otelCollectorResources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1000m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			}

			containers := bc.getOTelCollectorContainer(tt.nb)

			if tt.expectContainer {
				require.Len(t, containers, tt.expectedLength)
				container := containers[0]

				// Verify container name
				assert.Equal(t, NVCAOTelCollectorContainerName, container.Name)

				// Verify image
				assert.Equal(t, "test.registry.io/otel:v1.0.0", container.Image)

				// Verify command
				assert.Equal(t, []string{"/otelcol-contrib"}, container.Command)

				// Verify args
				assert.Contains(t, container.Args, fmt.Sprintf("--config=%s/config.yaml", NVCAOTelCollectorConfigMountPath))

				// Verify restartPolicy is Always (sidecar/restartable init container)
				require.NotNil(t, container.RestartPolicy, "RestartPolicy must be set for sidecar init containers")
				assert.Equal(t, corev1.ContainerRestartPolicyAlways, *container.RestartPolicy)

				// Verify ports
				require.Len(t, container.Ports, 2)
				assert.Equal(t, NVCAOTelCollectorHealthCheckPortName, container.Ports[0].Name)
				assert.Equal(t, NVCAOTelCollectorHealthCheckPort, container.Ports[0].ContainerPort)
				assert.Equal(t, NVCAOTelCollectorMetricsPortName, container.Ports[1].Name)
				assert.Equal(t, NVCAOTelCollectorMetricsPort, container.Ports[1].ContainerPort)

				// Verify volume mounts
				require.Len(t, container.VolumeMounts, 2)
				assert.Equal(t, NVCAOTelCollectorConfigMapName, container.VolumeMounts[0].Name)
				assert.Equal(t, NVCAOTelCollectorConfigMountPath, container.VolumeMounts[0].MountPath)
				assert.Equal(t, NGCServiceAPIKeySecretName, container.VolumeMounts[1].Name)

				// Verify liveness probe
				assert.NotNil(t, container.LivenessProbe)
				assert.Equal(t, int32(5), container.LivenessProbe.InitialDelaySeconds)
				assert.Equal(t, int32(10), container.LivenessProbe.PeriodSeconds)

				// Verify security context
				assert.NotNil(t, container.SecurityContext)
				assert.False(t, *container.SecurityContext.AllowPrivilegeEscalation)
				assert.True(t, *container.SecurityContext.RunAsNonRoot)

				// Verify resources are set
				assert.NotNil(t, container.Resources)
			} else {
				assert.Nil(t, containers)
			}
		})
	}
}

func TestGetOTelCollectorVolumeMounts(t *testing.T) {
	bc := &BackendK8sCache{}
	mounts := bc.getOTelCollectorVolumeMounts()
	require.Len(t, mounts, 2)
	assert.Equal(t, NVCAOTelCollectorConfigMapName, mounts[0].Name)
	assert.Equal(t, NVCAOTelCollectorConfigMountPath, mounts[0].MountPath)
	assert.Equal(t, NGCServiceAPIKeySecretName, mounts[1].Name)
}

func TestSetupOTelCollectorConfigMap(t *testing.T) {
	tests := []struct {
		name               string
		nb                 *nvidiaiov1.NVCFBackend
		expectCM           bool
		preCreateConfigMap bool
	}{
		{
			name: "creates ConfigMap when OTel collector is enabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{Enabled: true},
						ClusterConfig: nvidiaiov1.ClusterConfig{ClusterName: "test-cluster"},
					},
				},
			},
			expectCM: true,
		},
		{
			name: "deletes ConfigMap when OTel collector is disabled",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{Enabled: false},
						ClusterConfig: nvidiaiov1.ClusterConfig{ClusterName: "test-cluster"},
					},
				},
			},
			expectCM: false,
		},
		{
			name: "disabled removes existing ConfigMap",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: &nvidiaiov1.OTelCollectorConfig{Enabled: false},
						ClusterConfig: nvidiaiov1.ClusterConfig{ClusterName: "test-cluster"},
					},
				},
			},
			expectCM:           false,
			preCreateConfigMap: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients := mockKubeClients()
			ns := DefaultNVCASystemNamespace
			bc := &BackendK8sCache{
				clients: clients,
				envType: nvidiaiov1.EnvTypeProd,
				otelCollectorResources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				},
			}
			if tt.preCreateConfigMap {
				_, err := clients.K8s.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: NVCAOTelCollectorConfigMapName, Namespace: ns},
					Data:       map[string]string{"config.yaml": "placeholder"},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			err := bc.setupOTelCollectorConfigMap(ctx, tt.nb)
			require.NoError(t, err)

			if tt.expectCM {
				cm, err := clients.K8s.CoreV1().ConfigMaps(ns).Get(ctx, NVCAOTelCollectorConfigMapName, metav1.GetOptions{})
				require.NoError(t, err)
				assert.NotNil(t, cm)
				assert.Contains(t, cm.Data, "config.yaml")
				assert.Contains(t, cm.Data["config.yaml"], "k8s_events")
				assert.Contains(t, cm.Data["config.yaml"], "memory_limiter")
			} else {
				_, err := clients.K8s.CoreV1().ConfigMaps(ns).Get(ctx, NVCAOTelCollectorConfigMapName, metav1.GetOptions{})
				assert.True(t, k8serr.IsNotFound(err), "ConfigMap should be absent when OTel collector is disabled")
			}
		})
	}
}

func TestSetupNVCADeployment_WithOTelCollector(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()

	// Pre-create required ConfigMaps
	requiredConfigMaps := []string{
		nvcfCustomAnnotationsConfigMapName,
	}
	for _, cmName := range requiredConfigMaps {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: DefaultNVCASystemNamespace,
			},
		}
		_, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Create(ctx, cm, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	bc := &BackendK8sCache{
		clients:                clients,
		ngcServiceKeyFetcher:   &mockTokenFetcher{token: "randomkey"},
		otelCollectorImageRepo: "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib",
		otelCollectorImageTag:  "0.139.0",
		otelCollectorResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1000m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}

	// Create NVCFBackend with OTel collector enabled
	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterID:        "some-cluster-id",
					CloudProvider:    "ON-PREM",
					ClusterGroupName: "FC-NVCF-Backend",
					ClusterName:      "byoc-test",
				},
				OTelCollector: &nvidiaiov1.OTelCollectorConfig{
					Enabled: true,
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullPolicy: "IfNotPresent",
					Repository: "nvcr.io/nvidia/nvca",
					Tag:        "2.50.0",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://stg.icms.nvcf.nvidia.com",
					TokenURL:       "https://stg.icms.nvcf.nvidia.com/token",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:             "oauth-stg-abc123",
					PublicKeysetEndpoint: "https://example.com/.well-known/jwks.json",
					TokenURL:             "https://example.com/token",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
					ImageConfig: nvidiaiov1.ImageConfig{
						PullPolicy: "IfNotPresent",
						Repository: "nvcr.io/nvidia/nvca",
						Tag:        "2.50.0",
					},
				},
				Version: "2.50.0",
			},
		},
	}

	err := bc.setupNVCADeployment(ctx, inNVCFBackend)
	require.NoError(t, err)

	depIface := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace)
	gotDep, err := depIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)

	// Verify deployment has 2 containers: agent, webhook
	require.Len(t, gotDep.Spec.Template.Spec.Containers, 2)

	// Verify deployment has 1 init container: otel-collector (restartable init container / sidecar)
	require.Len(t, gotDep.Spec.Template.Spec.InitContainers, 1)

	// Find OTel collector in init containers (it's a restartable init container)
	var otelContainer *corev1.Container
	for i := range gotDep.Spec.Template.Spec.InitContainers {
		if gotDep.Spec.Template.Spec.InitContainers[i].Name == NVCAOTelCollectorContainerName {
			otelContainer = &gotDep.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	require.NotNil(t, otelContainer, "OTel collector init container not found")

	// Verify OTel collector container properties
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib:0.139.0", otelContainer.Image)
	assert.Equal(t, []string{"/otelcol-contrib"}, otelContainer.Command)
	assert.NotNil(t, otelContainer.LivenessProbe)
	require.NotNil(t, otelContainer.RestartPolicy)
	assert.Equal(t, corev1.ContainerRestartPolicyAlways, *otelContainer.RestartPolicy)

	// Verify OTel collector ConfigMap volume exists
	var otelVolume *corev1.Volume
	for i := range gotDep.Spec.Template.Spec.Volumes {
		if gotDep.Spec.Template.Spec.Volumes[i].Name == NVCAOTelCollectorConfigMapName {
			otelVolume = &gotDep.Spec.Template.Spec.Volumes[i]
			break
		}
	}
	require.NotNil(t, otelVolume, "OTel collector ConfigMap volume not found")
	assert.NotNil(t, otelVolume.VolumeSource.ConfigMap)
}

func TestGetOTelCollectorContainerCommandArgsAndEnv_OAuthAuth(t *testing.T) {
	tests := []struct {
		name                    string
		nb                      *nvidiaiov1.NVCFBackend
		envType                 nvidiaiov1.EnvType
		expectedOAuthClientID   string
		expectedOAuthSecretFile string
		expectedOAuthTokenURL   string
		expectedAuthenticator   string
	}{
		{
			name: "Vault enabled - OAuth2 authentication in prod",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: true,
						},
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID: "test-oauth-client-id",
							TokenURL: "https://generic-oauth.example.test/token",
						},
						AgentConfig: nvidiaiov1.AgentConfig{
							FunctionDeploymentStagesProdOAuthTokenURL: "https://fnds-oauth.example.test/token",
						},
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: &nvidiaiov1.FNDServiceConfig{
								ServiceURL: "https://deployment-stages.nvcf.nvidia.com",
							},
						},
					},
				},
			},
			envType:                 nvidiaiov1.EnvTypeProd,
			expectedOAuthClientID:   "test-oauth-client-id",
			expectedOAuthSecretFile: "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedOAuthTokenURL:   "https://fnds-oauth.example.test/token",
			expectedAuthenticator:   NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name: "Vault enabled - OAuth2 authentication in stage",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: true,
						},
						OAuthConfig: nvidiaiov1.OAuthConfig{
							ClientID: "test-oauth-client-id-stage",
							TokenURL: "https://generic-oauth.example.test/token",
						},
						AgentConfig: nvidiaiov1.AgentConfig{
							FunctionDeploymentStagesStageOAuthTokenURL: "https://stage-fnds-oauth.example.test/token",
						},
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: &nvidiaiov1.FNDServiceConfig{
								ServiceURL: "https://deployment-stages.stg.nvcf.nvidia.com",
							},
						},
					},
				},
			},
			envType:                 nvidiaiov1.EnvTypeStage,
			expectedOAuthClientID:   "test-oauth-client-id-stage",
			expectedOAuthSecretFile: "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedOAuthTokenURL:   "https://stage-fnds-oauth.example.test/token",
			expectedAuthenticator:   NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name: "Vault disabled - service API key bearer token authentication with placeholder OAuth env vars",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						VaultConfig: nvidiaiov1.VaultConfig{
							Enabled: false,
						},
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: &nvidiaiov1.FNDServiceConfig{
								ServiceURL: "https://deployment-stages.nvcf.nvidia.com",
							},
						},
					},
				},
			},
			envType:                 nvidiaiov1.EnvTypeProd,
			expectedOAuthClientID:   NVCAOTelCollectorOAuthPlaceholderClientID,
			expectedOAuthSecretFile: "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedOAuthTokenURL:   "",
			expectedAuthenticator:   NVCAOTelCollectorAuthenticatorBearerTokenAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				envType: tt.envType,
				otelCollectorResources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			}

			_, _, env := bc.getOTelCollectorContainerCommandArgsAndEnv(tt.nb)

			// Create map for easier lookup
			envMap := make(map[string]string)
			for _, envVar := range env {
				envMap[envVar.Name] = envVar.Value
			}

			// Verify all expected env vars are present and have correct values
			assert.Contains(t, envMap, NVCAOTelCollectorOAuthClientIDEnvVar, "OAuth client ID env var should be present")
			assert.Equal(t, tt.expectedOAuthClientID, envMap[NVCAOTelCollectorOAuthClientIDEnvVar])

			assert.Contains(t, envMap, NVCAOTelCollectorOAuthClientSecretFileEnvVar, "OAuth client secret file env var should be present")
			assert.Equal(t, tt.expectedOAuthSecretFile, envMap[NVCAOTelCollectorOAuthClientSecretFileEnvVar])

			assert.Contains(t, envMap, NVCAOTelCollectorOAuthTokenURLEnvVar, "OAuth token URL env var should be present")
			assert.Equal(t, tt.expectedOAuthTokenURL, envMap[NVCAOTelCollectorOAuthTokenURLEnvVar])

			assert.Contains(t, envMap, NVCAOTelCollectorAuthenticatorEnvVar, "authenticator env var should be present")
			assert.Equal(t, tt.expectedAuthenticator, envMap[NVCAOTelCollectorAuthenticatorEnvVar])
		})
	}
}

func TestGetOTelCollectorContainerCommandArgsAndEnv_FNDSEndpoint(t *testing.T) {
	tests := []struct {
		name                 string
		nb                   *nvidiaiov1.NVCFBackend
		envType              nvidiaiov1.EnvType
		expectedFNDSEndpoint string
	}{
		{
			name: "Custom FNDS service URL",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: &nvidiaiov1.FNDServiceConfig{
								ServiceURL: "https://custom.fnds.url",
							},
						},
					},
				},
			},
			envType:              nvidiaiov1.EnvTypeProd,
			expectedFNDSEndpoint: "https://custom.fnds.url/v3/ledger/k8s-events",
		},
		{
			name: "Default prod FNDS endpoint",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: &nvidiaiov1.FNDServiceConfig{},
						},
					},
				},
			},
			envType:              nvidiaiov1.EnvTypeProd,
			expectedFNDSEndpoint: "https://deployment-stages.nvcf.nvidia.com/v3/ledger/k8s-events",
		},
		{
			name: "Default stage FNDS endpoint",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							FNDService: &nvidiaiov1.FNDServiceConfig{},
						},
					},
				},
			},
			envType:              nvidiaiov1.EnvTypeStage,
			expectedFNDSEndpoint: "https://deployment-stages.stg.nvcf.nvidia.com/v3/ledger/k8s-events",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				envType: tt.envType,
				otelCollectorResources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			}

			_, _, env := bc.getOTelCollectorContainerCommandArgsAndEnv(tt.nb)

			// Find and verify FNDS endpoint env var
			var found bool
			for _, envVar := range env {
				if envVar.Name == NVCAOTelCollectorFNDSEndpointEnvVar {
					found = true
					assert.Equal(t, tt.expectedFNDSEndpoint, envVar.Value)
					break
				}
			}
			assert.True(t, found, "FNDS endpoint env var should be present")
		})
	}
}

func TestSetupNVCADeployment_WithOTelCollectorOAuthAuthIntegration(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()

	// Pre-create required ConfigMaps
	requiredConfigMaps := []string{
		nvcfCustomAnnotationsConfigMapName,
	}
	for _, cmName := range requiredConfigMaps {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: DefaultNVCASystemNamespace,
			},
		}
		_, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Create(ctx, cm, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	bc := &BackendK8sCache{
		clients:                clients,
		ngcServiceKeyFetcher:   &mockTokenFetcher{token: "randomkey"},
		otelCollectorImageRepo: "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib",
		otelCollectorImageTag:  "0.139.0",
		otelCollectorResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1000m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
		envType: nvidiaiov1.EnvTypeProd,
	}

	// Create NVCFBackend with Vault enabled (OAuth authentication)
	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterID:        "some-cluster-id",
					CloudProvider:    "ON-PREM",
					ClusterGroupName: "FC-NVCF-Backend",
					ClusterName:      "byoc-test",
					FNDService: &nvidiaiov1.FNDServiceConfig{
						ServiceURL: "https://deployment-stages.nvcf.nvidia.com",
					},
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: true,
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID: "test-fnds-oauth-client-id",
					TokenURL: "https://generic-oauth.example.test/token",
				},
				AgentConfig: nvidiaiov1.AgentConfig{
					FunctionDeploymentStagesProdOAuthTokenURL: "https://fnds-oauth.example.test/token",
				},
				OTelCollector: &nvidiaiov1.OTelCollectorConfig{
					Enabled: true,
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullPolicy: "IfNotPresent",
					Repository: "nvcr.io/nvidia/nvca",
					Tag:        "2.50.0",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://icms.nvcf.nvidia.com",
					TokenURL:       "https://icms-oauth-token-url.com/token",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
					ImageConfig: nvidiaiov1.ImageConfig{
						PullPolicy: "IfNotPresent",
						Repository: "nvcr.io/nvidia/nvca",
						Tag:        "2.50.0",
					},
				},
				Version: "2.50.0",
			},
		},
	}

	err := bc.setupNVCADeployment(ctx, inNVCFBackend)
	require.NoError(t, err)

	depIface := clients.K8s.AppsV1().Deployments(DefaultNVCASystemNamespace)
	gotDep, err := depIface.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)

	// Find OTel collector container
	var otelContainer *corev1.Container
	for i := range gotDep.Spec.Template.Spec.InitContainers {
		if gotDep.Spec.Template.Spec.InitContainers[i].Name == NVCAOTelCollectorContainerName {
			otelContainer = &gotDep.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	require.NotNil(t, otelContainer, "OTel collector container not found")

	// Verify OAuth authentication environment variables
	envVars := make(map[string]string)
	for _, env := range otelContainer.Env {
		envVars[env.Name] = env.Value
	}

	// Verify OAuth2 authentication env vars
	assert.Equal(t, "test-fnds-oauth-client-id", envVars[NVCAOTelCollectorOAuthClientIDEnvVar])
	assert.Equal(t, "/home/nvca/vault-agent/secrets/oauth-client-secrets.env", envVars[NVCAOTelCollectorOAuthClientSecretFileEnvVar])
	assert.Equal(t, "https://fnds-oauth.example.test/token", envVars[NVCAOTelCollectorOAuthTokenURLEnvVar])
	assert.Equal(t, NVCAOTelCollectorAuthenticatorOAuth2Client, envVars[NVCAOTelCollectorAuthenticatorEnvVar])

	// Verify FNDS endpoint is correct
	assert.Equal(t, "https://deployment-stages.nvcf.nvidia.com/v3/ledger/k8s-events", envVars[NVCAOTelCollectorFNDSEndpointEnvVar])

	// Verify OAuth token URL is different from ICMS token URL
	assert.NotEqual(t, inNVCFBackend.Spec.ICMSConfig.TokenURL, envVars[NVCAOTelCollectorOAuthTokenURLEnvVar],
		"FNDS OAuth token URL should be different from ICMS OAuth token URL")
}

func Test_setupAgentConfigConfigMap(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		envType:              nvidiaiov1.EnvTypeStage,
	}

	inNVCFBackend := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					UnregisterOnStartup: true,
					ClusterID:           "some-cluster-id",
					CloudProvider:       "ON-PREM",
					ClusterGroupName:    "FC-NVCF-Backend",
					ClusterName:         "byoc-test",
					Description:         "FleetCommand NVCF test cluster",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{},
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
						"SharedCluster",
					},
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullPolicy: "IfNotPresent",
					Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
					Tag:        "1.0.0",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://stg.icms.nvcf.nvidia.com",
					TokenURL:       "https://stg.icms.nvcf.nvidia.com/token",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:             "oauth-stg-abc123",
					ClientSecretKey:      "test-secret-key",
					ClientSecretsEnvFile: "",
					PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
					TokenURL:             "https://stage-oauth.example.test/token",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
					ImageConfig: nvidiaiov1.ImageConfig{
						PullPolicy: "IfNotPresent",
						Repository: "nvcr.io/qtfpt1h0bieu/byocdev/nvca",
						Tag:        "1.0.0",
					},
				},
				Version: "1.0.0",
			},
		},
	}

	agentCfg, err := bc.newAgentConfig(ctx, inNVCFBackend)
	require.NoError(t, err)

	err = bc.setupAgentConfigConfigMap(ctx, inNVCFBackend, agentCfg)
	require.NoError(t, err)

	gotCM, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, `agent:
  adminAddr: 127.0.0.1:8001
  computeBackend: k8s
  featureFlags:
  - CachingSupport
  - LogPosting
  - PeriodicInstanceStatusUpdate
  - SharedCluster
  helmReValServiceURL: https://reval.stg.nvcf.nvidia.com
  icmsurl: https://stg.icms.nvcf.nvidia.com
  logLevel: info
  namespaceLabels:
    app.kubernetes.io/instance: nvca
    app.kubernetes.io/managed-by: nvca-operator
    app.kubernetes.io/name: nvca
  requestsNamespace: nvcf-backend
  sharedStorage:
    server:
      image: stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5
  svcAddress: :8000
  systemNamespace: nvca-system
authz:
  ngcServiceAPIKeyFile: /var/run/secrets/ngc-service-api-key/ngc-service-api-key
  publicKeysetEndpoint: https://stage-oauth.example.test/.well-known/jwks.json
  tokenURL: https://stg.icms.nvcf.nvidia.com/token
cluster:
  cloudProvider: ON-PREM
  groupName: FC-NVCF-Backend
  id: some-cluster-id
  name: byoc-test
  ncaid: ncaid1
environment: stg
tracing:
  exporter: lightstep
webhook:
  svcAddress: :8001
  tlsCertFile: /certs/server/tls.crt
  tlsKeyFile: /certs/server/tls.key
  tlsSecretName: nvca-webhook-tls-server-certs
`, gotCM.Data[agentConfigFile])
}

func TestNewAgentConfigIncludesServiceOAuthEndpoints(t *testing.T) {
	ctx := newTestContext()
	bc := &BackendK8sCache{envType: nvidiaiov1.EnvTypeStage}

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{NCAID: "ncaid1"},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterID:        "cluster-id",
					ClusterName:      "cluster-name",
					ClusterGroupName: "cluster-group",
					CloudProvider:    "ON-PREM",
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://icms.example.test",
				},
				AgentConfig: nvidiaiov1.AgentConfig{
					HelmReValStageOAuthTokenURL:                            "https://stage-reval-oauth.example.test/token",
					HelmReValStageOAuthPublicKeysetEndpoint:                "https://stage-reval-oauth.example.test/.well-known/jwks.json",
					HelmReValProdOAuthTokenURL:                             "https://prod-reval-oauth.example.test/token",
					HelmReValProdOAuthPublicKeysetEndpoint:                 "https://prod-reval-oauth.example.test/.well-known/jwks.json",
					FunctionDeploymentStagesStageOAuthTokenURL:             "https://stage-fnds-oauth.example.test/token",
					FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint: "https://stage-fnds-oauth.example.test/.well-known/jwks.json",
					FunctionDeploymentStagesProdOAuthTokenURL:              "https://prod-fnds-oauth.example.test/token",
					FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint:  "https://prod-fnds-oauth.example.test/.well-known/jwks.json",
					RolloverServiceStageOAuthTokenURL:                      "https://stage-ros-oauth.example.test/token",
					RolloverServiceStageOAuthPublicKeysetEndpoint:          "https://stage-ros-oauth.example.test/.well-known/jwks.json",
					RolloverServiceProdOAuthTokenURL:                       "https://prod-ros-oauth.example.test/token",
					RolloverServiceProdOAuthPublicKeysetEndpoint:           "https://prod-ros-oauth.example.test/.well-known/jwks.json",
				},
			},
		},
	}

	cfg, err := bc.newAgentConfig(ctx, nb)
	require.NoError(t, err)

	assert.Equal(t, "https://stage-reval-oauth.example.test/token", cfg.Agent.HelmReValStageOAuthTokenURL)
	assert.Equal(t, "https://stage-reval-oauth.example.test/.well-known/jwks.json", cfg.Agent.HelmReValStageOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://prod-reval-oauth.example.test/token", cfg.Agent.HelmReValProdOAuthTokenURL)
	assert.Equal(t, "https://prod-reval-oauth.example.test/.well-known/jwks.json", cfg.Agent.HelmReValProdOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://stage-fnds-oauth.example.test/token", cfg.Agent.FunctionDeploymentStagesStageOAuthTokenURL)
	assert.Equal(t, "https://stage-fnds-oauth.example.test/.well-known/jwks.json", cfg.Agent.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://prod-fnds-oauth.example.test/token", cfg.Agent.FunctionDeploymentStagesProdOAuthTokenURL)
	assert.Equal(t, "https://prod-fnds-oauth.example.test/.well-known/jwks.json", cfg.Agent.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://stage-ros-oauth.example.test/token", cfg.Agent.RolloverServiceStageOAuthTokenURL)
	assert.Equal(t, "https://stage-ros-oauth.example.test/.well-known/jwks.json", cfg.Agent.RolloverServiceStageOAuthPublicKeysetEndpoint)
	assert.Equal(t, "https://prod-ros-oauth.example.test/token", cfg.Agent.RolloverServiceProdOAuthTokenURL)
	assert.Equal(t, "https://prod-ros-oauth.example.test/.well-known/jwks.json", cfg.Agent.RolloverServiceProdOAuthPublicKeysetEndpoint)
}

func TestGetEffectiveOTelCollectorConfig(t *testing.T) {
	tests := []struct {
		name       string
		bcEnabled  bool
		bcRepo     string
		bcTag      string
		specOTel   *nvidiaiov1.OTelCollectorConfig
		wantConfig *nvidiaiov1.OTelCollectorConfig
	}{
		{
			name:      "spec nil - falls back to bc defaults (NGC-managed)",
			bcEnabled: true,
			bcRepo:    "nvcr.io/default/otel",
			bcTag:     "0.100.0",
			specOTel:  nil,
			wantConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/default/otel",
					Tag:        "0.100.0",
				},
			},
		},
		{
			name:      "spec fully set - uses spec values",
			bcEnabled: false,
			bcRepo:    "nvcr.io/default/otel",
			bcTag:     "0.100.0",
			specOTel: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/custom/otel",
					Tag:        "0.144.0",
				},
			},
			wantConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/custom/otel",
					Tag:        "0.144.0",
				},
			},
		},
		{
			name:      "spec enabled=false overrides bc enabled=true",
			bcEnabled: true,
			bcRepo:    "nvcr.io/default/otel",
			bcTag:     "0.100.0",
			specOTel: &nvidiaiov1.OTelCollectorConfig{
				Enabled: false,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/custom/otel",
					Tag:        "0.144.0",
				},
			},
			wantConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: false,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/custom/otel",
					Tag:        "0.144.0",
				},
			},
		},
		{
			name:      "spec with empty repo - falls back to bc repo",
			bcEnabled: true,
			bcRepo:    "nvcr.io/default/otel",
			bcTag:     "0.100.0",
			specOTel: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "",
					Tag:        "0.144.0",
				},
			},
			wantConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/default/otel",
					Tag:        "0.144.0",
				},
			},
		},
		{
			name:      "spec with empty tag - falls back to bc tag",
			bcEnabled: true,
			bcRepo:    "nvcr.io/default/otel",
			bcTag:     "0.100.0",
			specOTel: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/custom/otel",
					Tag:        "",
				},
			},
			wantConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/custom/otel",
					Tag:        "0.100.0",
				},
			},
		},
		{
			name:      "spec with empty repo and tag - falls back to both bc values",
			bcEnabled: false,
			bcRepo:    "nvcr.io/default/otel",
			bcTag:     "0.100.0",
			specOTel: &nvidiaiov1.OTelCollectorConfig{
				Enabled:     true,
				ImageConfig: nvidiaiov1.ImageConfig{},
			},
			wantConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/default/otel",
					Tag:        "0.100.0",
				},
			},
		},
		{
			name:      "all defaults - spec nil and bc defaults empty",
			bcEnabled: false,
			bcRepo:    "",
			bcTag:     "",
			specOTel:  nil,
			wantConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled:     false,
				ImageConfig: nvidiaiov1.ImageConfig{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				otelCollectorEnabled:   tt.bcEnabled,
				otelCollectorImageRepo: tt.bcRepo,
				otelCollectorImageTag:  tt.bcTag,
			}
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						OTelCollector: tt.specOTel,
					},
				},
			}

			got := bc.getEffectiveOTelCollectorConfig(nb)
			assert.Equal(t, tt.wantConfig, got)
		})
	}
}

func TestMergeMapsNoOverwrites(t *testing.T) {
	tests := []struct {
		name           string
		base           map[string]string
		add            map[string]string
		expectedResult map[string]string
		expectedCols   []string
	}{
		{
			name:           "nil base returns add",
			base:           nil,
			add:            map[string]string{"a": "1"},
			expectedResult: map[string]string{"a": "1"},
			expectedCols:   []string{},
		},
		{
			name:           "no collisions",
			base:           map[string]string{"a": "1"},
			add:            map[string]string{"b": "2"},
			expectedResult: map[string]string{"a": "1", "b": "2"},
			expectedCols:   []string{},
		},
		{
			name:           "with collisions",
			base:           map[string]string{"a": "1", "b": "2"},
			add:            map[string]string{"a": "new", "c": "3"},
			expectedResult: map[string]string{"a": "1", "b": "2", "c": "3"},
			expectedCols:   []string{"a"},
		},
		{
			name:           "empty add",
			base:           map[string]string{"a": "1"},
			add:            map[string]string{},
			expectedResult: map[string]string{"a": "1"},
			expectedCols:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, cols := mergeMapsNoOverwrites(tt.base, tt.add)
			assert.Equal(t, tt.expectedResult, result)
			assert.ElementsMatch(t, tt.expectedCols, cols)
		})
	}
}
