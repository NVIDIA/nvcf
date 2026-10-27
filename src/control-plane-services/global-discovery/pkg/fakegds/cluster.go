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

package fakegds

import (
	"context"
	"maps"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
)

type clusterService struct {
	gdsv1.UnimplementedClusterServiceServer
	s *Server
}

func (c *clusterService) RegisterCluster(_ context.Context, req *gdsv1.RegisterClusterRequest) (*gdsv1.RegisterClusterResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "cluster", req.GetCluster()); err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.registerClusterLocked(req)
	return &gdsv1.RegisterClusterResponse{
		Cluster:    clusterView(rec, s.now()),
		ServerTime: timestamppb.New(s.now()),
	}, nil
}

func (s *Server) registerClusterLocked(req *gdsv1.RegisterClusterRequest) *clusterRec {
	now := s.now()
	cs := s.setLocked(req.GetClusterset())
	ttl := registry.ClampLeaseTTL(req.GetLeaseTtl().AsDuration(), registry.DefaultClusterLeaseTTL)
	var prevEpoch, prevGen uint64
	registered := now
	if old := cs.clusters[req.GetCluster()]; old != nil {
		prevEpoch, prevGen = old.c.GetLease().GetEpoch(), old.c.GetGeneration()
		registered = old.c.GetRegisteredAt().AsTime()
	}
	rec := &clusterRec{
		ttl: ttl,
		c: &gdsv1.Cluster{
			Clusterset:   req.GetClusterset(),
			Cluster:      req.GetCluster(),
			Region:       req.GetRegion(),
			Zones:        slices.Clone(req.GetZones()),
			Provider:     req.GetProvider(),
			Labels:       maps.Clone(req.GetLabels()),
			AgentId:      req.GetAgentId(),
			AgentVersion: req.GetAgentVersion(),
			Lease: &gdsv1.Lease{
				Epoch:     registry.NextGeneration(prevEpoch, now),
				Ttl:       durationpb.New(ttl),
				RenewedAt: timestamppb.New(now),
			},
			Generation:   prevGen + 1,
			RegisteredAt: timestamppb.New(registered),
			UpdatedAt:    timestamppb.New(now),
		},
	}
	cs.clusters[req.GetCluster()] = rec
	s.commitLocked(cs)
	return rec
}

func (c *clusterService) HeartbeatCluster(_ context.Context, req *gdsv1.HeartbeatClusterRequest) (*gdsv1.HeartbeatClusterResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "cluster", req.GetCluster()); err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cs := s.setLocked(req.GetClusterset())
	rec := cs.clusters[req.GetCluster()]
	if rec == nil {
		return nil, errClusterNotRegistered(codes.NotFound, req.GetClusterset(), req.GetCluster())
	}
	if cur := rec.c.GetLease().GetEpoch(); req.GetEpoch() != cur {
		return nil, errEpochMismatch("cluster", req.GetEpoch(), cur)
	}
	rec.c = proto.CloneOf(rec.c)
	rec.c.Lease.RenewedAt = timestamppb.New(now)
	s.commitLocked(cs)
	return &gdsv1.HeartbeatClusterResponse{
		Lease:      leaseView(rec.c.GetLease(), rec.ttl, now),
		ServerTime: timestamppb.New(now),
	}, nil
}

func (c *clusterService) DeregisterCluster(_ context.Context, req *gdsv1.DeregisterClusterRequest) (*gdsv1.DeregisterClusterResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "cluster", req.GetCluster()); err != nil {
		return nil, err
	}
	c.s.removeCluster(req.GetClusterset(), req.GetCluster())
	return &gdsv1.DeregisterClusterResponse{}, nil
}

// removeCluster deletes a cluster with its exports, endpoint sets and pools.
func (s *Server) removeCluster(clusterset, cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(clusterset)
	delete(cs.clusters, cluster)
	for k := range cs.exports {
		if k.cluster == cluster {
			delete(cs.exports, k)
		}
	}
	for k := range cs.endpoints {
		if k.cluster == cluster {
			delete(cs.endpoints, k)
		}
	}
	for id, p := range cs.pools {
		if p.p.GetLocation().GetCluster() == cluster {
			delete(cs.pools, id)
		}
	}
	s.commitLocked(cs)
}

func (c *clusterService) GetCluster(_ context.Context, req *gdsv1.GetClusterRequest) (*gdsv1.GetClusterResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "cluster", req.GetCluster()); err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.refreshedLocked(req.GetClusterset())
	obj, ok := cs.views[kindClusters][req.GetCluster()]
	if !ok {
		return nil, errClusterNotRegistered(codes.NotFound, req.GetClusterset(), req.GetCluster())
	}
	return &gdsv1.GetClusterResponse{Cluster: obj.(*gdsv1.Cluster)}, nil
}

func clusterMatcher(includeExpired bool) func(*gdsv1.Cluster) bool {
	return func(c *gdsv1.Cluster) bool {
		return includeExpired || c.GetLease().GetState() != gdsv1.LeaseState_LEASE_STATE_EXPIRED
	}
}

func (c *clusterService) ListClusters(_ context.Context, req *gdsv1.ListClustersRequest) (*gdsv1.ListClustersResponse, error) {
	if err := require("clusterset", req.GetClusterset()); err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.refreshedLocked(req.GetClusterset())
	return &gdsv1.ListClustersResponse{
		Clusters: sortedViews(cs, kindClusters, clusterMatcher(req.GetIncludeExpired())),
		Revision: s.revision(s.seq),
	}, nil
}

func (c *clusterService) WatchClusters(req *gdsv1.WatchClustersRequest, stream grpc.ServerStreamingServer[gdsv1.WatchClustersResponse]) error {
	match := clusterMatcher(req.GetIncludeExpired())
	return c.s.serveWatch(stream.Context(), &watchSpec{
		kind:   kindClusters,
		cs:     req.GetClusterset(),
		resume: req.GetResumeRevision(),
		match:  func(m proto.Message) bool { return match(m.(*gdsv1.Cluster)) },
		view:   func(m proto.Message) proto.Message { return m },
		send: func(typ gdsv1.EventType, obj proto.Message, rev string) error {
			resp := &gdsv1.WatchClustersResponse{Type: typ, Revision: rev}
			if obj != nil {
				resp.Cluster = obj.(*gdsv1.Cluster)
			}
			return stream.Send(resp)
		},
	})
}

func (c *clusterService) ListClusterSets(context.Context, *gdsv1.ListClusterSetsRequest) (*gdsv1.ListClusterSetsResponse, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &gdsv1.ListClusterSetsResponse{}
	for _, name := range slices.Sorted(maps.Keys(s.sets)) {
		cs := s.refreshedLocked(name)
		sum := &gdsv1.ClusterSetSummary{
			Clusterset:   name,
			ClusterCount: int32(len(cs.views[kindClusters])),
			ExportCount:  int32(len(cs.exports)),
			PoolCount:    int32(len(cs.views[kindPools])),
		}
		for _, obj := range cs.views[kindClusters] {
			if obj.(*gdsv1.Cluster).GetLease().GetState() == gdsv1.LeaseState_LEASE_STATE_ACTIVE {
				sum.ActiveClusterCount++
			}
		}
		resp.Clustersets = append(resp.Clustersets, sum)
	}
	return resp, nil
}
