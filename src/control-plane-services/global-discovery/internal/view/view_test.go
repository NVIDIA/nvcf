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

package view

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/clock"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

const cs = "prod"

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fixture struct {
	clock   *clock.Fake
	metrics *metrics.Metrics
	view    *View
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{clock: clock.NewFake(t0), metrics: metrics.New(prometheus.NewRegistry())}
	f.view = New(Options{
		Clock:            f.clock,
		LeaseSkew:        2 * time.Second,
		WatchHistory:     100,
		TombstoneTTL:     5 * time.Second,
		LocalWriteWindow: 2 * time.Second,
		Epoch:            "e1",
		Metrics:          f.metrics,
	})
	return f
}

func clusterRec(name, region string, renewed time.Time) store.ClusterRecord {
	return store.ClusterRecord{
		Key:          store.ClusterKey{ClusterSet: cs, Cluster: name},
		Region:       region,
		Zones:        []string{region + "a"},
		Lease:        store.Lease{Epoch: uint64(renewed.UnixMilli()), TTL: 30 * time.Second, RenewedAt: renewed},
		Generation:   1,
		RegisteredAt: renewed,
		UpdatedAt:    renewed,
	}
}

func exportKey(cluster, ns, name string) store.ExportKey {
	return store.ExportKey{ClusterSet: cs, Cluster: cluster, Namespace: ns, Name: name}
}

func exportRec(cluster, ns, name string, gen uint64, created time.Time, labels, annotations map[string]string, ports ...store.ServicePort) store.ExportRecord {
	return store.ExportRecord{
		Key: exportKey(cluster, ns, name),
		Spec: store.ExportSpec{
			Annotations:             annotations,
			Labels:                  labels,
			ExportCreationTimestamp: created,
			Ports:                   ports,
			Type:                    store.ServiceTypeClusterSetIP,
			SessionAffinity:         store.SessionAffinityNone,
			IPFamilies:              []store.IPFamily{store.IPFamilyIPv4},
		},
		Generation: gen,
		UpdatedAt:  created,
	}
}

func endpointSet(cluster, ns, name string, gen uint64, ready ...bool) store.EndpointSetRecord {
	rec := store.EndpointSetRecord{Key: exportKey(cluster, ns, name), Generation: gen, UpdatedAt: t0}
	for i, r := range ready {
		rec.Endpoints = append(rec.Endpoints, store.Endpoint{
			Addresses:   []string{fmt.Sprintf("10.%d.0.%d", len(cluster), i+1)},
			AddressType: store.AddressTypeIPv4,
			Conditions:  store.EndpointConditions{Ready: r, Serving: r},
			Hostname:    fmt.Sprintf("%s-%d", name, i),
			Zone:        "z1",
		})
	}
	return rec
}

func importOf(t *testing.T, v *View, ns, name string) *gdsv1.ServiceImport {
	t.Helper()
	o, ok := v.Get(cs, KindServiceImport, ImportKey(ns, name))
	require.True(t, ok, "import %s/%s", ns, name)
	return o.Full.(*gdsv1.ServiceImport)
}

func poolOf(t *testing.T, v *View, id string) *gdsv1.Pool {
	t.Helper()
	o, ok := v.Get(cs, KindPool, id)
	require.True(t, ok, "pool %s", id)
	return o.Full.(*gdsv1.Pool)
}

var httpPort = store.ServicePort{Name: "http", Protocol: store.ProtocolTCP, Port: 8000}

func baseSnapshot() store.ClusterSetSnapshot {
	snap := store.ClusterSetSnapshot{
		ClusterSet: cs,
		Clusters:   []store.ClusterRecord{clusterRec("a", "us-east-1", t0), clusterRec("b", "us-west-2", t0)},
		Exports: []store.ExportRecord{
			exportRec("a", "ns", "web", 1, t0.Add(-time.Hour), map[string]string{"app": "web"}, nil, httpPort),
			exportRec("b", "ns", "web", 1, t0, map[string]string{"app": "web"}, nil, httpPort),
		},
		EndpointSets: []store.EndpointSetRecord{
			endpointSet("a", "ns", "web", 5, true, false),
			endpointSet("b", "ns", "web", 6, true),
		},
	}
	snap.Sort()
	return snap
}

func TestReplaceDerivesImportsAndClusters(t *testing.T) {
	f := newFixture(t)
	f.view.Replace(baseSnapshot(), t0)

	si := importOf(t, f.view, "ns", "web")
	assert.Equal(t, gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP, si.GetType())
	assert.Nil(t, si.GetConflict())
	require.Len(t, si.GetClusters(), 2)
	assert.Equal(t, "a", si.GetClusters()[0].GetCluster())
	assert.Equal(t, "us-east-1", si.GetClusters()[0].GetRegion())
	assert.True(t, si.GetClusters()[0].GetParticipating())
	assert.Equal(t, int32(2), si.GetClusters()[0].GetEndpointCount())
	assert.Equal(t, int32(1), si.GetClusters()[0].GetReadyEndpointCount())
	require.Len(t, si.GetEndpoints(), 2)
	assert.Equal(t, uint64(5), si.GetEndpoints()[0].GetGeneration())
	assert.Len(t, si.GetEndpoints()[0].GetEndpoints(), 2)
	assert.Equal(t, "web", si.GetLabels()["app"])
	require.Len(t, si.GetClusters()[0].GetConditions(), 3)
	assert.Equal(t, t0, si.GetClusters()[0].GetConditions()[0].GetLastTransitionAt().AsTime())

	o, ok := f.view.Get(cs, KindCluster, "a")
	require.True(t, ok)
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_ACTIVE, o.Full.(*gdsv1.Cluster).GetLease().GetState())

	rev := f.view.Revision(cs)
	f.view.Replace(baseSnapshot(), t0)
	assert.Equal(t, rev, f.view.Revision(cs), "an unchanged snapshot publishes nothing")
	assert.Equal(t, "e1:3", rev, "two clusters and one import")
}

func TestLeaseExpiryExcludesCluster(t *testing.T) {
	f := newFixture(t)
	snap := baseSnapshot()
	snap.Clusters[1].Lease.RenewedAt = t0.Add(-40 * time.Second)
	snap.Exports = append(snap.Exports, exportRec("b", "ns", "router", 1, t0,
		map[string]string{api.LabelRole: api.RoleRouter}, nil, store.ServicePort{Name: "grpc", Port: 50071}))
	snap.EndpointSets = append(snap.EndpointSets, endpointSet("b", "ns", "router", 1, true))
	f.view.Replace(snap, t0)

	si := importOf(t, f.view, "ns", "web")
	require.Len(t, si.GetEndpoints(), 1, "b's lease is expired")
	assert.Equal(t, "a", si.GetEndpoints()[0].GetCluster())
	assert.False(t, si.GetClusters()[1].GetParticipating())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, si.GetClusters()[1].GetLeaseState())
	r, ok := f.view.Get(cs, KindRouter, RouterKey("b", "ns", "router"))
	require.True(t, ok)
	assert.Empty(t, r.Full.(*gdsv1.Router).GetReplicas())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, r.Full.(*gdsv1.Router).GetClusterLeaseState())

	// a expires too once the clock passes ttl + skew.
	f.clock.Advance(31 * time.Second)
	f.view.EvaluateLeases()
	assert.Len(t, importOf(t, f.view, "ns", "web").GetEndpoints(), 1)
	f.clock.Advance(2 * time.Second)
	f.view.EvaluateLeases()
	si = importOf(t, f.view, "ns", "web")
	assert.Empty(t, si.GetEndpoints())
	assert.Equal(t, gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP, si.GetType(), "the shape stays")
	assert.Equal(t, 1.0, testutilValue(f.metrics.LeaseExpirations.WithLabelValues(metrics.LeaseCluster)))

	// A renewal brings a back.
	f.view.ApplyCluster(clusterRec("a", "us-east-1", f.clock.Now()))
	assert.Len(t, importOf(t, f.view, "ns", "web").GetEndpoints(), 1)
}

func TestConflictAndTransitionTimes(t *testing.T) {
	f := newFixture(t)
	snap := baseSnapshot()
	snap.Exports[1].Spec.Type = store.ServiceTypeHeadless
	f.view.Replace(snap, t0)
	si := importOf(t, f.view, "ns", "web")
	require.NotNil(t, si.GetConflict())
	assert.Equal(t, api.ConditionReasonTypeConflict, si.GetConflict().GetReason())
	assert.Equal(t, "a", si.GetConflict().GetWinningCluster())
	assert.NotNil(t, si.GetClusters()[1].GetConflict())

	// The conflict is resolved later: the Conflict condition transitions,
	// Valid and Ready keep their times.
	f.clock.Advance(time.Minute)
	f.view.ApplyExport(exportRec("b", "ns", "web", 2, t0, map[string]string{"app": "web"}, nil, httpPort))
	si = importOf(t, f.view, "ns", "web")
	assert.Nil(t, si.GetConflict())
	conds := si.GetClusters()[1].GetConditions()
	assert.Equal(t, t0, conds[0].GetLastTransitionAt().AsTime())
	assert.Equal(t, t0.Add(time.Minute), conds[2].GetLastTransitionAt().AsTime())
}

func TestLocalWritesSurviveStaleSnapshot(t *testing.T) {
	f := newFixture(t)
	f.view.Replace(baseSnapshot(), t0)
	loadStart := f.clock.Now()

	f.clock.Advance(100 * time.Millisecond)
	newer := exportRec("a", "ns", "web", 2, t0.Add(-time.Hour), map[string]string{"app": "web", "v": "2"}, nil, httpPort)
	f.view.ApplyExport(newer)
	f.view.ApplyExportDelete(exportKey("b", "ns", "web"))
	added := exportRec("a", "ns", "new", 1, t0, nil, nil, httpPort)
	f.view.ApplyExport(added)

	// The snapshot was loaded before the writes.
	f.view.Replace(baseSnapshot(), loadStart)
	rec, ok := f.view.ExportRecord(exportKey("a", "ns", "web"))
	require.True(t, ok)
	assert.Equal(t, uint64(2), rec.Generation)
	_, ok = f.view.ExportRecord(exportKey("b", "ns", "web"))
	assert.False(t, ok, "the local delete wins over the stale snapshot")
	_, ok = f.view.ExportRecord(exportKey("a", "ns", "new"))
	assert.True(t, ok)

	// A snapshot with a newer version wins even within the window.
	snap := baseSnapshot()
	snap.Exports[0].Generation = 3
	snap.Exports[1].Generation = 9
	f.view.Replace(snap, loadStart)
	rec, _ = f.view.ExportRecord(exportKey("a", "ns", "web"))
	assert.Equal(t, uint64(3), rec.Generation)
	rec, ok = f.view.ExportRecord(exportKey("b", "ns", "web"))
	require.True(t, ok, "a newer version than the deleted one was written elsewhere")
	assert.Equal(t, uint64(9), rec.Generation)

	// After the window the store is the truth again.
	f.clock.Advance(5 * time.Second)
	f.view.Replace(baseSnapshot(), f.clock.Now())
	_, ok = f.view.ExportRecord(exportKey("a", "ns", "new"))
	assert.False(t, ok)
}

func TestClusterDeleteCascades(t *testing.T) {
	f := newFixture(t)
	snap := baseSnapshot()
	snap.Pools = []store.PoolRecord{{
		Key:  store.PoolKey{ClusterSet: cs, Cluster: "b", PoolID: "p1"},
		Name: "pool-b",
		Spec: store.PoolSpec{Location: store.Location{Cluster: "b"}, Frontends: []store.FrontendEndpoint{{Role: store.FrontendRoleOpenAI, URL: "http://lb"}}},
	}}
	f.view.Replace(snap, t0)
	_, ok := f.view.Get(cs, KindPool, "p1")
	require.True(t, ok)

	loadStart := f.clock.Now()
	f.clock.Advance(10 * time.Millisecond)
	f.view.ApplyClusterDelete(store.ClusterKey{ClusterSet: cs, Cluster: "b"})
	f.view.Replace(snap, loadStart)
	_, ok = f.view.ClusterRecord(store.ClusterKey{ClusterSet: cs, Cluster: "b"})
	assert.False(t, ok)
	_, ok = f.view.ExportRecord(exportKey("b", "ns", "web"))
	assert.False(t, ok)
	assert.Len(t, importOf(t, f.view, "ns", "web").GetClusters(), 1)
	p := poolOf(t, f.view, "p1")
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, p.GetState(), "a tombstone")

	// A re-registration after the delete is kept.
	f.clock.Advance(10 * time.Millisecond)
	f.view.ApplyCluster(clusterRec("b", "us-west-2", f.clock.Now()))
	f.view.Replace(snap, loadStart)
	_, ok = f.view.ClusterRecord(store.ClusterKey{ClusterSet: cs, Cluster: "b"})
	assert.True(t, ok)
}

func poolLabels(pool, role string) map[string]string {
	return map[string]string{api.LabelExport: "true", api.LabelRole: role, api.LabelPool: pool, "tier": "gold"}
}

func labelledPoolSnapshot() store.ClusterSetSnapshot {
	snap := store.ClusterSetSnapshot{
		ClusterSet: cs,
		Clusters:   []store.ClusterRecord{clusterRec("a", "us-east-1", t0)},
		Exports: []store.ExportRecord{
			exportRec("a", "dyn", "frontend", 1, t0.Add(-time.Minute), poolLabels("llama", api.RolePoolFrontend), map[string]string{
				api.AnnotationPoolModels:    "llama-3, llama-3-70b",
				api.AnnotationHardwareClass: "b200-ib",
				api.AnnotationDC:            "dc1",
				api.AnnotationHTTPPortName:  "web",
			}, store.ServicePort{Name: "web", Port: 8000}),
			exportRec("a", "dyn", "relay", 1, t0, poolLabels("llama", api.RoleKVRelay), nil, store.ServicePort{Name: "grpc", Port: 9000}),
		},
		EndpointSets: []store.EndpointSetRecord{
			endpointSet("a", "dyn", "frontend", 1, true, true, false),
			endpointSet("a", "dyn", "relay", 1, true),
		},
	}
	snap.Sort()
	return snap
}

func TestLabelDerivedPool(t *testing.T) {
	f := newFixture(t)
	f.view.Replace(labelledPoolSnapshot(), t0)
	id := registry.PoolID(cs, "a", "llama")
	p := poolOf(t, f.view, id)

	assert.Equal(t, "llama", p.GetName())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetDesiredState())
	assert.Equal(t, "us-east-1", p.GetLocation().GetRegion())
	assert.Equal(t, "dc1", p.GetLocation().GetDc())
	assert.Equal(t, "a", p.GetLocation().GetCluster())
	assert.Equal(t, "b200-ib", p.GetHardwareClass())
	require.Len(t, p.GetModels(), 2)
	assert.Equal(t, "llama-3-70b", p.GetModels()[1].GetModelId())
	assert.Equal(t, "gold", p.GetLabels()["tier"])
	assert.NotContains(t, p.GetLabels(), api.LabelRole)
	assert.Equal(t, int32(2), p.GetReadyEndpointCount(), "ready endpoints of the OPENAI frontend only")
	assert.Equal(t, t0.Add(-time.Minute), p.GetRegisteredAt().AsTime())
	assert.Nil(t, p.GetLease())

	require.Len(t, p.GetFrontends(), 2)
	fe := p.GetFrontends()[0]
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, fe.GetRole())
	assert.Equal(t, "frontend", fe.GetService().GetName())
	assert.Equal(t, "web", fe.GetPortName())
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY, p.GetFrontends()[1].GetRole())
	assert.Equal(t, "grpc", p.GetFrontends()[1].GetPortName())

	require.Len(t, p.GetEndpoints(), 2)
	assert.Equal(t, "frontend", p.GetEndpoints()[0].GetName())
	assert.Equal(t, int32(3), p.GetEndpoints()[0].GetEndpointCount())
	assert.Len(t, p.GetEndpoints()[0].GetEndpoints(), 3)

	lite, _ := f.view.Get(cs, KindPool, id)
	assert.Empty(t, lite.Lite.(*gdsv1.Pool).GetEndpoints()[0].GetEndpoints())
	assert.Equal(t, int32(3), lite.Lite.(*gdsv1.Pool).GetEndpoints()[0].GetEndpointCount())

	// Readiness loss after the pool started makes it UNHEALTHY, not
	// REGISTERED.
	f.view.ApplyEndpointSet(endpointSet("a", "dyn", "frontend", 2, false))
	f.view.ApplyEndpointSet(endpointSet("a", "dyn", "relay", 2, false))
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_UNHEALTHY, poolOf(t, f.view, id).GetState())

	// The cluster lease expiring hides the endpoints.
	f.view.ApplyEndpointSet(endpointSet("a", "dyn", "frontend", 3, true))
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, poolOf(t, f.view, id).GetState())
	f.clock.Advance(33 * time.Second)
	f.view.EvaluateLeases()
	p = poolOf(t, f.view, id)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_UNHEALTHY, p.GetState())
	assert.Zero(t, p.GetReadyEndpointCount())
}

func TestPoolRegisteredUntilReady(t *testing.T) {
	f := newFixture(t)
	snap := labelledPoolSnapshot()
	snap.EndpointSets = nil
	f.view.Replace(snap, t0)
	id := registry.PoolID(cs, "a", "llama")
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, poolOf(t, f.view, id).GetState())
}

func TestPoolOverlay(t *testing.T) {
	f := newFixture(t)
	snap := labelledPoolSnapshot()
	snap.Pools = []store.PoolRecord{{
		Key:  store.PoolKey{ClusterSet: cs, Cluster: "a", PoolID: "custom-id"},
		Name: "llama",
		Spec: store.PoolSpec{
			Location:         store.Location{Cluster: "a", DC: "dc9"},
			WorkerCount:      8,
			ReadyWorkerCount: 6,
			Labels:           map[string]string{"tier": "silver", "color": "blue"},
			DesiredState:     store.DesiredStateDraining,
			Frontends:        []store.FrontendEndpoint{{Role: store.FrontendRoleOpenAI, URL: "https://lb.example"}},
		},
		Generation:   7,
		OwnerID:      "relay-0",
		RegisteredAt: t0.Add(-time.Hour),
		UpdatedAt:    t0,
	}}
	f.view.Replace(snap, t0)

	_, ok := f.view.Get(cs, KindPool, registry.PoolID(cs, "a", "llama"))
	assert.False(t, ok, "the overlay's pool id wins")
	p := poolOf(t, f.view, "custom-id")
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_DRAINING, p.GetState())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_DRAINING, p.GetDesiredState())
	assert.Equal(t, int32(8), p.GetWorkerCount())
	assert.Equal(t, "dc9", p.GetLocation().GetDc())
	assert.Equal(t, "b200-ib", p.GetHardwareClass(), "label metadata fills gaps")
	assert.Equal(t, map[string]string{"tier": "silver", "color": "blue", api.LabelExport: "true", api.LabelPool: "llama"}, p.GetLabels())
	assert.Len(t, p.GetFrontends(), 3)
	assert.Equal(t, "https://lb.example", p.GetFrontends()[0].GetUrl())
	assert.Equal(t, uint64(7), p.GetGeneration())
	assert.Equal(t, "relay-0", p.GetOwnerId())
	assert.Equal(t, t0.Add(-time.Hour), p.GetRegisteredAt().AsTime())

	got, ok := f.view.PoolByName(cs, "a", "llama")
	require.True(t, ok)
	assert.Equal(t, "custom-id", got.GetPoolId())
	rec, ok := f.view.PoolRecord(cs, "custom-id")
	require.True(t, ok)
	assert.Equal(t, "llama", rec.Name)
	_, ok = f.view.PoolRecord(cs, "nope")
	assert.False(t, ok)
}

func apiPool(id, cluster, name string, lease store.Lease, services ...store.ServiceRef) store.PoolRecord {
	return store.PoolRecord{
		Key:  store.PoolKey{ClusterSet: cs, Cluster: cluster, PoolID: id},
		Name: name,
		Spec: store.PoolSpec{
			Location:  store.Location{Cluster: cluster},
			Models:    []store.ModelServed{{ModelID: "mistral", Aliases: []string{"mistral-7b"}}},
			Frontends: []store.FrontendEndpoint{{Role: store.FrontendRoleOpenAI, URL: "https://" + name}},
			Services:  services,
		},
		Lease:        lease,
		Generation:   1,
		RegisteredAt: lease.RenewedAt,
		UpdatedAt:    lease.RenewedAt,
	}
}

func TestAPIOnlyPoolLifecycle(t *testing.T) {
	f := newFixture(t)
	lease := store.Lease{Epoch: uint64(t0.UnixMilli()), TTL: 60 * time.Second, RenewedAt: t0}
	rec := apiPool("p1", "a", "mistral", lease)
	f.view.ApplyPool(rec)
	p := poolOf(t, f.view, "p1")
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, p.GetState())
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_ACTIVE, p.GetLease().GetState())

	// The first heartbeat starts it.
	f.clock.Advance(20 * time.Second)
	rec.Lease.RenewedAt = f.clock.Now()
	f.view.ApplyPool(rec)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, poolOf(t, f.view, "p1").GetState())

	// A replica that sees only the renewed record infers the heartbeat
	// from the lease epoch.
	g := newFixture(t)
	g.view.ApplyPool(rec)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, poolOf(t, g.view, "p1").GetState())

	// The lease expires.
	f.clock.Advance(63 * time.Second)
	f.view.EvaluateLeases()
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_UNHEALTHY, poolOf(t, f.view, "p1").GetState())
	assert.Equal(t, 1.0, testutilValue(f.metrics.LeaseExpirations.WithLabelValues(metrics.LeasePool)))

	// Deregistration leaves a GONE tombstone for the tombstone TTL.
	f.view.ApplyPoolDelete(rec.Key)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, poolOf(t, f.view, "p1").GetState())
	f.clock.Advance(4 * time.Second)
	f.view.EvaluateLeases()
	_, ok := f.view.Get(cs, KindPool, "p1")
	assert.True(t, ok)
	f.clock.Advance(time.Second)
	f.view.EvaluateLeases()
	_, ok = f.view.Get(cs, KindPool, "p1")
	assert.False(t, ok)
}

func TestJoinedAPIPool(t *testing.T) {
	f := newFixture(t)
	snap := store.ClusterSetSnapshot{
		ClusterSet:   cs,
		Clusters:     []store.ClusterRecord{clusterRec("a", "us-east-1", t0)},
		Exports:      []store.ExportRecord{exportRec("a", "ns", "fe", 1, t0, nil, map[string]string{api.AnnotationHTTPPortName: "openai"}, httpPort)},
		EndpointSets: []store.EndpointSetRecord{endpointSet("a", "ns", "fe", 1, true)},
		Pools: []store.PoolRecord{apiPool("p2", "a", "joined", store.Lease{}, store.ServiceRef{
			Namespace: "ns", Name: "fe", Role: store.FrontendRoleOpenAI,
		})},
	}
	f.view.Replace(snap, t0)
	p := poolOf(t, f.view, "p2")
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState(), "liveness from the cluster lease and ready endpoints")
	assert.Equal(t, int32(1), p.GetReadyEndpointCount())
	require.Len(t, p.GetFrontends(), 2)
	assert.Equal(t, "openai", p.GetFrontends()[1].GetPortName())
}

func TestRouterDerivation(t *testing.T) {
	f := newFixture(t)
	snap := store.ClusterSetSnapshot{
		ClusterSet: cs,
		Clusters:   []store.ClusterRecord{clusterRec("a", "us-east-1", t0)},
		Exports: []store.ExportRecord{
			exportRec("a", "llm", "backend-router", 1, t0, map[string]string{api.LabelRole: api.RoleRouter},
				map[string]string{api.AnnotationDialScheme: "HTTPS", api.AnnotationGRPCPortName: "registration"},
				store.ServicePort{Name: "registration", Port: 50071}, store.ServicePort{Name: "http", Port: 8080}),
			exportRec("a", "llm", "plain", 1, t0, map[string]string{api.LabelRole: api.RoleRouter}, nil,
				store.ServicePort{Name: "grpc", Port: 9000}),
		},
		EndpointSets: []store.EndpointSetRecord{endpointSet("a", "llm", "backend-router", 1, true, false)},
	}
	f.view.Replace(snap, t0)
	o, ok := f.view.Get(cs, KindRouter, RouterKey("a", "llm", "backend-router"))
	require.True(t, ok)
	r := o.Full.(*gdsv1.Router)
	assert.Equal(t, int32(50071), r.GetGrpcPort())
	assert.Equal(t, int32(8080), r.GetHttpPort())
	assert.Equal(t, "https", r.GetDialScheme())
	assert.Equal(t, "us-east-1", r.GetRegion())
	require.Len(t, r.GetReplicas(), 2)
	assert.True(t, r.GetReplicas()[0].GetReady())
	assert.False(t, r.GetReplicas()[1].GetReady())
	assert.Equal(t, "backend-router-0", r.GetReplicas()[0].GetHostname())

	o, _ = f.view.Get(cs, KindRouter, RouterKey("a", "llm", "plain"))
	plain := o.Full.(*gdsv1.Router)
	assert.Equal(t, int32(9000), plain.GetGrpcPort())
	assert.Zero(t, plain.GetHttpPort())
	assert.Equal(t, "http", plain.GetDialScheme())

	routers, _ := f.view.List(cs, RouterFilter{Cluster: "a", Region: "us-east-1"})
	assert.Len(t, routers, 2)
	routers, _ = f.view.List(cs, RouterFilter{Region: "eu"})
	assert.Empty(t, routers)
}

func TestListFiltersAndIndexes(t *testing.T) {
	f := newFixture(t)
	snap := labelledPoolSnapshot()
	snap.Clusters = append(snap.Clusters, clusterRec("b", "us-west-2", t0))
	snap.Exports = append(snap.Exports, exportRec("b", "other", "svc", 1, t0, map[string]string{"app": "x"}, nil, httpPort))
	snap.Pools = []store.PoolRecord{apiPool("p1", "b", "mistral", store.Lease{Epoch: uint64(t0.UnixMilli()), TTL: time.Minute, RenewedAt: t0})}
	f.view.Replace(snap, t0)
	llama := registry.PoolID(cs, "a", "llama")

	ids := func(f PoolFilter, v *View) []string {
		msgs, _ := v.List(cs, f)
		var out []string
		for _, m := range msgs {
			out = append(out, m.(*gdsv1.Pool).GetPoolId())
		}
		return out
	}
	assert.ElementsMatch(t, []string{llama, "p1"}, ids(PoolFilter{}, f.view))
	assert.Equal(t, []string{llama}, ids(PoolFilter{Model: "llama-3"}, f.view))
	assert.Equal(t, []string{"p1"}, ids(PoolFilter{Model: "mistral-7b"}, f.view), "aliases match")
	assert.Empty(t, ids(PoolFilter{Model: "gpt"}, f.view))
	assert.Equal(t, []string{llama}, ids(PoolFilter{Selector: map[string]string{"tier": "gold"}, Model: "llama-3"}, f.view))
	assert.Equal(t, []string{llama}, ids(PoolFilter{Region: "us-east-1", DC: "dc1", Cluster: "a", HardwareClass: "b200-ib"}, f.view))
	assert.Empty(t, ids(PoolFilter{HardwareClass: "h100"}, f.view))
	assert.Equal(t, []string{"p1"}, ids(PoolFilter{States: []gdsv1.PoolState{gdsv1.PoolState_POOL_STATE_REGISTERED}}, f.view))

	msgs, rev := f.view.List(cs, ImportFilter{Selector: map[string]string{"app": "x"}})
	require.Len(t, msgs, 1)
	assert.Equal(t, "svc", msgs[0].(*gdsv1.ServiceImport).GetName())
	assert.Equal(t, f.view.Revision(cs), rev)
	msgs, _ = f.view.List(cs, ImportFilter{Namespace: "dyn"})
	assert.Len(t, msgs, 2)
	msgs, _ = f.view.List(cs, ImportFilter{Namespace: "dyn", Endpoints: true})
	assert.NotEmpty(t, msgs[0].(*gdsv1.ServiceImport).GetEndpoints()[0].GetEndpoints())
	msgs, _ = f.view.List(cs, ImportFilter{Namespace: "dyn"})
	assert.Empty(t, msgs[0].(*gdsv1.ServiceImport).GetEndpoints()[0].GetEndpoints())

	msgs, _ = f.view.List(cs, ClusterFilter{})
	assert.Len(t, msgs, 2)
	f.clock.Advance(time.Hour)
	f.view.EvaluateLeases()
	msgs, _ = f.view.List(cs, ClusterFilter{})
	assert.Empty(t, msgs)
	msgs, _ = f.view.List(cs, ClusterFilter{IncludeExpired: true})
	assert.Len(t, msgs, 2)

	msgs, rev = f.view.List("unknown", ClusterFilter{})
	assert.Empty(t, msgs)
	assert.Equal(t, "e1:0", rev)
}

// Lite objects must equal their full objects without endpoint lists, so
// that a new proto field is not silently dropped from Lite.
func TestLiteMatchesFull(t *testing.T) {
	f := newFixture(t)
	snap := labelledPoolSnapshot()
	snap.Pools = []store.PoolRecord{apiPool("p1", "a", "llama", store.Lease{Epoch: 1, TTL: time.Minute, RenewedAt: t0})}
	f.view.Replace(snap, t0)
	for _, kind := range []Kind{KindServiceImport, KindPool} {
		msgs, _ := f.view.List(cs, filterOf(kind, true))
		require.NotEmpty(t, msgs)
		for _, m := range msgs {
			full := proto.Clone(m)
			switch x := full.(type) {
			case *gdsv1.ServiceImport:
				for _, ce := range x.GetEndpoints() {
					ce.Endpoints = nil
				}
			case *gdsv1.Pool:
				for _, ce := range x.GetEndpoints() {
					ce.Endpoints = nil
				}
			}
			lites, _ := f.view.List(cs, filterOf(kind, false))
			found := false
			for _, l := range lites {
				if proto.Equal(l, full) {
					found = true
				}
			}
			assert.True(t, found, "%T", m)
		}
	}
}

func filterOf(kind Kind, endpoints bool) Filter {
	if kind == KindPool {
		return PoolFilter{Endpoints: endpoints}
	}
	return ImportFilter{Endpoints: endpoints}
}

func TestAccessors(t *testing.T) {
	f := newFixture(t)
	f.view.Replace(baseSnapshot(), t0)

	exps := f.view.Exports(store.ClusterKey{ClusterSet: cs, Cluster: "a"})
	require.Len(t, exps, 1)
	assert.Equal(t, uint64(5), exps[0].EndpointsGeneration)
	assert.Equal(t, int32(2), exps[0].EndpointCount)
	assert.Equal(t, int32(1), exps[0].ReadyEndpointCount)
	assert.Nil(t, f.view.Exports(store.ClusterKey{ClusterSet: "x", Cluster: "a"}))

	gen, ok := f.view.EndpointSetGeneration(exportKey("b", "ns", "web"))
	assert.True(t, ok)
	assert.Equal(t, uint64(6), gen)
	_, ok = f.view.EndpointSetGeneration(store.ExportKey{ClusterSet: "x"})
	assert.False(t, ok)
	_, ok = f.view.ExportRecord(store.ExportKey{ClusterSet: "x"})
	assert.False(t, ok)
	_, ok = f.view.ClusterRecord(store.ClusterKey{ClusterSet: "x"})
	assert.False(t, ok)
	_, ok = f.view.PoolRecord("x", "p")
	assert.False(t, ok)
	_, ok = f.view.PoolByName("x", "a", "p")
	assert.False(t, ok)
	_, ok = f.view.Get("x", KindPool, "p")
	assert.False(t, ok)
	assert.Equal(t, "e1:0", f.view.Revision("x"))

	recs := f.view.Records(cs)
	assert.Len(t, recs.Clusters, 2)
	assert.Len(t, recs.Exports, 2)
	assert.Len(t, recs.EndpointSets, 2)
	assert.Empty(t, f.view.Records("x").Clusters)

	f.view.SetDiscovered([]string{cs})
	sums := f.view.Summaries()
	require.Len(t, sums, 1)
	assert.Equal(t, Summary{ClusterSet: cs, Clusters: 2, ActiveClusters: 2, Exports: 2}, sums[0])
	assert.Equal(t, t0, f.view.Now())
}

func TestReadinessAndDiscovery(t *testing.T) {
	f := newFixture(t)
	ok, why := f.view.Ready(t0, 6*time.Second)
	assert.False(t, ok)
	assert.Contains(t, why, "not discovered")

	f.view.SetDiscovered([]string{cs})
	ok, why = f.view.Ready(t0, 6*time.Second)
	assert.False(t, ok)
	assert.Contains(t, why, "not loaded")

	f.view.Replace(baseSnapshot(), t0)
	ok, _ = f.view.Ready(t0, 6*time.Second)
	assert.True(t, ok)
	assert.Equal(t, 5*time.Second, f.view.Age(t0.Add(5*time.Second)))
	ok, why = f.view.Ready(t0.Add(7*time.Second), 6*time.Second)
	assert.False(t, ok)
	assert.Contains(t, why, "old")

	// A clusterset created by a request does not count for readiness and
	// is dropped once idle and unknown to the store.
	f.view.Ensure("adhoc")
	select {
	case <-f.view.Kicked():
	default:
		t.Fatal("a new clusterset kicks the refresher")
	}
	ok, _ = f.view.Ready(t0, 6*time.Second)
	assert.True(t, ok)
	assert.Equal(t, []string{"adhoc", cs}, f.view.ClusterSetNames())
	f.view.SetDiscovered([]string{cs})
	assert.Equal(t, []string{cs}, f.view.ClusterSetNames())

	// A clusterset with records stays tracked when the store forgets it.
	f.view.SetDiscovered(nil)
	assert.Equal(t, []string{cs}, f.view.ClusterSetNames())
}

func TestRevisions(t *testing.T) {
	assert.Equal(t, "1761480000000-ab:42", Revision("1761480000000-ab", 42))
	epoch, seq, err := ParseRevision("1761480000000-ab:42")
	require.NoError(t, err)
	assert.Equal(t, "1761480000000-ab", epoch)
	assert.Equal(t, uint64(42), seq)
	for _, bad := range []string{"", "42", ":1", "e:x", "e:-1"} {
		_, _, err := ParseRevision(bad)
		assert.Error(t, err, bad)
	}
	e := NewEpoch(t0)
	assert.Regexp(t, `^\d+-[0-9a-f]{8}$`, e)
	assert.NotEqual(t, e, NewEpoch(t0))
	assert.NotEmpty(t, New(Options{}).Epoch())
	assert.Equal(t, "service_import", KindServiceImport.String())
}

func TestDialScheme(t *testing.T) {
	assert.Equal(t, "http", DialScheme(nil))
	assert.Equal(t, "https", DialScheme(map[string]string{api.AnnotationDialScheme: " https "}))
	assert.Equal(t, "http", DialScheme(map[string]string{api.AnnotationDialScheme: "quic"}))
}
