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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func protocolPtr(p corev1.Protocol) *corev1.Protocol { return &p }

func makeNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"kubernetes.io/metadata.name": name},
		},
	}
}

func makeNetworkPolicy(
	name, namespace string,
	podSelector map[string]string,
	policyTypes []networkingv1.PolicyType,
	ingress []networkingv1.NetworkPolicyIngressRule,
	egress []networkingv1.NetworkPolicyEgressRule,
) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podSelector},
			PolicyTypes: policyTypes,
			Ingress:     ingress,
			Egress:      egress,
		},
	}
}

func nsSelector(ns string) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns},
	}
}

// ---------------------------------------------------------------------------
// egressAllowsTraffic
// ---------------------------------------------------------------------------

func TestEgressAllowsTraffic(t *testing.T) {
	ctx := context.Background()
	dstLabels := map[string]string{"kubernetes.io/metadata.name": "dst-ns"}

	t.Run("no policies means not isolated, all egress allowed", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		assert.True(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("egress policy with no rules blocks everything", func(t *testing.T) {
		np := makeNetworkPolicy("deny-all-egress", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{})
		client := fake.NewSimpleClientset(np)
		assert.False(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("egress rule with matching namespace selector", func(t *testing.T) {
		np := makeNetworkPolicy("allow-egress-dst", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("dst-ns"),
				}},
			}})
		client := fake.NewSimpleClientset(np)
		assert.True(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("egress rule with non-matching namespace selector", func(t *testing.T) {
		np := makeNetworkPolicy("allow-egress-other", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("other-ns"),
				}},
			}})
		client := fake.NewSimpleClientset(np)
		assert.False(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("egress rule with empty to matches all destinations", func(t *testing.T) {
		np := makeNetworkPolicy("allow-all-egress", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: nil,
			}})
		client := fake.NewSimpleClientset(np)
		assert.True(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("egress rule with port restriction blocks wrong port", func(t *testing.T) {
		port := intstr.FromInt32(9090)
		np := makeNetworkPolicy("allow-egress-port", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("dst-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}})
		client := fake.NewSimpleClientset(np)
		assert.False(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
		assert.True(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 9090, corev1.ProtocolTCP))
	})

	t.Run("ipBlock-only peer does not match namespace", func(t *testing.T) {
		np := makeNetworkPolicy("egress-ipblock", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"},
				}},
			}})
		client := fake.NewSimpleClientset(np)
		assert.False(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("ingress-only policy does not isolate egress", func(t *testing.T) {
		np := makeNetworkPolicy("ingress-only", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, nil, nil)
		client := fake.NewSimpleClientset(np)
		assert.True(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("pod selector filtering", func(t *testing.T) {
		np := makeNetworkPolicy("app-egress", "src-ns",
			map[string]string{"app": "web"},
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{})
		client := fake.NewSimpleClientset(np)
		// Pods matching "app: web" are isolated and denied
		assert.False(t, egressAllowsTraffic(ctx, client, "src-ns",
			map[string]string{"app": "web"}, dstLabels, nil, 8080, corev1.ProtocolTCP))
		// Pods matching "app: api" are NOT isolated → all allowed
		assert.True(t, egressAllowsTraffic(ctx, client, "src-ns",
			map[string]string{"app": "api"}, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("same-namespace peer without namespaceSelector", func(t *testing.T) {
		np := makeNetworkPolicy("intra-ns-egress", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{},
				}},
			}})
		client := fake.NewSimpleClientset(np)
		srcLabels := map[string]string{"kubernetes.io/metadata.name": "src-ns"}
		// Same namespace → matches
		assert.True(t, egressAllowsTraffic(ctx, client, "src-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
		// Different namespace → does not match
		assert.False(t, egressAllowsTraffic(ctx, client, "src-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
	})
}

// ---------------------------------------------------------------------------
// ingressAllowsTraffic
// ---------------------------------------------------------------------------

func TestIngressAllowsTraffic(t *testing.T) {
	ctx := context.Background()
	srcLabels := map[string]string{"kubernetes.io/metadata.name": "src-ns"}

	t.Run("no policies means not isolated, all ingress allowed", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		assert.True(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("ingress policy with no rules blocks everything", func(t *testing.T) {
		np := makeNetworkPolicy("deny-all-ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{}, nil)
		client := fake.NewSimpleClientset(np)
		assert.False(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("ingress from matching namespace on matching port", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		np := makeNetworkPolicy("allow-from-src", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.True(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("ingress from non-matching namespace", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		np := makeNetworkPolicy("allow-from-other", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("monitoring"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.False(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("ingress on wrong port", func(t *testing.T) {
		port := intstr.FromInt32(9090)
		np := makeNetworkPolicy("allow-from-src-wrong-port", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.False(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("ingress rule with empty from matches all sources", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		np := makeNetworkPolicy("allow-all-sources", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: nil,
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.True(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("ingress rule with empty ports matches all ports", func(t *testing.T) {
		np := makeNetworkPolicy("allow-all-ports", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.True(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("port range", func(t *testing.T) {
		startPort := intstr.FromInt32(8000)
		endPort := int32(9000)
		np := makeNetworkPolicy("range-ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &startPort,
					EndPort:  &endPort,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.True(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
		assert.False(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 9999, corev1.ProtocolTCP))
	})

	t.Run("nil protocol defaults to TCP", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		np := makeNetworkPolicy("default-proto", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.True(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("wrong protocol", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		np := makeNetworkPolicy("udp-ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolUDP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		assert.False(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("same-namespace peer without namespaceSelector", func(t *testing.T) {
		np := makeNetworkPolicy("intra-ns-ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{},
				}},
			}}, nil)
		client := fake.NewSimpleClientset(np)
		dstLabels := map[string]string{"kubernetes.io/metadata.name": "dst-ns"}
		// Same namespace → matches
		assert.True(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, dstLabels, nil, 8080, corev1.ProtocolTCP))
		// Different namespace → does not match
		assert.False(t, ingressAllowsTraffic(ctx, client, "dst-ns", nil, srcLabels, nil, 8080, corev1.ProtocolTCP))
	})
}

// ---------------------------------------------------------------------------
// checkDirection (integration of egress + ingress + namespace existence)
// ---------------------------------------------------------------------------

func TestCheckDirection(t *testing.T) {
	ctx := context.Background()

	t.Run("namespace does not exist returns false", func(t *testing.T) {
		client := fake.NewSimpleClientset(makeNamespace("src-ns"))
		result := checkDirection(ctx, client,
			"src-ns", nil, "nonexistent", nil, 8080, corev1.ProtocolTCP)
		assert.False(t, result.Allowed)
	})

	t.Run("source namespace does not exist returns false", func(t *testing.T) {
		client := fake.NewSimpleClientset(makeNamespace("dst-ns"))
		result := checkDirection(ctx, client,
			"nonexistent", nil, "dst-ns", nil, 8080, corev1.ProtocolTCP)
		assert.False(t, result.Allowed)
	})

	t.Run("no policies in either namespace allows all", func(t *testing.T) {
		client := fake.NewSimpleClientset(makeNamespace("src-ns"), makeNamespace("dst-ns"))
		result := checkDirection(ctx, client,
			"src-ns", nil, "dst-ns", nil, 8080, corev1.ProtocolTCP)
		assert.True(t, result.Allowed)
	})

	t.Run("egress allows dst and ingress allows src on port", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		egressNP := makeNetworkPolicy("egress", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("dst-ns"),
				}},
			}})
		ingressNP := makeNetworkPolicy("ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(
			makeNamespace("src-ns"), makeNamespace("dst-ns"),
			egressNP, ingressNP)
		result := checkDirection(ctx, client,
			"src-ns", nil, "dst-ns", nil, 8080, corev1.ProtocolTCP)
		assert.True(t, result.Allowed)
	})

	t.Run("egress targets wrong namespace", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		egressNP := makeNetworkPolicy("egress", "src-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil,
			[]networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("other-ns"),
				}},
			}})
		ingressNP := makeNetworkPolicy("ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("src-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(
			makeNamespace("src-ns"), makeNamespace("dst-ns"),
			egressNP, ingressNP)
		result := checkDirection(ctx, client,
			"src-ns", nil, "dst-ns", nil, 8080, corev1.ProtocolTCP)
		assert.False(t, result.Allowed)
	})

	t.Run("ingress allows wrong source namespace", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		ingressNP := makeNetworkPolicy("ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("monitoring"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(
			makeNamespace("src-ns"), makeNamespace("dst-ns"),
			ingressNP)
		result := checkDirection(ctx, client,
			"src-ns", nil, "dst-ns", nil, 8080, corev1.ProtocolTCP)
		assert.False(t, result.Allowed)
	})

	t.Run("no egress policy and no ingress policy allows traffic", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			makeNamespace("src-ns"), makeNamespace("dst-ns"))
		result := checkDirection(ctx, client,
			"src-ns", nil, "dst-ns", nil, 8080, corev1.ProtocolTCP)
		assert.True(t, result.Allowed)
	})

	t.Run("no egress policy but ingress blocks source namespace", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		ingressNP := makeNetworkPolicy("ingress", "dst-ns", nil,
			[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			[]networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: nsSelector("other-ns"),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: protocolPtr(corev1.ProtocolTCP),
					Port:     &port,
				}},
			}}, nil)
		client := fake.NewSimpleClientset(
			makeNamespace("src-ns"), makeNamespace("dst-ns"),
			ingressNP)
		result := checkDirection(ctx, client,
			"src-ns", nil, "dst-ns", nil, 8080, corev1.ProtocolTCP)
		assert.False(t, result.Allowed)
	})
}

// ---------------------------------------------------------------------------
// checkConfigurableNetworkPolicies (end-to-end)
// ---------------------------------------------------------------------------

func TestCheckConfigurableNetworkPolicies(t *testing.T) {
	ctx := context.Background()

	t.Run("all pairs pass with proper namespace selectors", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		objects := []runtime.Object{
			makeNamespace("ns-a"),
			makeNamespace("ns-b"),
			makeNetworkPolicy("a-egress", "ns-a", nil,
				[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress, networkingv1.PolicyTypeIngress},
				[]networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: nsSelector("ns-b"),
					}},
					Ports: []networkingv1.NetworkPolicyPort{{
						Protocol: protocolPtr(corev1.ProtocolTCP),
						Port:     &port,
					}},
				}},
				[]networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: nsSelector("ns-b"),
					}},
				}}),
			makeNetworkPolicy("b-egress", "ns-b", nil,
				[]networkingv1.PolicyType{networkingv1.PolicyTypeEgress, networkingv1.PolicyTypeIngress},
				[]networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: nsSelector("ns-a"),
					}},
					Ports: []networkingv1.NetworkPolicyPort{{
						Protocol: protocolPtr(corev1.ProtocolTCP),
						Port:     &port,
					}},
				}},
				[]networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: nsSelector("ns-a"),
					}},
				}}),
		}
		client := fake.NewSimpleClientset(objects...)
		state := &ValidationState{Log: testLog()}
		cfg := &NetworkPoliciesConfig{
			Pairs: []NetworkPolicyPair{{
				Name:     "A to B",
				A:        NetworkPolicyEndpoint{Namespace: "ns-a"},
				B:        NetworkPolicyEndpoint{Namespace: "ns-b"},
				Port:     8080,
				Protocol: "TCP",
			}},
		}
		checkConfigurableNetworkPolicies(ctx, client, state, cfg)
		require.NotNil(t, state.ConfigurableNetPolOK)
		assert.True(t, *state.ConfigurableNetPolOK)
	})

	t.Run("missing namespace fails", func(t *testing.T) {
		client := fake.NewSimpleClientset(makeNamespace("ns-a"))
		state := &ValidationState{Log: testLog()}
		cfg := &NetworkPoliciesConfig{
			Pairs: []NetworkPolicyPair{{
				Name:     "A to B",
				A:        NetworkPolicyEndpoint{Namespace: "ns-a"},
				B:        NetworkPolicyEndpoint{Namespace: "ns-b"},
				Port:     8080,
				Protocol: "TCP",
			}},
		}
		checkConfigurableNetworkPolicies(ctx, client, state, cfg)
		require.NotNil(t, state.ConfigurableNetPolOK)
		assert.False(t, *state.ConfigurableNetPolOK)
	})

	t.Run("ingress from wrong namespace fails", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		objects := []runtime.Object{
			makeNamespace("ns-a"),
			makeNamespace("ns-b"),
			makeNetworkPolicy("b-ingress", "ns-b", nil,
				[]networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				[]networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: nsSelector("monitoring"),
					}},
					Ports: []networkingv1.NetworkPolicyPort{{
						Protocol: protocolPtr(corev1.ProtocolTCP),
						Port:     &port,
					}},
				}}, nil),
		}
		client := fake.NewSimpleClientset(objects...)
		state := &ValidationState{Log: testLog()}
		cfg := &NetworkPoliciesConfig{
			Pairs: []NetworkPolicyPair{{
				Name:     "A to B",
				A:        NetworkPolicyEndpoint{Namespace: "ns-a"},
				B:        NetworkPolicyEndpoint{Namespace: "ns-b"},
				Port:     8080,
				Protocol: "TCP",
			}},
		}
		checkConfigurableNetworkPolicies(ctx, client, state, cfg)
		require.NotNil(t, state.ConfigurableNetPolOK)
		assert.False(t, *state.ConfigurableNetPolOK)
		assert.NotEmpty(t, state.Warnings)
	})
}

// ---------------------------------------------------------------------------
// Helper unit tests
// ---------------------------------------------------------------------------

func TestMatchesPodSelector(t *testing.T) {
	t.Run("empty requested matches all", func(t *testing.T) {
		sel := metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
		assert.True(t, matchesPodSelector(sel, nil))
	})

	t.Run("subset match", func(t *testing.T) {
		sel := metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
		assert.True(t, matchesPodSelector(sel, map[string]string{"app": "web", "tier": "frontend"}))
	})

	t.Run("mismatch", func(t *testing.T) {
		sel := metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
		assert.False(t, matchesPodSelector(sel, map[string]string{"app": "web"}))
	})

	t.Run("empty policy selector matches everything", func(t *testing.T) {
		sel := metav1.LabelSelector{}
		assert.True(t, matchesPodSelector(sel, map[string]string{"app": "web"}))
	})
}

func TestLabelSelectorMatches(t *testing.T) {
	t.Run("nil selector", func(t *testing.T) {
		assert.False(t, labelSelectorMatches(nil, map[string]string{"a": "b"}))
	})

	t.Run("empty selector matches everything", func(t *testing.T) {
		assert.True(t, labelSelectorMatches(&metav1.LabelSelector{}, map[string]string{"a": "b"}))
	})

	t.Run("matchLabels match", func(t *testing.T) {
		sel := &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": "monitoring"},
		}
		assert.True(t, labelSelectorMatches(sel, map[string]string{
			"kubernetes.io/metadata.name": "monitoring",
		}))
	})

	t.Run("matchLabels mismatch", func(t *testing.T) {
		sel := &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": "monitoring"},
		}
		assert.False(t, labelSelectorMatches(sel, map[string]string{
			"kubernetes.io/metadata.name": "nvcf-backend",
		}))
	})

	t.Run("matchExpressions In", func(t *testing.T) {
		sel := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      "env",
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{"prod", "staging"},
			}},
		}
		assert.True(t, labelSelectorMatches(sel, map[string]string{"env": "prod"}))
		assert.False(t, labelSelectorMatches(sel, map[string]string{"env": "dev"}))
		assert.False(t, labelSelectorMatches(sel, map[string]string{}))
	})

	t.Run("matchExpressions NotIn", func(t *testing.T) {
		sel := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      "env",
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"dev"},
			}},
		}
		assert.True(t, labelSelectorMatches(sel, map[string]string{"env": "prod"}))
		assert.False(t, labelSelectorMatches(sel, map[string]string{"env": "dev"}))
		assert.True(t, labelSelectorMatches(sel, map[string]string{}))
	})

	t.Run("matchExpressions Exists", func(t *testing.T) {
		sel := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      "tier",
				Operator: metav1.LabelSelectorOpExists,
			}},
		}
		assert.True(t, labelSelectorMatches(sel, map[string]string{"tier": "frontend"}))
		assert.False(t, labelSelectorMatches(sel, map[string]string{}))
	})

	t.Run("matchExpressions DoesNotExist", func(t *testing.T) {
		sel := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      "tier",
				Operator: metav1.LabelSelectorOpDoesNotExist,
			}},
		}
		assert.False(t, labelSelectorMatches(sel, map[string]string{"tier": "frontend"}))
		assert.True(t, labelSelectorMatches(sel, map[string]string{}))
	})
}

func TestPeerMatchesNamespace(t *testing.T) {
	nsLabels := map[string]string{"kubernetes.io/metadata.name": "monitoring"}

	t.Run("ipBlock with no pod IPs returns false", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"},
		}
		assert.False(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, nil))
	})

	t.Run("ipBlock matches pod IP in CIDR", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: "10.244.0.0/16"},
		}
		assert.True(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, []string{"10.244.1.15"}))
	})

	t.Run("ipBlock does not match pod IP outside CIDR", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: "192.168.0.0/16"},
		}
		assert.False(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, []string{"10.244.1.15"}))
	})

	t.Run("ipBlock excludes pod IP via except", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{
				CIDR:   "0.0.0.0/0",
				Except: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
			},
		}
		// Pod IP 10.244.1.15 is in 0.0.0.0/0 but excluded by 10.0.0.0/8
		assert.False(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, []string{"10.244.1.15"}))
	})

	t.Run("ipBlock allows public IP not in except", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{
				CIDR:   "0.0.0.0/0",
				Except: []string{"10.0.0.0/8"},
			},
		}
		assert.True(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, []string{"8.8.8.8"}))
	})

	t.Run("namespaceSelector matching", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			NamespaceSelector: nsSelector("monitoring"),
		}
		assert.True(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, nil))
	})

	t.Run("namespaceSelector non-matching", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			NamespaceSelector: nsSelector("other-ns"),
		}
		assert.False(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, nil))
	})

	t.Run("empty namespaceSelector matches all namespaces", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{},
		}
		assert.True(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, nil))
	})

	t.Run("podSelector only matches same namespace", func(t *testing.T) {
		peer := &networkingv1.NetworkPolicyPeer{
			PodSelector: &metav1.LabelSelector{},
		}
		sameNSLabels := map[string]string{"kubernetes.io/metadata.name": "dst-ns"}
		assert.True(t, peerMatchesNamespace(peer, "dst-ns", sameNSLabels, nil))
		assert.False(t, peerMatchesNamespace(peer, "dst-ns", nsLabels, nil))
	})
}

func TestMatchExpression_UnknownOperator(t *testing.T) {
	expr := metav1.LabelSelectorRequirement{
		Key:      "env",
		Operator: metav1.LabelSelectorOperator("FooBarOp"),
		Values:   []string{"x"},
	}
	assert.False(t, matchExpression(expr, map[string]string{"env": "x"}))
}

func TestPoliciesAllowTraffic_ListError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "networkpolicies", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("injected error")
	})
	got := egressAllowsTraffic(context.Background(), client, "ns", nil,
		map[string]string{"kubernetes.io/metadata.name": "dst"}, nil, 80, corev1.ProtocolTCP)
	assert.False(t, got)
}

func TestGetPodIPs(t *testing.T) {
	ctx := context.Background()

	t.Run("returns empty when no pods", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		assert.Empty(t, getPodIPs(ctx, client, "test-ns"))
	})

	t.Run("collects all IPs from dual-stack pod", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "dual", Namespace: "test-ns"},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				PodIP: "10.244.1.5",
				PodIPs: []corev1.PodIP{
					{IP: "10.244.1.5"},
					{IP: "fd00::5"},
				},
			},
		}
		client := fake.NewSimpleClientset(pod)
		ips := getPodIPs(ctx, client, "test-ns")
		assert.ElementsMatch(t, []string{"10.244.1.5", "fd00::5"}, ips)
	})

	t.Run("skips non-running pods", func(t *testing.T) {
		runningPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "test-ns"},
			Status: corev1.PodStatus{
				Phase:  corev1.PodRunning,
				PodIPs: []corev1.PodIP{{IP: "10.244.1.5"}},
			},
		}
		pendingPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "test-ns"},
			Status: corev1.PodStatus{
				Phase:  corev1.PodPending,
				PodIPs: []corev1.PodIP{{IP: "10.244.1.6"}},
			},
		}
		client := fake.NewSimpleClientset(runningPod, pendingPod)
		ips := getPodIPs(ctx, client, "test-ns")
		assert.ElementsMatch(t, []string{"10.244.1.5"}, ips)
	})

	t.Run("ignores empty IPs in PodIPs list", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "weird", Namespace: "test-ns"},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				PodIPs: []corev1.PodIP{
					{IP: "10.244.1.5"},
					{IP: ""},
				},
			},
		}
		client := fake.NewSimpleClientset(pod)
		ips := getPodIPs(ctx, client, "test-ns")
		assert.ElementsMatch(t, []string{"10.244.1.5"}, ips)
	})

	t.Run("returns empty for namespace with only non-running pods", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "test-ns"},
			Status: corev1.PodStatus{
				Phase:  corev1.PodFailed,
				PodIPs: []corev1.PodIP{{IP: "10.244.1.5"}},
			},
		}
		client := fake.NewSimpleClientset(pod)
		assert.Empty(t, getPodIPs(ctx, client, "test-ns"))
	})
}

func TestPortsMatch(t *testing.T) {
	t.Run("empty ports matches all", func(t *testing.T) {
		assert.True(t, portsMatch(nil, 8080, corev1.ProtocolTCP))
	})

	t.Run("exact port match", func(t *testing.T) {
		port := intstr.FromInt32(8080)
		ports := []networkingv1.NetworkPolicyPort{{
			Protocol: protocolPtr(corev1.ProtocolTCP),
			Port:     &port,
		}}
		assert.True(t, portsMatch(ports, 8080, corev1.ProtocolTCP))
		assert.False(t, portsMatch(ports, 9090, corev1.ProtocolTCP))
	})

	t.Run("nil port in rule allows all ports for protocol", func(t *testing.T) {
		ports := []networkingv1.NetworkPolicyPort{{
			Protocol: protocolPtr(corev1.ProtocolTCP),
		}}
		assert.True(t, portsMatch(ports, 8080, corev1.ProtocolTCP))
		assert.False(t, portsMatch(ports, 8080, corev1.ProtocolUDP))
	})

	t.Run("port range", func(t *testing.T) {
		startPort := intstr.FromInt32(8000)
		endPort := int32(9000)
		ports := []networkingv1.NetworkPolicyPort{{
			Protocol: protocolPtr(corev1.ProtocolTCP),
			Port:     &startPort,
			EndPort:  &endPort,
		}}
		assert.True(t, portsMatch(ports, 8500, corev1.ProtocolTCP))
		assert.False(t, portsMatch(ports, 9999, corev1.ProtocolTCP))
	})
}
