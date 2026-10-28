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

package client

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// The sub-clients are thin typed wrappers over the generated clients. Unary
// calls whose context has no deadline get Config.RPCTimeout, and unary
// calls failing with UNAVAILABLE are retried up to Config.MaxAttempts
// times; both apply to Conn() users as well. Watch calls return the raw
// stream; Watcher adapts them to a WatchLoop.

// ClusterClient calls the ClusterService.
type ClusterClient struct{ c gdsv1.ClusterServiceClient }

func (x *ClusterClient) Register(ctx context.Context, req *gdsv1.RegisterClusterRequest, opts ...grpc.CallOption) (*gdsv1.RegisterClusterResponse, error) {
	return x.c.RegisterCluster(ctx, req, opts...)
}

func (x *ClusterClient) Heartbeat(ctx context.Context, req *gdsv1.HeartbeatClusterRequest, opts ...grpc.CallOption) (*gdsv1.HeartbeatClusterResponse, error) {
	return x.c.HeartbeatCluster(ctx, req, opts...)
}

func (x *ClusterClient) Deregister(ctx context.Context, req *gdsv1.DeregisterClusterRequest, opts ...grpc.CallOption) (*gdsv1.DeregisterClusterResponse, error) {
	return x.c.DeregisterCluster(ctx, req, opts...)
}

func (x *ClusterClient) Get(ctx context.Context, req *gdsv1.GetClusterRequest, opts ...grpc.CallOption) (*gdsv1.GetClusterResponse, error) {
	return x.c.GetCluster(ctx, req, opts...)
}

func (x *ClusterClient) List(ctx context.Context, req *gdsv1.ListClustersRequest, opts ...grpc.CallOption) (*gdsv1.ListClustersResponse, error) {
	return x.c.ListClusters(ctx, req, opts...)
}

func (x *ClusterClient) Watch(ctx context.Context, req *gdsv1.WatchClustersRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[gdsv1.WatchClustersResponse], error) {
	return x.c.WatchClusters(ctx, req, opts...)
}

func (x *ClusterClient) ListClusterSets(ctx context.Context, req *gdsv1.ListClusterSetsRequest, opts ...grpc.CallOption) (*gdsv1.ListClusterSetsResponse, error) {
	return x.c.ListClusterSets(ctx, req, opts...)
}

// Watcher returns a WatchFunc for WatchClusters with req's filters.
func (x *ClusterClient) Watcher(req *gdsv1.WatchClustersRequest) WatchFunc[*gdsv1.Cluster] {
	req = proto.CloneOf(req)
	return func(ctx context.Context, revision string) (WatchStream[*gdsv1.Cluster], error) {
		r := proto.CloneOf(req)
		r.ResumeRevision = revision
		s, err := x.c.WatchClusters(ctx, r)
		if err != nil {
			return nil, err
		}
		return recvFunc[*gdsv1.Cluster](func() (Event[*gdsv1.Cluster], error) {
			m, err := s.Recv()
			if err != nil {
				return Event[*gdsv1.Cluster]{}, err
			}
			return Event[*gdsv1.Cluster]{Type: m.GetType(), Object: m.GetCluster(), Revision: m.GetRevision()}, nil
		}), nil
	}
}

// ServiceExportClient calls the ServiceExportService.
type ServiceExportClient struct {
	c gdsv1.ServiceExportServiceClient
}

func (x *ServiceExportClient) Put(ctx context.Context, req *gdsv1.PutServiceExportRequest, opts ...grpc.CallOption) (*gdsv1.PutServiceExportResponse, error) {
	return x.c.PutServiceExport(ctx, req, opts...)
}

func (x *ServiceExportClient) Delete(ctx context.Context, req *gdsv1.DeleteServiceExportRequest, opts ...grpc.CallOption) (*gdsv1.DeleteServiceExportResponse, error) {
	return x.c.DeleteServiceExport(ctx, req, opts...)
}

func (x *ServiceExportClient) List(ctx context.Context, req *gdsv1.ListServiceExportsRequest, opts ...grpc.CallOption) (*gdsv1.ListServiceExportsResponse, error) {
	return x.c.ListServiceExports(ctx, req, opts...)
}

func (x *ServiceExportClient) ReplaceEndpoints(ctx context.Context, req *gdsv1.ReplaceEndpointsRequest, opts ...grpc.CallOption) (*gdsv1.ReplaceEndpointsResponse, error) {
	return x.c.ReplaceEndpoints(ctx, req, opts...)
}

// ServiceImportClient calls the ServiceImportService.
type ServiceImportClient struct {
	c gdsv1.ServiceImportServiceClient
}

func (x *ServiceImportClient) Get(ctx context.Context, req *gdsv1.GetServiceImportRequest, opts ...grpc.CallOption) (*gdsv1.GetServiceImportResponse, error) {
	return x.c.GetServiceImport(ctx, req, opts...)
}

func (x *ServiceImportClient) List(ctx context.Context, req *gdsv1.ListServiceImportsRequest, opts ...grpc.CallOption) (*gdsv1.ListServiceImportsResponse, error) {
	return x.c.ListServiceImports(ctx, req, opts...)
}

func (x *ServiceImportClient) Watch(ctx context.Context, req *gdsv1.WatchServiceImportsRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[gdsv1.WatchServiceImportsResponse], error) {
	return x.c.WatchServiceImports(ctx, req, opts...)
}

// Watcher returns a WatchFunc for WatchServiceImports with req's filters.
func (x *ServiceImportClient) Watcher(req *gdsv1.WatchServiceImportsRequest) WatchFunc[*gdsv1.ServiceImport] {
	req = proto.CloneOf(req)
	return func(ctx context.Context, revision string) (WatchStream[*gdsv1.ServiceImport], error) {
		r := proto.CloneOf(req)
		r.ResumeRevision = revision
		s, err := x.c.WatchServiceImports(ctx, r)
		if err != nil {
			return nil, err
		}
		return recvFunc[*gdsv1.ServiceImport](func() (Event[*gdsv1.ServiceImport], error) {
			m, err := s.Recv()
			if err != nil {
				return Event[*gdsv1.ServiceImport]{}, err
			}
			return Event[*gdsv1.ServiceImport]{Type: m.GetType(), Object: m.GetServiceImport(), Revision: m.GetRevision()}, nil
		}), nil
	}
}

// PoolClient calls the pool RPCs of the PoolService.
type PoolClient struct{ c gdsv1.PoolServiceClient }

func (x *PoolClient) Register(ctx context.Context, req *gdsv1.RegisterPoolRequest, opts ...grpc.CallOption) (*gdsv1.RegisterPoolResponse, error) {
	return x.c.RegisterPool(ctx, req, opts...)
}

func (x *PoolClient) Update(ctx context.Context, req *gdsv1.UpdatePoolRequest, opts ...grpc.CallOption) (*gdsv1.UpdatePoolResponse, error) {
	return x.c.UpdatePool(ctx, req, opts...)
}

func (x *PoolClient) Heartbeat(ctx context.Context, req *gdsv1.HeartbeatPoolRequest, opts ...grpc.CallOption) (*gdsv1.HeartbeatPoolResponse, error) {
	return x.c.HeartbeatPool(ctx, req, opts...)
}

func (x *PoolClient) Deregister(ctx context.Context, req *gdsv1.DeregisterPoolRequest, opts ...grpc.CallOption) (*gdsv1.DeregisterPoolResponse, error) {
	return x.c.DeregisterPool(ctx, req, opts...)
}

// RegisterAgent calls RegisterPoolAgent. AgentRegistration wraps it with a
// heartbeat.
func (x *PoolClient) RegisterAgent(ctx context.Context, req *gdsv1.RegisterPoolAgentRequest, opts ...grpc.CallOption) (*gdsv1.RegisterPoolAgentResponse, error) {
	return x.c.RegisterPoolAgent(ctx, req, opts...)
}

// HeartbeatAgent calls HeartbeatPoolAgent.
func (x *PoolClient) HeartbeatAgent(ctx context.Context, req *gdsv1.HeartbeatPoolAgentRequest, opts ...grpc.CallOption) (*gdsv1.HeartbeatPoolAgentResponse, error) {
	return x.c.HeartbeatPoolAgent(ctx, req, opts...)
}

// DeregisterAgent calls DeregisterPoolAgent.
func (x *PoolClient) DeregisterAgent(ctx context.Context, req *gdsv1.DeregisterPoolAgentRequest, opts ...grpc.CallOption) (*gdsv1.DeregisterPoolAgentResponse, error) {
	return x.c.DeregisterPoolAgent(ctx, req, opts...)
}

func (x *PoolClient) Get(ctx context.Context, req *gdsv1.GetPoolRequest, opts ...grpc.CallOption) (*gdsv1.GetPoolResponse, error) {
	return x.c.GetPool(ctx, req, opts...)
}

func (x *PoolClient) List(ctx context.Context, req *gdsv1.ListPoolsRequest, opts ...grpc.CallOption) (*gdsv1.ListPoolsResponse, error) {
	return x.c.ListPools(ctx, req, opts...)
}

func (x *PoolClient) Watch(ctx context.Context, req *gdsv1.WatchPoolsRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[gdsv1.WatchPoolsResponse], error) {
	return x.c.WatchPools(ctx, req, opts...)
}

// Watcher returns a WatchFunc for WatchPools with req's filters.
func (x *PoolClient) Watcher(req *gdsv1.WatchPoolsRequest) WatchFunc[*gdsv1.Pool] {
	req = proto.CloneOf(req)
	return func(ctx context.Context, revision string) (WatchStream[*gdsv1.Pool], error) {
		r := proto.CloneOf(req)
		r.ResumeRevision = revision
		s, err := x.c.WatchPools(ctx, r)
		if err != nil {
			return nil, err
		}
		return recvFunc[*gdsv1.Pool](func() (Event[*gdsv1.Pool], error) {
			m, err := s.Recv()
			if err != nil {
				return Event[*gdsv1.Pool]{}, err
			}
			return Event[*gdsv1.Pool]{Type: m.GetType(), Object: m.GetPool(), Revision: m.GetRevision()}, nil
		}), nil
	}
}

// RouterClient calls the router RPCs of the PoolService.
type RouterClient struct{ c gdsv1.PoolServiceClient }

func (x *RouterClient) List(ctx context.Context, req *gdsv1.ListRoutersRequest, opts ...grpc.CallOption) (*gdsv1.ListRoutersResponse, error) {
	return x.c.ListRouters(ctx, req, opts...)
}

func (x *RouterClient) Watch(ctx context.Context, req *gdsv1.WatchRoutersRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[gdsv1.WatchRoutersResponse], error) {
	return x.c.WatchRouters(ctx, req, opts...)
}

// Watcher returns a WatchFunc for WatchRouters with req's filters.
func (x *RouterClient) Watcher(req *gdsv1.WatchRoutersRequest) WatchFunc[*gdsv1.Router] {
	req = proto.CloneOf(req)
	return func(ctx context.Context, revision string) (WatchStream[*gdsv1.Router], error) {
		r := proto.CloneOf(req)
		r.ResumeRevision = revision
		s, err := x.c.WatchRouters(ctx, r)
		if err != nil {
			return nil, err
		}
		return recvFunc[*gdsv1.Router](func() (Event[*gdsv1.Router], error) {
			m, err := s.Recv()
			if err != nil {
				return Event[*gdsv1.Router]{}, err
			}
			return Event[*gdsv1.Router]{Type: m.GetType(), Object: m.GetRouter(), Revision: m.GetRevision()}, nil
		}), nil
	}
}

// ServerInfoClient calls the ServerInfoService.
type ServerInfoClient struct {
	c gdsv1.ServerInfoServiceClient
}

func (x *ServerInfoClient) Get(ctx context.Context, opts ...grpc.CallOption) (*gdsv1.GetServerInfoResponse, error) {
	return x.c.GetServerInfo(ctx, &gdsv1.GetServerInfoRequest{}, opts...)
}
