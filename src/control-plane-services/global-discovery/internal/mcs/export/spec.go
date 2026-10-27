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

package export

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/convert"
)

// Endpoint attribute keys carrying the target reference of an endpoint.
const (
	AttrTargetKind = "targetRef.kind"
	AttrTargetName = "targetRef.name"
	AttrTargetUID  = "targetRef.uid"
)

// forwardedKey reports whether a Service label or annotation is forwarded to
// GDS: every gds.nvidia.com/ key except the controller's own markers.
func forwardedKey(k string) bool {
	switch k {
	case api.LabelManagedBy, api.LabelClusterSet, api.LabelPoolID:
		return false
	}
	return api.IsGDSKey(k)
}

// mergeExported returns the Service's gds.nvidia.com/ entries overlaid with
// the ServiceExport's explicit ones, which win.
func mergeExported(fromService, explicit map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range fromService {
		if forwardedKey(k) {
			out[k] = v
		}
	}
	maps.Copy(out, explicit)
	if len(out) == 0 {
		return nil
	}
	return out
}

// BuildSpec returns the GDS export spec of a Service and its ServiceExport.
// The caller has checked that the Service is not of type ExternalName.
func BuildSpec(svc *corev1.Service, se *mcsv1beta1.ServiceExport) *gdsv1.ServiceExportSpec {
	spec := &gdsv1.ServiceExportSpec{
		Labels:                mergeExported(svc.Labels, se.Spec.ExportedLabels),
		Annotations:           mergeExported(svc.Annotations, se.Spec.ExportedAnnotations),
		Type:                  convert.ServiceTypeOf(svc),
		SessionAffinity:       convert.SessionAffinityToGDS(svc.Spec.SessionAffinity),
		SessionAffinityConfig: convert.SessionAffinityConfigToGDS(svc.Spec.SessionAffinityConfig),
		IpFamilies:            convert.IPFamiliesToGDS(svc.Spec.IPFamilies),
		InternalTrafficPolicy: convert.InternalTrafficPolicyToGDS(svc.Spec.InternalTrafficPolicy),
	}
	if !se.CreationTimestamp.IsZero() {
		spec.ExportCreationTimestamp = timestamppb.New(se.CreationTimestamp.Time)
	}
	if svc.Spec.TrafficDistribution != nil {
		spec.TrafficDistribution = *svc.Spec.TrafficDistribution
	}
	if spec.GetSessionAffinity() != gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP {
		spec.SessionAffinityConfig = nil
	}
	for _, p := range svc.Spec.Ports {
		sp := &gdsv1.ServicePort{Name: p.Name, Protocol: convert.ProtocolToGDS(p.Protocol), Port: p.Port}
		if p.AppProtocol != nil {
			sp.AppProtocol = *p.AppProtocol
		}
		spec.Ports = append(spec.Ports, sp)
	}
	return spec
}

// hashMessage returns a stable hash of m: deterministic serialization sorts
// map entries.
func hashMessage(m proto.Message) string {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		// Only invalid UTF-8 fails; never equal to a real hash, so the
		// write is attempted and GDS reports the problem.
		return "unmarshalable:" + err.Error()
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SpecHash returns the hash of an export spec.
func SpecHash(spec *gdsv1.ServiceExportSpec) string { return hashMessage(spec) }

// EndpointsHash returns the hash of an endpoint list built by BuildEndpoints.
func EndpointsHash(eps []*gdsv1.Endpoint) string {
	return hashMessage(&gdsv1.ReplaceEndpointsRequest{Endpoints: eps})
}

// BuildEndpoints returns the endpoint set of a Service from its
// EndpointSlices, like the AWS controller's extractEndpoints: per slice and
// endpoint the addresses, the slice address type, the ready, serving and
// terminating conditions, hostname, node name, zone, the slice ports and the
// target reference as attributes. The result is sorted and de-duplicated so
// that its hash is stable. When there are more than max endpoints, ready
// ones are kept first and dropped reports how many were cut.
func BuildEndpoints(epSlices []discoveryv1.EndpointSlice, max int) (eps []*gdsv1.Endpoint, dropped int) {
	seen := map[string]bool{}
	for i := range epSlices {
		s := &epSlices[i]
		if s.DeletionTimestamp != nil {
			continue
		}
		ports := endpointPorts(s.Ports)
		for _, e := range s.Endpoints {
			if len(e.Addresses) == 0 {
				continue
			}
			ep := &gdsv1.Endpoint{
				Addresses:   slices.Clone(e.Addresses),
				AddressType: convert.AddressTypeToGDS(s.AddressType),
				Conditions:  conditions(e.Conditions),
				Ports:       ports,
			}
			if e.Hostname != nil {
				ep.Hostname = *e.Hostname
			}
			if e.NodeName != nil {
				ep.NodeName = *e.NodeName
			}
			if e.Zone != nil {
				ep.Zone = *e.Zone
			}
			if ref := e.TargetRef; ref != nil {
				ep.Attributes = map[string]string{}
				for k, v := range map[string]string{AttrTargetKind: ref.Kind, AttrTargetName: ref.Name, AttrTargetUID: string(ref.UID)} {
					if v != "" {
						ep.Attributes[k] = v
					}
				}
				if len(ep.Attributes) == 0 {
					ep.Attributes = nil
				}
			}
			key := hashMessage(ep)
			if seen[key] {
				continue
			}
			seen[key] = true
			eps = append(eps, ep)
		}
	}
	slices.SortFunc(eps, compareEndpoints)
	if max > 0 && len(eps) > max {
		// Keep ready endpoints first, then the rest, each in sorted order.
		slices.SortStableFunc(eps, func(a, b *gdsv1.Endpoint) int {
			return -cmpBool(a.GetConditions().GetReady(), b.GetConditions().GetReady())
		})
		dropped = len(eps) - max
		eps = eps[:max]
		slices.SortFunc(eps, compareEndpoints)
	}
	return eps, dropped
}

// conditions maps EndpointSlice conditions. A nil ready means ready, as the
// EndpointSlice API defines; a nil serving follows ready; a nil terminating
// means not terminating.
func conditions(c discoveryv1.EndpointConditions) *gdsv1.EndpointConditions {
	ready := c.Ready == nil || *c.Ready
	serving := ready
	if c.Serving != nil {
		serving = *c.Serving
	}
	terminating := c.Terminating != nil && *c.Terminating
	return &gdsv1.EndpointConditions{Ready: ready, Serving: serving, Terminating: terminating}
}

func endpointPorts(ports []discoveryv1.EndpointPort) []*gdsv1.EndpointPort {
	out := make([]*gdsv1.EndpointPort, 0, len(ports))
	for _, p := range ports {
		ep := &gdsv1.EndpointPort{Protocol: gdsv1.Protocol_PROTOCOL_TCP}
		if p.Name != nil {
			ep.Name = *p.Name
		}
		if p.Protocol != nil {
			ep.Protocol = convert.ProtocolToGDS(*p.Protocol)
		}
		if p.AppProtocol != nil {
			ep.AppProtocol = *p.AppProtocol
		}
		if p.Port != nil {
			ep.Port = *p.Port
		}
		out = append(out, ep)
	}
	slices.SortFunc(out, func(a, b *gdsv1.EndpointPort) int {
		return cmp.Or(strings.Compare(a.GetName(), b.GetName()), cmp.Compare(a.GetPort(), b.GetPort()),
			cmp.Compare(a.GetProtocol(), b.GetProtocol()))
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func compareEndpoints(a, b *gdsv1.Endpoint) int {
	if c := slices.Compare(a.GetAddresses(), b.GetAddresses()); c != 0 {
		return c
	}
	if c := cmp.Compare(a.GetAddressType(), b.GetAddressType()); c != 0 {
		return c
	}
	// Same addresses in several slices with other ports or conditions.
	return strings.Compare(hashMessage(a), hashMessage(b))
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}
