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

package k8smock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
)

func TestNewNetworkPolicyConfigMap(t *testing.T) {
	gotCM := NewNetworkPolicyConfigMap("foo")
	assert.Equal(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: k8sutil.NetworkPoliciesConfigMapName, Namespace: "foo"},
		Data: map[string]string{
			k8sutil.AllPodsEgressNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.AllPodsEgressNetworkPolicyName + `
spec:
  policyTypes:
  - Egress
  egress:
  - to:
    - ipBlock:
        cidr: 0.0.0.0/0
`,
			k8sutil.AllowEgressIntraNamespaceNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.AllowEgressIntraNamespaceNetworkPolicyName + `
spec:
  policyTypes:
  - Egress
  egress:
  - to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: foo
`,
			k8sutil.MonitoringIngressNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.MonitoringIngressNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
			k8sutil.EgressGXCacheNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.EgressGXCacheNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
			k8sutil.IngressGXCacheNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.IngressGXCacheNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
			k8sutil.IngressMonitoringDCGMNetworkPolicyNameKey: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.IngressMonitoringDCGMNetworkPolicyNameKey + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
			k8sutil.EgressNVCFCacheNetworkPolicyNameKey: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.EgressNVCFCacheNetworkPolicyNameKey + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: bar
`,
			k8sutil.EgressBYOOOTelPrometheusNetworkPolicyName: `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.EgressBYOOOTelPrometheusNetworkPolicyName + `
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
		},
	}, gotCM)
}
