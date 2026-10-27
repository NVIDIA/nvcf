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

package convert

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

func TestProtocols(t *testing.T) {
	for k8s, gds := range map[corev1.Protocol]gdsv1.Protocol{
		"":                  gdsv1.Protocol_PROTOCOL_TCP,
		corev1.ProtocolTCP:  gdsv1.Protocol_PROTOCOL_TCP,
		corev1.ProtocolUDP:  gdsv1.Protocol_PROTOCOL_UDP,
		corev1.ProtocolSCTP: gdsv1.Protocol_PROTOCOL_SCTP,
		"QUIC":              gdsv1.Protocol_PROTOCOL_UNSPECIFIED,
	} {
		assert.Equal(t, gds, ProtocolToGDS(k8s), k8s)
	}
	for gds, k8s := range map[gdsv1.Protocol]corev1.Protocol{
		gdsv1.Protocol_PROTOCOL_UNSPECIFIED: corev1.ProtocolTCP,
		gdsv1.Protocol_PROTOCOL_TCP:         corev1.ProtocolTCP,
		gdsv1.Protocol_PROTOCOL_UDP:         corev1.ProtocolUDP,
		gdsv1.Protocol_PROTOCOL_SCTP:        corev1.ProtocolSCTP,
	} {
		assert.Equal(t, k8s, ProtocolFromGDS(gds), gds.String())
	}
}

func TestServiceTypes(t *testing.T) {
	tests := []struct {
		name string
		spec corev1.ServiceSpec
		want gdsv1.ServiceType
	}{
		{"cluster ip", corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1"}, gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP},
		{"headless", corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: corev1.ClusterIPNone}, gdsv1.ServiceType_SERVICE_TYPE_HEADLESS},
		{"node port", corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, ClusterIP: "10.0.0.2"}, gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP},
		{"load balancer", corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, ClusterIP: "10.0.0.3"}, gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP},
		{"unallocated", corev1.ServiceSpec{}, gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ServiceTypeOf(&corev1.Service{Spec: tt.spec}))
		})
	}
	assert.Equal(t, mcsv1beta1.Headless, ImportTypeFromGDS(gdsv1.ServiceType_SERVICE_TYPE_HEADLESS))
	assert.Equal(t, mcsv1beta1.ClusterSetIP, ImportTypeFromGDS(gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP))
	assert.Equal(t, mcsv1beta1.ClusterSetIP, ImportTypeFromGDS(gdsv1.ServiceType_SERVICE_TYPE_UNSPECIFIED))
}

func TestSessionAffinity(t *testing.T) {
	assert.Equal(t, gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP, SessionAffinityToGDS(corev1.ServiceAffinityClientIP))
	assert.Equal(t, gdsv1.SessionAffinity_SESSION_AFFINITY_NONE, SessionAffinityToGDS(corev1.ServiceAffinityNone))
	assert.Equal(t, gdsv1.SessionAffinity_SESSION_AFFINITY_NONE, SessionAffinityToGDS(""))
	assert.Equal(t, corev1.ServiceAffinityClientIP, SessionAffinityFromGDS(gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP))
	assert.Equal(t, corev1.ServiceAffinityNone, SessionAffinityFromGDS(gdsv1.SessionAffinity_SESSION_AFFINITY_UNSPECIFIED))

	timeout := int32(600)
	cfg := SessionAffinityConfigToGDS(&corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{TimeoutSeconds: &timeout}})
	assert.Equal(t, int32(600), cfg.GetClientIp().GetTimeoutSeconds())
	assert.Nil(t, SessionAffinityConfigToGDS(nil))
	assert.Nil(t, SessionAffinityConfigToGDS(&corev1.SessionAffinityConfig{}))
	back := SessionAffinityConfigFromGDS(cfg)
	assert.Equal(t, int32(600), *back.ClientIP.TimeoutSeconds)
	assert.Nil(t, SessionAffinityConfigFromGDS(nil))
}

func TestIPFamiliesAndPolicies(t *testing.T) {
	fams := IPFamiliesToGDS([]corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol, "IPv5"})
	assert.Equal(t, []gdsv1.IPFamily{gdsv1.IPFamily_IP_FAMILY_IPV6, gdsv1.IPFamily_IP_FAMILY_IPV4}, fams)
	assert.Equal(t, []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol},
		IPFamiliesFromGDS(append(fams, gdsv1.IPFamily_IP_FAMILY_UNSPECIFIED)))

	local, cluster, other := corev1.ServiceInternalTrafficPolicyLocal, corev1.ServiceInternalTrafficPolicyCluster, corev1.ServiceInternalTrafficPolicy("x")
	assert.Equal(t, gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_LOCAL, InternalTrafficPolicyToGDS(&local))
	assert.Equal(t, gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_CLUSTER, InternalTrafficPolicyToGDS(&cluster))
	assert.Equal(t, gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_UNSPECIFIED, InternalTrafficPolicyToGDS(&other))
	assert.Equal(t, gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_UNSPECIFIED, InternalTrafficPolicyToGDS(nil))
}

func TestAddressTypes(t *testing.T) {
	for k8s, gds := range map[discoveryv1.AddressType]gdsv1.AddressType{
		discoveryv1.AddressTypeIPv4: gdsv1.AddressType_ADDRESS_TYPE_IPV4,
		discoveryv1.AddressTypeIPv6: gdsv1.AddressType_ADDRESS_TYPE_IPV6,
		discoveryv1.AddressTypeFQDN: gdsv1.AddressType_ADDRESS_TYPE_FQDN,
		"other":                     gdsv1.AddressType_ADDRESS_TYPE_UNSPECIFIED,
	} {
		assert.Equal(t, gds, AddressTypeToGDS(k8s), k8s)
		if gds != gdsv1.AddressType_ADDRESS_TYPE_UNSPECIFIED {
			assert.Equal(t, k8s, AddressTypeFromGDS(gds, nil))
		}
	}
	unspecified := gdsv1.AddressType_ADDRESS_TYPE_UNSPECIFIED
	assert.Equal(t, discoveryv1.AddressTypeIPv4, AddressTypeFromGDS(unspecified, []string{"10.0.0.1"}))
	assert.Equal(t, discoveryv1.AddressTypeIPv6, AddressTypeFromGDS(unspecified, []string{"fd00::1"}))
	assert.Equal(t, discoveryv1.AddressTypeFQDN, AddressTypeFromGDS(unspecified, []string{"pod.example"}))
	assert.Equal(t, discoveryv1.AddressTypeFQDN, AddressTypeFromGDS(unspecified, nil))
}
