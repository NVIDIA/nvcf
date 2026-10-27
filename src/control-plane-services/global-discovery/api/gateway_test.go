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

package api_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// recorder implements every GDS service. Each call records the RPC name and
// request; watches send one SNAPSHOT_END event.
type recorder struct {
	gdsv1.UnimplementedClusterServiceServer
	gdsv1.UnimplementedServiceExportServiceServer
	gdsv1.UnimplementedServiceImportServiceServer
	gdsv1.UnimplementedPoolServiceServer
	gdsv1.UnimplementedServerInfoServiceServer

	mu  sync.Mutex
	rpc string
	req proto.Message
}

func (r *recorder) record(rpc string, req proto.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rpc, r.req = rpc, req
}

func (r *recorder) last() (string, proto.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rpc, r.req
}

func (r *recorder) RegisterCluster(_ context.Context, req *gdsv1.RegisterClusterRequest) (*gdsv1.RegisterClusterResponse, error) {
	r.record("RegisterCluster", req)
	return &gdsv1.RegisterClusterResponse{Cluster: &gdsv1.Cluster{Clusterset: req.Clusterset, Cluster: req.Cluster, AgentVersion: "v1"}}, nil
}

func (r *recorder) HeartbeatCluster(_ context.Context, req *gdsv1.HeartbeatClusterRequest) (*gdsv1.HeartbeatClusterResponse, error) {
	r.record("HeartbeatCluster", req)
	return &gdsv1.HeartbeatClusterResponse{Lease: &gdsv1.Lease{Epoch: req.Epoch, State: gdsv1.LeaseState_LEASE_STATE_ACTIVE}}, nil
}

func (r *recorder) DeregisterCluster(_ context.Context, req *gdsv1.DeregisterClusterRequest) (*gdsv1.DeregisterClusterResponse, error) {
	r.record("DeregisterCluster", req)
	return &gdsv1.DeregisterClusterResponse{}, nil
}

func (r *recorder) GetCluster(_ context.Context, req *gdsv1.GetClusterRequest) (*gdsv1.GetClusterResponse, error) {
	r.record("GetCluster", req)
	return &gdsv1.GetClusterResponse{}, nil
}

func (r *recorder) ListClusters(_ context.Context, req *gdsv1.ListClustersRequest) (*gdsv1.ListClustersResponse, error) {
	r.record("ListClusters", req)
	return &gdsv1.ListClustersResponse{}, nil
}

func (r *recorder) WatchClusters(req *gdsv1.WatchClustersRequest, s grpc.ServerStreamingServer[gdsv1.WatchClustersResponse]) error {
	r.record("WatchClusters", req)
	return s.Send(&gdsv1.WatchClustersResponse{Type: gdsv1.EventType_EVENT_TYPE_SNAPSHOT_END, Revision: "7:42"})
}

func (r *recorder) ListClusterSets(_ context.Context, req *gdsv1.ListClusterSetsRequest) (*gdsv1.ListClusterSetsResponse, error) {
	r.record("ListClusterSets", req)
	return &gdsv1.ListClusterSetsResponse{}, nil
}

func (r *recorder) PutServiceExport(_ context.Context, req *gdsv1.PutServiceExportRequest) (*gdsv1.PutServiceExportResponse, error) {
	r.record("PutServiceExport", req)
	return &gdsv1.PutServiceExportResponse{}, nil
}

func (r *recorder) DeleteServiceExport(_ context.Context, req *gdsv1.DeleteServiceExportRequest) (*gdsv1.DeleteServiceExportResponse, error) {
	r.record("DeleteServiceExport", req)
	return &gdsv1.DeleteServiceExportResponse{}, nil
}

func (r *recorder) ListServiceExports(_ context.Context, req *gdsv1.ListServiceExportsRequest) (*gdsv1.ListServiceExportsResponse, error) {
	r.record("ListServiceExports", req)
	return &gdsv1.ListServiceExportsResponse{}, nil
}

func (r *recorder) ReplaceEndpoints(_ context.Context, req *gdsv1.ReplaceEndpointsRequest) (*gdsv1.ReplaceEndpointsResponse, error) {
	r.record("ReplaceEndpoints", req)
	return &gdsv1.ReplaceEndpointsResponse{}, nil
}

func (r *recorder) GetServiceImport(_ context.Context, req *gdsv1.GetServiceImportRequest) (*gdsv1.GetServiceImportResponse, error) {
	r.record("GetServiceImport", req)
	return &gdsv1.GetServiceImportResponse{}, nil
}

func (r *recorder) ListServiceImports(_ context.Context, req *gdsv1.ListServiceImportsRequest) (*gdsv1.ListServiceImportsResponse, error) {
	r.record("ListServiceImports", req)
	return &gdsv1.ListServiceImportsResponse{}, nil
}

func (r *recorder) WatchServiceImports(req *gdsv1.WatchServiceImportsRequest, s grpc.ServerStreamingServer[gdsv1.WatchServiceImportsResponse]) error {
	r.record("WatchServiceImports", req)
	return s.Send(&gdsv1.WatchServiceImportsResponse{Type: gdsv1.EventType_EVENT_TYPE_SNAPSHOT_END, Revision: "7:42"})
}

func (r *recorder) RegisterPool(_ context.Context, req *gdsv1.RegisterPoolRequest) (*gdsv1.RegisterPoolResponse, error) {
	r.record("RegisterPool", req)
	return &gdsv1.RegisterPoolResponse{}, nil
}

func (r *recorder) UpdatePool(_ context.Context, req *gdsv1.UpdatePoolRequest) (*gdsv1.UpdatePoolResponse, error) {
	r.record("UpdatePool", req)
	return &gdsv1.UpdatePoolResponse{}, nil
}

func (r *recorder) HeartbeatPool(_ context.Context, req *gdsv1.HeartbeatPoolRequest) (*gdsv1.HeartbeatPoolResponse, error) {
	r.record("HeartbeatPool", req)
	return &gdsv1.HeartbeatPoolResponse{}, nil
}

func (r *recorder) DeregisterPool(_ context.Context, req *gdsv1.DeregisterPoolRequest) (*gdsv1.DeregisterPoolResponse, error) {
	r.record("DeregisterPool", req)
	return &gdsv1.DeregisterPoolResponse{}, nil
}

func (r *recorder) GetPool(_ context.Context, req *gdsv1.GetPoolRequest) (*gdsv1.GetPoolResponse, error) {
	r.record("GetPool", req)
	return &gdsv1.GetPoolResponse{Pool: &gdsv1.Pool{
		Clusterset:       req.Clusterset,
		PoolId:           req.PoolId,
		ReadyWorkerCount: 3,
		State:            gdsv1.PoolState_POOL_STATE_DRAINING,
		Generation:       1761480000000,
	}}, nil
}

func (r *recorder) ListPools(_ context.Context, req *gdsv1.ListPoolsRequest) (*gdsv1.ListPoolsResponse, error) {
	r.record("ListPools", req)
	return &gdsv1.ListPoolsResponse{}, nil
}

func (r *recorder) WatchPools(req *gdsv1.WatchPoolsRequest, s grpc.ServerStreamingServer[gdsv1.WatchPoolsResponse]) error {
	r.record("WatchPools", req)
	if err := s.Send(&gdsv1.WatchPoolsResponse{Type: gdsv1.EventType_EVENT_TYPE_UPSERT, Pool: &gdsv1.Pool{PoolId: "p1"}, Revision: "7:41"}); err != nil {
		return err
	}
	return s.Send(&gdsv1.WatchPoolsResponse{Type: gdsv1.EventType_EVENT_TYPE_BOOKMARK, Revision: "7:42"})
}

func (r *recorder) ListRouters(_ context.Context, req *gdsv1.ListRoutersRequest) (*gdsv1.ListRoutersResponse, error) {
	r.record("ListRouters", req)
	return &gdsv1.ListRoutersResponse{}, nil
}

func (r *recorder) WatchRouters(req *gdsv1.WatchRoutersRequest, s grpc.ServerStreamingServer[gdsv1.WatchRoutersResponse]) error {
	r.record("WatchRouters", req)
	return s.Send(&gdsv1.WatchRoutersResponse{Type: gdsv1.EventType_EVENT_TYPE_SNAPSHOT_END, Revision: "7:42"})
}

func (r *recorder) GetServerInfo(_ context.Context, req *gdsv1.GetServerInfoRequest) (*gdsv1.GetServerInfoResponse, error) {
	r.record("GetServerInfo", req)
	return &gdsv1.GetServerInfoResponse{Region: "us-east-1"}, nil
}

// newGateway serves the recorder over an in-memory gRPC connection and
// returns a grpc-gateway mux dialling it, as the server's REST listener does.
func newGateway(t *testing.T) (*recorder, http.Handler) {
	t.Helper()
	rec := &recorder{}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	gdsv1.RegisterClusterServiceServer(srv, rec)
	gdsv1.RegisterServiceExportServiceServer(srv, rec)
	gdsv1.RegisterServiceImportServiceServer(srv, rec)
	gdsv1.RegisterPoolServiceServer(srv, rec)
	gdsv1.RegisterServerInfoServiceServer(srv, rec)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx := t.Context()
	mux := runtime.NewServeMux()
	require.NoError(t, gdsv1.RegisterClusterServiceHandler(ctx, mux, conn))
	require.NoError(t, gdsv1.RegisterServiceExportServiceHandler(ctx, mux, conn))
	require.NoError(t, gdsv1.RegisterServiceImportServiceHandler(ctx, mux, conn))
	require.NoError(t, gdsv1.RegisterPoolServiceHandler(ctx, mux, conn))
	require.NoError(t, gdsv1.RegisterServerInfoServiceHandler(ctx, mux, conn))
	return rec, mux
}

// TestRESTMapping pins the REST paths of the design to their RPCs and checks
// that path, query and body fields reach the request.
func TestRESTMapping(t *testing.T) {
	rec, mux := newGateway(t)
	const base = "/v1/clustersets/cs1"

	tests := []struct {
		method, path, body string
		rpc                string
		want               proto.Message
	}{
		{"PUT", base + "/clusters/c1", `{"region":"us-east-1","leaseTtl":"45s"}`, "RegisterCluster",
			&gdsv1.RegisterClusterRequest{Clusterset: "cs1", Cluster: "c1", Region: "us-east-1", LeaseTtl: durationpbSeconds(45)}},
		{"POST", base + "/clusters/c1:heartbeat", `{"epoch":"1761480000000"}`, "HeartbeatCluster",
			&gdsv1.HeartbeatClusterRequest{Clusterset: "cs1", Cluster: "c1", Epoch: 1761480000000}},
		{"DELETE", base + "/clusters/c1", "", "DeregisterCluster",
			&gdsv1.DeregisterClusterRequest{Clusterset: "cs1", Cluster: "c1"}},
		{"GET", base + "/clusters/c1", "", "GetCluster",
			&gdsv1.GetClusterRequest{Clusterset: "cs1", Cluster: "c1"}},
		{"GET", base + "/clusters?includeExpired=true", "", "ListClusters",
			&gdsv1.ListClustersRequest{Clusterset: "cs1", IncludeExpired: true}},
		{"GET", base + "/clusters:watch?resumeRevision=7:40", "", "WatchClusters",
			&gdsv1.WatchClustersRequest{Clusterset: "cs1", ResumeRevision: "7:40"}},
		{"GET", "/v1/clustersets", "", "ListClusterSets",
			&gdsv1.ListClusterSetsRequest{}},
		{"PUT", base + "/clusters/c1/exports/llm/router", `{"generation":"5","spec":{"type":"SERVICE_TYPE_HEADLESS"}}`, "PutServiceExport",
			&gdsv1.PutServiceExportRequest{Clusterset: "cs1", Cluster: "c1", Namespace: "llm", Name: "router", Generation: 5,
				Spec: &gdsv1.ServiceExportSpec{Type: gdsv1.ServiceType_SERVICE_TYPE_HEADLESS}}},
		{"DELETE", base + "/clusters/c1/exports/llm/router", "", "DeleteServiceExport",
			&gdsv1.DeleteServiceExportRequest{Clusterset: "cs1", Cluster: "c1", Namespace: "llm", Name: "router"}},
		{"GET", base + "/clusters/c1/exports", "", "ListServiceExports",
			&gdsv1.ListServiceExportsRequest{Clusterset: "cs1", Cluster: "c1"}},
		{"PUT", base + "/clusters/c1/exports/llm/router/endpoints", `{"generation":"6","endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":true}}]}`, "ReplaceEndpoints",
			&gdsv1.ReplaceEndpointsRequest{Clusterset: "cs1", Cluster: "c1", Namespace: "llm", Name: "router", Generation: 6,
				Endpoints: []*gdsv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: &gdsv1.EndpointConditions{Ready: true}}}}},
		{"GET", base + "/imports/llm/router?includeEndpoints=true", "", "GetServiceImport",
			&gdsv1.GetServiceImportRequest{Clusterset: "cs1", Namespace: "llm", Name: "router", IncludeEndpoints: true}},
		{"GET", base + "/imports?namespace=llm", "", "ListServiceImports",
			&gdsv1.ListServiceImportsRequest{Clusterset: "cs1", Namespace: "llm"}},
		{"GET", base + "/imports:watch?includeEndpoints=true", "", "WatchServiceImports",
			&gdsv1.WatchServiceImportsRequest{Clusterset: "cs1", IncludeEndpoints: true}},
		{"POST", base + "/pools", `{"pool":{"name":"pool-a","location":{"cluster":"c1"}},"generation":"1"}`, "RegisterPool",
			&gdsv1.RegisterPoolRequest{Clusterset: "cs1", Generation: 1,
				Pool: &gdsv1.Pool{Name: "pool-a", Location: &gdsv1.Location{Cluster: "c1"}}}},
		{"PATCH", base + "/pools/p1", `{"pool":{"desiredState":"POOL_STATE_DRAINING"},"updateMask":"desiredState","generation":"2"}`, "UpdatePool",
			&gdsv1.UpdatePoolRequest{Clusterset: "cs1", PoolId: "p1", Generation: 2,
				Pool:       &gdsv1.Pool{DesiredState: gdsv1.PoolState_POOL_STATE_DRAINING},
				UpdateMask: fieldmask("desired_state")}},
		{"POST", base + "/pools/p1:heartbeat", `{"epoch":"9","workerCount":4}`, "HeartbeatPool",
			&gdsv1.HeartbeatPoolRequest{Clusterset: "cs1", PoolId: "p1", Epoch: 9, WorkerCount: proto.Int32(4)}},
		{"DELETE", base + "/pools/p1", "", "DeregisterPool",
			&gdsv1.DeregisterPoolRequest{Clusterset: "cs1", PoolId: "p1"}},
		{"GET", base + "/pools/p1", "", "GetPool",
			&gdsv1.GetPoolRequest{Clusterset: "cs1", PoolId: "p1"}},
		{"GET", base + "/pools?model=llama&hardwareClass=b200-ib&states=POOL_STATE_ACTIVE&states=POOL_STATE_DRAINING", "", "ListPools",
			&gdsv1.ListPoolsRequest{Clusterset: "cs1", Model: "llama", HardwareClass: "b200-ib",
				States: []gdsv1.PoolState{gdsv1.PoolState_POOL_STATE_ACTIVE, gdsv1.PoolState_POOL_STATE_DRAINING}}},
		{"GET", base + "/pools:watch?region=us-east-1", "", "WatchPools",
			&gdsv1.WatchPoolsRequest{Clusterset: "cs1", Region: "us-east-1"}},
		{"GET", base + "/routers?cluster=c1", "", "ListRouters",
			&gdsv1.ListRoutersRequest{Clusterset: "cs1", Cluster: "c1"}},
		{"GET", base + "/routers:watch", "", "WatchRouters",
			&gdsv1.WatchRoutersRequest{Clusterset: "cs1"}},
		{"GET", "/v1/serverinfo", "", "GetServerInfo",
			&gdsv1.GetServerInfoRequest{}},
	}
	for _, tt := range tests {
		t.Run(tt.rpc, func(t *testing.T) {
			resp := serve(t, mux, tt.method, tt.path, tt.body)
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			rpc, req := rec.last()
			assert.Equal(t, tt.rpc, rpc)
			assert.True(t, proto.Equal(tt.want, req), "got %v, want %v", req, tt.want)
		})
	}
}

// TestJSONShape checks the proto3 JSON mapping clients rely on.
func TestJSONShape(t *testing.T) {
	_, mux := newGateway(t)
	resp := serve(t, mux, "GET", "/v1/clustersets/cs1/pools/p1", "")
	require.Equal(t, http.StatusOK, resp.Code)
	body := resp.Body.String()
	assert.Contains(t, body, `"poolId":"p1"`, "lowerCamelCase field names")
	assert.Contains(t, body, `"readyWorkerCount":3`)
	assert.Contains(t, body, `"state":"POOL_STATE_DRAINING"`, "enums as strings")
	assert.Contains(t, body, `"generation":"1761480000000"`, "64-bit integers as strings")
}

// TestWatchStreamShape checks that watches stream newline-delimited
// {"result": {...}} objects over REST.
func TestWatchStreamShape(t *testing.T) {
	_, mux := newGateway(t)
	resp := serve(t, mux, "GET", "/v1/clustersets/cs1/pools:watch", "")
	require.Equal(t, http.StatusOK, resp.Code)
	lines := strings.Split(strings.TrimSpace(resp.Body.String()), "\n")
	require.Len(t, lines, 2, resp.Body.String())
	assert.True(t, strings.HasPrefix(lines[0], `{"result":{`), lines[0])
	assert.Contains(t, lines[0], `"type":"EVENT_TYPE_UPSERT"`)
	assert.Contains(t, lines[0], `"poolId":"p1"`)
	assert.Contains(t, lines[1], `"type":"EVENT_TYPE_BOOKMARK"`)
	assert.Contains(t, lines[1], `"revision":"7:42"`)
}

// TestUnknownPathIsNotFound guards against accidental catch-all routes.
func TestUnknownPathIsNotFound(t *testing.T) {
	_, mux := newGateway(t)
	assert.Equal(t, http.StatusNotFound, serve(t, mux, "GET", "/v1/clustersets/cs1/nope", "").Code)
	assert.Equal(t, http.StatusNotImplemented, serve(t, mux, "PUT", "/v1/clustersets/cs1/pools/p1", "{}").Code)
}

func serve(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	return resp
}

func durationpbSeconds(s int64) *durationpb.Duration {
	return durationpb.New(time.Duration(s) * time.Second)
}

func fieldmask(paths ...string) *fieldmaskpb.FieldMask {
	return &fieldmaskpb.FieldMask{Paths: paths}
}
