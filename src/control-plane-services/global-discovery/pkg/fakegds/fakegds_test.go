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

package fakegds_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/fakegds"
)

const cs = "prod"

// clock is an adjustable test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type clients struct {
	clusters gdsv1.ClusterServiceClient
	exports  gdsv1.ServiceExportServiceClient
	imports  gdsv1.ServiceImportServiceClient
	pools    gdsv1.PoolServiceClient
	info     gdsv1.ServerInfoServiceClient
}

func start(t *testing.T, opts ...fakegds.Option) (*fakegds.Server, clients) {
	t.Helper()
	srv := fakegds.New(opts...)
	run := srv.ServeBufconn()
	t.Cleanup(run.Stop)
	conn, err := grpc.NewClient("passthrough:///fakegds",
		grpc.WithContextDialer(run.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return srv, clients{
		clusters: gdsv1.NewClusterServiceClient(conn),
		exports:  gdsv1.NewServiceExportServiceClient(conn),
		imports:  gdsv1.NewServiceImportServiceClient(conn),
		pools:    gdsv1.NewPoolServiceClient(conn),
		info:     gdsv1.NewServerInfoServiceClient(conn),
	}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func errorInfo(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "not a status error: %v", err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	t.Fatalf("no ErrorInfo in %v", err)
	return nil
}

func TestClusterLeaseLifecycle(t *testing.T) {
	clk := newClock()
	_, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0))

	reg, err := c.clusters.RegisterCluster(ctx(t), &gdsv1.RegisterClusterRequest{
		Clusterset: cs, Cluster: "a", Region: "us-east-1", LeaseTtl: durationpb.New(time.Second),
	})
	require.NoError(t, err)
	lease := reg.GetCluster().GetLease()
	assert.Equal(t, uint64(clk.Now().UnixMilli()), lease.GetEpoch())
	assert.Equal(t, 10*time.Second, lease.GetTtl().AsDuration(), "TTL is clamped to 10 s")
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_ACTIVE, lease.GetState())
	assert.Equal(t, uint64(1), reg.GetCluster().GetGeneration())

	_, err = c.clusters.HeartbeatCluster(ctx(t), &gdsv1.HeartbeatClusterRequest{Clusterset: cs, Cluster: "a", Epoch: lease.GetEpoch() + 1})
	assert.Equal(t, codes.Aborted, status.Code(err))
	assert.Equal(t, api.ReasonLeaseEpochMismatch, errorInfo(t, err).GetReason())
	assert.Equal(t, api.ErrorDomain, errorInfo(t, err).GetDomain())

	_, err = c.clusters.HeartbeatCluster(ctx(t), &gdsv1.HeartbeatClusterRequest{Clusterset: cs, Cluster: "nope", Epoch: 1})
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Equal(t, api.ReasonClusterNotRegistered, errorInfo(t, err).GetReason())

	clk.Advance(5 * time.Second)
	hb, err := c.clusters.HeartbeatCluster(ctx(t), &gdsv1.HeartbeatClusterRequest{Clusterset: cs, Cluster: "a", Epoch: lease.GetEpoch()})
	require.NoError(t, err)
	assert.Equal(t, clk.Now(), hb.GetLease().GetRenewedAt().AsTime())

	clk.Advance(13 * time.Second)
	list, err := c.clusters.ListClusters(ctx(t), &gdsv1.ListClustersRequest{Clusterset: cs})
	require.NoError(t, err)
	assert.Empty(t, list.GetClusters(), "expired clusters are hidden")
	list, err = c.clusters.ListClusters(ctx(t), &gdsv1.ListClustersRequest{Clusterset: cs, IncludeExpired: true})
	require.NoError(t, err)
	require.Len(t, list.GetClusters(), 1)
	assert.Equal(t, gdsv1.LeaseState_LEASE_STATE_EXPIRED, list.GetClusters()[0].GetLease().GetState())

	// Re-registration mints a new epoch and keeps registered_at.
	clk.Advance(time.Second)
	again, err := c.clusters.RegisterCluster(ctx(t), &gdsv1.RegisterClusterRequest{Clusterset: cs, Cluster: "a"})
	require.NoError(t, err)
	assert.Greater(t, again.GetCluster().GetLease().GetEpoch(), lease.GetEpoch())
	assert.Equal(t, reg.GetCluster().GetRegisteredAt().AsTime(), again.GetCluster().GetRegisteredAt().AsTime())
	assert.Equal(t, uint64(2), again.GetCluster().GetGeneration())

	sets, err := c.clusters.ListClusterSets(ctx(t), &gdsv1.ListClusterSetsRequest{})
	require.NoError(t, err)
	require.Len(t, sets.GetClustersets(), 1)
	assert.Equal(t, int32(1), sets.GetClustersets()[0].GetActiveClusterCount())

	_, err = c.clusters.DeregisterCluster(ctx(t), &gdsv1.DeregisterClusterRequest{Clusterset: cs, Cluster: "a"})
	require.NoError(t, err)
	_, err = c.clusters.GetCluster(ctx(t), &gdsv1.GetClusterRequest{Clusterset: cs, Cluster: "a"})
	assert.Equal(t, codes.NotFound, status.Code(err))

	_, err = c.clusters.RegisterCluster(ctx(t), &gdsv1.RegisterClusterRequest{Clusterset: cs})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestExportGenerationsAndImports(t *testing.T) {
	clk := newClock()
	srv, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0))

	put := func(cluster string, gen uint64, created time.Time, ports ...int32) error {
		spec := &gdsv1.ServiceExportSpec{
			Type:                    gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
			ExportCreationTimestamp: timestamppb.New(created),
			Labels:                  map[string]string{"owner": cluster},
		}
		for _, p := range ports {
			spec.Ports = append(spec.Ports, &gdsv1.ServicePort{Name: "p", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: p})
		}
		_, err := c.exports.PutServiceExport(ctx(t), &gdsv1.PutServiceExportRequest{
			Clusterset: cs, Cluster: cluster, Namespace: "ns", Name: "svc", Spec: spec, Generation: gen,
		})
		return err
	}

	err := put("a", 1, clk.Now(), 80)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Equal(t, api.ReasonClusterNotRegistered, errorInfo(t, err).GetReason())

	srv.EnsureCluster(cs, "a", "us-east-1")
	srv.EnsureCluster(cs, "b", "us-west-2")
	require.NoError(t, put("a", 5, clk.Now(), 80))
	require.NoError(t, put("b", 1, clk.Now().Add(-time.Hour), 80, 443))

	err = put("a", 5, clk.Now(), 80)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	info := errorInfo(t, err)
	assert.Equal(t, api.ReasonStaleGeneration, info.GetReason())
	assert.Equal(t, "5", info.GetMetadata()[api.MetadataCurrentGeneration])
	st, _ := status.FromError(err)
	var pf *errdetails.PreconditionFailure
	for _, d := range st.Details() {
		if v, ok := d.(*errdetails.PreconditionFailure); ok {
			pf = v
		}
	}
	require.NotNil(t, pf)
	assert.Equal(t, api.ReasonStaleGeneration, pf.GetViolations()[0].GetType())

	_, err = c.exports.ReplaceEndpoints(ctx(t), &gdsv1.ReplaceEndpointsRequest{
		Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "svc", Generation: 1,
		Endpoints: fakegds.ReadyEndpoints("10.0.0.1", "10.0.0.2"),
	})
	require.NoError(t, err)
	_, err = c.exports.ReplaceEndpoints(ctx(t), &gdsv1.ReplaceEndpointsRequest{
		Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "svc", Generation: 1,
	})
	assert.Equal(t, api.ReasonStaleGeneration, errorInfo(t, err).GetReason())
	_, err = c.exports.ReplaceEndpoints(ctx(t), &gdsv1.ReplaceEndpointsRequest{
		Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "svc", Generation: 9,
		Endpoints: make([]*gdsv1.Endpoint, api.MaxEndpointsPerExport+1),
	})
	assert.Equal(t, api.ReasonTooManyEndpoints, errorInfo(t, err).GetReason())
	srv.SetEndpoints(cs, "b", "ns", "svc", fakegds.ReadyEndpoints("10.1.0.1"))

	exports, err := c.exports.ListServiceExports(ctx(t), &gdsv1.ListServiceExportsRequest{Clusterset: cs, Cluster: "a"})
	require.NoError(t, err)
	require.Len(t, exports.GetExports(), 1)
	assert.Equal(t, int32(2), exports.GetExports()[0].GetReadyEndpointCount())
	assert.Equal(t, uint64(1), exports.GetExports()[0].GetEndpointsGeneration())

	imp, err := c.imports.GetServiceImport(ctx(t), &gdsv1.GetServiceImportRequest{Clusterset: cs, Namespace: "ns", Name: "svc", IncludeEndpoints: true})
	require.NoError(t, err)
	si := imp.GetServiceImport()
	assert.Equal(t, "b", si.GetLabels()["owner"], "the oldest export wins")
	require.Len(t, si.GetPorts(), 2, "ports are unioned")
	require.Len(t, si.GetEndpoints(), 2)
	assert.Equal(t, "a", si.GetEndpoints()[0].GetCluster())
	assert.Len(t, si.GetEndpoints()[0].GetEndpoints(), 2)
	assert.Nil(t, si.GetConflict())

	// Without include_endpoints only the counts remain.
	list, err := c.imports.ListServiceImports(ctx(t), &gdsv1.ListServiceImportsRequest{Clusterset: cs, Namespace: "ns"})
	require.NoError(t, err)
	require.Len(t, list.GetServiceImports(), 1)
	assert.Empty(t, list.GetServiceImports()[0].GetEndpoints()[0].GetEndpoints())
	assert.Equal(t, int32(2), list.GetServiceImports()[0].GetEndpoints()[0].GetReadyEndpointCount())
	list, err = c.imports.ListServiceImports(ctx(t), &gdsv1.ListServiceImportsRequest{
		Clusterset: cs, Selector: &gdsv1.LabelSelector{MatchLabels: map[string]string{"owner": "a"}},
	})
	require.NoError(t, err)
	assert.Empty(t, list.GetServiceImports())

	// When b's lease expires, a wins and b's endpoints leave the import.
	clk.Advance(20 * time.Second)
	srv.EnsureCluster(cs, "a", "")
	clk.Advance(15 * time.Second)
	imp, err = c.imports.GetServiceImport(ctx(t), &gdsv1.GetServiceImportRequest{Clusterset: cs, Namespace: "ns", Name: "svc"})
	require.NoError(t, err)
	si = imp.GetServiceImport()
	assert.Equal(t, "a", si.GetLabels()["owner"])
	require.Len(t, si.GetEndpoints(), 1)
	require.Len(t, si.GetClusters(), 2)
	assert.False(t, si.GetClusters()[1].GetParticipating())
	assert.Equal(t, api.ConditionReasonClusterLeaseExpired, si.GetClusters()[1].GetConditions()[0].GetReason())

	_, err = c.exports.DeleteServiceExport(ctx(t), &gdsv1.DeleteServiceExportRequest{Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "svc"})
	require.NoError(t, err)
	srv.DeleteExport(cs, "b", "ns", "svc")
	_, err = c.imports.GetServiceImport(ctx(t), &gdsv1.GetServiceImportRequest{Clusterset: cs, Namespace: "ns", Name: "svc"})
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestRoutersAndLabelPools(t *testing.T) {
	srv, c := start(t)
	srv.EnsureCluster(cs, "a", "us-east-1")
	srv.PutExport(cs, "a", "llm", "backend-router", fakegds.RouterSpec(50071, 8080, "https"),
		fakegds.ReadyEndpoints("10.0.0.1", "10.0.0.2"))
	srv.PutExport(cs, "b", "llm", "backend-router", fakegds.RouterSpec(50071, 0, ""), nil)

	routers, err := c.pools.ListRouters(ctx(t), &gdsv1.ListRoutersRequest{Clusterset: cs})
	require.NoError(t, err)
	require.Len(t, routers.GetRouters(), 2)
	r := routers.GetRouters()[0]
	assert.Equal(t, "a", r.GetCluster())
	assert.Equal(t, "us-east-1", r.GetRegion())
	assert.Equal(t, int32(50071), r.GetGrpcPort())
	assert.Equal(t, int32(8080), r.GetHttpPort())
	assert.Equal(t, "https", r.GetDialScheme())
	assert.Len(t, r.GetReplicas(), 2)
	assert.Equal(t, fakegds.DefaultRegion, routers.GetRouters()[1].GetRegion())
	routers, err = c.pools.ListRouters(ctx(t), &gdsv1.ListRoutersRequest{Clusterset: cs, Cluster: "b"})
	require.NoError(t, err)
	assert.Len(t, routers.GetRouters(), 1)

	spec := fakegds.PoolFrontendSpec("blue", 8000, "llama-3", "mistral")
	spec.Annotations[api.AnnotationHardwareClass] = "b200-ib"
	srv.PutExport(cs, "a", "pools", "blue-frontend", spec, []*gdsv1.Endpoint{})
	pools, err := c.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs})
	require.NoError(t, err)
	require.Len(t, pools.GetPools(), 1)
	p := pools.GetPools()[0]
	assert.Equal(t, fakegds.LabelPoolID(cs, "a", "blue"), p.GetPoolId())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, p.GetState(), "no ready endpoint yet")
	assert.Equal(t, "b200-ib", p.GetHardwareClass())
	require.Len(t, p.GetFrontends(), 1)
	assert.Equal(t, "blue-frontend", p.GetFrontends()[0].GetService().GetName())
	assert.Equal(t, api.DefaultHTTPPortName, p.GetFrontends()[0].GetPortName())

	srv.SetEndpoints(cs, "a", "pools", "blue-frontend", fakegds.ReadyEndpoints("10.0.1.1"))
	got, err := c.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: p.GetPoolId(), IncludeEndpoints: true})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, got.GetPool().GetState())
	assert.Equal(t, int32(1), got.GetPool().GetReadyEndpointCount())
	require.Len(t, got.GetPool().GetEndpoints(), 1)
	assert.Len(t, got.GetPool().GetEndpoints()[0].GetEndpoints(), 1)

	for model, want := range map[string]int{"llama-3": 1, "mistral": 1, "gpt": 0} {
		pools, err = c.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, Model: model})
		require.NoError(t, err)
		assert.Len(t, pools.GetPools(), want, model)
	}
}

func TestAPIPools(t *testing.T) {
	clk := newClock()
	_, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0))
	reg, err := c.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{
		Clusterset: cs,
		Generation: 1,
		Pool: &gdsv1.Pool{
			Name:      "green",
			Location:  &gdsv1.Location{Cluster: "c", Region: "eu-west-1"},
			Models:    []*gdsv1.ModelServed{{ModelId: "llama-3", Aliases: []string{"llama"}}},
			Frontends: []*gdsv1.FrontendEndpoint{{Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, Url: "https://lb.example"}},
		},
	})
	require.NoError(t, err)
	p := reg.GetPool()
	require.NotEmpty(t, p.GetPoolId())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, p.GetState())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetDesiredState())
	assert.Equal(t, 60*time.Second, p.GetLease().GetTtl().AsDuration())

	_, err = c.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{
		Clusterset: cs, Generation: 2,
		Pool: &gdsv1.Pool{PoolId: "other", Name: "green", Location: &gdsv1.Location{Cluster: "c"}},
	})
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	assert.Equal(t, api.ReasonPoolNameTaken, errorInfo(t, err).GetReason())

	again, err := c.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{
		Clusterset: cs, Generation: 1,
		Pool: &gdsv1.Pool{Name: "green", Location: &gdsv1.Location{Cluster: "c"}},
	})
	assert.Equal(t, api.ReasonStaleGeneration, errorInfo(t, err).GetReason())
	assert.Nil(t, again)

	hb, err := c.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{
		Clusterset: cs, PoolId: p.GetPoolId(), Epoch: p.GetLease().GetEpoch(), WorkerCount: ptr(int32(4)),
	})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, hb.GetState())
	_, err = c.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{Clusterset: cs, PoolId: p.GetPoolId(), Epoch: 1})
	assert.Equal(t, api.ReasonLeaseEpochMismatch, errorInfo(t, err).GetReason())

	upd, err := c.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: p.GetPoolId(), Generation: 2,
		Pool:       &gdsv1.Pool{DesiredState: gdsv1.PoolState_POOL_STATE_DRAINING, WorkerCount: 99},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"desired_state"}},
	})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_DRAINING, upd.GetPool().GetState())
	assert.Equal(t, int32(4), upd.GetPool().GetWorkerCount(), "fields outside the mask are kept")
	_, err = c.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: p.GetPoolId(), Generation: 3,
		Pool: &gdsv1.Pool{}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"state"}},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	list, err := c.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, Model: "llama", Region: "eu-west-1"})
	require.NoError(t, err)
	assert.Len(t, list.GetPools(), 1)
	list, err = c.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, States: []gdsv1.PoolState{gdsv1.PoolState_POOL_STATE_ACTIVE}})
	require.NoError(t, err)
	assert.Empty(t, list.GetPools())

	clk.Advance(63 * time.Second)
	got, err := c.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: p.GetPoolId()})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_UNHEALTHY, got.GetPool().GetState())

	_, err = c.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: p.GetPoolId()})
	require.NoError(t, err)
	_, err = c.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: p.GetPoolId()})
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func ptr[T any](v T) *T { return &v }

// recvPools reads n events from a pool watch.
func recvPools(t *testing.T, w grpc.ServerStreamingClient[gdsv1.WatchPoolsResponse], n int) []*gdsv1.WatchPoolsResponse {
	t.Helper()
	var out []*gdsv1.WatchPoolsResponse
	for range n {
		ev, err := w.Recv()
		require.NoError(t, err)
		out = append(out, ev)
	}
	return out
}

func types(evs []*gdsv1.WatchPoolsResponse) []gdsv1.EventType {
	var out []gdsv1.EventType
	for _, ev := range evs {
		out = append(out, ev.GetType())
	}
	return out
}

const (
	start_  = gdsv1.EventType_EVENT_TYPE_SNAPSHOT_START
	upsert  = gdsv1.EventType_EVENT_TYPE_UPSERT
	del     = gdsv1.EventType_EVENT_TYPE_DELETE
	end     = gdsv1.EventType_EVENT_TYPE_SNAPSHOT_END
	bookmrk = gdsv1.EventType_EVENT_TYPE_BOOKMARK
)

func TestWatchSnapshotDeltasFiltersAndResume(t *testing.T) {
	srv, c := start(t, fakegds.WithBookmarkInterval(0))
	srv.PutExport(cs, "a", "pools", "blue", fakegds.PoolFrontendSpec("blue", 8000, "llama-3"), fakegds.ReadyEndpoints("10.0.0.1"))

	wctx, cancel := context.WithCancel(ctx(t))
	w, err := c.pools.WatchPools(wctx, &gdsv1.WatchPoolsRequest{Clusterset: cs, Model: "llama-3"})
	require.NoError(t, err)
	evs := recvPools(t, w, 3)
	assert.Equal(t, []gdsv1.EventType{start_, upsert, end}, types(evs))
	assert.Empty(t, evs[1].GetRevision())
	assert.Equal(t, srv.Revision(), evs[2].GetRevision())

	// A non-matching pool is not sent; a pool that stops matching is a
	// DELETE.
	srv.PutExport(cs, "a", "pools", "red", fakegds.PoolFrontendSpec("red", 8000, "mistral"), fakegds.ReadyEndpoints("10.0.0.2"))
	srv.PutExport(cs, "a", "pools", "blue", fakegds.PoolFrontendSpec("blue", 8000, "mistral"), nil)
	evs = recvPools(t, w, 1)
	assert.Equal(t, del, evs[0].GetType())
	assert.Equal(t, "blue", evs[0].GetPool().GetName())
	resume := evs[0].GetRevision()
	assert.NotEmpty(t, resume)
	cancel()

	// Changes while disconnected are replayed on resume.
	srv.PutExport(cs, "a", "pools", "blue", fakegds.PoolFrontendSpec("blue", 8000, "llama-3"), nil)
	w, err = c.pools.WatchPools(ctx(t), &gdsv1.WatchPoolsRequest{Clusterset: cs, Model: "llama-3", ResumeRevision: resume})
	require.NoError(t, err)
	evs = recvPools(t, w, 1)
	assert.Equal(t, upsert, evs[0].GetType())
	assert.Equal(t, "blue", evs[0].GetPool().GetName())

	// A deleted pool is a DELETE carrying state GONE.
	srv.DeleteExport(cs, "a", "pools", "blue")
	evs = recvPools(t, w, 1)
	assert.Equal(t, del, evs[0].GetType())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, evs[0].GetPool().GetState())

	// An unknown revision yields a new snapshot.
	w, err = c.pools.WatchPools(ctx(t), &gdsv1.WatchPoolsRequest{Clusterset: cs, ResumeRevision: "other:5"})
	require.NoError(t, err)
	evs = recvPools(t, w, 3)
	assert.Equal(t, []gdsv1.EventType{start_, upsert, end}, types(evs))
	assert.Equal(t, "red", evs[1].GetPool().GetName())
}

func TestWatchBookmarksLagAndClose(t *testing.T) {
	srv, c := start(t, fakegds.WithBookmarkInterval(20*time.Millisecond), fakegds.WithWatchQueue(1))
	w, err := c.pools.WatchRouters(ctx(t), &gdsv1.WatchRoutersRequest{Clusterset: cs})
	require.NoError(t, err)
	ev, err := w.Recv()
	require.NoError(t, err)
	assert.Equal(t, start_, ev.GetType())
	ev, err = w.Recv()
	require.NoError(t, err)
	assert.Equal(t, end, ev.GetType())
	ev, err = w.Recv()
	require.NoError(t, err)
	assert.Equal(t, bookmrk, ev.GetType())
	assert.Equal(t, srv.Revision(), ev.GetRevision())
	assert.Equal(t, 1, srv.WatcherCount())

	// Overflow the one-event queue with one commit that removes many
	// routers at once: the watcher is re-snapshotted.
	for i := range 50 {
		srv.PutExport(cs, "a", "llm", fmt.Sprintf("r%d", i), fakegds.RouterSpec(50071, 0, ""), nil)
	}
	srv.RemoveCluster(cs, "a")
	sawSnapshot := false
	for !sawSnapshot {
		ev, err = w.Recv()
		require.NoError(t, err)
		sawSnapshot = ev.GetType() == start_
	}
	for ev.GetType() != end {
		ev, err = w.Recv()
		require.NoError(t, err)
	}

	srv.CloseWatches(nil)
	for {
		_, err = w.Recv()
		if err != nil {
			break
		}
	}
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, api.ReasonShuttingDown, errorInfo(t, err).GetReason())
	assert.Eventually(t, func() bool { return srv.WatcherCount() == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestWatchLeaseExpiryOnRefresh(t *testing.T) {
	clk := newClock()
	srv, c := start(t, fakegds.WithClock(clk.Now), fakegds.WithRefreshInterval(0), fakegds.WithBookmarkInterval(0))
	srv.EnsureCluster(cs, "a", "")
	w, err := c.clusters.WatchClusters(ctx(t), &gdsv1.WatchClustersRequest{Clusterset: cs})
	require.NoError(t, err)
	for range 3 {
		_, err = w.Recv()
		require.NoError(t, err)
	}
	clk.Advance(time.Minute)
	srv.Refresh()
	ev, err := w.Recv()
	require.NoError(t, err)
	assert.Equal(t, del, ev.GetType(), "an expired cluster leaves a watch without include_expired")
}

func TestFailNextCallCountServerInfoAndServeLocal(t *testing.T) {
	srv := fakegds.New()
	run, err := srv.ServeLocal()
	require.NoError(t, err)
	defer run.Stop()
	assert.NotZero(t, run.Port())
	assert.Equal(t, "http://"+run.Addr(), run.URL())
	conn, err := grpc.NewClient("passthrough:///"+run.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	info := gdsv1.NewServerInfoServiceClient(conn)

	srv.FailNext("GetServerInfo", codes.Unavailable)
	srv.FailNext(gdsv1.ServerInfoService_GetServerInfo_FullMethodName, codes.Internal)
	_, err = info.GetServerInfo(ctx(t), &gdsv1.GetServerInfoRequest{})
	assert.Equal(t, codes.Unavailable, status.Code(err))
	_, err = info.GetServerInfo(ctx(t), &gdsv1.GetServerInfoRequest{})
	assert.Equal(t, codes.Internal, status.Code(err))
	got, err := info.GetServerInfo(ctx(t), &gdsv1.GetServerInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, fakegds.DefaultRegion, got.GetRegion())
	assert.Equal(t, srv.ViewEpoch(), got.GetViewEpoch())
	assert.Equal(t, 3, srv.CallCount("GetServerInfo"))

	srv.SetServerInfo(&gdsv1.GetServerInfoResponse{
		Region:    "us-east-1",
		Endpoints: []*gdsv1.ServerEndpoint{{Region: "us-west-2", GrpcUrl: "https://gds.us-west-2.example:443"}},
	})
	got, err = info.GetServerInfo(ctx(t), &gdsv1.GetServerInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", got.GetRegion())
	assert.Len(t, got.GetEndpoints(), 1)
	assert.NotNil(t, got.GetServerTime())
	srv.ResetCallCounts()
	assert.Zero(t, srv.CallCount("GetServerInfo"))

	// A restarted listener on the same address serves the same state.
	addr := run.Addr()
	run.Stop()
	run2, err := srv.ServeAddr(addr)
	require.NoError(t, err)
	defer run2.GracefulStop()
	// The first call may still go out on the connection the stop closed.
	assert.Eventually(t, func() bool {
		_, err = info.GetServerInfo(ctx(t), &gdsv1.GetServerInfoRequest{}, grpc.WaitForReady(true))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
}
