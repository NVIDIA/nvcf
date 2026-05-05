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

package k8sutil

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func TestEnsureNetworkPolicies(t *testing.T) {
	// Mock feature flag fetcher
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.GXCache,
			featureflag.BYOObservability,
		},
	}
	// Setup test context
	ctx := context.Background()
	namespace := "test-namespace"
	npCM := newNetworkPolicyConfigMap("nvca-system")
	namespaces := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvca-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ContainerCachingNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProxyCacheNamespace}},
	}
	k8sClient := k8sfake.NewSimpleClientset(namespaces...)
	crClient := clientfake.NewFakeClient(namespaces...)

	err := EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		nil,
		nil,
	)
	require.Error(t, err)

	// Ensure with k8sClient.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)
	npList1, err := k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npList1.Items, 8)
	// Try again, no errors or updates.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)
	npList2, err := k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npList2.Items, 8)

	// Ensure with ctrl client.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		nil,
		crClient,
	)
	require.NoError(t, err)
	npList1 = &netv1.NetworkPolicyList{}
	err = crClient.List(ctx, npList1, client.InNamespace(namespace))
	require.NoError(t, err)
	assert.Len(t, npList1.Items, 8)
	// Try again, no errors or updates.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)
	npList2 = &netv1.NetworkPolicyList{}
	err = crClient.List(ctx, npList2, client.InNamespace(namespace))
	require.NoError(t, err)
	assert.Len(t, npList2.Items, 8)

	// No caching namespaces
	k8sClient = k8sfake.NewSimpleClientset()
	crClient = clientfake.NewFakeClient()

	// Ensure with k8sClient.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)
	npList1, err = k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npList1.Items, 7)

	// Ensure with ctrl client.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		nil,
		crClient,
	)
	require.NoError(t, err)
	npList1 = &netv1.NetworkPolicyList{}
	err = crClient.List(ctx, npList1, client.InNamespace(namespace))
	require.NoError(t, err)
	assert.Len(t, npList1.Items, 7)

	// No feature flags.
	featureFlagFetcher.EnabledFFs = nil
	k8sClient = k8sfake.NewSimpleClientset()
	crClient = clientfake.NewFakeClient()

	// Ensure with k8sClient.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)
	npList1, err = k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npList1.Items, 4)

	// Ensure with ctrl client.
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		nil,
		crClient,
	)
	require.NoError(t, err)
	npList1 = &netv1.NetworkPolicyList{}
	err = crClient.List(ctx, npList1, client.InNamespace(namespace))
	require.NoError(t, err)
	assert.Len(t, npList1.Items, 4)
}

func TestEnsureNetworkPoliciesWithCustomPolicies(t *testing.T) {
	// Mock feature flag fetcher
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.GXCache,
			featureflag.BYOObservability,
		},
	}

	ctx := context.Background()
	namespace := "test-namespace"

	// Create configmap with custom network policies
	npCM := newNetworkPolicyConfigMapWithCustomPolicies("nvca-system")
	namespaces := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvca-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ContainerCachingNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProxyCacheNamespace}},
	}
	k8sClient := k8sfake.NewSimpleClientset(namespaces...)

	// Test custom network policy creation
	err := EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)

	// Verify custom network policies were created
	npList, err := k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// Should have standard policies + custom policies
	assert.Len(t, npList.Items, 10) // 8 standard + 2 custom

	// Check that custom policies have the correct label
	customPolicyCount := 0
	for _, np := range npList.Items {
		if strings.HasPrefix(np.Name, "nvcf-custom-") {
			customPolicyCount++
			assert.Equal(t, "enabled", np.Labels["nvca.nvcf.nvidia.io/network-policy-customization"])
		}
	}
	assert.Equal(t, 2, customPolicyCount)

	// Verify specific custom policies exist
	customPolicyNames := make(map[string]bool)
	for _, np := range npList.Items {
		if strings.HasPrefix(np.Name, "nvcf-custom-") {
			customPolicyNames[np.Name] = true
		}
	}
	assert.True(t, customPolicyNames["nvcf-custom-allow-all-ingress"])
	assert.True(t, customPolicyNames["nvcf-custom-restrict-egress"])
}

func TestEnsureNetworkPoliciesCustomPolicyPruning(t *testing.T) {
	// Mock feature flag fetcher
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.GXCache,
			featureflag.BYOObservability,
		},
	}

	ctx := context.Background()
	namespace := "test-namespace"

	// Create configmap with custom network policies
	npCM := newNetworkPolicyConfigMapWithCustomPolicies("nvca-system")
	namespaces := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvca-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ContainerCachingNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProxyCacheNamespace}},
	}
	k8sClient := k8sfake.NewSimpleClientset(namespaces...)

	// First, create network policies with custom policies
	err := EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)

	// Verify custom policies exist
	npList, err := k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npList.Items, 10) // 8 standard + 2 custom

	// Now create a configmap without custom policies
	npCMNoCustom := newNetworkPolicyConfigMap("nvca-system")

	// Update the configmap in the fake client
	_, err = k8sClient.CoreV1().ConfigMaps("nvca-system").Create(ctx, npCMNoCustom, metav1.CreateOptions{})
	require.NoError(t, err)

	// Wait for the configmap to be created
	assert.Eventually(t, func() bool {
		_, err := k8sClient.CoreV1().ConfigMaps("nvca-system").Get(ctx, npCMNoCustom.Name, metav1.GetOptions{})
		return err == nil
	}, time.Second*10, time.Millisecond*100)

	// Ensure network policies again - custom policies should be pruned
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCMNoCustom.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)

	// Verify custom policies were pruned
	npListAfter, err := k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npListAfter.Items, 8) // Only standard policies remain

	// Verify no custom policies exist
	for _, np := range npListAfter.Items {
		assert.False(t, strings.HasPrefix(np.Name, "nvcf-custom-"))
	}
}

func TestGetValidNetworkPolicyNames(t *testing.T) {
	tests := []struct {
		name     string
		nps      map[string]string
		expected []string
	}{
		{
			name: "standard policies only",
			nps: map[string]string{
				"allow-egress-internet-no-internal-no-api": "policy1",
				"allow-ingress-monitoring":                 "policy2",
				"allow-egress-crowdstrike":                 "policy3",
				"allow-ingress-crowdstrike":                "policy4",
			},
			expected: []string{
				"allow-egress-internet-no-internal-no-api",
				"allow-ingress-monitoring",
				"allow-egress-gxcache",
				"allow-ingress-monitoring-gxcache",
				"allow-ingress-monitoring-dcgm",
				"allow-egress-nvcf-cache",
				"allow-egress-crowdstrike",
				"allow-ingress-crowdstrike",
				"allow-egress-prometheus-nvcf-byoo",
			},
		},
		{
			name: "with custom policies",
			nps: map[string]string{
				"allow-egress-internet-no-internal-no-api": "policy1",
				"allow-egress-crowdstrike":                 "policy3",
				"allow-ingress-crowdstrike":                "policy4",
				"nvcf-custom-allow-all-ingress":            "custom1",
				"nvcf-custom-restrict-egress":              "custom2",
				"some-other-policy":                        "policy3", // Should be ignored
			},
			expected: []string{
				"allow-egress-internet-no-internal-no-api",
				"allow-ingress-monitoring",
				"allow-egress-gxcache",
				"allow-ingress-monitoring-gxcache",
				"allow-ingress-monitoring-dcgm",
				"allow-egress-nvcf-cache",
				"allow-egress-crowdstrike",
				"allow-ingress-crowdstrike",
				"allow-egress-prometheus-nvcf-byoo",
				"nvcf-custom-allow-all-ingress",
				"nvcf-custom-restrict-egress",
			},
		},
		{
			name: "only custom policies",
			nps: map[string]string{
				"allow-egress-crowdstrike":  "policy3",
				"allow-ingress-crowdstrike": "policy4",
				"nvcf-custom-policy1":       "custom1",
				"nvcf-custom-policy2":       "custom2",
			},
			expected: []string{
				"allow-egress-internet-no-internal-no-api",
				"allow-ingress-monitoring",
				"allow-egress-gxcache",
				"allow-ingress-monitoring-gxcache",
				"allow-ingress-monitoring-dcgm",
				"allow-egress-nvcf-cache",
				"allow-egress-crowdstrike",
				"allow-ingress-crowdstrike",
				"allow-egress-prometheus-nvcf-byoo",
				"nvcf-custom-policy1",
				"nvcf-custom-policy2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getValidNetworkPolicyNames(tt.nps)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func newNetworkPolicyConfigMapWithCustomPolicies(namespace string) *corev1.ConfigMap {
	npCM := newNetworkPolicyConfigMap(namespace)

	// Add custom network policies
	npCM.Data["nvcf-custom-allow-all-ingress"] = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-all-ingress
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - {}
`

	npCM.Data["nvcf-custom-restrict-egress"] = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: restrict-egress
spec:
  podSelector:
    matchLabels:
      app: restricted
  policyTypes:
  - Egress
  egress:
  - to:
    - ipBlock:
        cidr: 10.0.0.0/8
`

	return npCM
}

func newNetworkPolicyConfigMapWithNameOverrideCustomPolicies(namespace string) *corev1.ConfigMap {
	npCM := newNetworkPolicyConfigMap(namespace)

	// Add custom network policy where the key name differs from the YAML name
	// The key is "nvcf-custom-override-test" but the YAML has name "different-name-in-yaml"
	npCM.Data["nvcf-custom-override-test"] = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: different-name-in-yaml
spec:
  podSelector:
    matchLabels:
      app: test
  policyTypes:
  - Ingress
  ingress:
  - from:
    - podSelector:
        matchLabels:
          role: frontend
`

	return npCM
}

func newNetworkPolicyConfigMap(namespace string) *corev1.ConfigMap {
	npCM := &corev1.ConfigMap{}
	npCM.Name = NetworkPoliciesConfigMapName
	npCM.Namespace = namespace
	npCM.Data = map[string]string{
		AllPodsEgressNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + AllPodsEgressNetworkPolicyName + `
spec:
  policyTypes:
  - Egress
  egress:
  - to:
    - ipBlock:
        cidr: 0.0.0.0/0
`,
		MonitoringIngressNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + MonitoringIngressNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
		EgressGXCacheNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + EgressGXCacheNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
		IngressGXCacheNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + IngressGXCacheNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
		IngressMonitoringDCGMNetworkPolicyNameKey: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + IngressMonitoringDCGMNetworkPolicyNameKey + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
		EgressNVCFCacheNetworkPolicyNameKey: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + EgressNVCFCacheNetworkPolicyNameKey + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
		EgressCrowdstrikeNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + EgressCrowdstrikeNetworkPolicyName + `
spec:
  policyTypes:
  - Egress
  egress:
  - to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: crowdstrike-injector
`,
		IngressCrowdstrikeNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + IngressCrowdstrikeNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: crowdstrike-injector
`,
		EgressBYOOOTelPrometheusNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + EgressBYOOOTelPrometheusNetworkPolicyName + `
spec:
  podSelector:
    matchLabels:
      nvca.nvcf.nvidia.io/byoo-metrics-egress-target: byoo-otel-collector
  egress:
  - ports:
    - port: 9090
      protocol: UDP
    - port: 9090
      protocol: TCP
    to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: monitoring
      podSelector:
        matchLabels:
          app.kubernetes.io/name: prometheus
          app.kubernetes.io/instance: prometheus-nvcf-byoo-kube-prometheus
  policyTypes:
  - Egress
`,
	}

	return npCM
}

func TestEnsureNetworkPoliciesCustomPolicyNameOverride(t *testing.T) {
	// Mock feature flag fetcher
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.GXCache,
			featureflag.BYOObservability,
		},
	}

	ctx := context.Background()
	namespace := "test-namespace"

	// Create configmap with custom network policies that have different names in YAML vs key
	npCM := newNetworkPolicyConfigMapWithNameOverrideCustomPolicies("nvca-system")
	namespaces := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvca-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ContainerCachingNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProxyCacheNamespace}},
	}
	k8sClient := k8sfake.NewSimpleClientset(namespaces...)

	// Test custom network policy creation
	err := EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		k8sClient,
		nil,
	)
	require.NoError(t, err)

	// Verify custom network policies were created with the key name, not the YAML name
	npList, err := k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// Should have standard policies + custom policies
	assert.Len(t, npList.Items, 9) // 8 standard + 1 custom

	// Verify the custom policy has the key name, not the YAML name
	customPolicyFound := false
	for _, np := range npList.Items {
		if np.Name == "nvcf-custom-override-test" {
			customPolicyFound = true
			// Verify the policy has the correct label
			assert.Equal(t, "enabled", np.Labels["nvca.nvcf.nvidia.io/network-policy-customization"])
			// Verify the policy doesn't have the YAML name
			assert.NotEqual(t, "different-name-in-yaml", np.Name)
		}
	}
	assert.True(t, customPolicyFound, "Custom policy with key name should exist")

	// Verify no policy exists with the YAML name
	yamlNameFound := false
	for _, np := range npList.Items {
		if np.Name == "different-name-in-yaml" {
			yamlNameFound = true
		}
	}
	assert.False(t, yamlNameFound, "Policy with YAML name should not exist")
}

func TestPruneCustomNetworkPoliciesWithCRClient(t *testing.T) {
	// Mock feature flag fetcher
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.GXCache,
			featureflag.BYOObservability,
		},
	}

	ctx := context.Background()
	namespace := "test-namespace"

	// Create configmap with custom network policies
	npCM := newNetworkPolicyConfigMapWithCustomPolicies("nvca-system")
	namespaces := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvca-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ContainerCachingNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProxyCacheNamespace}},
	}
	crClient := clientfake.NewFakeClient(namespaces...)

	// First, create network policies with custom policies using crClient
	err := EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		nil,
		crClient,
	)
	require.NoError(t, err)

	// Verify custom policies exist
	npList := &netv1.NetworkPolicyList{}
	err = crClient.List(ctx, npList, client.InNamespace(namespace))
	require.NoError(t, err)
	assert.Len(t, npList.Items, 10) // 8 standard + 2 custom

	// Now create a configmap without custom policies
	npCMNoCustom := newNetworkPolicyConfigMap("nvca-system")

	// Ensure network policies again - custom policies should be pruned
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCMNoCustom.Data,
		featureFlagFetcher,
		nil,
		crClient,
	)
	require.NoError(t, err)

	// Verify custom policies were pruned
	npListAfter := &netv1.NetworkPolicyList{}
	err = crClient.List(ctx, npListAfter, client.InNamespace(namespace))
	require.NoError(t, err)
	assert.Len(t, npListAfter.Items, 8) // Only standard policies remain

	// Verify no custom policies exist
	for _, np := range npListAfter.Items {
		assert.False(t, strings.HasPrefix(np.Name, "nvcf-custom-"))
	}
}

func TestPruneCustomNetworkPoliciesWithCRClientAndExistingPolicies(t *testing.T) {
	// Mock feature flag fetcher
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.GXCache,
			featureflag.BYOObservability,
		},
	}

	ctx := context.Background()
	namespace := "test-namespace"

	// Create existing custom network policies in the namespace
	existingCustomNP1 := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-custom-existing-policy-1",
			Namespace: namespace,
			Labels: map[string]string{
				"nvca.nvcf.nvidia.io/network-policy-customization": "enabled",
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
		},
	}

	existingCustomNP2 := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-custom-existing-policy-2",
			Namespace: namespace,
			Labels: map[string]string{
				"nvca.nvcf.nvidia.io/network-policy-customization": "enabled",
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
		},
	}

	// Create a standard network policy (should not be pruned)
	standardNP := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "standard-policy",
			Namespace: namespace,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
		},
	}

	namespaces := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvca-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ContainerCachingNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProxyCacheNamespace}},
	}
	crClient := clientfake.NewFakeClient(namespaces...)

	// Create the existing policies
	err := crClient.Create(ctx, existingCustomNP1)
	require.NoError(t, err)
	err = crClient.Create(ctx, existingCustomNP2)
	require.NoError(t, err)
	err = crClient.Create(ctx, standardNP)
	require.NoError(t, err)

	// Create configmap with only one custom policy (different from existing ones)
	npCM := newNetworkPolicyConfigMap("nvca-system")
	npCM.Data["nvcf-custom-new-policy"] = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: new-policy
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - podSelector:
        matchLabels:
          role: frontend
`

	// Ensure network policies - should prune existing custom policies and create new one
	err = EnsureNetworkPoliciesFunctionNamespace(
		ctx,
		namespace,
		npCM.Data,
		featureFlagFetcher,
		nil,
		crClient,
	)
	require.NoError(t, err)

	// Verify policies after pruning
	npList := &netv1.NetworkPolicyList{}
	err = crClient.List(ctx, npList, client.InNamespace(namespace))
	require.NoError(t, err)

	// Should have: 8 standard policies + 1 new custom policy + 1 standard policy we created manually
	assert.Len(t, npList.Items, 10)

	// Verify old custom policies were pruned
	oldCustomPolicy1Exists := false
	oldCustomPolicy2Exists := false
	for _, np := range npList.Items {
		if np.Name == "nvcf-custom-existing-policy-1" {
			oldCustomPolicy1Exists = true
		}
		if np.Name == "nvcf-custom-existing-policy-2" {
			oldCustomPolicy2Exists = true
		}
	}
	assert.False(t, oldCustomPolicy1Exists, "Old custom policy 1 should be pruned")
	assert.False(t, oldCustomPolicy2Exists, "Old custom policy 2 should be pruned")

	// Verify new custom policy exists
	newCustomPolicyExists := false
	for _, np := range npList.Items {
		if np.Name == "nvcf-custom-new-policy" {
			newCustomPolicyExists = true
			assert.Equal(t, "enabled", np.Labels["nvca.nvcf.nvidia.io/network-policy-customization"])
		}
	}
	assert.True(t, newCustomPolicyExists, "New custom policy should exist")

	// Verify standard policy still exists
	standardPolicyExists := false
	for _, np := range npList.Items {
		if np.Name == "standard-policy" {
			standardPolicyExists = true
		}
	}
	assert.True(t, standardPolicyExists, "Standard policy should not be pruned")
}
