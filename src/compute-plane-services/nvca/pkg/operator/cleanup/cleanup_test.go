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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	fakenvcaop "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
)

func TestBackendNamespaces(t *testing.T) {
	tests := []struct {
		name              string
		nb                *nvidiaiov1.NVCFBackend
		expectedSystemNS  string
		expectedRequestNS string
	}{
		{
			name: "default namespaces",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{},
			},
			expectedSystemNS:  DefaultNVCASystemNamespace,
			expectedRequestNS: DefaultNVCARequestsNamespace,
		},
		{
			name: "custom system namespace",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							SystemNamespace: "custom-system",
						},
					},
				},
			},
			expectedSystemNS:  "custom-system",
			expectedRequestNS: DefaultNVCARequestsNamespace,
		},
		{
			name: "custom requests namespace",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							RequestsNamespace: "custom-requests",
						},
					},
				},
			},
			expectedSystemNS:  DefaultNVCASystemNamespace,
			expectedRequestNS: "custom-requests",
		},
		{
			name: "both custom namespaces",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							SystemNamespace:   "custom-system",
							RequestsNamespace: "custom-requests",
						},
					},
				},
			},
			expectedSystemNS:  "custom-system",
			expectedRequestNS: "custom-requests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			systemNS, requestsNS := BackendNamespaces(tt.nb)
			assert.Equal(t, tt.expectedSystemNS, systemNS)
			assert.Equal(t, tt.expectedRequestNS, requestsNS)
		})
	}
}

func TestIsSentinelBeingDeleted(t *testing.T) {
	tests := []struct {
		name           string
		configMap      *corev1.ConfigMap
		expectDeleting bool
		expectError    bool
	}{
		{
			name: "sentinel not being deleted",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sentinel",
					Namespace: "test-namespace",
				},
			},
			expectDeleting: false,
			expectError:    false,
		},
		{
			name: "sentinel being deleted",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-sentinel",
					Namespace:         "test-namespace",
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{SentinelFinalizer},
				},
			},
			expectDeleting: true,
			expectError:    false,
		},
		{
			name:           "sentinel not found",
			configMap:      nil,
			expectDeleting: true, // not found is treated as being deleted
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var objs []runtime.Object
			if tt.configMap != nil {
				objs = append(objs, tt.configMap)
			}
			k8sClient := fake.NewSimpleClientset(objs...)

			isDeleting, err := IsSentinelBeingDeleted(ctx, k8sClient, "test-namespace", "test-sentinel")
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectDeleting, isDeleting)
			}
		})
	}
}

func TestRemoveSentinelFinalizer(t *testing.T) {
	tests := []struct {
		name        string
		configMap   *corev1.ConfigMap
		finalizer   string
		expectError bool
	}{
		{
			name: "remove existing finalizer",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-sentinel",
					Namespace:  "test-namespace",
					Finalizers: []string{SentinelFinalizer, "other-finalizer"},
				},
			},
			finalizer:   SentinelFinalizer,
			expectError: false,
		},
		{
			name: "finalizer already removed",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-sentinel",
					Namespace:  "test-namespace",
					Finalizers: []string{"other-finalizer"},
				},
			},
			finalizer:   SentinelFinalizer,
			expectError: false,
		},
		{
			name:        "configmap not found",
			configMap:   nil,
			finalizer:   SentinelFinalizer,
			expectError: false, // not found is not an error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var objs []runtime.Object
			if tt.configMap != nil {
				objs = append(objs, tt.configMap)
			}
			k8sClient := fake.NewSimpleClientset(objs...)

			err := RemoveSentinelFinalizer(ctx, k8sClient, "test-namespace", "test-sentinel", tt.finalizer)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify finalizer was removed
				if tt.configMap != nil {
					cm, err := k8sClient.CoreV1().ConfigMaps("test-namespace").Get(ctx, "test-sentinel", metav1.GetOptions{})
					require.NoError(t, err)
					assert.NotContains(t, cm.Finalizers, tt.finalizer)
				}
			}
		})
	}
}

func TestContainsString(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		s        string
		expected bool
	}{
		{
			name:     "contains string",
			slice:    []string{"a", "b", "c"},
			s:        "b",
			expected: true,
		},
		{
			name:     "does not contain string",
			slice:    []string{"a", "b", "c"},
			s:        "d",
			expected: false,
		},
		{
			name:     "empty slice",
			slice:    []string{},
			s:        "a",
			expected: false,
		},
		{
			name:     "nil slice",
			slice:    nil,
			s:        "a",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsString(tt.slice, tt.s)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRemoveString(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		s        string
		expected []string
	}{
		{
			name:     "remove existing string",
			slice:    []string{"a", "b", "c"},
			s:        "b",
			expected: []string{"a", "c"},
		},
		{
			name:     "remove non-existing string",
			slice:    []string{"a", "b", "c"},
			s:        "d",
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "remove from empty slice",
			slice:    []string{},
			s:        "a",
			expected: []string{},
		},
		{
			name:     "remove all occurrences",
			slice:    []string{"a", "b", "a", "c"},
			s:        "a",
			expected: []string{"b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeString(tt.slice, tt.s)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRemoveRBACFinalizers(t *testing.T) {
	tests := []struct {
		name               string
		clusterRole        *rbacv1.ClusterRole
		clusterRoleBinding *rbacv1.ClusterRoleBinding
		serviceAccount     *corev1.ServiceAccount
		expectError        bool
	}{
		{
			name: "remove all finalizers successfully",
			clusterRole: &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-operator",
					Finalizers: []string{SentinelFinalizer},
				},
			},
			clusterRoleBinding: &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-operator",
					Finalizers: []string{SentinelFinalizer},
				},
			},
			serviceAccount: &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-operator",
					Namespace:  "test-namespace",
					Finalizers: []string{SentinelFinalizer},
				},
			},
			expectError: false,
		},
		{
			name:               "all resources not found",
			clusterRole:        nil,
			clusterRoleBinding: nil,
			serviceAccount:     nil,
			expectError:        false, // not found is not an error
		},
		{
			name: "partial resources exist",
			clusterRole: &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-operator",
					Finalizers: []string{SentinelFinalizer},
				},
			},
			clusterRoleBinding: nil,
			serviceAccount:     nil,
			expectError:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var objs []runtime.Object
			if tt.clusterRole != nil {
				objs = append(objs, tt.clusterRole)
			}
			if tt.clusterRoleBinding != nil {
				objs = append(objs, tt.clusterRoleBinding)
			}
			if tt.serviceAccount != nil {
				objs = append(objs, tt.serviceAccount)
			}
			k8sClient := fake.NewSimpleClientset(objs...)

			err := RemoveRBACFinalizers(ctx, k8sClient,
				"test-operator",
				"test-operator",
				"test-operator",
				"test-namespace",
				SentinelFinalizer,
			)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify finalizers were removed from all existing resources
				if tt.clusterRole != nil {
					cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, "test-operator", metav1.GetOptions{})
					require.NoError(t, err)
					assert.NotContains(t, cr.Finalizers, SentinelFinalizer)
				}
				if tt.clusterRoleBinding != nil {
					crb, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, "test-operator", metav1.GetOptions{})
					require.NoError(t, err)
					assert.NotContains(t, crb.Finalizers, SentinelFinalizer)
				}
				if tt.serviceAccount != nil {
					sa, err := k8sClient.CoreV1().ServiceAccounts("test-namespace").Get(ctx, "test-operator", metav1.GetOptions{})
					require.NoError(t, err)
					assert.NotContains(t, sa.Finalizers, SentinelFinalizer)
				}
			}
		})
	}
}

func TestWaitForDeploymentRollout(t *testing.T) {
	replicas := int32(1)
	tests := []struct {
		name        string
		deployment  *appsv1.Deployment
		expectError bool
	}{
		{
			name: "deployment rollout complete",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
				},
				Status: appsv1.DeploymentStatus{
					UpdatedReplicas:     1,
					AvailableReplicas:   1,
					UnavailableReplicas: 0,
				},
			},
			expectError: false,
		},
		{
			name:        "deployment not found",
			deployment:  nil,
			expectError: false, // not found is not an error, just skip
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var objs []runtime.Object
			if tt.deployment != nil {
				objs = append(objs, tt.deployment)
			}
			k8sClient := fake.NewSimpleClientset(objs...)

			// Use a very short timeout for tests
			err := waitForDeploymentRollout(ctx, k8sClient, "test-namespace", "test-deployment", 100*time.Millisecond)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestWaitForDeploymentRollout_Timeout(t *testing.T) {
	replicas := int32(3)
	ctx := context.Background()

	// Create a deployment that's not ready (fewer available replicas than desired)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "test-namespace",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			UpdatedReplicas:     1, // Only 1 updated
			AvailableReplicas:   1, // Only 1 available
			UnavailableReplicas: 2, // 2 unavailable
		},
	}
	k8sClient := fake.NewSimpleClientset(deployment)

	// Use a very short timeout to trigger timeout error
	err := waitForDeploymentRollout(ctx, k8sClient, "test-namespace", "test-deployment", 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestAddFeatureFlagToConfig(t *testing.T) {
	tests := []struct {
		name        string
		configYAML  string
		featureFlag string
		expected    string
	}{
		{
			name: "add feature flag to existing featureFlags section",
			configYAML: `agent:
  featureFlags:
    - ExistingFlag
  someOther: value`,
			featureFlag: "NewFlag",
			expected: `agent:
  featureFlags:
  - NewFlag
    - ExistingFlag
  someOther: value`,
		},
		{
			name: "feature flag already present",
			configYAML: `agent:
  featureFlags:
  - CordonAndDrainMaintenance
  someOther: value`,
			featureFlag: "CordonAndDrainMaintenance",
			expected: `agent:
  featureFlags:
  - CordonAndDrainMaintenance
  someOther: value`,
		},
		{
			name: "add featureFlags section when not present",
			configYAML: `agent:
  someOther: value`,
			featureFlag: "NewFlag",
			expected: `agent:
  featureFlags:
  - NewFlag
  someOther: value`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := addFeatureFlagToConfig(tt.configYAML, tt.featureFlag)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAddMaintenanceModeToConfig(t *testing.T) {
	tests := []struct {
		name            string
		configYAML      string
		maintenanceMode string
		expected        string
	}{
		{
			name: "add maintenance mode to config",
			configYAML: `agent:
  someOther: value`,
			maintenanceMode: "CordonAndDrain",
			expected: `agent:
  maintenanceMode: CordonAndDrain
  someOther: value`,
		},
		{
			name: "replace existing maintenance mode",
			configYAML: `agent:
  maintenanceMode: None
  someOther: value`,
			maintenanceMode: "CordonAndDrain",
			expected: `agent:
  maintenanceMode: CordonAndDrain
  someOther: value`,
		},
		{
			name: "replace with indented maintenance mode",
			configYAML: `agent:
  featureFlags:
    - SomeFlag
  maintenanceMode: None`,
			maintenanceMode: "CordonAndDrain",
			expected: `agent:
  featureFlags:
    - SomeFlag
  maintenanceMode: CordonAndDrain`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := addMaintenanceModeToConfig(tt.configYAML, tt.maintenanceMode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInsertAfter(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		index    int
		newLine  string
		expected []string
	}{
		{
			name:     "insert after first line",
			lines:    []string{"a", "b", "c"},
			index:    0,
			newLine:  "new",
			expected: []string{"a", "new", "b", "c"},
		},
		{
			name:     "insert after last line",
			lines:    []string{"a", "b", "c"},
			index:    2,
			newLine:  "new",
			expected: []string{"a", "b", "c", "new"},
		},
		{
			name:     "insert in middle",
			lines:    []string{"a", "b", "c"},
			index:    1,
			newLine:  "new",
			expected: []string{"a", "b", "new", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := insertAfter(tt.lines, tt.index, tt.newLine)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTriggerNVCARollout(t *testing.T) {
	ctx := context.Background()
	replicas := int32(1)

	// Create a deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      NVCAModuleName,
			Namespace: "test-namespace",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
	}
	k8sClient := fake.NewSimpleClientset(deployment)

	err := triggerNVCARollout(ctx, k8sClient, "test-namespace")
	require.NoError(t, err)

	// Verify the annotation was added
	updated, err := k8sClient.AppsV1().Deployments("test-namespace").Get(ctx, NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, updated.Spec.Template.Annotations, "kubectl.kubernetes.io/restartedAt")
}

func TestTriggerNVCARollout_NoAnnotations(t *testing.T) {
	ctx := context.Background()
	replicas := int32(1)

	// Create a deployment without annotations
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      NVCAModuleName,
			Namespace: "test-namespace",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{},
		},
	}
	k8sClient := fake.NewSimpleClientset(deployment)

	err := triggerNVCARollout(ctx, k8sClient, "test-namespace")
	require.NoError(t, err)

	// Verify the annotation was added even when annotations was nil
	updated, err := k8sClient.AppsV1().Deployments("test-namespace").Get(ctx, NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotNil(t, updated.Spec.Template.Annotations)
	assert.Contains(t, updated.Spec.Template.Annotations, "kubectl.kubernetes.io/restartedAt")
}

func TestTriggerNVCARollout_DeploymentNotFound(t *testing.T) {
	ctx := context.Background()
	k8sClient := fake.NewSimpleClientset()

	err := triggerNVCARollout(ctx, k8sClient, "test-namespace")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get NVCA deployment")
}

func TestPatchNVCAMaintenanceMode(t *testing.T) {
	ctx := context.Background()
	replicas := int32(1)

	// Create the agent-config ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentConfigConfigMapName,
			Namespace: "test-namespace",
		},
		Data: map[string]string{
			agentConfigKey: `agent:
  featureFlags:
    - ExistingFlag
  someOther: value`,
		},
	}

	// Create the NVCA deployment for rollout trigger
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      NVCAModuleName,
			Namespace: "test-namespace",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{},
		},
	}

	k8sClient := fake.NewSimpleClientset(configMap, deployment)

	err := PatchNVCAMaintenanceMode(ctx, k8sClient, "test-namespace", "CordonAndDrain")
	require.NoError(t, err)

	// Verify the ConfigMap was updated
	updated, err := k8sClient.CoreV1().ConfigMaps("test-namespace").Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)

	configYAML := updated.Data[agentConfigKey]
	assert.Contains(t, configYAML, "CordonAndDrainMaintenance")
	assert.Contains(t, configYAML, "maintenanceMode: CordonAndDrain")
}

func TestPatchNVCAMaintenanceMode_ConfigMapNotFound(t *testing.T) {
	ctx := context.Background()
	k8sClient := fake.NewSimpleClientset()

	err := PatchNVCAMaintenanceMode(ctx, k8sClient, "test-namespace", "CordonAndDrain")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get agent-config ConfigMap")
}

func TestPatchNVCAMaintenanceMode_MissingConfigKey(t *testing.T) {
	ctx := context.Background()

	// Create ConfigMap without the config.yaml key
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentConfigConfigMapName,
			Namespace: "test-namespace",
		},
		Data: map[string]string{},
	}
	k8sClient := fake.NewSimpleClientset(configMap)

	err := PatchNVCAMaintenanceMode(ctx, k8sClient, "test-namespace", "CordonAndDrain")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing config.yaml key")
}

func TestCountICMSRequests(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	tests := []struct {
		name          string
		icmsRequests  []*unstructured.Unstructured
		expectedCount int
		expectError   bool
	}{
		{
			name:          "no ICMSRequests",
			icmsRequests:  nil,
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "one ICMSRequest",
			icmsRequests: []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
						"kind":       "ICMSRequest",
						"metadata": map[string]interface{}{
							"name":      "sr-1",
							"namespace": "test-namespace",
						},
					},
				},
			},
			expectedCount: 1,
			expectError:   false,
		},
		{
			name: "multiple ICMSRequests",
			icmsRequests: []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
						"kind":       "ICMSRequest",
						"metadata": map[string]interface{}{
							"name":      "sr-1",
							"namespace": "test-namespace",
						},
					},
				},
				{
					Object: map[string]interface{}{
						"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
						"kind":       "ICMSRequest",
						"metadata": map[string]interface{}{
							"name":      "sr-2",
							"namespace": "test-namespace",
						},
					},
				},
				{
					Object: map[string]interface{}{
						"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
						"kind":       "ICMSRequest",
						"metadata": map[string]interface{}{
							"name":      "sr-3",
							"namespace": "test-namespace",
						},
					},
				},
			},
			expectedCount: 3,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			for _, item := range tt.icmsRequests {
				objs = append(objs, item)
			}

			dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
				map[schema.GroupVersionResource]string{
					icmsGVR: "ICMSRequestList",
				},
				objs...)

			count, err := CountICMSRequests(ctx, dynamicClient, "test-namespace")
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedCount, count)
			}
		})
	}
}

func TestDeleteICMSRequests(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	tests := []struct {
		name         string
		icmsRequests []*unstructured.Unstructured
		expectError  bool
	}{
		{
			name:         "no ICMSRequests to delete",
			icmsRequests: nil,
			expectError:  false,
		},
		{
			name: "delete ICMSRequests with finalizers",
			icmsRequests: []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
						"kind":       "ICMSRequest",
						"metadata": map[string]interface{}{
							"name":       "sr-1",
							"namespace":  "test-namespace",
							"finalizers": []interface{}{"test-finalizer"},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "delete ICMSRequests without finalizers",
			icmsRequests: []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
						"kind":       "ICMSRequest",
						"metadata": map[string]interface{}{
							"name":      "sr-1",
							"namespace": "test-namespace",
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			for _, item := range tt.icmsRequests {
				objs = append(objs, item)
			}

			dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
				map[schema.GroupVersionResource]string{
					icmsGVR: "ICMSRequestList",
				},
				objs...)

			err := deleteICMSRequests(ctx, dynamicClient, "test-namespace")
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				remaining, err := dynamicClient.Resource(icmsGVR).Namespace("test-namespace").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				assert.Empty(t, remaining.Items)
			}
		})
	}
}

func TestDeleteNVCFBackend(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		backend     *nvidiaiov1.NVCFBackend
		preCreate   bool
		expectError bool
	}{
		{
			name: "delete existing backend",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-namespace",
				},
			},
			preCreate:   true,
			expectError: false,
		},
		{
			name: "delete non-existent backend",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "non-existent",
					Namespace: "test-namespace",
				},
			},
			preCreate:   false,
			expectError: false, // Not found is not an error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			if tt.preCreate {
				objs = append(objs, tt.backend)
			}
			nvcaClient := fakenvcaop.NewSimpleClientset(objs...)

			err := DeleteNVCFBackend(ctx, nvcaClient, tt.backend)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRemoveNVCFBackendFinalizer(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		backend     *nvidiaiov1.NVCFBackend
		expectError bool
	}{
		{
			name: "remove existing finalizer",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-backend",
					Namespace:  "test-namespace",
					Finalizers: []string{NVCAOperatorFinalizer, "other-finalizer"},
				},
			},
			expectError: false,
		},
		{
			name: "finalizer already removed",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-backend",
					Namespace:  "test-namespace",
					Finalizers: []string{"other-finalizer"},
				},
			},
			expectError: false,
		},
		{
			name: "no finalizers",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-namespace",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nvcaClient := fakenvcaop.NewSimpleClientset(tt.backend)

			err := RemoveNVCFBackendFinalizer(ctx, nvcaClient, tt.backend)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify finalizer was removed
				updated, err := nvcaClient.NvcfV1().NVCFBackends(tt.backend.Namespace).Get(ctx, tt.backend.Name, metav1.GetOptions{})
				require.NoError(t, err)
				assert.NotContains(t, updated.Finalizers, NVCAOperatorFinalizer)
			}
		})
	}
}

func TestRemoveNVCFBackendFinalizer_NotFound(t *testing.T) {
	ctx := context.Background()

	// Backend that doesn't exist
	backend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "non-existent",
			Namespace:  "test-namespace",
			Finalizers: []string{NVCAOperatorFinalizer},
		},
	}

	nvcaClient := fakenvcaop.NewSimpleClientset()

	err := RemoveNVCFBackendFinalizer(ctx, nvcaClient, backend)
	// Not found should not return error
	require.NoError(t, err)
}

func TestCleanupBackendResources(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	tests := []struct {
		name        string
		backend     *nvidiaiov1.NVCFBackend
		namespaces  []*corev1.Namespace
		expectError bool
	}{
		{
			name: "cleanup default namespaces",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-namespace",
				},
			},
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: DefaultNVCASystemNamespace,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: DefaultNVCARequestsNamespace,
					},
				},
			},
			expectError: false,
		},
		{
			name: "cleanup custom namespaces",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-namespace",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							SystemNamespace:   "custom-system",
							RequestsNamespace: "custom-requests",
						},
					},
				},
			},
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "custom-system",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "custom-requests",
					},
				},
			},
			expectError: false,
		},
		{
			name: "cleanup when namespaces don't exist",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-namespace",
				},
			},
			namespaces:  nil,
			expectError: false, // Not found errors are ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			for _, ns := range tt.namespaces {
				objs = append(objs, ns)
			}
			k8sClient := fake.NewSimpleClientset(objs...)
			dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
				map[schema.GroupVersionResource]string{
					icmsGVR: "ICMSRequestList",
				})

			err := CleanupBackendResources(ctx, k8sClient, dynamicClient, tt.backend)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify namespaces were deleted
				systemNS, requestsNS := BackendNamespaces(tt.backend)
				_, err := k8sClient.CoreV1().Namespaces().Get(ctx, systemNS, metav1.GetOptions{})
				assert.True(t, err != nil, "system namespace should be deleted")
				_, err = k8sClient.CoreV1().Namespaces().Get(ctx, requestsNS, metav1.GetOptions{})
				assert.True(t, err != nil, "requests namespace should be deleted")
			}
		})
	}
}

func TestCleanupBackendResources_WithICMSRequests(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	backend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
	}

	icmsRequests := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
				"kind":       "ICMSRequest",
				"metadata": map[string]interface{}{
					"name":       "sr-1",
					"namespace":  DefaultNVCARequestsNamespace,
					"finalizers": []interface{}{"test-finalizer"},
				},
			},
		},
	}

	var dynamicObjs []runtime.Object
	for _, item := range icmsRequests {
		dynamicObjs = append(dynamicObjs, item)
	}

	k8sClient := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCASystemNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCARequestsNamespace}},
	)
	dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			icmsGVR: "ICMSRequestList",
		},
		dynamicObjs...)

	err := CleanupBackendResources(ctx, k8sClient, dynamicClient, backend)
	require.NoError(t, err)

	remaining, err := dynamicClient.Resource(icmsGVR).Namespace(DefaultNVCARequestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, remaining.Items)
}

func TestCleanupBackendResources_WithWebhooks(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	backend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
	}

	// Create webhooks
	k8sClient := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCASystemNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCARequestsNamespace}},
	)
	dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			icmsGVR: "ICMSRequestList",
		})

	err := CleanupBackendResources(ctx, k8sClient, dynamicClient, backend)
	require.NoError(t, err)
}

func TestRemoveRBACFinalizers_EmptyNames(t *testing.T) {
	ctx := context.Background()
	k8sClient := fake.NewSimpleClientset()

	// Test with all empty names - should succeed without doing anything
	err := RemoveRBACFinalizers(ctx, k8sClient, "", "", "", "", SentinelFinalizer)
	require.NoError(t, err)
}

func TestRemoveRBACFinalizers_PartialFailure(t *testing.T) {
	ctx := context.Background()

	// Create only the ClusterRole, not the others
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-operator",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(clusterRole)

	// ClusterRoleBinding and ServiceAccount don't exist - should still succeed
	// because not found errors are handled gracefully
	err := RemoveRBACFinalizers(ctx, k8sClient,
		"test-operator",
		"test-operator", // doesn't exist
		"test-operator", // doesn't exist
		"test-namespace",
		SentinelFinalizer,
	)
	require.NoError(t, err)

	// Verify ClusterRole finalizer was removed
	cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, "test-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, cr.Finalizers, SentinelFinalizer)
}

func TestRemoveClusterRoleFinalizer(t *testing.T) {
	ctx := context.Background()

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-role",
			Finalizers: []string{SentinelFinalizer, "other"},
		},
	}

	k8sClient := fake.NewSimpleClientset(clusterRole)

	err := removeClusterRoleFinalizer(ctx, k8sClient, "test-role", SentinelFinalizer)
	require.NoError(t, err)

	cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, "test-role", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, cr.Finalizers, SentinelFinalizer)
	assert.Contains(t, cr.Finalizers, "other")
}

func TestRemoveClusterRoleBindingFinalizer(t *testing.T) {
	ctx := context.Background()

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-binding",
			Finalizers: []string{SentinelFinalizer, "other"},
		},
	}

	k8sClient := fake.NewSimpleClientset(clusterRoleBinding)

	err := removeClusterRoleBindingFinalizer(ctx, k8sClient, "test-binding", SentinelFinalizer)
	require.NoError(t, err)

	crb, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, "test-binding", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, crb.Finalizers, SentinelFinalizer)
	assert.Contains(t, crb.Finalizers, "other")
}

func TestRemoveServiceAccountFinalizer(t *testing.T) {
	ctx := context.Background()

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-sa",
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer, "other"},
		},
	}

	k8sClient := fake.NewSimpleClientset(serviceAccount)

	err := removeServiceAccountFinalizer(ctx, k8sClient, "test-namespace", "test-sa", SentinelFinalizer)
	require.NoError(t, err)

	sa, err := k8sClient.CoreV1().ServiceAccounts("test-namespace").Get(ctx, "test-sa", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, sa.Finalizers, SentinelFinalizer)
	assert.Contains(t, sa.Finalizers, "other")
}

func TestRemoveFinalizer_AlreadyRemoved(t *testing.T) {
	ctx := context.Background()

	// ClusterRole without the finalizer we're trying to remove
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-role",
			Finalizers: []string{"other"},
		},
	}

	k8sClient := fake.NewSimpleClientset(clusterRole)

	err := removeClusterRoleFinalizer(ctx, k8sClient, "test-role", SentinelFinalizer)
	require.NoError(t, err)

	cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, "test-role", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, cr.Finalizers, "other")
}

func TestRemoveRBACFinalizers_AllResourceTypes(t *testing.T) {
	ctx := context.Background()

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

	k8sClient := fake.NewSimpleClientset(clusterRole, clusterRoleBinding, serviceAccount)

	err := RemoveRBACFinalizers(ctx, k8sClient,
		"test-operator",
		"test-operator",
		"test-operator",
		"test-namespace",
		SentinelFinalizer,
	)
	require.NoError(t, err)

	// Verify all finalizers were removed
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

func TestCountICMSRequests_Multiple(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	sr1 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
			"kind":       "ICMSRequest",
			"metadata": map[string]interface{}{
				"name":      "sr-1",
				"namespace": DefaultNVCARequestsNamespace,
			},
		},
	}
	sr2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
			"kind":       "ICMSRequest",
			"metadata": map[string]interface{}{
				"name":      "sr-2",
				"namespace": DefaultNVCARequestsNamespace,
			},
		},
	}
	sr3 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
			"kind":       "ICMSRequest",
			"metadata": map[string]interface{}{
				"name":      "sr-3",
				"namespace": DefaultNVCARequestsNamespace,
			},
		},
	}

	dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			icmsGVR: "ICMSRequestList",
		}, sr1, sr2, sr3)

	count, err := CountICMSRequests(ctx, dynamicClient, DefaultNVCARequestsNamespace)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestCountICMSRequests_EmptyNamespace(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			icmsGVR: "ICMSRequestList",
		})

	count, err := CountICMSRequests(ctx, dynamicClient, DefaultNVCARequestsNamespace)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestIsSentinelBeingDeleted_Exists(t *testing.T) {
	ctx := context.Background()

	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ShutdownSentinelConfigMapName,
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)

	isDeleting, err := IsSentinelBeingDeleted(ctx, k8sClient, "test-namespace", ShutdownSentinelConfigMapName)
	require.NoError(t, err)
	assert.False(t, isDeleting)
}

func TestIsSentinelBeingDeleted_WithDeletionTimestamp(t *testing.T) {
	ctx := context.Background()

	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)

	isDeleting, err := IsSentinelBeingDeleted(ctx, k8sClient, "test-namespace", ShutdownSentinelConfigMapName)
	require.NoError(t, err)
	assert.True(t, isDeleting)
}

func TestRemoveSentinelFinalizer_Success(t *testing.T) {
	ctx := context.Background()

	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ShutdownSentinelConfigMapName,
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)

	err := RemoveSentinelFinalizer(ctx, k8sClient, "test-namespace", ShutdownSentinelConfigMapName, SentinelFinalizer)
	require.NoError(t, err)

	cm, err := k8sClient.CoreV1().ConfigMaps("test-namespace").Get(ctx, ShutdownSentinelConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, cm.Finalizers, SentinelFinalizer)
}

func TestRemoveSentinelFinalizer_NotFound(t *testing.T) {
	ctx := context.Background()

	k8sClient := fake.NewSimpleClientset()

	// Should not error if sentinel doesn't exist
	err := RemoveSentinelFinalizer(ctx, k8sClient, "test-namespace", ShutdownSentinelConfigMapName, SentinelFinalizer)
	require.NoError(t, err)
}

func TestDeleteWorkloadNamespaces(t *testing.T) {
	tests := []struct {
		name               string
		namespaces         []*corev1.Namespace
		expectDeletedCount int
		expectError        bool
	}{
		{
			name:               "no workload namespaces",
			namespaces:         nil,
			expectDeletedCount: 0,
			expectError:        false,
		},
		{
			name: "deletes labeled workload namespaces",
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sr-abc123",
						Labels: map[string]string{
							"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sr-def456",
						Labels: map[string]string{
							"nvca.nvcf.nvidia.io/workload-instance-type": "helm_chart",
						},
					},
				},
			},
			expectDeletedCount: 2,
			expectError:        false,
		},
		{
			name: "ignores namespaces without workload label",
			namespaces: []*corev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sr-abc123",
						Labels: map[string]string{
							"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "kube-system",
						Labels: map[string]string{},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "some-other-ns",
					},
				},
			},
			expectDeletedCount: 1,
			expectError:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var objs []runtime.Object
			for _, ns := range tt.namespaces {
				objs = append(objs, ns)
			}
			k8sClient := fake.NewSimpleClientset(objs...)

			err := deleteWorkloadNamespaces(ctx, k8sClient)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Verify labeled namespaces were deleted
			for _, ns := range tt.namespaces {
				if _, hasLabel := ns.Labels["nvca.nvcf.nvidia.io/workload-instance-type"]; hasLabel {
					_, err := k8sClient.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
					assert.True(t, err != nil, "workload namespace %s should be deleted", ns.Name)
				} else {
					_, err := k8sClient.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
					assert.NoError(t, err, "non-workload namespace %s should still exist", ns.Name)
				}
			}
		})
	}
}

func TestCleanupBackendResources_WithWorkloadNamespaces(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	backend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
	}

	k8sClient := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCASystemNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCARequestsNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultModelCacheInitNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "sr-workload-1",
			Labels: map[string]string{"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice"},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "sr-workload-2",
			Labels: map[string]string{"nvca.nvcf.nvidia.io/workload-instance-type": "helm_chart"},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	dynamicClient := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			icmsGVR: "ICMSRequestList",
		})

	err := CleanupBackendResources(ctx, k8sClient, dynamicClient, backend)
	require.NoError(t, err)

	// Workload namespaces should be deleted
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, "sr-workload-1", metav1.GetOptions{})
	assert.True(t, err != nil, "workload namespace sr-workload-1 should be deleted")
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, "sr-workload-2", metav1.GetOptions{})
	assert.True(t, err != nil, "workload namespace sr-workload-2 should be deleted")

	// System namespaces should be deleted
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, DefaultNVCASystemNamespace, metav1.GetOptions{})
	assert.True(t, err != nil, "system namespace should be deleted")
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, DefaultNVCARequestsNamespace, metav1.GetOptions{})
	assert.True(t, err != nil, "requests namespace should be deleted")
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, DefaultModelCacheInitNamespace, metav1.GetOptions{})
	assert.True(t, err != nil, "model cache init namespace should be deleted")

	// Unrelated namespaces should not be deleted
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	assert.NoError(t, err, "kube-system should still exist")
}
