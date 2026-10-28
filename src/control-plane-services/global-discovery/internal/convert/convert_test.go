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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func TestEnumsRoundTrip(t *testing.T) {
	for k, v := range serviceTypes {
		assert.Equal(t, k, ServiceTypeFromProto(ServiceTypeToProto(k)))
		assert.Equal(t, v, ServiceTypeToProto(k))
	}
	for k := range protocols {
		assert.Equal(t, k, ProtocolFromProto(ProtocolToProto(k)))
	}
	for k := range sessionAffinities {
		assert.Equal(t, k, SessionAffinityFromProto(SessionAffinityToProto(k)))
	}
	for k := range ipFamilies {
		assert.Equal(t, k, IPFamilyFromProto(IPFamilyToProto(k)))
	}
	for k := range trafficPolicies {
		assert.Equal(t, k, InternalTrafficPolicyFromProto(InternalTrafficPolicyToProto(k)))
	}
	for k := range addressTypes {
		assert.Equal(t, k, AddressTypeFromProto(AddressTypeToProto(k)))
	}
	for k := range frontendRoles {
		assert.Equal(t, k, FrontendRoleFromProto(FrontendRoleToProto(k)))
	}
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON, FrontendRoleToProto(store.FrontendRolePylon))
	for k := range agentSources {
		assert.Equal(t, k, AgentSourceFromProto(AgentSourceToProto(k)))
	}
	for k := range dialModes {
		assert.Equal(t, k, DialModeFromProto(DialModeToProto(k)))
	}
	assert.Equal(t, gdsv1.AgentSource_AGENT_SOURCE_UNSPECIFIED, AgentSourceToProto(""))
	assert.Equal(t, gdsv1.DialMode_DIAL_MODE_UNSPECIFIED, DialModeToProto(""))
	for k := range desiredStates {
		assert.Equal(t, k, DesiredStateFromProto(DesiredStateToProto(k)))
	}
	for k := range poolStates {
		assert.Equal(t, k, PoolStateFromProto(PoolStateToProto(k)))
	}
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, DesiredStateToProto(""))
	assert.Equal(t, store.DesiredState(""), DesiredStateFromProto(gdsv1.PoolState_POOL_STATE_GONE))
	assert.Equal(t, store.ServiceType(""), ServiceTypeFromProto(gdsv1.ServiceType_SERVICE_TYPE_UNSPECIFIED))
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, LeaseStateToProto(registry.LeaseStateExpired))
}

func TestTimes(t *testing.T) {
	assert.Nil(t, Timestamp(time.Time{}))
	assert.True(t, Time(nil).IsZero())
	assert.Equal(t, t0, Time(Timestamp(t0)))
	assert.Nil(t, Duration(0))
	assert.Equal(t, time.Second, Duration(time.Second).AsDuration())
}

func TestExportSpecRoundTrip(t *testing.T) {
	spec := store.ExportSpec{
		Annotations:             map[string]string{"a": "1"},
		Labels:                  map[string]string{"l": "2"},
		ExportCreationTimestamp: t0,
		Ports:                   []store.ServicePort{{Name: "http", Protocol: store.ProtocolTCP, AppProtocol: "h2c", Port: 80}},
		Type:                    store.ServiceTypeHeadless,
		SessionAffinity:         store.SessionAffinityClientIP,
		SessionAffinityConfig:   &store.SessionAffinityConfig{ClientIP: &store.ClientIPConfig{TimeoutSeconds: 30}},
		IPFamilies:              []store.IPFamily{store.IPFamilyIPv6, store.IPFamilyIPv4},
		InternalTrafficPolicy:   store.InternalTrafficPolicyLocal,
		TrafficDistribution:     "PreferClose",
	}
	assert.Equal(t, spec, ExportSpecFromProto(ExportSpecToProto(spec)))
	assert.Equal(t, store.ExportSpec{}, ExportSpecFromProto(nil))

	empty := ExportSpecFromProto(&gdsv1.ServiceExportSpec{SessionAffinityConfig: &gdsv1.SessionAffinityConfig{}})
	require.NotNil(t, empty.SessionAffinityConfig)
	assert.Nil(t, empty.SessionAffinityConfig.ClientIP)
	assert.Nil(t, SessionAffinityConfigToProto(nil))
	assert.Nil(t, ServicePortsToProto(nil))
	assert.Nil(t, IPFamiliesToProto(nil))
}

func TestEndpointRoundTrip(t *testing.T) {
	ep := store.Endpoint{
		Addresses:   []string{"10.0.0.1"},
		AddressType: store.AddressTypeIPv4,
		Conditions:  store.EndpointConditions{Ready: true, Serving: true},
		Hostname:    "pod-0",
		NodeName:    "node-a",
		Zone:        "us-east-1a",
		Ports:       []store.EndpointPort{{Name: "grpc", Protocol: store.ProtocolTCP, Port: 50071}},
		Attributes:  map[string]string{"k": "v"},
	}
	assert.Equal(t, ep, EndpointFromProto(EndpointToProto(ep)))
}

func TestCluster(t *testing.T) {
	rec := store.ClusterRecord{
		Key:          store.ClusterKey{ClusterSet: "cs", Cluster: "a"},
		Region:       "us-east-1",
		Zones:        []string{"z"},
		Provider:     "eks",
		Labels:       map[string]string{"env": "test"},
		AgentID:      "agent",
		AgentVersion: "v1",
		Lease:        store.Lease{Epoch: 7, TTL: 30 * time.Second, RenewedAt: t0},
		Generation:   3,
		RegisteredAt: t0,
		UpdatedAt:    t0,
	}
	c := ClusterToProto(rec, registry.LeaseStateActive)
	assert.Equal(t, "a", c.GetCluster())
	assert.Equal(t, uint64(7), c.GetLease().GetEpoch())
	assert.Equal(t, t0.Add(30*time.Second), c.GetLease().GetExpiresAt().AsTime())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_ACTIVE, c.GetLease().GetState())
	assert.Nil(t, LeaseToProto(store.Lease{}, registry.LeaseStateActive))
}

func TestPoolAgents(t *testing.T) {
	assert.Equal(t, registry.DialModeInbound, DialModeOf(store.FrontendRoleOpenAI))
	assert.Equal(t, registry.DialModeOutbound, DialModeOf(store.FrontendRoleKVRelay))
	assert.Equal(t, registry.DialModeOutbound, DialModeOf(store.FrontendRolePylon))
	assert.Empty(t, DialModeOf("ROUTER"))
	assert.True(t, IsIngressRole(store.FrontendRoleOpenAI))
	assert.True(t, IsIngressRole(store.FrontendRolePylon))
	assert.False(t, IsIngressRole(store.FrontendRoleKVRelay))

	rec := store.PoolAgentRecord{
		Key:          store.PoolAgentKey{ClusterSet: "cs", Cluster: "a", PoolID: "p", Role: store.FrontendRolePylon, AgentID: "pod-0"},
		PoolName:     "pool",
		Attributes:   map[string]string{"k": "v"},
		Lease:        store.Lease{Epoch: 9, TTL: time.Minute, RenewedAt: t0},
		Generation:   4,
		RegisteredAt: t0,
		UpdatedAt:    t0.Add(time.Second),
	}
	r := PoolAgentRegistrationToProto(rec, registry.LeaseStateExpired)
	assert.Equal(t, "pod-0", r.GetAgentId())
	assert.Equal(t, uint64(9), r.GetLease().GetEpoch())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, r.GetLease().GetState())
	assert.Equal(t, uint64(4), r.GetGeneration())
	assert.Equal(t, t0.Add(time.Second), r.GetUpdatedAt().AsTime())
	r.Attributes["k"] = "mutated"
	assert.Equal(t, "v", rec.Attributes["k"], "attributes are copied")
}

func TestPoolSpecRoundTrip(t *testing.T) {
	spec := store.PoolSpec{
		Location:         store.Location{Region: "r", DC: "dc1", Cluster: "a", Zones: []string{"z"}},
		HardwareClass:    "b200",
		Models:           []store.ModelServed{{ModelID: "m", Aliases: []string{"x"}, Attributes: map[string]string{"k": "v"}}},
		WorkerCount:      4,
		ReadyWorkerCount: 3,
		Frontends: []store.FrontendEndpoint{
			{Role: store.FrontendRoleOpenAI, URL: "https://lb"},
			{Role: store.FrontendRoleKVRelay, Service: &store.ServiceRef{Namespace: "ns", Name: "relay", Role: store.FrontendRoleKVRelay}, PortName: "grpc"},
		},
		Services:     []store.ServiceRef{{Namespace: "ns", Name: "fe", Role: store.FrontendRoleOpenAI}},
		Labels:       map[string]string{"tier": "gold"},
		DesiredState: store.DesiredStateDraining,
	}
	p := &gdsv1.Pool{}
	PoolSpecToProto(spec, p)
	assert.Equal(t, spec, PoolSpecFromProto(p))
	assert.True(t, proto.Equal(p, func() *gdsv1.Pool { q := &gdsv1.Pool{}; PoolSpecToProto(PoolSpecFromProto(p), q); return q }()))
}
