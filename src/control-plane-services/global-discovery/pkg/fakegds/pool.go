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
	"crypto/rand"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
)

type poolService struct {
	gdsv1.UnimplementedPoolServiceServer
	s *Server
}

// newPoolID returns a random version 4 UUID.
func newPoolID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// writerFields copies the writer-owned fields of a pool.
func writerFields(p *gdsv1.Pool) *gdsv1.Pool {
	out := &gdsv1.Pool{
		PoolId:           p.GetPoolId(),
		Name:             p.GetName(),
		Location:         proto.CloneOf(p.GetLocation()),
		HardwareClass:    p.GetHardwareClass(),
		WorkerCount:      p.GetWorkerCount(),
		ReadyWorkerCount: p.GetReadyWorkerCount(),
		Labels:           maps.Clone(p.GetLabels()),
		DesiredState:     p.GetDesiredState(),
		OwnerId:          p.GetOwnerId(),
	}
	for _, m := range p.GetModels() {
		out.Models = append(out.Models, proto.CloneOf(m))
	}
	for _, f := range p.GetFrontends() {
		out.Frontends = append(out.Frontends, proto.CloneOf(f))
	}
	for _, r := range p.GetServices() {
		out.Services = append(out.Services, proto.CloneOf(r))
	}
	return out
}

func validDesiredState(st gdsv1.PoolState) error {
	switch st {
	case gdsv1.PoolState_POOL_STATE_UNSPECIFIED, gdsv1.PoolState_POOL_STATE_ACTIVE, gdsv1.PoolState_POOL_STATE_DRAINING:
		return nil
	}
	return errInvalid("desired_state", "must be POOL_STATE_ACTIVE or POOL_STATE_DRAINING")
}

// poolByNameLocked returns the API pool holding a name in a cluster.
func poolByNameLocked(cs *clusterSet, cluster, name string) *poolRec {
	for _, rec := range cs.pools {
		if rec.p.GetLocation().GetCluster() == cluster && rec.p.GetName() == name {
			return rec
		}
	}
	return nil
}

func (ps *poolService) RegisterPool(_ context.Context, req *gdsv1.RegisterPoolRequest) (*gdsv1.RegisterPoolResponse, error) {
	pool := req.GetPool()
	if err := require("clusterset", req.GetClusterset(), "pool.name", pool.GetName(),
		"pool.location.cluster", pool.GetLocation().GetCluster()); err != nil {
		return nil, err
	}
	if err := validDesiredState(pool.GetDesiredState()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cs := s.setLocked(req.GetClusterset())
	cluster, name := pool.GetLocation().GetCluster(), pool.GetName()
	id := pool.GetPoolId()
	old := poolByNameLocked(cs, cluster, name)
	switch {
	case old != nil && id != "" && id != old.p.GetPoolId():
		return nil, errPoolNameTaken(cluster, name, old.p.GetPoolId())
	case old != nil:
		id = old.p.GetPoolId()
	case id != "":
		old = cs.pools[id]
	default:
		id = newPoolID()
	}
	var prevEpoch uint64
	registered := now
	if old != nil {
		if req.GetGeneration() <= old.p.GetGeneration() {
			return nil, errStaleGeneration("pool", req.GetGeneration(), old.p.GetGeneration())
		}
		prevEpoch = old.p.GetLease().GetEpoch()
		registered = old.p.GetRegisteredAt().AsTime()
	}
	ttl := registry.ClampLeaseTTL(req.GetLeaseTtl().AsDuration(), registry.DefaultPoolLeaseTTL)
	p := writerFields(pool)
	p.PoolId = id
	p.Clusterset = req.GetClusterset()
	if p.DesiredState == gdsv1.PoolState_POOL_STATE_UNSPECIFIED {
		p.DesiredState = gdsv1.PoolState_POOL_STATE_ACTIVE
	}
	p.Generation = req.GetGeneration()
	p.RegisteredAt = timestamppb.New(registered)
	p.UpdatedAt = timestamppb.New(now)
	p.Lease = &gdsv1.Lease{
		Epoch:     registry.NextGeneration(prevEpoch, now),
		Ttl:       durationpb.New(ttl),
		RenewedAt: timestamppb.New(now),
	}
	cs.pools[id] = &poolRec{p: p, ttl: ttl}
	s.commitLocked(cs)
	return &gdsv1.RegisterPoolResponse{
		Pool:       cs.views[kindPools][id].(*gdsv1.Pool),
		ServerTime: timestamppb.New(now),
	}, nil
}

func (ps *poolService) UpdatePool(_ context.Context, req *gdsv1.UpdatePoolRequest) (*gdsv1.UpdatePoolResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "pool_id", req.GetPoolId()); err != nil {
		return nil, err
	}
	in := req.GetPool()
	if err := validDesiredState(in.GetDesiredState()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(req.GetClusterset())
	rec := cs.pools[req.GetPoolId()]
	if rec == nil {
		return nil, errNotFound("pool %s not found in clusterset %q", req.GetPoolId(), req.GetClusterset())
	}
	if req.GetGeneration() <= rec.p.GetGeneration() {
		return nil, errStaleGeneration("pool", req.GetGeneration(), rec.p.GetGeneration())
	}
	paths := req.GetUpdateMask().GetPaths()
	if len(paths) == 0 {
		paths = []string{"name", "location", "hardware_class", "models", "worker_count", "ready_worker_count",
			"frontends", "services", "labels", "desired_state", "owner_id"}
	}
	next := proto.CloneOf(rec.p)
	src := writerFields(in)
	for _, path := range paths {
		switch path {
		case "name":
			next.Name = src.Name
		case "location":
			next.Location = src.Location
		case "hardware_class":
			next.HardwareClass = src.HardwareClass
		case "models":
			next.Models = src.Models
		case "worker_count":
			next.WorkerCount = src.WorkerCount
		case "ready_worker_count":
			next.ReadyWorkerCount = src.ReadyWorkerCount
		case "frontends":
			next.Frontends = src.Frontends
		case "services":
			next.Services = src.Services
		case "labels":
			next.Labels = src.Labels
		case "desired_state":
			next.DesiredState = src.DesiredState
		case "owner_id":
			next.OwnerId = src.OwnerId
		default:
			return nil, errInvalid("update_mask", fmt.Sprintf("unknown or server-owned path %q", path))
		}
	}
	if next.GetName() == "" || next.GetLocation().GetCluster() == "" {
		return nil, errInvalid("pool", "name and location.cluster are required")
	}
	if next.DesiredState == gdsv1.PoolState_POOL_STATE_UNSPECIFIED {
		next.DesiredState = gdsv1.PoolState_POOL_STATE_ACTIVE
	}
	if other := poolByNameLocked(cs, next.GetLocation().GetCluster(), next.GetName()); other != nil && other != rec {
		return nil, errPoolNameTaken(next.GetLocation().GetCluster(), next.GetName(), other.p.GetPoolId())
	}
	next.Generation = req.GetGeneration()
	next.UpdatedAt = timestamppb.New(s.now())
	rec.p = next
	s.commitLocked(cs)
	return &gdsv1.UpdatePoolResponse{Pool: cs.views[kindPools][rec.p.GetPoolId()].(*gdsv1.Pool)}, nil
}

func (ps *poolService) HeartbeatPool(_ context.Context, req *gdsv1.HeartbeatPoolRequest) (*gdsv1.HeartbeatPoolResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "pool_id", req.GetPoolId()); err != nil {
		return nil, err
	}
	if err := validDesiredState(req.GetDesiredState()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cs := s.setLocked(req.GetClusterset())
	rec := cs.pools[req.GetPoolId()]
	if rec == nil {
		return nil, errNotFound("pool %s not found in clusterset %q", req.GetPoolId(), req.GetClusterset())
	}
	if cur := rec.p.GetLease().GetEpoch(); req.GetEpoch() != cur {
		return nil, errEpochMismatch("pool", req.GetEpoch(), cur)
	}
	next := proto.CloneOf(rec.p)
	next.Lease.RenewedAt = timestamppb.New(now)
	next.UpdatedAt = timestamppb.New(now)
	if req.WorkerCount != nil {
		next.WorkerCount = req.GetWorkerCount()
	}
	if req.ReadyWorkerCount != nil {
		next.ReadyWorkerCount = req.GetReadyWorkerCount()
	}
	if req.GetDesiredState() != gdsv1.PoolState_POOL_STATE_UNSPECIFIED {
		next.DesiredState = req.GetDesiredState()
	}
	rec.p = next
	rec.started = true
	s.commitLocked(cs)
	return &gdsv1.HeartbeatPoolResponse{
		Lease:      leaseView(next.GetLease(), rec.ttl, now),
		State:      cs.views[kindPools][rec.p.GetPoolId()].(*gdsv1.Pool).GetState(),
		ServerTime: timestamppb.New(now),
	}, nil
}

func (ps *poolService) DeregisterPool(_ context.Context, req *gdsv1.DeregisterPoolRequest) (*gdsv1.DeregisterPoolResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "pool_id", req.GetPoolId()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(req.GetClusterset())
	if _, ok := cs.pools[req.GetPoolId()]; ok {
		delete(cs.pools, req.GetPoolId())
		s.commitLocked(cs)
	}
	return &gdsv1.DeregisterPoolResponse{}, nil
}

// poolView returns p without endpoint lists unless includeEndpoints.
func poolEndpointsView(p *gdsv1.Pool, includeEndpoints bool) *gdsv1.Pool {
	if includeEndpoints {
		return p
	}
	p = proto.CloneOf(p)
	for _, ce := range p.Endpoints {
		ce.Endpoints = nil
	}
	return p
}

func (ps *poolService) GetPool(_ context.Context, req *gdsv1.GetPoolRequest) (*gdsv1.GetPoolResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "pool_id", req.GetPoolId()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.refreshedLocked(req.GetClusterset())
	obj, ok := cs.views[kindPools][req.GetPoolId()]
	if !ok {
		return nil, errNotFound("pool %s not found in clusterset %q", req.GetPoolId(), req.GetClusterset())
	}
	return &gdsv1.GetPoolResponse{Pool: poolEndpointsView(obj.(*gdsv1.Pool), req.GetIncludeEndpoints())}, nil
}

// poolFilter holds the filters shared by ListPools and WatchPools.
type poolFilter interface {
	GetRegion() string
	GetDc() string
	GetCluster() string
	GetHardwareClass() string
	GetModel() string
	GetStates() []gdsv1.PoolState
	GetSelector() *gdsv1.LabelSelector
}

func servesModel(p *gdsv1.Pool, model string) bool {
	for _, m := range p.GetModels() {
		if m.GetModelId() == model || slices.Contains(m.GetAliases(), model) {
			return true
		}
	}
	return false
}

func poolMatcher(f poolFilter) func(*gdsv1.Pool) bool {
	return func(p *gdsv1.Pool) bool {
		loc := p.GetLocation()
		switch {
		case f.GetRegion() != "" && loc.GetRegion() != f.GetRegion(),
			f.GetDc() != "" && loc.GetDc() != f.GetDc(),
			f.GetCluster() != "" && loc.GetCluster() != f.GetCluster(),
			f.GetHardwareClass() != "" && p.GetHardwareClass() != f.GetHardwareClass(),
			f.GetModel() != "" && !servesModel(p, f.GetModel()),
			!selects(f.GetSelector(), p.GetLabels()):
			return false
		}
		if len(f.GetStates()) == 0 {
			return p.GetState() != gdsv1.PoolState_POOL_STATE_GONE
		}
		return slices.Contains(f.GetStates(), p.GetState())
	}
}

func (ps *poolService) ListPools(_ context.Context, req *gdsv1.ListPoolsRequest) (*gdsv1.ListPoolsResponse, error) {
	if err := require("clusterset", req.GetClusterset()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.refreshedLocked(req.GetClusterset())
	resp := &gdsv1.ListPoolsResponse{Revision: s.revision(s.seq)}
	for _, p := range sortedViews(cs, kindPools, poolMatcher(req)) {
		resp.Pools = append(resp.Pools, poolEndpointsView(p, req.GetIncludeEndpoints()))
	}
	return resp, nil
}

func (ps *poolService) WatchPools(req *gdsv1.WatchPoolsRequest, stream grpc.ServerStreamingServer[gdsv1.WatchPoolsResponse]) error {
	match := poolMatcher(req)
	return ps.s.serveWatch(stream.Context(), &watchSpec{
		kind:   kindPools,
		cs:     req.GetClusterset(),
		resume: req.GetResumeRevision(),
		match:  func(m proto.Message) bool { return match(m.(*gdsv1.Pool)) },
		view: func(m proto.Message) proto.Message {
			return poolEndpointsView(m.(*gdsv1.Pool), req.GetIncludeEndpoints())
		},
		send: func(typ gdsv1.EventType, obj proto.Message, rev string) error {
			resp := &gdsv1.WatchPoolsResponse{Type: typ, Revision: rev}
			if obj != nil {
				resp.Pool = obj.(*gdsv1.Pool)
			}
			return stream.Send(resp)
		},
	})
}

func routerMatcher(region, cluster string) func(*gdsv1.Router) bool {
	return func(r *gdsv1.Router) bool {
		return (region == "" || r.GetRegion() == region) && (cluster == "" || r.GetCluster() == cluster)
	}
}

func (ps *poolService) ListRouters(_ context.Context, req *gdsv1.ListRoutersRequest) (*gdsv1.ListRoutersResponse, error) {
	if err := require("clusterset", req.GetClusterset()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.refreshedLocked(req.GetClusterset())
	return &gdsv1.ListRoutersResponse{
		Routers:  sortedViews(cs, kindRouters, routerMatcher(req.GetRegion(), req.GetCluster())),
		Revision: s.revision(s.seq),
	}, nil
}

func (ps *poolService) WatchRouters(req *gdsv1.WatchRoutersRequest, stream grpc.ServerStreamingServer[gdsv1.WatchRoutersResponse]) error {
	match := routerMatcher(req.GetRegion(), req.GetCluster())
	return ps.s.serveWatch(stream.Context(), &watchSpec{
		kind:   kindRouters,
		cs:     req.GetClusterset(),
		resume: req.GetResumeRevision(),
		match:  func(m proto.Message) bool { return match(m.(*gdsv1.Router)) },
		view:   func(m proto.Message) proto.Message { return m },
		send: func(typ gdsv1.EventType, obj proto.Message, rev string) error {
			resp := &gdsv1.WatchRoutersResponse{Type: typ, Revision: rev}
			if obj != nil {
				resp.Router = obj.(*gdsv1.Router)
			}
			return stream.Send(resp)
		},
	})
}

type serverInfoService struct {
	gdsv1.UnimplementedServerInfoServiceServer
	s *Server
}

func (si *serverInfoService) GetServerInfo(context.Context, *gdsv1.GetServerInfoRequest) (*gdsv1.GetServerInfoResponse, error) {
	return si.s.serverInfo(), nil
}
