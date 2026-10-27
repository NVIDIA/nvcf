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

package api

import (
	"context"
	"errors"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/convert"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

func validateClusterKey(v *validator, clusterSet, cluster string) store.ClusterKey {
	v.dnsLabel("clusterset", clusterSet)
	v.dnsLabel("cluster", cluster)
	return store.ClusterKey{ClusterSet: clusterSet, Cluster: cluster}
}

// RegisterCluster implements gdsv1.ClusterServiceServer.
func (s *Server) RegisterCluster(ctx context.Context, req *gdsv1.RegisterClusterRequest) (*gdsv1.RegisterClusterResponse, error) {
	var v validator
	key := validateClusterKey(&v, req.GetClusterset(), req.GetCluster())
	v.maxLen("region", req.GetRegion(), 63)
	v.maxLen("provider", req.GetProvider(), 63)
	v.maxLen("agent_id", req.GetAgentId(), maxNameBytes)
	v.maxLen("agent_version", req.GetAgentVersion(), maxNameBytes)
	if len(req.GetZones()) > maxZones {
		v.add("zones", "has more than %d entries", maxZones)
	}
	v.labels("labels", req.GetLabels())
	ttl := v.leaseTTL("lease_ttl", req.GetLeaseTtl(), registry.DefaultClusterLeaseTTL)
	if err := v.err(); err != nil {
		return nil, err
	}
	now := s.now()
	rec, err := s.store.PutCluster(ctx, store.ClusterRecord{
		Key:          key,
		Region:       req.GetRegion(),
		Zones:        req.GetZones(),
		Provider:     req.GetProvider(),
		Labels:       req.GetLabels(),
		AgentID:      req.GetAgentId(),
		AgentVersion: req.GetAgentVersion(),
		Lease:        store.Lease{TTL: ttl},
	}, now)
	if err != nil {
		return nil, storeError(err, "register cluster")
	}
	s.view.ApplyCluster(rec)
	s.log.Info("cluster registered", zap.String("clusterset", key.ClusterSet), zap.String("cluster", key.Cluster),
		zap.String("region", rec.Region), zap.Uint64("epoch", rec.Lease.Epoch), zap.Duration("lease_ttl", ttl))
	return &gdsv1.RegisterClusterResponse{
		Cluster:    convert.ClusterToProto(rec, registry.LeaseStateActive),
		ServerTime: convert.Timestamp(now),
	}, nil
}

// HeartbeatCluster implements gdsv1.ClusterServiceServer.
func (s *Server) HeartbeatCluster(ctx context.Context, req *gdsv1.HeartbeatClusterRequest) (*gdsv1.HeartbeatClusterResponse, error) {
	var v validator
	key := validateClusterKey(&v, req.GetClusterset(), req.GetCluster())
	if req.GetEpoch() == 0 {
		v.add("epoch", "is required")
	}
	if err := v.err(); err != nil {
		return nil, err
	}
	now := s.now()
	rec, err := s.store.RenewClusterLease(ctx, key, req.GetEpoch(), now)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, clusterNotRegistered(codes.NotFound, key)
	case err != nil:
		return nil, storeError(err, "heartbeat cluster")
	}
	s.view.ApplyCluster(rec)
	return &gdsv1.HeartbeatClusterResponse{
		Lease:      convert.LeaseToProto(rec.Lease, registry.LeaseStateActive),
		ServerTime: convert.Timestamp(now),
	}, nil
}

// DeregisterCluster implements gdsv1.ClusterServiceServer.
func (s *Server) DeregisterCluster(ctx context.Context, req *gdsv1.DeregisterClusterRequest) (*gdsv1.DeregisterClusterResponse, error) {
	var v validator
	key := validateClusterKey(&v, req.GetClusterset(), req.GetCluster())
	if err := v.err(); err != nil {
		return nil, err
	}
	if err := s.store.DeleteCluster(ctx, key); err != nil {
		return nil, storeError(err, "deregister cluster")
	}
	s.view.ApplyClusterDelete(key)
	s.log.Info("cluster deregistered", zap.String("clusterset", key.ClusterSet), zap.String("cluster", key.Cluster))
	return &gdsv1.DeregisterClusterResponse{}, nil
}

// GetCluster implements gdsv1.ClusterServiceServer.
func (s *Server) GetCluster(_ context.Context, req *gdsv1.GetClusterRequest) (*gdsv1.GetClusterResponse, error) {
	var v validator
	key := validateClusterKey(&v, req.GetClusterset(), req.GetCluster())
	if err := v.err(); err != nil {
		return nil, err
	}
	o, ok := s.view.Get(key.ClusterSet, view.KindCluster, key.Cluster)
	if !ok {
		return nil, clusterNotRegistered(codes.NotFound, key)
	}
	return &gdsv1.GetClusterResponse{Cluster: o.Full.(*gdsv1.Cluster)}, nil
}

// ListClusters implements gdsv1.ClusterServiceServer.
func (s *Server) ListClusters(_ context.Context, req *gdsv1.ListClustersRequest) (*gdsv1.ListClustersResponse, error) {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	if err := v.err(); err != nil {
		return nil, err
	}
	msgs, rev := s.view.List(req.GetClusterset(), view.ClusterFilter{IncludeExpired: req.GetIncludeExpired()})
	resp := &gdsv1.ListClustersResponse{Revision: rev}
	for _, m := range msgs {
		resp.Clusters = append(resp.Clusters, m.(*gdsv1.Cluster))
	}
	return resp, nil
}

// WatchClusters implements gdsv1.ClusterServiceServer.
func (s *Server) WatchClusters(req *gdsv1.WatchClustersRequest, stream grpc.ServerStreamingServer[gdsv1.WatchClustersResponse]) error {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	if err := v.err(); err != nil {
		return err
	}
	f := view.ClusterFilter{IncludeExpired: req.GetIncludeExpired()}
	return watchError(s.hub.Watch(stream.Context(), req.GetClusterset(), f, req.GetResumeRevision(), func(ev view.WatchEvent) error {
		resp := &gdsv1.WatchClustersResponse{Type: ev.Type, Revision: ev.Revision}
		if ev.Object != nil {
			resp.Cluster = ev.Object.(*gdsv1.Cluster)
		}
		return stream.Send(resp)
	}))
}

// ListClusterSets implements gdsv1.ClusterServiceServer.
func (s *Server) ListClusterSets(context.Context, *gdsv1.ListClusterSetsRequest) (*gdsv1.ListClusterSetsResponse, error) {
	resp := &gdsv1.ListClusterSetsResponse{}
	for _, sum := range s.view.Summaries() {
		resp.Clustersets = append(resp.Clustersets, &gdsv1.ClusterSetSummary{
			Clusterset:         sum.ClusterSet,
			ClusterCount:       int32(sum.Clusters),
			ActiveClusterCount: int32(sum.ActiveClusters),
			ExportCount:        int32(sum.Exports),
			PoolCount:          int32(sum.Pools),
		})
	}
	return resp, nil
}
