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
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

func TestContainsString(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		s        string
		expected bool
	}{
		{
			name:     "empty slice",
			slice:    []string{},
			s:        "test",
			expected: false,
		},
		{
			name:     "string exists",
			slice:    []string{"a", "b", "c"},
			s:        "b",
			expected: true,
		},
		{
			name:     "string doesn't exist",
			slice:    []string{"a", "b", "c"},
			s:        "d",
			expected: false,
		},
		{
			name:     "empty string search",
			slice:    []string{"a", "", "c"},
			s:        "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsString(tt.slice, tt.s)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetImagePullSecretDockerConfigJSONFromNGCKey(t *testing.T) {
	tests := []struct {
		name           string
		registryServer string
		accessKey      string
		expectError    bool
	}{
		{
			name:           "valid NGC key",
			registryServer: "nvcr.io",
			accessKey:      "test-access-key",
			expectError:    false,
		},
		{
			name:           "empty registry server",
			registryServer: "",
			accessKey:      "test-key",
			expectError:    false,
		},
		{
			name:           "empty access key",
			registryServer: "nvcr.io",
			accessKey:      "",
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := getImagePullSecretDockerConfigJSONFromNGCKey(tt.registryServer, tt.accessKey)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, data)

				// Verify it's valid JSON
				var config RegistryConfig
				err = json.Unmarshal(data, &config)
				assert.NoError(t, err)
				assert.Contains(t, config.Auths, tt.registryServer)
				assert.Equal(t, "$oauthtoken", config.Auths[tt.registryServer].Username)
				assert.Equal(t, tt.accessKey, config.Auths[tt.registryServer].Password)
			}
		})
	}
}

func TestGetAppLabels(t *testing.T) {
	labels := getAppLabels()
	assert.NotEmpty(t, labels)
	assert.Equal(t, nvcaoptypes.NVCAModuleName, labels[InstanceLabelKey])
	assert.Equal(t, NVCAOperatorName, labels[ManagedbyLabelKey])
	assert.Equal(t, nvcaoptypes.NVCAModuleName, labels[NameLabelKey])
}

func TestGetNBAnnotations(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "test-cluster",
					ClusterGroupName: "test-group",
				},
			},
		},
	}

	annotations := getNBAnnotations(nb)
	assert.NotEmpty(t, annotations)
	assert.Equal(t, "test-cluster", annotations[ClusterName])
	assert.Equal(t, "test-group", annotations[ClusterGroupKey])
}

func TestBackendK8sCache_CreateOrUpdateNamespace(t *testing.T) {
	ctx := context.Background()

	t.Run("create new namespace", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ns",
				Labels: map[string]string{
					"test": "label",
				},
			},
		}

		err := bc.createOrUpdateNamespace(ctx, ns)
		assert.NoError(t, err)

		// Verify namespace was created
		created, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-ns", created.Name)
		assert.Equal(t, "label", created.Labels["test"])
	})

	t.Run("update existing namespace", func(t *testing.T) {
		existing := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ns",
				Labels: map[string]string{
					"old": "label",
				},
			},
		}

		clientset := fake.NewSimpleClientset(existing)
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ns",
				Labels: map[string]string{
					"new": "label",
				},
			},
		}

		err := bc.createOrUpdateNamespace(ctx, ns)
		assert.NoError(t, err)

		// Verify namespace was updated
		updated, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "label", updated.Labels["new"])
	})
}

func TestBackendK8sCache_CreateOrUpdateConfigMap(t *testing.T) {
	ctx := context.Background()

	t.Run("create new configmap", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "value",
			},
		}

		err := bc.createOrUpdateConfigMap(ctx, cm)
		assert.NoError(t, err)

		// Verify configmap was created
		created, err := clientset.CoreV1().ConfigMaps("default").Get(ctx, "test-cm", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-cm", created.Name)
		assert.Equal(t, "value", created.Data["key"])
	})

	t.Run("update existing configmap", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "default",
			},
			Data: map[string]string{
				"old": "value",
			},
		}

		clientset := fake.NewSimpleClientset(existing)
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "default",
			},
			Data: map[string]string{
				"new": "value",
			},
		}

		err := bc.createOrUpdateConfigMap(ctx, cm)
		assert.NoError(t, err)

		// Verify configmap was updated
		updated, err := clientset.CoreV1().ConfigMaps("default").Get(ctx, "test-cm", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "value", updated.Data["new"])
	})
}

func TestBackendK8sCache_CreateOrUpdateSecret(t *testing.T) {
	ctx := context.Background()

	t.Run("create new secret", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"key": []byte("value"),
			},
		}

		err := bc.createOrUpdateSecret(ctx, secret)
		assert.NoError(t, err)

		// Verify secret was created
		created, err := clientset.CoreV1().Secrets("default").Get(ctx, "test-secret", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-secret", created.Name)
		assert.Equal(t, []byte("value"), created.Data["key"])
	})
}

func TestBackendK8sCache_CreateOrUpdateService(t *testing.T) {
	ctx := context.Background()

	t.Run("create new service", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "http",
						Port: 80,
					},
				},
			},
		}

		err := bc.createOrUpdateService(ctx, svc)
		assert.NoError(t, err)

		// Verify service was created
		created, err := clientset.CoreV1().Services("default").Get(ctx, "test-svc", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-svc", created.Name)
	})
}

func TestBackendK8sCache_CreateOrUpdateClusterRole(t *testing.T) {
	ctx := context.Background()

	t.Run("create new clusterrole", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-role",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
			},
		}

		err := bc.createOrUpdateClusterRole(ctx, cr)
		assert.NoError(t, err)

		// Verify clusterrole was created
		created, err := clientset.RbacV1().ClusterRoles().Get(ctx, "test-role", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-role", created.Name)
	})
}

func TestBackendK8sCache_CreateOrUpdateClusterRoleBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("create new clusterrolebinding", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-binding",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "test-sa",
					Namespace: "default",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "test-role",
			},
		}

		err := bc.createOrUpdateClusterRoleBinding(ctx, crb)
		assert.NoError(t, err)

		// Verify clusterrolebinding was created
		created, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, "test-binding", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-binding", created.Name)
	})
}

func TestBackendK8sCache_CreateOrUpdateServiceAccount(t *testing.T) {
	ctx := context.Background()

	t.Run("create new serviceaccount", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		automount := true
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
			AutomountServiceAccountToken: &automount,
		}

		err := bc.createOrUpdateServiceAccount(ctx, sa)
		assert.NoError(t, err)

		// Verify serviceaccount was created
		created, err := clientset.CoreV1().ServiceAccounts("default").Get(ctx, "test-sa", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-sa", created.Name)
	})
}

func TestBackendK8sCache_CreateOrUpdateResourceQuota(t *testing.T) {
	ctx := context.Background()

	t.Run("create new resourcequota", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		rq := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-quota",
				Namespace: "default",
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: corev1.ResourceList{
					corev1.ResourcePods: resource.MustParse("10"),
				},
			},
		}

		err := bc.createOrUpdateResourceQuota(ctx, rq)
		assert.NoError(t, err)

		// Verify resourcequota was created
		created, err := clientset.CoreV1().ResourceQuotas("default").Get(ctx, "test-quota", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-quota", created.Name)
	})
}

func TestBackendK8sCache_CreateOrUpdateValidatingWebhookConfiguration(t *testing.T) {
	ctx := context.Background()

	t.Run("create new validating webhook", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		sideEffect := admissionregistrationv1.SideEffectClassNone
		vw := &admissionregistrationv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-webhook",
			},
			Webhooks: []admissionregistrationv1.ValidatingWebhook{
				{
					Name:                    "test.example.com",
					SideEffects:             &sideEffect,
					AdmissionReviewVersions: []string{"v1"},
					ClientConfig: admissionregistrationv1.WebhookClientConfig{
						Service: &admissionregistrationv1.ServiceReference{
							Name:      "test-svc",
							Namespace: "default",
						},
					},
				},
			},
		}

		err := bc.createOrUpdateValidatingWebhookConfiguration(ctx, vw)
		assert.NoError(t, err)

		// Verify webhook was created
		created, err := clientset.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, "test-webhook", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-webhook", created.Name)
	})
}

func TestBackendK8sCache_CreateOrUpdateMutatingWebhookConfiguration(t *testing.T) {
	ctx := context.Background()

	t.Run("create new mutating webhook", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
		}

		sideEffect := admissionregistrationv1.SideEffectClassNone
		vw := &admissionregistrationv1.MutatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-webhook",
			},
			Webhooks: []admissionregistrationv1.MutatingWebhook{
				{
					Name:                    "test.example.com",
					SideEffects:             &sideEffect,
					AdmissionReviewVersions: []string{"v1"},
					ClientConfig: admissionregistrationv1.WebhookClientConfig{
						Service: &admissionregistrationv1.ServiceReference{
							Name:      "test-svc",
							Namespace: "default",
						},
					},
				},
			},
		}

		err := bc.createOrUpdateMutatingWebhookConfiguration(ctx, vw)
		assert.NoError(t, err)

		// Verify webhook was created
		created, err := clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, "test-webhook", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-webhook", created.Name)
	})
}

func TestBackendK8sCache_CreateOrUpdateDeployment(t *testing.T) {
	ctx := context.Background()

	t.Run("create new deployment", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
			deploymentDeleteTimeout: 60,
		}

		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "test",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "test:latest",
							},
						},
					},
				},
			},
		}

		err := bc.createOrUpdateDeployment(ctx, deployment)
		assert.NoError(t, err)

		// Verify deployment was created
		created, err := clientset.AppsV1().Deployments("default").Get(ctx, "test-deployment", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, "test-deployment", created.Name)
	})

	t.Run("update existing deployment", func(t *testing.T) {
		existing := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "test",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "test:v1",
							},
						},
					},
				},
			},
		}

		clientset := fake.NewSimpleClientset(existing)
		bc := &BackendK8sCache{
			clients: &kubeclients.KubeClients{
				K8s: clientset,
			},
			deploymentDeleteTimeout: 60,
		}

		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "test",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "test:v2",
							},
						},
					},
				},
			},
		}

		err := bc.createOrUpdateDeployment(ctx, deployment)
		assert.NoError(t, err)

		// Verify deployment was updated with restart annotation
		updated, err := clientset.AppsV1().Deployments("default").Get(ctx, "test-deployment", metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NotEmpty(t, updated.Spec.Template.Annotations[restartedAtAnnotation])
	})
}

func TestDecodeEnvOverrides(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    map[string]string
		expectError bool
	}{
		{
			name:        "empty string returns nil",
			input:       "",
			expected:    nil,
			expectError: false,
		},
		{
			name:        "valid base64 encoded JSON map",
			input:       base64.StdEncoding.EncodeToString([]byte(`{"INIT_CONTAINER":"test-image:1.0","UTILS_CONTAINER":"utils:2.0"}`)),
			expected:    map[string]string{"INIT_CONTAINER": "test-image:1.0", "UTILS_CONTAINER": "utils:2.0"},
			expectError: false,
		},
		{
			name:        "valid base64 encoded empty JSON map",
			input:       base64.StdEncoding.EncodeToString([]byte(`{}`)),
			expected:    map[string]string{},
			expectError: false,
		},
		{
			name:        "invalid base64 string",
			input:       "not-valid-base64!!!",
			expected:    nil,
			expectError: true,
		},
		{
			name:        "valid base64 but invalid JSON",
			input:       base64.StdEncoding.EncodeToString([]byte(`{invalid json}`)),
			expected:    nil,
			expectError: true,
		},
		{
			name:        "valid base64 but JSON array instead of map",
			input:       base64.StdEncoding.EncodeToString([]byte(`["a","b"]`)),
			expected:    nil,
			expectError: true,
		},
		{
			name:        "single key-value pair",
			input:       base64.StdEncoding.EncodeToString([]byte(`{"OTEL_CONTAINER":"otel:latest"}`)),
			expected:    map[string]string{"OTEL_CONTAINER": "otel:latest"},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := decodeEnvOverrides(tt.input)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestDecodeEnvOverrides_RoundTrip(t *testing.T) {
	// Test that encoding and decoding produces the same result
	original := map[string]string{
		"INIT_CONTAINER":      "nvcr.io/qtfpt1h0bieu/nvcf-core/nvcf_worker_init:2.97.2",
		"UTILS_CONTAINER":     "nvcr.io/qtfpt1h0bieu/nvcf-core/nvcf_worker_utils:2.94.0",
		"ESS_AGENT_CONTAINER": "nvcr.io/some/ess-agent:1.0.0",
	}

	// Encode
	jsonBytes, err := json.Marshal(original)
	require.NoError(t, err)
	encoded := base64.StdEncoding.EncodeToString(jsonBytes)

	// Decode
	decoded, err := decodeEnvOverrides(encoded)
	require.NoError(t, err)

	assert.Equal(t, original, decoded)
}
