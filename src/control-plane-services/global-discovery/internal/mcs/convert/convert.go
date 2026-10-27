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

// Package convert maps between Kubernetes Service and EndpointSlice
// spellings and the nvidia.gds.v1 messages, in both directions. Values the
// target side does not know map to the unspecified value (towards GDS) or
// to the Kubernetes default (towards Kubernetes).
package convert

import (
	"net/netip"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// ProtocolToGDS maps a Kubernetes protocol; empty means TCP.
func ProtocolToGDS(p corev1.Protocol) gdsv1.Protocol {
	switch p {
	case corev1.ProtocolTCP, "":
		return gdsv1.Protocol_PROTOCOL_TCP
	case corev1.ProtocolUDP:
		return gdsv1.Protocol_PROTOCOL_UDP
	case corev1.ProtocolSCTP:
		return gdsv1.Protocol_PROTOCOL_SCTP
	}
	return gdsv1.Protocol_PROTOCOL_UNSPECIFIED
}

// ProtocolFromGDS maps a GDS protocol; unspecified means TCP.
func ProtocolFromGDS(p gdsv1.Protocol) corev1.Protocol {
	switch p {
	case gdsv1.Protocol_PROTOCOL_UDP:
		return corev1.ProtocolUDP
	case gdsv1.Protocol_PROTOCOL_SCTP:
		return corev1.ProtocolSCTP
	}
	return corev1.ProtocolTCP
}

// ServiceTypeOf returns HEADLESS for a Service with clusterIP None and
// CLUSTER_SET_IP otherwise (ClusterIP, NodePort and LoadBalancer Services
// export their pod endpoints). ExternalName Services cannot be exported;
// the caller rejects them first.
func ServiceTypeOf(svc *corev1.Service) gdsv1.ServiceType {
	if svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return gdsv1.ServiceType_SERVICE_TYPE_HEADLESS
	}
	return gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP
}

// ImportTypeFromGDS maps an import type; unspecified means ClusterSetIP.
func ImportTypeFromGDS(t gdsv1.ServiceType) mcsv1beta1.ServiceImportType {
	if t == gdsv1.ServiceType_SERVICE_TYPE_HEADLESS {
		return mcsv1beta1.Headless
	}
	return mcsv1beta1.ClusterSetIP
}

// SessionAffinityToGDS maps a Service session affinity; empty means None.
func SessionAffinityToGDS(a corev1.ServiceAffinity) gdsv1.SessionAffinity {
	if a == corev1.ServiceAffinityClientIP {
		return gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP
	}
	return gdsv1.SessionAffinity_SESSION_AFFINITY_NONE
}

// SessionAffinityFromGDS maps a GDS session affinity; unspecified means None.
func SessionAffinityFromGDS(a gdsv1.SessionAffinity) corev1.ServiceAffinity {
	if a == gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP {
		return corev1.ServiceAffinityClientIP
	}
	return corev1.ServiceAffinityNone
}

// SessionAffinityConfigToGDS maps the ClientIP timeout, or returns nil.
func SessionAffinityConfigToGDS(c *corev1.SessionAffinityConfig) *gdsv1.SessionAffinityConfig {
	if c == nil || c.ClientIP == nil || c.ClientIP.TimeoutSeconds == nil {
		return nil
	}
	return &gdsv1.SessionAffinityConfig{ClientIp: &gdsv1.ClientIPConfig{TimeoutSeconds: *c.ClientIP.TimeoutSeconds}}
}

// SessionAffinityConfigFromGDS maps the ClientIP timeout, or returns nil.
func SessionAffinityConfigFromGDS(c *gdsv1.SessionAffinityConfig) *corev1.SessionAffinityConfig {
	if c.GetClientIp() == nil {
		return nil
	}
	t := c.GetClientIp().GetTimeoutSeconds()
	return &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{TimeoutSeconds: &t}}
}

// IPFamiliesToGDS maps IP families, dropping unknown ones.
func IPFamiliesToGDS(fams []corev1.IPFamily) []gdsv1.IPFamily {
	var out []gdsv1.IPFamily
	for _, f := range fams {
		switch f {
		case corev1.IPv4Protocol:
			out = append(out, gdsv1.IPFamily_IP_FAMILY_IPV4)
		case corev1.IPv6Protocol:
			out = append(out, gdsv1.IPFamily_IP_FAMILY_IPV6)
		}
	}
	return out
}

// IPFamiliesFromGDS maps IP families, dropping unspecified ones.
func IPFamiliesFromGDS(fams []gdsv1.IPFamily) []corev1.IPFamily {
	var out []corev1.IPFamily
	for _, f := range fams {
		switch f {
		case gdsv1.IPFamily_IP_FAMILY_IPV4:
			out = append(out, corev1.IPv4Protocol)
		case gdsv1.IPFamily_IP_FAMILY_IPV6:
			out = append(out, corev1.IPv6Protocol)
		}
	}
	return out
}

// InternalTrafficPolicyToGDS maps a Service internal traffic policy.
func InternalTrafficPolicyToGDS(p *corev1.ServiceInternalTrafficPolicy) gdsv1.InternalTrafficPolicy {
	if p == nil {
		return gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_UNSPECIFIED
	}
	switch *p {
	case corev1.ServiceInternalTrafficPolicyCluster:
		return gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_CLUSTER
	case corev1.ServiceInternalTrafficPolicyLocal:
		return gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_LOCAL
	}
	return gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_UNSPECIFIED
}

// AddressTypeToGDS maps an EndpointSlice address type.
func AddressTypeToGDS(t discoveryv1.AddressType) gdsv1.AddressType {
	switch t {
	case discoveryv1.AddressTypeIPv4:
		return gdsv1.AddressType_ADDRESS_TYPE_IPV4
	case discoveryv1.AddressTypeIPv6:
		return gdsv1.AddressType_ADDRESS_TYPE_IPV6
	case discoveryv1.AddressTypeFQDN:
		return gdsv1.AddressType_ADDRESS_TYPE_FQDN
	}
	return gdsv1.AddressType_ADDRESS_TYPE_UNSPECIFIED
}

// AddressTypeFromGDS maps a GDS address type; unspecified is inferred from
// the first address.
func AddressTypeFromGDS(t gdsv1.AddressType, addresses []string) discoveryv1.AddressType {
	switch t {
	case gdsv1.AddressType_ADDRESS_TYPE_IPV4:
		return discoveryv1.AddressTypeIPv4
	case gdsv1.AddressType_ADDRESS_TYPE_IPV6:
		return discoveryv1.AddressTypeIPv6
	case gdsv1.AddressType_ADDRESS_TYPE_FQDN:
		return discoveryv1.AddressTypeFQDN
	}
	if len(addresses) > 0 {
		if ip, err := netip.ParseAddr(addresses[0]); err == nil {
			if ip.Is4() {
				return discoveryv1.AddressTypeIPv4
			}
			return discoveryv1.AddressTypeIPv6
		}
	}
	return discoveryv1.AddressTypeFQDN
}
