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

package dynamodb

import (
	"testing"
	"time"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 123456789, time.UTC)

func fullCluster() store.ClusterRecord {
	return store.ClusterRecord{
		Key:          store.ClusterKey{ClusterSet: "prod", Cluster: "c1"},
		Region:       "us-east-1",
		Zones:        []string{"us-east-1a", "us-east-1b"},
		Provider:     "eks",
		Labels:       map[string]string{"env": "prod", "tier": "gold"},
		AgentID:      "gds-mcs-controller-0",
		AgentVersion: "v0.1.0",
		Lease:        store.Lease{Epoch: 1792000000000, TTL: 30 * time.Second, RenewedAt: t0.Add(time.Minute)},
		Generation:   7,
		RegisteredAt: t0,
		UpdatedAt:    t0.Add(time.Second),
	}
}

func fullExport() store.ExportRecord {
	return store.ExportRecord{
		Key: store.ExportKey{ClusterSet: "prod", Cluster: "c1", Namespace: "llm", Name: "router"},
		Spec: store.ExportSpec{
			Annotations:             map[string]string{"gds.nvidia.com/dial-scheme": "https"},
			Labels:                  map[string]string{"gds.nvidia.com/role": "router"},
			ExportCreationTimestamp: t0.Add(-time.Hour),
			Ports: []store.ServicePort{
				{Name: "grpc", Protocol: store.ProtocolTCP, AppProtocol: "grpc", Port: 50071},
				{Name: "quic", Protocol: store.ProtocolUDP, Port: 50072},
				{Name: "sctp", Protocol: store.ProtocolSCTP, Port: 9},
			},
			Type:                  store.ServiceTypeHeadless,
			SessionAffinity:       store.SessionAffinityClientIP,
			SessionAffinityConfig: &store.SessionAffinityConfig{ClientIP: &store.ClientIPConfig{TimeoutSeconds: 10800}},
			IPFamilies:            []store.IPFamily{store.IPFamilyIPv4, store.IPFamilyIPv6},
			InternalTrafficPolicy: store.InternalTrafficPolicyLocal,
			TrafficDistribution:   "PreferClose",
		},
		Generation: 1792000000001,
		UpdatedAt:  t0,
	}
}

func fullEndpointSet() store.EndpointSetRecord {
	return store.EndpointSetRecord{
		Key: store.ExportKey{ClusterSet: "prod", Cluster: "c1", Namespace: "llm", Name: "router"},
		Endpoints: []store.Endpoint{
			{
				Addresses:   []string{"10.0.0.1"},
				AddressType: store.AddressTypeIPv4,
				Conditions:  store.EndpointConditions{Ready: true, Serving: true},
				Hostname:    "router-0",
				NodeName:    "node-1",
				Zone:        "us-east-1a",
				Ports:       []store.EndpointPort{{Name: "grpc", Protocol: store.ProtocolTCP, AppProtocol: "grpc", Port: 50071}},
				Attributes:  map[string]string{"pod": "router-0"},
			},
			{Addresses: []string{"fd00::1"}, AddressType: store.AddressTypeIPv6, Conditions: store.EndpointConditions{Terminating: true}},
			{Addresses: []string{"router.example.com"}, AddressType: store.AddressTypeFQDN},
		},
		Generation: 3,
		UpdatedAt:  t0,
	}
}

func fullPool() store.PoolRecord {
	return store.PoolRecord{
		Key:  store.PoolKey{ClusterSet: "prod", Cluster: "c1", PoolID: "p1"},
		Name: "pool-a",
		Spec: store.PoolSpec{
			Location:      store.Location{Region: "us-east-1", DC: "iad1", Cluster: "c1", Zones: []string{"us-east-1a"}},
			HardwareClass: "b200-ib",
			Models: []store.ModelServed{
				{ModelID: "meta/llama-3.1-8b-instruct", Aliases: []string{"llama-8b"}, Attributes: map[string]string{"context": "128k"}},
			},
			WorkerCount:      4,
			ReadyWorkerCount: 3,
			Frontends: []store.FrontendEndpoint{
				{Role: store.FrontendRoleOpenAI, Service: &store.ServiceRef{Namespace: "dynamo", Name: "frontend", Role: store.FrontendRoleOpenAI}, PortName: "http"},
				{Role: store.FrontendRoleKVRelay, URL: "https://relay.example.com"},
			},
			Services:     []store.ServiceRef{{Namespace: "dynamo", Name: "frontend", Role: store.FrontendRoleOpenAI}},
			Labels:       map[string]string{"tier": "gold"},
			DesiredState: store.DesiredStateDraining,
		},
		Lease:        store.Lease{Epoch: 42, TTL: time.Minute, RenewedAt: t0.Add(time.Minute)},
		Generation:   5,
		OwnerID:      "pool-operator",
		RegisteredAt: t0,
		UpdatedAt:    t0.Add(time.Second),
	}
}

// roundTrip marshals an item to attributes and back, as a write and a read
// do.
func roundTrip(t *testing.T, it item) item {
	t.Helper()
	av, err := it.attributes()
	require.NoError(t, err)
	got, err := parseItem(av)
	require.NoError(t, err)
	return got
}

func TestCodecRoundTrip(t *testing.T) {
	t.Run("cluster", func(t *testing.T) {
		rec := fullCluster()
		it, err := encodeCluster(rec, 1234)
		require.NoError(t, err)
		assert.Equal(t, int64(1234), it.ExpiresAt)
		got, err := decodeCluster(roundTrip(t, it))
		require.NoError(t, err)
		assert.Equal(t, rec, got)
	})
	t.Run("export", func(t *testing.T) {
		rec := fullExport()
		it, err := encodeExport(rec)
		require.NoError(t, err)
		got, err := decodeExport(roundTrip(t, it))
		require.NoError(t, err)
		assert.Equal(t, rec, got)
	})
	t.Run("endpoint set", func(t *testing.T) {
		rec := fullEndpointSet()
		it, err := encodeEndpointSet(rec)
		require.NoError(t, err)
		got, err := decodeEndpointSet(roundTrip(t, it))
		require.NoError(t, err)
		assert.Equal(t, rec, got)
	})
	t.Run("pool", func(t *testing.T) {
		rec := fullPool()
		it, err := encodePool(rec, 99)
		require.NoError(t, err)
		got, err := decodePool(roundTrip(t, it))
		require.NoError(t, err)
		assert.Equal(t, rec, got)
	})
}

// TestCodecZeroValues checks that empty records round-trip: zero times stay
// zero, a pool without a lease has no lease attributes, and generation 0 is
// still stored for the gen < :gen condition.
func TestCodecZeroValues(t *testing.T) {
	exp := store.ExportRecord{Key: store.ExportKey{ClusterSet: "cs", Cluster: "c", Namespace: "n", Name: "s"}}
	it, err := encodeExport(exp)
	require.NoError(t, err)
	av, err := it.attributes()
	require.NoError(t, err)
	assert.Equal(t, &ddbtypes.AttributeValueMemberN{Value: "0"}, av[attrGen])
	assert.NotContains(t, av, attrUpdatedAt)
	assert.NotContains(t, av, attrExpiresAt)
	got, err := decodeExport(roundTrip(t, it))
	require.NoError(t, err)
	assert.Equal(t, exp, got)

	pool := store.PoolRecord{Key: store.PoolKey{ClusterSet: "cs", Cluster: "c", PoolID: "p"}}
	it, err = encodePool(pool, 0)
	require.NoError(t, err)
	av, err = it.attributes()
	require.NoError(t, err)
	assert.NotContains(t, av, attrLeaseEpoch)
	assert.NotContains(t, av, attrLeaseRenewedAt)
	assert.NotContains(t, av, attrOwnerID)
	gotPool, err := decodePool(roundTrip(t, it))
	require.NoError(t, err)
	assert.Equal(t, store.Lease{}, gotPool.Lease)
	assert.True(t, gotPool.RegisteredAt.IsZero())

	eps := store.EndpointSetRecord{Key: exp.Key, Endpoints: []store.Endpoint{}}
	it, err = encodeEndpointSet(eps)
	require.NoError(t, err)
	gotEps, err := decodeEndpointSet(roundTrip(t, it))
	require.NoError(t, err)
	assert.Nil(t, gotEps.Endpoints)

	cl := store.ClusterRecord{Key: store.ClusterKey{ClusterSet: "cs", Cluster: "c"}}
	it, err = encodeCluster(cl, 0)
	require.NoError(t, err)
	gotCl, err := decodeCluster(roundTrip(t, it))
	require.NoError(t, err)
	assert.Equal(t, cl, gotCl)
}

// TestCodecTopLevelAttributes checks that the attributes kept at top level
// are not also in the body.
func TestCodecTopLevelAttributes(t *testing.T) {
	it, err := encodeCluster(fullCluster(), 0)
	require.NoError(t, err)
	var cl gdsv1.Cluster
	require.NoError(t, proto.Unmarshal(it.Body, &cl))
	assert.Empty(t, cl.Clusterset)
	assert.Empty(t, cl.Cluster)
	assert.Zero(t, cl.Generation)
	assert.Nil(t, cl.UpdatedAt)
	assert.Zero(t, cl.Lease.Epoch)
	assert.Nil(t, cl.Lease.RenewedAt)
	assert.Equal(t, 30*time.Second, cl.Lease.Ttl.AsDuration())
	assert.Equal(t, uint64(1792000000000), it.LeaseEpoch)

	it, err = encodePool(fullPool(), 0)
	require.NoError(t, err)
	var p gdsv1.Pool
	require.NoError(t, proto.Unmarshal(it.Body, &p))
	assert.Empty(t, p.PoolId)
	assert.Empty(t, p.OwnerId)
	assert.Zero(t, p.Generation)
	assert.Nil(t, p.UpdatedAt)
	assert.Zero(t, p.Lease.Epoch)
	assert.Equal(t, "pool-operator", it.OwnerID)
}

func TestCodecBodyIsDeterministic(t *testing.T) {
	rec := fullExport()
	for k := range 50 {
		rec.Spec.Annotations[string(rune('a'+k%26))+"-key"] = "v"
	}
	first, err := encodeExport(rec)
	require.NoError(t, err)
	for range 10 {
		again, err := encodeExport(rec)
		require.NoError(t, err)
		assert.Equal(t, first.Body, again.Body)
	}
}

func TestCodecRejectsUnknownEnums(t *testing.T) {
	exp := fullExport()
	exp.Spec.Type = "LoadBalancer"
	_, err := encodeExport(exp)
	assert.ErrorContains(t, err, `unknown service type "LoadBalancer"`)

	exp = fullExport()
	exp.Spec.IPFamilies = []store.IPFamily{"IPv5"}
	_, err = encodeExport(exp)
	assert.ErrorContains(t, err, "unknown IP family")

	eps := fullEndpointSet()
	eps.Endpoints[0].Ports[0].Protocol = "QUIC"
	_, err = encodeEndpointSet(eps)
	assert.ErrorContains(t, err, `unknown protocol "QUIC"`)

	pool := fullPool()
	pool.Spec.DesiredState = "GONE"
	_, err = encodePool(pool, 0)
	assert.ErrorContains(t, err, `unknown desired state "GONE"`)

	pool = fullPool()
	pool.Spec.Services[0].Role = "ROUTER"
	_, err = encodePool(pool, 0)
	assert.ErrorContains(t, err, `unknown frontend role "ROUTER"`)
}

// TestCodecDecodesUnknownProtoEnums checks that values from a newer
// contract decode to the empty value instead of failing a load.
func TestCodecDecodesUnknownProtoEnums(t *testing.T) {
	body, err := proto.Marshal(&gdsv1.ServiceExportSpec{Type: 99, Ports: []*gdsv1.ServicePort{{Protocol: 42, Port: 80}}})
	require.NoError(t, err)
	k := exportKey(store.ExportKey{ClusterSet: "cs", Cluster: "c", Namespace: "n", Name: "s"})
	got, err := decodeExport(item{PK: k.pk, SK: k.sk, Body: body, Gen: ptr(uint64(1))})
	require.NoError(t, err)
	assert.Equal(t, store.ServiceType(""), got.Spec.Type)
	assert.Equal(t, []store.ServicePort{{Port: 80}}, got.Spec.Ports)
}

func TestCodecDecodeErrors(t *testing.T) {
	bad := item{PK: "CSX", SK: "CL#c1"}
	_, err := decodeCluster(bad)
	assert.Error(t, err)
	_, err = decodeExport(bad)
	assert.Error(t, err)
	_, err = decodePool(bad)
	assert.Error(t, err)
	_, err = decodeEndpointSet(bad)
	assert.Error(t, err)

	wrongSK := item{PK: "CS#cs", SK: "XX#c1"}
	_, err = decodeCluster(wrongSK)
	assert.Error(t, err)
	_, err = decodeExport(wrongSK)
	assert.Error(t, err)
	_, err = decodePool(wrongSK)
	assert.Error(t, err)
	_, err = decodeEndpointSet(item{PK: "EP#cs#c1", SK: "only-one-part"})
	assert.Error(t, err)

	garbage := []byte{0xff, 0xff, 0xff}
	_, err = decodeCluster(item{PK: "CS#cs", SK: "CL#c1", Body: garbage})
	assert.ErrorContains(t, err, "unmarshal body of CS#cs/CL#c1")
	_, err = decodeExport(item{PK: "CS#cs", SK: "EX#c1#n#s", Body: garbage})
	assert.Error(t, err)
	_, err = decodePool(item{PK: "CS#cs", SK: "PL#c1#p", Body: garbage})
	assert.Error(t, err)
	_, err = decodeEndpointSet(item{PK: "EP#cs#c1", SK: "n#s", Body: garbage})
	assert.Error(t, err)

	_, err = parseItem(map[string]ddbtypes.AttributeValue{attrGen: &ddbtypes.AttributeValueMemberS{Value: "x"}})
	assert.ErrorContains(t, err, "unmarshal item")
}
