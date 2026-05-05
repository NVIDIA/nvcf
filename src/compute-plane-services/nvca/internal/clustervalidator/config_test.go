/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package clustervalidator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLoadNetworkCheckConfig(t *testing.T) {
	ctx := context.Background()
	const ns = "test-ns"
	const name = "test-config"

	t.Run("missing ConfigMap returns nil nil", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		cfg, err := LoadNetworkCheckConfig(ctx, client, ns, name)
		assert.NoError(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("valid config parsed", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data: map[string]string{
				"config.yaml": `
reachability:
  endpoints:
    - name: "Test HTTPS"
      host: "example.com"
      port: 443
      protocol: "https"
      url: "https://example.com"
      critical: true
    - name: "Test TCP"
      host: "example.com"
      port: 8080
      protocol: "tcp"
networkPolicies:
  pairs:
    - name: "Frontend to Backend"
      a:
        namespace: "frontend"
        podSelector:
          app: "web"
      b:
        namespace: "backend"
        podSelector:
          app: "api"
      port: 8080
      protocol: "TCP"
`,
			},
		}
		client := fake.NewSimpleClientset(cm)
		cfg, err := LoadNetworkCheckConfig(ctx, client, ns, name)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		require.NotNil(t, cfg.Reachability)
		assert.Len(t, cfg.Reachability.Endpoints, 2)
		assert.Equal(t, "Test HTTPS", cfg.Reachability.Endpoints[0].Name)
		assert.Equal(t, "https", cfg.Reachability.Endpoints[0].Protocol)
		assert.True(t, cfg.Reachability.Endpoints[0].Critical, "first endpoint should be critical")
		assert.False(t, cfg.Reachability.Endpoints[1].Critical, "second endpoint defaults to non-critical")

		require.NotNil(t, cfg.NetworkPolicies)
		assert.Len(t, cfg.NetworkPolicies.Pairs, 1)
		assert.Equal(t, "Frontend to Backend", cfg.NetworkPolicies.Pairs[0].Name)
		assert.Equal(t, "frontend", cfg.NetworkPolicies.Pairs[0].A.Namespace)
		assert.Equal(t, "backend", cfg.NetworkPolicies.Pairs[0].B.Namespace)
		assert.Equal(t, 8080, cfg.NetworkPolicies.Pairs[0].Port)
		assert.False(t, cfg.NetworkPolicies.Pairs[0].Critical, "pair defaults to non-critical")
	})

	t.Run("critical flags parsed for netpol pairs and enforcement", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data: map[string]string{
				"config.yaml": `
networkPolicies:
  pairs:
    - name: "critical pair"
      a:
        namespace: "ns-a"
      b:
        namespace: "ns-b"
      port: 8080
      protocol: "TCP"
      critical: true
    - name: "non-critical pair"
      a:
        namespace: "ns-c"
      b:
        namespace: "ns-d"
      port: 9090
      protocol: "TCP"
enforcement:
  enabled: true
  testImage: "busybox:1.36"
  timeoutSeconds: 60
  critical: true
`,
			},
		}
		client := fake.NewSimpleClientset(cm)
		cfg, err := LoadNetworkCheckConfig(ctx, client, ns, name)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		require.NotNil(t, cfg.NetworkPolicies)
		assert.Len(t, cfg.NetworkPolicies.Pairs, 2)
		assert.True(t, cfg.NetworkPolicies.Pairs[0].Critical, "first pair should be critical")
		assert.False(t, cfg.NetworkPolicies.Pairs[1].Critical, "second pair defaults to non-critical")

		require.NotNil(t, cfg.Enforcement)
		assert.True(t, cfg.Enforcement.Critical, "enforcement should be critical")
	})

	t.Run("missing config.yaml key returns error", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string]string{"other-key": "value"},
		}
		client := fake.NewSimpleClientset(cm)
		cfg, err := LoadNetworkCheckConfig(ctx, client, ns, name)
		assert.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "missing key")
	})

	t.Run("malformed YAML returns error", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string]string{"config.yaml": "{{not yaml}}"},
		}
		client := fake.NewSimpleClientset(cm)
		cfg, err := LoadNetworkCheckConfig(ctx, client, ns, name)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("empty config is valid", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string]string{"config.yaml": "{}"},
		}
		client := fake.NewSimpleClientset(cm)
		cfg, err := LoadNetworkCheckConfig(ctx, client, ns, name)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Nil(t, cfg.Reachability)
		assert.Nil(t, cfg.NetworkPolicies)
	})
}

func TestValidateConfig(t *testing.T) {
	t.Run("reachability endpoint missing name", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Reachability: &ReachabilityConfig{
				Endpoints: []ReachabilityEndpoint{{Host: "a.com", Port: 80, Protocol: "tcp"}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("reachability endpoint missing host and url", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Reachability: &ReachabilityConfig{
				Endpoints: []ReachabilityEndpoint{{Name: "test", Port: 80, Protocol: "tcp"}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("reachability endpoint invalid protocol", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Reachability: &ReachabilityConfig{
				Endpoints: []ReachabilityEndpoint{{Name: "test", Host: "a.com", Port: 80, Protocol: "grpc"}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("reachability endpoint invalid port", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Reachability: &ReachabilityConfig{
				Endpoints: []ReachabilityEndpoint{{Name: "test", Host: "a.com", Port: -1, Protocol: "tcp"}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("reachability endpoint missing protocol", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Reachability: &ReachabilityConfig{
				Endpoints: []ReachabilityEndpoint{{Name: "test", Host: "a.com", Port: 80}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("netpol pair missing name", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			NetworkPolicies: &NetworkPoliciesConfig{
				Pairs: []NetworkPolicyPair{{
					A:    NetworkPolicyEndpoint{Namespace: "ns-a"},
					B:    NetworkPolicyEndpoint{Namespace: "ns-b"},
					Port: 80, Protocol: "TCP",
				}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("netpol pair missing a.namespace", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			NetworkPolicies: &NetworkPoliciesConfig{
				Pairs: []NetworkPolicyPair{{
					Name: "test",
					A:    NetworkPolicyEndpoint{},
					B:    NetworkPolicyEndpoint{Namespace: "ns-b"},
					Port: 80, Protocol: "TCP",
				}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("netpol pair missing b.namespace", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			NetworkPolicies: &NetworkPoliciesConfig{
				Pairs: []NetworkPolicyPair{{
					Name: "test",
					A:    NetworkPolicyEndpoint{Namespace: "ns-a"},
					B:    NetworkPolicyEndpoint{},
					Port: 80, Protocol: "TCP",
				}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("netpol pair invalid port", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			NetworkPolicies: &NetworkPoliciesConfig{
				Pairs: []NetworkPolicyPair{{
					Name: "test",
					A:    NetworkPolicyEndpoint{Namespace: "ns-a"},
					B:    NetworkPolicyEndpoint{Namespace: "ns-b"},
					Port: 0, Protocol: "TCP",
				}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("netpol pair invalid protocol", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			NetworkPolicies: &NetworkPoliciesConfig{
				Pairs: []NetworkPolicyPair{{
					Name: "test",
					A:    NetworkPolicyEndpoint{Namespace: "ns-a"},
					B:    NetworkPolicyEndpoint{Namespace: "ns-b"},
					Port: 80, Protocol: "SCTP",
				}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("netpol pair missing protocol", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			NetworkPolicies: &NetworkPoliciesConfig{
				Pairs: []NetworkPolicyPair{{
					Name: "test",
					A:    NetworkPolicyEndpoint{Namespace: "ns-a"},
					B:    NetworkPolicyEndpoint{Namespace: "ns-b"},
					Port: 80,
				}},
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("enforcement testImage with whitespace", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Enforcement: &EnforcementConfig{
				Enabled:   true,
				TestImage: "busybox 1.36",
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("enforcement negative timeout", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Enforcement: &EnforcementConfig{
				Enabled:        true,
				TimeoutSeconds: -1,
			},
		}
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("enforcement valid", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Enforcement: &EnforcementConfig{
				Enabled:        true,
				TestImage:      "busybox:1.36",
				TimeoutSeconds: 30,
			},
		}
		assert.NoError(t, validateConfig(cfg))
	})

	t.Run("valid full config", func(t *testing.T) {
		cfg := &NetworkCheckConfig{
			Reachability: &ReachabilityConfig{
				Endpoints: []ReachabilityEndpoint{
					{Name: "https ep", URL: "https://example.com", Protocol: "https"},
					{Name: "tcp ep", Host: "example.com", Port: 9090, Protocol: "tcp"},
				},
			},
			NetworkPolicies: &NetworkPoliciesConfig{
				Pairs: []NetworkPolicyPair{{
					Name: "test pair",
					A:    NetworkPolicyEndpoint{Namespace: "ns-a"},
					B:    NetworkPolicyEndpoint{Namespace: "ns-b"},
					Port: 8080, Protocol: "TCP",
				}},
			},
		}
		assert.NoError(t, validateConfig(cfg))
	})
}
