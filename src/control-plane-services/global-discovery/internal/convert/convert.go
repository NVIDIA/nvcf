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

// Package convert maps between the store record types, which use Kubernetes
// spellings, and the nvidia.gds.v1 messages. Conversions are total: an
// unknown or unspecified enum maps to the zero value on the other side.
// Defaults (TCP, ClusterSetIP, None) are applied by the API layer, not here.
package convert

import (
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// Timestamp returns t as a Timestamp, or nil for the zero time.
func Timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// Time returns ts as a time in UTC, or the zero time for nil.
func Time(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime().UTC()
}

// Duration returns d as a Duration, or nil for zero.
func Duration(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

var serviceTypes = map[store.ServiceType]gdsv1.ServiceType{
	store.ServiceTypeClusterSetIP: gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
	store.ServiceTypeHeadless:     gdsv1.ServiceType_SERVICE_TYPE_HEADLESS,
}

var protocols = map[store.Protocol]gdsv1.Protocol{
	store.ProtocolTCP:  gdsv1.Protocol_PROTOCOL_TCP,
	store.ProtocolUDP:  gdsv1.Protocol_PROTOCOL_UDP,
	store.ProtocolSCTP: gdsv1.Protocol_PROTOCOL_SCTP,
}

var sessionAffinities = map[store.SessionAffinity]gdsv1.SessionAffinity{
	store.SessionAffinityNone:     gdsv1.SessionAffinity_SESSION_AFFINITY_NONE,
	store.SessionAffinityClientIP: gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP,
}

var ipFamilies = map[store.IPFamily]gdsv1.IPFamily{
	store.IPFamilyIPv4: gdsv1.IPFamily_IP_FAMILY_IPV4,
	store.IPFamilyIPv6: gdsv1.IPFamily_IP_FAMILY_IPV6,
}

var trafficPolicies = map[store.InternalTrafficPolicy]gdsv1.InternalTrafficPolicy{
	store.InternalTrafficPolicyCluster: gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_CLUSTER,
	store.InternalTrafficPolicyLocal:   gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_LOCAL,
}

var addressTypes = map[store.AddressType]gdsv1.AddressType{
	store.AddressTypeIPv4: gdsv1.AddressType_ADDRESS_TYPE_IPV4,
	store.AddressTypeIPv6: gdsv1.AddressType_ADDRESS_TYPE_IPV6,
	store.AddressTypeFQDN: gdsv1.AddressType_ADDRESS_TYPE_FQDN,
}

var frontendRoles = map[store.FrontendRole]gdsv1.FrontendRole{
	store.FrontendRoleOpenAI:  gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI,
	store.FrontendRoleKVRelay: gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY,
	store.FrontendRolePylon:   gdsv1.FrontendRole_FRONTEND_ROLE_PYLON,
}

var agentSources = map[registry.AgentSource]gdsv1.AgentSource{
	registry.AgentSourceLabel: gdsv1.AgentSource_AGENT_SOURCE_LABEL,
	registry.AgentSourceAPI:   gdsv1.AgentSource_AGENT_SOURCE_API,
	registry.AgentSourceBoth:  gdsv1.AgentSource_AGENT_SOURCE_BOTH,
}

var dialModes = map[registry.DialMode]gdsv1.DialMode{
	registry.DialModeInbound:  gdsv1.DialMode_DIAL_MODE_INBOUND,
	registry.DialModeOutbound: gdsv1.DialMode_DIAL_MODE_OUTBOUND,
}

var desiredStates = map[store.DesiredState]gdsv1.PoolState{
	store.DesiredStateActive:   gdsv1.PoolState_POOL_STATE_ACTIVE,
	store.DesiredStateDraining: gdsv1.PoolState_POOL_STATE_DRAINING,
}

var poolStates = map[registry.PoolState]gdsv1.PoolState{
	registry.PoolStateRegistered: gdsv1.PoolState_POOL_STATE_REGISTERED,
	registry.PoolStateActive:     gdsv1.PoolState_POOL_STATE_ACTIVE,
	registry.PoolStateDraining:   gdsv1.PoolState_POOL_STATE_DRAINING,
	registry.PoolStateUnhealthy:  gdsv1.PoolState_POOL_STATE_UNHEALTHY,
	registry.PoolStateGone:       gdsv1.PoolState_POOL_STATE_GONE,
}

var leaseStates = map[registry.LeaseState]gdsv1.LeaseState{
	registry.LeaseStateActive:  gdsv1.LeaseState_LEASE_STATE_ACTIVE,
	registry.LeaseStateExpired: gdsv1.LeaseState_LEASE_STATE_EXPIRED,
}

func reverse[K, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

var (
	serviceTypesRev      = reverse(serviceTypes)
	protocolsRev         = reverse(protocols)
	sessionAffinitiesRev = reverse(sessionAffinities)
	ipFamiliesRev        = reverse(ipFamilies)
	trafficPoliciesRev   = reverse(trafficPolicies)
	addressTypesRev      = reverse(addressTypes)
	frontendRolesRev     = reverse(frontendRoles)
	agentSourcesRev      = reverse(agentSources)
	dialModesRev         = reverse(dialModes)
	desiredStatesRev     = reverse(desiredStates)
	poolStatesRev        = reverse(poolStates)
)

// ServiceTypeToProto maps a store service type.
func ServiceTypeToProto(t store.ServiceType) gdsv1.ServiceType { return serviceTypes[t] }

// ServiceTypeFromProto maps a proto service type.
func ServiceTypeFromProto(t gdsv1.ServiceType) store.ServiceType { return serviceTypesRev[t] }

// ProtocolToProto maps a store protocol.
func ProtocolToProto(p store.Protocol) gdsv1.Protocol { return protocols[p] }

// ProtocolFromProto maps a proto protocol.
func ProtocolFromProto(p gdsv1.Protocol) store.Protocol { return protocolsRev[p] }

// SessionAffinityToProto maps a store session affinity.
func SessionAffinityToProto(a store.SessionAffinity) gdsv1.SessionAffinity {
	return sessionAffinities[a]
}

// SessionAffinityFromProto maps a proto session affinity.
func SessionAffinityFromProto(a gdsv1.SessionAffinity) store.SessionAffinity {
	return sessionAffinitiesRev[a]
}

// IPFamilyToProto maps a store IP family.
func IPFamilyToProto(f store.IPFamily) gdsv1.IPFamily { return ipFamilies[f] }

// IPFamilyFromProto maps a proto IP family.
func IPFamilyFromProto(f gdsv1.IPFamily) store.IPFamily { return ipFamiliesRev[f] }

// InternalTrafficPolicyToProto maps a store internal traffic policy.
func InternalTrafficPolicyToProto(p store.InternalTrafficPolicy) gdsv1.InternalTrafficPolicy {
	return trafficPolicies[p]
}

// InternalTrafficPolicyFromProto maps a proto internal traffic policy.
func InternalTrafficPolicyFromProto(p gdsv1.InternalTrafficPolicy) store.InternalTrafficPolicy {
	return trafficPoliciesRev[p]
}

// AddressTypeToProto maps a store address type.
func AddressTypeToProto(t store.AddressType) gdsv1.AddressType { return addressTypes[t] }

// AddressTypeFromProto maps a proto address type.
func AddressTypeFromProto(t gdsv1.AddressType) store.AddressType { return addressTypesRev[t] }

// FrontendRoleToProto maps a store frontend role.
func FrontendRoleToProto(r store.FrontendRole) gdsv1.FrontendRole { return frontendRoles[r] }

// FrontendRoleFromProto maps a proto frontend role.
func FrontendRoleFromProto(r gdsv1.FrontendRole) store.FrontendRole { return frontendRolesRev[r] }

// IsIngressRole reports whether agents of the role take requests: OPENAI
// by inbound dispatch, PYLON over its reverse tunnel.
func IsIngressRole(r store.FrontendRole) bool {
	return r == store.FrontendRoleOpenAI || r == store.FrontendRolePylon
}

// DialModeOf returns how routers and an agent of the role connect: routers
// dial the OPENAI frontend (INBOUND); PYLON and KV_RELAY dial the routers
// (OUTBOUND). Unknown roles yield the empty value.
func DialModeOf(r store.FrontendRole) registry.DialMode {
	switch r {
	case store.FrontendRoleOpenAI:
		return registry.DialModeInbound
	case store.FrontendRoleKVRelay, store.FrontendRolePylon:
		return registry.DialModeOutbound
	}
	return ""
}

// AgentSourceToProto maps an agent source.
func AgentSourceToProto(s registry.AgentSource) gdsv1.AgentSource { return agentSources[s] }

// AgentSourceFromProto maps a proto agent source.
func AgentSourceFromProto(s gdsv1.AgentSource) registry.AgentSource { return agentSourcesRev[s] }

// DialModeToProto maps a dial mode.
func DialModeToProto(m registry.DialMode) gdsv1.DialMode { return dialModes[m] }

// DialModeFromProto maps a proto dial mode.
func DialModeFromProto(m gdsv1.DialMode) registry.DialMode { return dialModesRev[m] }

// PoolAgentRegistrationToProto converts an agent registration whose lease
// evaluated to state.
func PoolAgentRegistrationToProto(r store.PoolAgentRecord, state registry.LeaseState) *gdsv1.PoolAgentRegistration {
	return &gdsv1.PoolAgentRegistration{
		AgentId:      r.Key.AgentID,
		Lease:        LeaseToProto(r.Lease, state),
		Attributes:   maps.Clone(r.Attributes),
		Generation:   r.Generation,
		RegisteredAt: Timestamp(r.RegisteredAt),
		UpdatedAt:    Timestamp(r.UpdatedAt),
	}
}

// DesiredStateToProto maps a desired state; empty means ACTIVE.
func DesiredStateToProto(s store.DesiredState) gdsv1.PoolState {
	if s == "" {
		return gdsv1.PoolState_POOL_STATE_ACTIVE
	}
	return desiredStates[s]
}

// DesiredStateFromProto maps ACTIVE and DRAINING; anything else is empty.
func DesiredStateFromProto(s gdsv1.PoolState) store.DesiredState { return desiredStatesRev[s] }

// PoolStateToProto maps a derived pool state.
func PoolStateToProto(s registry.PoolState) gdsv1.PoolState { return poolStates[s] }

// PoolStateFromProto maps a proto pool state.
func PoolStateFromProto(s gdsv1.PoolState) registry.PoolState { return poolStatesRev[s] }

// LeaseStateToProto maps a lease state.
func LeaseStateToProto(s registry.LeaseState) gdsv1.LeaseState { return leaseStates[s] }

// ExportSpecToProto converts an export spec.
func ExportSpecToProto(s store.ExportSpec) *gdsv1.ServiceExportSpec {
	out := &gdsv1.ServiceExportSpec{
		Annotations:             maps.Clone(s.Annotations),
		Labels:                  maps.Clone(s.Labels),
		ExportCreationTimestamp: Timestamp(s.ExportCreationTimestamp),
		Ports:                   ServicePortsToProto(s.Ports),
		Type:                    ServiceTypeToProto(s.Type),
		SessionAffinity:         SessionAffinityToProto(s.SessionAffinity),
		SessionAffinityConfig:   SessionAffinityConfigToProto(s.SessionAffinityConfig),
		IpFamilies:              IPFamiliesToProto(s.IPFamilies),
		InternalTrafficPolicy:   InternalTrafficPolicyToProto(s.InternalTrafficPolicy),
		TrafficDistribution:     s.TrafficDistribution,
	}
	return out
}

// ExportSpecFromProto converts an export spec; nil yields the zero spec.
func ExportSpecFromProto(s *gdsv1.ServiceExportSpec) store.ExportSpec {
	if s == nil {
		return store.ExportSpec{}
	}
	out := store.ExportSpec{
		Annotations:             maps.Clone(s.GetAnnotations()),
		Labels:                  maps.Clone(s.GetLabels()),
		ExportCreationTimestamp: Time(s.GetExportCreationTimestamp()),
		Type:                    ServiceTypeFromProto(s.GetType()),
		SessionAffinity:         SessionAffinityFromProto(s.GetSessionAffinity()),
		InternalTrafficPolicy:   InternalTrafficPolicyFromProto(s.GetInternalTrafficPolicy()),
		TrafficDistribution:     s.GetTrafficDistribution(),
	}
	for _, p := range s.GetPorts() {
		out.Ports = append(out.Ports, store.ServicePort{
			Name: p.GetName(), Protocol: ProtocolFromProto(p.GetProtocol()), AppProtocol: p.GetAppProtocol(), Port: p.GetPort(),
		})
	}
	if c := s.GetSessionAffinityConfig(); c != nil {
		out.SessionAffinityConfig = &store.SessionAffinityConfig{}
		if ip := c.GetClientIp(); ip != nil {
			out.SessionAffinityConfig.ClientIP = &store.ClientIPConfig{TimeoutSeconds: ip.GetTimeoutSeconds()}
		}
	}
	for _, f := range s.GetIpFamilies() {
		out.IPFamilies = append(out.IPFamilies, IPFamilyFromProto(f))
	}
	return out
}

// ServicePortsToProto converts Service ports.
func ServicePortsToProto(ports []store.ServicePort) []*gdsv1.ServicePort {
	if ports == nil {
		return nil
	}
	out := make([]*gdsv1.ServicePort, 0, len(ports))
	for _, p := range ports {
		out = append(out, &gdsv1.ServicePort{
			Name: p.Name, Protocol: ProtocolToProto(p.Protocol), AppProtocol: p.AppProtocol, Port: p.Port,
		})
	}
	return out
}

// SessionAffinityConfigToProto converts a session affinity config.
func SessionAffinityConfigToProto(c *store.SessionAffinityConfig) *gdsv1.SessionAffinityConfig {
	if c == nil {
		return nil
	}
	out := &gdsv1.SessionAffinityConfig{}
	if c.ClientIP != nil {
		out.ClientIp = &gdsv1.ClientIPConfig{TimeoutSeconds: c.ClientIP.TimeoutSeconds}
	}
	return out
}

// IPFamiliesToProto converts IP families.
func IPFamiliesToProto(fs []store.IPFamily) []gdsv1.IPFamily {
	if fs == nil {
		return nil
	}
	out := make([]gdsv1.IPFamily, 0, len(fs))
	for _, f := range fs {
		out = append(out, IPFamilyToProto(f))
	}
	return out
}

// EndpointToProto converts an endpoint.
func EndpointToProto(e store.Endpoint) *gdsv1.Endpoint {
	out := &gdsv1.Endpoint{
		Addresses:   slices.Clone(e.Addresses),
		AddressType: AddressTypeToProto(e.AddressType),
		Conditions: &gdsv1.EndpointConditions{
			Ready: e.Conditions.Ready, Serving: e.Conditions.Serving, Terminating: e.Conditions.Terminating,
		},
		Hostname:   e.Hostname,
		NodeName:   e.NodeName,
		Zone:       e.Zone,
		Attributes: maps.Clone(e.Attributes),
	}
	for _, p := range e.Ports {
		out.Ports = append(out.Ports, &gdsv1.EndpointPort{
			Name: p.Name, Protocol: ProtocolToProto(p.Protocol), AppProtocol: p.AppProtocol, Port: p.Port,
		})
	}
	return out
}

// EndpointFromProto converts an endpoint.
func EndpointFromProto(e *gdsv1.Endpoint) store.Endpoint {
	out := store.Endpoint{
		Addresses:   slices.Clone(e.GetAddresses()),
		AddressType: AddressTypeFromProto(e.GetAddressType()),
		Conditions: store.EndpointConditions{
			Ready:       e.GetConditions().GetReady(),
			Serving:     e.GetConditions().GetServing(),
			Terminating: e.GetConditions().GetTerminating(),
		},
		Hostname:   e.GetHostname(),
		NodeName:   e.GetNodeName(),
		Zone:       e.GetZone(),
		Attributes: maps.Clone(e.GetAttributes()),
	}
	for _, p := range e.GetPorts() {
		out.Ports = append(out.Ports, store.EndpointPort{
			Name: p.GetName(), Protocol: ProtocolFromProto(p.GetProtocol()), AppProtocol: p.GetAppProtocol(), Port: p.GetPort(),
		})
	}
	return out
}

// LeaseToProto converts a lease evaluated to state. A lease with epoch 0
// (none) yields nil.
func LeaseToProto(l store.Lease, state registry.LeaseState) *gdsv1.Lease {
	if l.Epoch == 0 {
		return nil
	}
	return &gdsv1.Lease{
		Epoch:     l.Epoch,
		Ttl:       Duration(l.TTL),
		RenewedAt: Timestamp(l.RenewedAt),
		ExpiresAt: Timestamp(l.ExpiresAt()),
		State:     LeaseStateToProto(state),
	}
}

// ClusterToProto converts a cluster registration whose lease evaluated to
// state.
func ClusterToProto(r store.ClusterRecord, state registry.LeaseState) *gdsv1.Cluster {
	return &gdsv1.Cluster{
		Clusterset:   r.Key.ClusterSet,
		Cluster:      r.Key.Cluster,
		Region:       r.Region,
		Zones:        slices.Clone(r.Zones),
		Provider:     r.Provider,
		Labels:       maps.Clone(r.Labels),
		AgentId:      r.AgentID,
		AgentVersion: r.AgentVersion,
		Lease:        LeaseToProto(r.Lease, state),
		Generation:   r.Generation,
		RegisteredAt: Timestamp(r.RegisteredAt),
		UpdatedAt:    Timestamp(r.UpdatedAt),
	}
}

// ServiceRefToProto converts a Service reference.
func ServiceRefToProto(r store.ServiceRef) *gdsv1.ServiceRef {
	return &gdsv1.ServiceRef{Namespace: r.Namespace, Name: r.Name, Role: FrontendRoleToProto(r.Role)}
}

// ServiceRefFromProto converts a Service reference.
func ServiceRefFromProto(r *gdsv1.ServiceRef) store.ServiceRef {
	return store.ServiceRef{Namespace: r.GetNamespace(), Name: r.GetName(), Role: FrontendRoleFromProto(r.GetRole())}
}

// FrontendToProto converts a frontend endpoint.
func FrontendToProto(f store.FrontendEndpoint) *gdsv1.FrontendEndpoint {
	out := &gdsv1.FrontendEndpoint{Role: FrontendRoleToProto(f.Role), Url: f.URL, PortName: f.PortName}
	if f.Service != nil {
		out.Service = ServiceRefToProto(*f.Service)
	}
	return out
}

// FrontendFromProto converts a frontend endpoint.
func FrontendFromProto(f *gdsv1.FrontendEndpoint) store.FrontendEndpoint {
	out := store.FrontendEndpoint{Role: FrontendRoleFromProto(f.GetRole()), URL: f.GetUrl(), PortName: f.GetPortName()}
	if f.GetService() != nil {
		ref := ServiceRefFromProto(f.GetService())
		out.Service = &ref
	}
	return out
}

// ModelToProto converts a served model.
func ModelToProto(m store.ModelServed) *gdsv1.ModelServed {
	return &gdsv1.ModelServed{ModelId: m.ModelID, Aliases: slices.Clone(m.Aliases), Attributes: maps.Clone(m.Attributes)}
}

// PoolSpecFromProto returns the writer-owned fields of a pool.
func PoolSpecFromProto(p *gdsv1.Pool) store.PoolSpec {
	out := store.PoolSpec{
		Location: store.Location{
			Region:  p.GetLocation().GetRegion(),
			DC:      p.GetLocation().GetDc(),
			Cluster: p.GetLocation().GetCluster(),
			Zones:   slices.Clone(p.GetLocation().GetZones()),
		},
		HardwareClass:    p.GetHardwareClass(),
		WorkerCount:      p.GetWorkerCount(),
		ReadyWorkerCount: p.GetReadyWorkerCount(),
		Labels:           maps.Clone(p.GetLabels()),
		DesiredState:     DesiredStateFromProto(p.GetDesiredState()),
	}
	for _, m := range p.GetModels() {
		out.Models = append(out.Models, store.ModelServed{
			ModelID: m.GetModelId(), Aliases: slices.Clone(m.GetAliases()), Attributes: maps.Clone(m.GetAttributes()),
		})
	}
	for _, f := range p.GetFrontends() {
		out.Frontends = append(out.Frontends, FrontendFromProto(f))
	}
	for _, s := range p.GetServices() {
		out.Services = append(out.Services, ServiceRefFromProto(s))
	}
	return out
}

// PoolSpecToProto fills the writer-owned fields of p from a pool spec.
func PoolSpecToProto(s store.PoolSpec, p *gdsv1.Pool) {
	p.Location = &gdsv1.Location{
		Region: s.Location.Region, Dc: s.Location.DC, Cluster: s.Location.Cluster, Zones: slices.Clone(s.Location.Zones),
	}
	p.HardwareClass = s.HardwareClass
	p.WorkerCount = s.WorkerCount
	p.ReadyWorkerCount = s.ReadyWorkerCount
	p.Labels = maps.Clone(s.Labels)
	p.DesiredState = DesiredStateToProto(s.DesiredState)
	p.Models = nil
	for _, m := range s.Models {
		p.Models = append(p.Models, ModelToProto(m))
	}
	p.Frontends = nil
	for _, f := range s.Frontends {
		p.Frontends = append(p.Frontends, FrontendToProto(f))
	}
	p.Services = nil
	for _, r := range s.Services {
		p.Services = append(p.Services, ServiceRefToProto(r))
	}
}
