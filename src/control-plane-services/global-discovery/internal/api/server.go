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

// Package api implements the nvidia.gds.v1 gRPC services over the store and
// the materialised view: request validation against the API limits, error
// mapping to google.rpc.Status with an ErrorInfo in the gds.nvidia.com
// domain, lease epochs and writer generations, the write-through to the
// view after every successful store write, and watches through the hub.
//
// Reads are answered from the view of the handling replica. Writes go to
// the store and are applied to the view before the response, so a writer
// reads its own write at once.
package api

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/clock"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

// Options configures a Server.
type Options struct {
	Store store.Store
	View  *view.View
	Hub   *view.Hub
	// Refresher, when set, reloads a clusterset synchronously when a
	// request names an object this replica has not seen yet (a write that
	// another replica handled since the last refresh).
	Refresher *view.Refresher
	Clock     clock.Clock
	// Region and Version are reported by GetServerInfo.
	Region  string
	Version string
	// Endpoints are the advertised GDS endpoints of every region.
	Endpoints []*gdsv1.ServerEndpoint
	Logger    *zap.Logger
}

// Server implements every nvidia.gds.v1 service.
type Server struct {
	gdsv1.UnimplementedClusterServiceServer
	gdsv1.UnimplementedServiceExportServiceServer
	gdsv1.UnimplementedServiceImportServiceServer
	gdsv1.UnimplementedPoolServiceServer
	gdsv1.UnimplementedServerInfoServiceServer

	store     store.Store
	view      *view.View
	hub       *view.Hub
	refresher *view.Refresher
	clock     clock.Clock
	opts      Options
	log       *zap.Logger
}

// New returns a Server.
func New(opts Options) *Server {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	return &Server{
		store:     opts.Store,
		view:      opts.View,
		hub:       opts.Hub,
		refresher: opts.Refresher,
		clock:     opts.Clock,
		opts:      opts,
		log:       opts.Logger,
	}
}

// ServiceNames are the fully qualified names of the services Register adds.
var ServiceNames = []string{
	gdsv1.ClusterService_ServiceDesc.ServiceName,
	gdsv1.ServiceExportService_ServiceDesc.ServiceName,
	gdsv1.ServiceImportService_ServiceDesc.ServiceName,
	gdsv1.PoolService_ServiceDesc.ServiceName,
	gdsv1.ServerInfoService_ServiceDesc.ServiceName,
}

// Register registers every service on r.
func (s *Server) Register(r grpc.ServiceRegistrar) {
	gdsv1.RegisterClusterServiceServer(r, s)
	gdsv1.RegisterServiceExportServiceServer(r, s)
	gdsv1.RegisterServiceImportServiceServer(r, s)
	gdsv1.RegisterPoolServiceServer(r, s)
	gdsv1.RegisterServerInfoServiceServer(r, s)
}

func (s *Server) now() time.Time { return s.clock.Now().UTC() }

// refresh reloads a clusterset synchronously, best effort.
func (s *Server) refresh(ctx context.Context, clusterSet string) {
	if s.refresher == nil {
		return
	}
	if err := s.refresher.Refresh(ctx, clusterSet); err != nil {
		s.log.Debug("synchronous refresh failed", zap.String("clusterset", clusterSet), zap.Error(err))
	}
}

// requireCluster fails with FAILED_PRECONDITION CLUSTER_NOT_REGISTERED when
// the cluster has no registration.
func (s *Server) requireCluster(ctx context.Context, key store.ClusterKey) error {
	if _, ok := s.view.ClusterRecord(key); ok {
		return nil
	}
	_, err := s.store.GetCluster(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return clusterNotRegistered(codes.FailedPrecondition, key)
	case err != nil:
		return storeError(err, "get cluster")
	}
	return nil
}

// generation returns the writer's generation, or one the server assigns
// when the writer sends 0: max(stored + 1, now in unix milliseconds).
func generation(requested, stored uint64, now time.Time) (gen uint64, assigned bool) {
	if requested != 0 {
		return requested, false
	}
	return registry.NextGeneration(stored, now), true
}
