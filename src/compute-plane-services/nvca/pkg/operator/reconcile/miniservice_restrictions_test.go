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

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
)

func testBoolPtr(b bool) *bool {
	return &b
}

func TestSetupNVCAMiniServiceInfra(t *testing.T) {
	logger := logrus.New()
	ctx := context.WithValue(context.Background(), logrus.StandardLogger(), logger)

	tests := []struct {
		name          string
		nvcfBackend   *nvidiaiov1.NVCFBackend
		expectedError bool
	}{
		{
			name: "successful setup",
			nvcfBackend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName:      "test-cluster",
							ClusterGroupName: "test-group",
						},
					},
				},
			},
			expectedError: false,
		},
		{
			name: "miniservice disabled",
			nvcfBackend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName:      "test-cluster",
							ClusterGroupName: "test-group",
						},
					},
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = nvidiaiov1.AddToScheme(s)
			_ = corev1.AddToScheme(s)
			_ = admissionregistrationv1.AddToScheme(s)

			k8sClient := fake.NewSimpleClientset()
			clients := &kubeclients.KubeClients{
				K8s: k8sClient,
			}

			bc := &BackendK8sCache{
				clients: clients,
			}

			webhookCert := WebhookCert{
				CACertBytes: []byte("test-ca-cert"),
			}

			err := bc.setupNVCAMiniServiceInfra(ctx, tt.nvcfBackend, webhookCert)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSetupMiniServiceRBACConfigmap(t *testing.T) {
	logger := logrus.New()
	ctx := context.WithValue(context.Background(), logrus.StandardLogger(), logger)

	tests := []struct {
		name          string
		nvcfBackend   *nvidiaiov1.NVCFBackend
		expectedError bool
	}{
		{
			name: "successful setup",
			nvcfBackend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName:      "test-cluster",
							ClusterGroupName: "test-group",
						},
					},
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = nvidiaiov1.AddToScheme(s)
			_ = corev1.AddToScheme(s)

			k8sClient := fake.NewSimpleClientset()
			clients := &kubeclients.KubeClients{
				K8s: k8sClient,
			}

			bc := &BackendK8sCache{
				clients: clients,
			}

			err := bc.setupMiniServiceRBACConfigmap(ctx, tt.nvcfBackend)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSetupMiniServiceValidatingWebhook(t *testing.T) {
	logger := logrus.New()
	ctx := context.WithValue(context.Background(), logrus.StandardLogger(), logger)

	tests := []struct {
		name          string
		nvcfBackend   *nvidiaiov1.NVCFBackend
		expectedError bool
	}{
		{
			name: "successful setup",
			nvcfBackend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							ClusterName:      "test-cluster",
							ClusterGroupName: "test-group",
						},
					},
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = nvidiaiov1.AddToScheme(s)
			_ = corev1.AddToScheme(s)
			_ = admissionregistrationv1.AddToScheme(s)

			k8sClient := fake.NewSimpleClientset()
			clients := &kubeclients.KubeClients{
				K8s: k8sClient,
			}

			bc := &BackendK8sCache{
				clients: clients,
			}

			webhookCert := WebhookCert{
				CACertBytes: []byte("test-ca-cert"),
			}

			err := bc.setupMiniServiceValidatingWebhook(ctx, tt.nvcfBackend, webhookCert)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetMiniServiceRBACCmData(t *testing.T) {
	const expRoleBase = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: mini-service-restrictions
  labels:
    app.kubernetes.io/version: "1.0"
    app.kubernetes.io/instance: nvca
    app.kubernetes.io/managed-by: nvca-operator
    app.kubernetes.io/name: nvca
rules:
- apiGroups: [""]
  resources:
  - configmaps
  - persistentvolumeclaims
  - pods
  - pods/status
  - pods/log
  - secrets
  - serviceaccounts
  - services
  verbs: ["get", "list", "watch", "create", "update", "delete", "patch"]
- apiGroups: ["apps"]
  resources: ["deployments", "replicasets", "statefulsets"]
  verbs: ["get", "list", "watch", "create", "update", "delete", "patch"]
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["roles", "rolebindings"]
  verbs: ["get", "list", "watch", "create", "update", "delete", "patch"]
- apiGroups: ["batch"]
  resources: ["jobs", "cronjobs"]
  verbs: ["get", "list", "watch", "create", "update", "delete", "patch"]
`

	tests := []struct {
		name          string
		createCfgCM   bool
		cfg           nvcaconfig.Config
		expRole       string
		expectedError bool
	}{
		{
			name:          "default",
			expRole:       expRoleBase,
			expectedError: false,
		},
		{
			name:        "validation policy",
			createCfgCM: true,
			cfg: nvcaconfig.Config{
				Cluster: nvcaconfig.NVCFClusterConfig{
					ValidationPolicy: &nvcaconfig.ValidationPolicyConfig{
						Name: "Default",
						AllowedExtraKubernetesTypes: []nvcaconfig.AllowedExtraKubernetesTypeConfig{
							{
								Group:    "foo.com",
								Resource: "foos",
							},
						},
					},
				},
			},
			expRole: expRoleBase + `- apiGroups: ["foo.com"]
  resources: ["foos"]
  verbs: ["get", "list", "watch", "create", "update", "delete", "patch"]
`,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fake.NewSimpleClientset()
			clients := &kubeclients.KubeClients{
				K8s: k8sClient,
			}
			bc := &BackendK8sCache{
				clients:           clients,
				operatorNamespace: NVCAOperatorNamespace,
			}

			if tt.createCfgCM {
				cfgBytes, err := nvcaconfig.EncodeConfig(tt.cfg)
				require.NoError(t, err)

				cm := &corev1.ConfigMap{}
				cm.ObjectMeta.Name, cm.ObjectMeta.Namespace = agentConfigMergeConfigMapName, bc.operatorNamespace
				cm.Data = map[string]string{
					agentConfigFile: string(cfgBytes),
				}

				_, err = bc.clients.K8s.CoreV1().ConfigMaps(bc.operatorNamespace).Create(t.Context(), cm, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			data, err := bc.getMiniServiceRBACCmData(t.Context(), &nvidiaiov1.NVCFBackend{})
			if tt.expectedError {
				assert.Error(t, err)
			} else if assert.NoError(t, err) {
				assert.NotNil(t, data)
				assert.Contains(t, data, MiniServicesPermissionsRoleName)
				assert.Equal(t, tt.expRole, data[MiniServicesPermissionsRoleName])
			}
		})
	}
}
