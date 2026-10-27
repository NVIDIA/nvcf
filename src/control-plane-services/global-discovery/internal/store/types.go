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

package store

import (
	"maps"
	"slices"
	"time"
)

// The spec types below mirror the nvidia.gds.v1 messages field by field.
// Enumerations use the Kubernetes and KEP-1645 spellings, so that the
// controller maps Kubernetes objects without translation and the API layer
// maps them to and from the proto enums.

// ServiceType is the KEP-1645 ServiceImport type.
type ServiceType string

const (
	ServiceTypeClusterSetIP ServiceType = "ClusterSetIP"
	ServiceTypeHeadless     ServiceType = "Headless"
)

// Protocol is a Kubernetes port protocol.
type Protocol string

const (
	ProtocolTCP  Protocol = "TCP"
	ProtocolUDP  Protocol = "UDP"
	ProtocolSCTP Protocol = "SCTP"
)

// SessionAffinity is the Kubernetes Service session affinity.
type SessionAffinity string

const (
	SessionAffinityNone     SessionAffinity = "None"
	SessionAffinityClientIP SessionAffinity = "ClientIP"
)

// IPFamily is a Kubernetes IP family.
type IPFamily string

const (
	IPFamilyIPv4 IPFamily = "IPv4"
	IPFamilyIPv6 IPFamily = "IPv6"
)

// InternalTrafficPolicy is the Kubernetes internal traffic policy; empty
// means unset.
type InternalTrafficPolicy string

const (
	InternalTrafficPolicyCluster InternalTrafficPolicy = "Cluster"
	InternalTrafficPolicyLocal   InternalTrafficPolicy = "Local"
)

// AddressType is the EndpointSlice address type.
type AddressType string

const (
	AddressTypeIPv4 AddressType = "IPv4"
	AddressTypeIPv6 AddressType = "IPv6"
	AddressTypeFQDN AddressType = "FQDN"
)

// FrontendRole is what a pool Service or frontend serves.
type FrontendRole string

const (
	FrontendRoleOpenAI  FrontendRole = "OPENAI"
	FrontendRoleKVRelay FrontendRole = "KV_RELAY"
)

// DesiredState is the writer-requested state of a pool; empty means ACTIVE.
type DesiredState string

const (
	DesiredStateActive   DesiredState = "ACTIVE"
	DesiredStateDraining DesiredState = "DRAINING"
)

// ServicePort mirrors nvidia.gds.v1.ServicePort.
type ServicePort struct {
	Name        string
	Protocol    Protocol
	AppProtocol string
	Port        int32
}

// SessionAffinityConfig mirrors nvidia.gds.v1.SessionAffinityConfig.
type SessionAffinityConfig struct {
	ClientIP *ClientIPConfig
}

// ClientIPConfig mirrors nvidia.gds.v1.ClientIPConfig.
type ClientIPConfig struct {
	TimeoutSeconds int32
}

// ExportSpec mirrors nvidia.gds.v1.ServiceExportSpec.
type ExportSpec struct {
	Annotations             map[string]string
	Labels                  map[string]string
	ExportCreationTimestamp time.Time
	Ports                   []ServicePort
	Type                    ServiceType
	SessionAffinity         SessionAffinity
	SessionAffinityConfig   *SessionAffinityConfig
	IPFamilies              []IPFamily
	InternalTrafficPolicy   InternalTrafficPolicy
	TrafficDistribution     string
}

// EndpointConditions mirrors nvidia.gds.v1.EndpointConditions.
type EndpointConditions struct {
	Ready       bool
	Serving     bool
	Terminating bool
}

// EndpointPort mirrors nvidia.gds.v1.EndpointPort.
type EndpointPort struct {
	Name        string
	Protocol    Protocol
	AppProtocol string
	Port        int32
}

// Endpoint mirrors nvidia.gds.v1.Endpoint.
type Endpoint struct {
	Addresses   []string
	AddressType AddressType
	Conditions  EndpointConditions
	Hostname    string
	NodeName    string
	Zone        string
	Ports       []EndpointPort
	Attributes  map[string]string
}

// Location mirrors nvidia.gds.v1.Location.
type Location struct {
	Region  string
	DC      string
	Cluster string
	Zones   []string
}

// ModelServed mirrors nvidia.gds.v1.ModelServed.
type ModelServed struct {
	ModelID    string
	Aliases    []string
	Attributes map[string]string
}

// ServiceRef mirrors nvidia.gds.v1.ServiceRef.
type ServiceRef struct {
	Namespace string
	Name      string
	Role      FrontendRole
}

// FrontendEndpoint mirrors nvidia.gds.v1.FrontendEndpoint.
type FrontendEndpoint struct {
	Role     FrontendRole
	URL      string
	Service  *ServiceRef
	PortName string
}

// PoolSpec holds the writer-owned fields of nvidia.gds.v1.Pool.
type PoolSpec struct {
	Location         Location
	HardwareClass    string
	Models           []ModelServed
	WorkerCount      int32
	ReadyWorkerCount int32
	Frontends        []FrontendEndpoint
	Services         []ServiceRef
	Labels           map[string]string
	DesiredState     DesiredState
}

// Clone returns a deep copy.
func (r ClusterRecord) Clone() ClusterRecord {
	r.Zones = slices.Clone(r.Zones)
	r.Labels = maps.Clone(r.Labels)
	return r
}

// Clone returns a deep copy.
func (r ExportRecord) Clone() ExportRecord {
	r.Spec = r.Spec.Clone()
	return r
}

// Clone returns a deep copy.
func (r EndpointSetRecord) Clone() EndpointSetRecord {
	r.Endpoints = cloneSlice(r.Endpoints, Endpoint.Clone)
	return r
}

// Clone returns a deep copy.
func (r PoolRecord) Clone() PoolRecord {
	r.Spec = r.Spec.Clone()
	return r
}

// Clone returns a deep copy.
func (s ExportSpec) Clone() ExportSpec {
	s.Annotations = maps.Clone(s.Annotations)
	s.Labels = maps.Clone(s.Labels)
	s.Ports = slices.Clone(s.Ports)
	if s.SessionAffinityConfig != nil {
		c := *s.SessionAffinityConfig
		if c.ClientIP != nil {
			ip := *c.ClientIP
			c.ClientIP = &ip
		}
		s.SessionAffinityConfig = &c
	}
	s.IPFamilies = slices.Clone(s.IPFamilies)
	return s
}

// Clone returns a deep copy.
func (e Endpoint) Clone() Endpoint {
	e.Addresses = slices.Clone(e.Addresses)
	e.Ports = slices.Clone(e.Ports)
	e.Attributes = maps.Clone(e.Attributes)
	return e
}

// Clone returns a deep copy.
func (s PoolSpec) Clone() PoolSpec {
	s.Location.Zones = slices.Clone(s.Location.Zones)
	s.Models = cloneSlice(s.Models, func(m ModelServed) ModelServed {
		m.Aliases = slices.Clone(m.Aliases)
		m.Attributes = maps.Clone(m.Attributes)
		return m
	})
	s.Frontends = cloneSlice(s.Frontends, func(f FrontendEndpoint) FrontendEndpoint {
		if f.Service != nil {
			ref := *f.Service
			f.Service = &ref
		}
		return f
	})
	s.Services = slices.Clone(s.Services)
	s.Labels = maps.Clone(s.Labels)
	return s
}

func cloneSlice[T any](in []T, clone func(T) T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	for i, v := range in {
		out[i] = clone(v)
	}
	return out
}
