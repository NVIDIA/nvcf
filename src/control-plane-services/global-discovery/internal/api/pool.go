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
	"fmt"
	"slices"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	gds "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/convert"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

func validatePoolKey(v *validator, clusterSet, poolID string) {
	v.dnsLabel("clusterset", clusterSet)
	switch {
	case poolID == "":
		v.add("pool_id", "is required")
	case !registry.ValidPoolID(poolID):
		v.add("pool_id", "%q is not a valid pool id", poolID)
	}
}

func poolNameTaken(msg string) error {
	return gdsError(codes.AlreadyExists, gds.ReasonPoolNameTaken, msg, nil)
}

// poolIDFor returns the pool id a registration uses: the requested one, the
// id of the pool already holding (cluster, name), or the derived id.
func (s *Server) poolIDFor(clusterSet, cluster, name, requested string) (string, error) {
	if requested != "" {
		if o, ok := s.view.Get(clusterSet, view.KindPool, requested); ok {
			p := o.Full.(*gdsv1.Pool)
			if c := p.GetLocation().GetCluster(); c != cluster && p.GetState() != gdsv1.PoolState_POOL_STATE_GONE {
				return "", poolNameTaken(fmt.Sprintf("pool id %q belongs to a pool of cluster %q", requested, c))
			}
		}
		return requested, nil
	}
	if p, ok := s.view.PoolByName(clusterSet, cluster, name); ok {
		return p.GetPoolId(), nil
	}
	return registry.PoolID(clusterSet, cluster, name), nil
}

// poolMessage returns the derived pool for a response, without endpoint
// lists unless asked.
func (s *Server) poolMessage(clusterSet, poolID string, includeEndpoints bool) *gdsv1.Pool {
	if o, ok := s.view.Get(clusterSet, view.KindPool, poolID); ok {
		return o.Message(includeEndpoints).(*gdsv1.Pool)
	}
	return &gdsv1.Pool{Clusterset: clusterSet, PoolId: poolID}
}

// poolRecord resolves a pool id to its API record, reloading the clusterset
// once when this replica has not seen it.
func (s *Server) poolRecord(ctx context.Context, clusterSet, poolID string) (store.PoolRecord, bool) {
	if rec, ok := s.view.PoolRecord(clusterSet, poolID); ok {
		return rec, true
	}
	s.refresh(ctx, clusterSet)
	return s.view.PoolRecord(clusterSet, poolID)
}

// livePool resolves a pool id to its derived pool, reloading once on a miss.
// GONE tombstones do not count.
func (s *Server) livePool(ctx context.Context, clusterSet, poolID string) (*gdsv1.Pool, bool) {
	get := func() (*gdsv1.Pool, bool) {
		o, ok := s.view.Get(clusterSet, view.KindPool, poolID)
		if !ok || o.Full.(*gdsv1.Pool).GetState() == gdsv1.PoolState_POOL_STATE_GONE {
			return nil, false
		}
		return o.Full.(*gdsv1.Pool), true
	}
	if p, ok := get(); ok {
		return p, true
	}
	s.refresh(ctx, clusterSet)
	return get()
}

// putPool writes a pool, retrying once with a fresh generation when the
// server assigned the generation and the view was behind.
func (s *Server) putPool(ctx context.Context, rec store.PoolRecord, assigned bool, opts store.PutPoolOptions) (store.PoolRecord, error) {
	out, err := s.store.PutPool(ctx, rec, opts)
	if cur, stale := store.StaleGeneration(err); stale && assigned {
		rec.Generation = registry.NextGeneration(cur, opts.Now)
		out, err = s.store.PutPool(ctx, rec, opts)
	}
	if cur, stale := store.StaleGeneration(err); stale {
		return out, staleGeneration("pool", rec.Generation, cur)
	}
	if err != nil {
		return out, storeError(err, "put pool")
	}
	return out, nil
}

// RegisterPool implements gdsv1.PoolServiceServer.
func (s *Server) RegisterPool(ctx context.Context, req *gdsv1.RegisterPoolRequest) (*gdsv1.RegisterPoolResponse, error) {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	p := req.GetPool()
	if p == nil {
		v.add("pool", "is required")
	} else {
		v.poolSpec("pool", p)
		if id := p.GetPoolId(); id != "" && !registry.ValidPoolID(id) {
			v.add("pool.pool_id", "%q is not a valid pool id", id)
		}
	}
	ttl := v.leaseTTL("lease_ttl", req.GetLeaseTtl(), registry.DefaultPoolLeaseTTL)
	if err := v.err(); err != nil {
		return nil, err
	}
	cs, cluster, name := req.GetClusterset(), p.GetLocation().GetCluster(), p.GetName()
	now := s.now()
	spec := convert.PoolSpecFromProto(p)

	for attempt := 0; ; attempt++ {
		id, err := s.poolIDFor(cs, cluster, name, p.GetPoolId())
		if err != nil {
			return nil, err
		}
		var storedGen uint64
		if existing, ok := s.view.PoolRecord(cs, id); ok && existing.Key.Cluster == cluster {
			storedGen = existing.Generation
		}
		gen, assigned := generation(req.GetGeneration(), storedGen, now)
		rec := store.PoolRecord{
			Key:        store.PoolKey{ClusterSet: cs, Cluster: cluster, PoolID: id},
			Name:       name,
			Spec:       spec,
			Lease:      store.Lease{TTL: ttl},
			Generation: gen,
			OwnerID:    p.GetOwnerId(),
		}
		out, err := s.putPool(ctx, rec, assigned, store.PutPoolOptions{Now: now, MintLease: true})
		if err != nil {
			// The view may not know the pool that holds the name yet.
			if p.GetPoolId() == "" && attempt == 0 && grpcstatus.Code(err) == codes.AlreadyExists {
				s.refresh(ctx, cs)
				continue
			}
			return nil, err
		}
		s.view.ApplyPool(out)
		s.log.Info("pool registered", zap.String("clusterset", cs), zap.String("cluster", cluster),
			zap.String("pool_id", id), zap.String("pool", name), zap.Uint64("epoch", out.Lease.Epoch))
		return &gdsv1.RegisterPoolResponse{Pool: s.poolMessage(cs, id, false), ServerTime: convert.Timestamp(now)}, nil
	}
}

// updatePaths are the writer-owned Pool field paths UpdatePool accepts.
var updatePaths = []string{
	"name", "location", "location.region", "location.dc", "location.zones", "hardware_class", "models",
	"worker_count", "ready_worker_count", "frontends", "services", "labels", "desired_state", "owner_id",
}

// applyMask copies the fields named by paths from p into rec. An empty mask
// copies every writer-owned field. The pool's cluster cannot change.
func applyMask(v *validator, rec *store.PoolRecord, p *gdsv1.Pool, paths []string) {
	if len(paths) == 0 {
		paths = slices.DeleteFunc(slices.Clone(updatePaths), func(path string) bool {
			return path == "location.region" || path == "location.dc" || path == "location.zones"
		})
	}
	src := convert.PoolSpecFromProto(p)
	for _, path := range paths {
		switch path {
		case "name":
			rec.Name = p.GetName()
		case "location":
			if c := p.GetLocation().GetCluster(); c != "" && c != rec.Key.Cluster {
				v.add("pool.location.cluster", "cannot change from %q", rec.Key.Cluster)
			}
			rec.Spec.Location = src.Location
			rec.Spec.Location.Cluster = rec.Key.Cluster
		case "location.region":
			rec.Spec.Location.Region = src.Location.Region
		case "location.dc":
			rec.Spec.Location.DC = src.Location.DC
		case "location.zones":
			rec.Spec.Location.Zones = src.Location.Zones
		case "hardware_class":
			rec.Spec.HardwareClass = src.HardwareClass
		case "models":
			rec.Spec.Models = src.Models
		case "worker_count":
			rec.Spec.WorkerCount = src.WorkerCount
		case "ready_worker_count":
			rec.Spec.ReadyWorkerCount = src.ReadyWorkerCount
		case "frontends":
			rec.Spec.Frontends = src.Frontends
		case "services":
			rec.Spec.Services = src.Services
		case "labels":
			rec.Spec.Labels = src.Labels
		case "desired_state":
			rec.Spec.DesiredState = src.DesiredState
		case "owner_id":
			rec.OwnerID = p.GetOwnerId()
		default:
			v.add("update_mask", "unknown or read-only path %q", path)
		}
	}
}

// UpdatePool implements gdsv1.PoolServiceServer.
func (s *Server) UpdatePool(ctx context.Context, req *gdsv1.UpdatePoolRequest) (*gdsv1.UpdatePoolResponse, error) {
	var v validator
	validatePoolKey(&v, req.GetClusterset(), req.GetPoolId())
	if req.GetPool() == nil {
		v.add("pool", "is required")
	}
	if err := v.err(); err != nil {
		return nil, err
	}
	cs, id := req.GetClusterset(), req.GetPoolId()
	cur, ok := s.livePool(ctx, cs, id)
	if !ok {
		return nil, notFound("no pool %q in clusterset %q", id, cs)
	}
	cluster := cur.GetLocation().GetCluster()
	rec, ok := s.view.PoolRecord(cs, id)
	if !ok || rec.Key.Cluster != cluster {
		// A label-derived pool gets its first API record (an overlay
		// without a lease).
		rec = store.PoolRecord{
			Key:  store.PoolKey{ClusterSet: cs, Cluster: cluster, PoolID: id},
			Name: cur.GetName(),
			Spec: store.PoolSpec{Location: store.Location{Cluster: cluster}},
		}
	}
	storedGen := rec.Generation
	applyMask(&v, &rec, req.GetPool(), req.GetUpdateMask().GetPaths())
	check := &gdsv1.Pool{Name: rec.Name, OwnerId: rec.OwnerID}
	convert.PoolSpecToProto(rec.Spec, check)
	v.poolSpec("pool", check)
	if err := v.err(); err != nil {
		return nil, err
	}
	now := s.now()
	var assigned bool
	rec.Generation, assigned = generation(req.GetGeneration(), storedGen, now)
	out, err := s.putPool(ctx, rec, assigned, store.PutPoolOptions{Now: now})
	if err != nil {
		return nil, err
	}
	s.view.ApplyPool(out)
	s.log.Info("pool updated", zap.String("clusterset", cs), zap.String("cluster", cluster), zap.String("pool_id", id),
		zap.Strings("paths", req.GetUpdateMask().GetPaths()), zap.String("desired_state", string(out.Spec.DesiredState)))
	return &gdsv1.UpdatePoolResponse{Pool: s.poolMessage(cs, id, false)}, nil
}

// HeartbeatPool implements gdsv1.PoolServiceServer.
func (s *Server) HeartbeatPool(ctx context.Context, req *gdsv1.HeartbeatPoolRequest) (*gdsv1.HeartbeatPoolResponse, error) {
	var v validator
	validatePoolKey(&v, req.GetClusterset(), req.GetPoolId())
	if req.GetEpoch() == 0 {
		v.add("epoch", "is required")
	}
	if req.WorkerCount != nil && req.GetWorkerCount() < 0 {
		v.add("worker_count", "must not be negative")
	}
	if req.ReadyWorkerCount != nil && req.GetReadyWorkerCount() < 0 {
		v.add("ready_worker_count", "must not be negative")
	}
	v.poolStateDesired("desired_state", req.GetDesiredState())
	if err := v.err(); err != nil {
		return nil, err
	}
	cs, id := req.GetClusterset(), req.GetPoolId()
	rec, ok := s.poolRecord(ctx, cs, id)
	if !ok {
		return nil, notFound("no registered pool %q in clusterset %q", id, cs)
	}
	now := s.now()
	out, err := s.store.RenewPoolLease(ctx, rec.Key, req.GetEpoch(), now, store.PoolHeartbeatPatch{
		WorkerCount:      req.WorkerCount,
		ReadyWorkerCount: req.ReadyWorkerCount,
		DesiredState:     convert.DesiredStateFromProto(req.GetDesiredState()),
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("no registered pool %q in clusterset %q", id, cs)
	case err != nil:
		return nil, storeError(err, "heartbeat pool")
	}
	s.view.ApplyPool(out)
	return &gdsv1.HeartbeatPoolResponse{
		Lease:      convert.LeaseToProto(out.Lease, registry.LeaseStateActive),
		State:      s.poolMessage(cs, id, false).GetState(),
		ServerTime: convert.Timestamp(now),
	}, nil
}

// DeregisterPool implements gdsv1.PoolServiceServer.
func (s *Server) DeregisterPool(ctx context.Context, req *gdsv1.DeregisterPoolRequest) (*gdsv1.DeregisterPoolResponse, error) {
	var v validator
	validatePoolKey(&v, req.GetClusterset(), req.GetPoolId())
	if err := v.err(); err != nil {
		return nil, err
	}
	rec, ok := s.poolRecord(ctx, req.GetClusterset(), req.GetPoolId())
	if !ok {
		return &gdsv1.DeregisterPoolResponse{}, nil
	}
	if err := s.store.DeletePool(ctx, rec.Key); err != nil {
		return nil, storeError(err, "deregister pool")
	}
	s.view.ApplyPoolDelete(rec.Key)
	s.log.Info("pool deregistered", zap.String("clusterset", rec.Key.ClusterSet), zap.String("cluster", rec.Key.Cluster),
		zap.String("pool_id", rec.Key.PoolID), zap.String("pool", rec.Name))
	return &gdsv1.DeregisterPoolResponse{}, nil
}

// GetPool implements gdsv1.PoolServiceServer.
func (s *Server) GetPool(_ context.Context, req *gdsv1.GetPoolRequest) (*gdsv1.GetPoolResponse, error) {
	var v validator
	validatePoolKey(&v, req.GetClusterset(), req.GetPoolId())
	if err := v.err(); err != nil {
		return nil, err
	}
	o, ok := s.view.Get(req.GetClusterset(), view.KindPool, req.GetPoolId())
	if !ok {
		return nil, notFound("no pool %q in clusterset %q", req.GetPoolId(), req.GetClusterset())
	}
	return &gdsv1.GetPoolResponse{Pool: o.Message(req.GetIncludeEndpoints()).(*gdsv1.Pool)}, nil
}

type poolQuery interface {
	GetRegion() string
	GetDc() string
	GetCluster() string
	GetHardwareClass() string
	GetModel() string
	GetStates() []gdsv1.PoolState
	GetSelector() *gdsv1.LabelSelector
	GetIncludeEndpoints() bool
}

func poolFilter(v *validator, q poolQuery) view.PoolFilter {
	v.optionalDNSLabel("cluster", q.GetCluster())
	v.selector("selector", q.GetSelector())
	for i, st := range q.GetStates() {
		if _, ok := gdsv1.PoolState_name[int32(st)]; !ok || st == gdsv1.PoolState_POOL_STATE_UNSPECIFIED {
			v.add(fmt.Sprintf("states[%d]", i), "unknown pool state %d", st)
		}
	}
	return view.PoolFilter{
		Region:        q.GetRegion(),
		DC:            q.GetDc(),
		Cluster:       q.GetCluster(),
		HardwareClass: q.GetHardwareClass(),
		Model:         q.GetModel(),
		States:        q.GetStates(),
		Selector:      q.GetSelector().GetMatchLabels(),
		Endpoints:     q.GetIncludeEndpoints(),
	}
}

// ListPools implements gdsv1.PoolServiceServer.
func (s *Server) ListPools(_ context.Context, req *gdsv1.ListPoolsRequest) (*gdsv1.ListPoolsResponse, error) {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	f := poolFilter(&v, req)
	if err := v.err(); err != nil {
		return nil, err
	}
	msgs, rev := s.view.List(req.GetClusterset(), f)
	resp := &gdsv1.ListPoolsResponse{Revision: rev}
	for _, m := range msgs {
		resp.Pools = append(resp.Pools, m.(*gdsv1.Pool))
	}
	return resp, nil
}

// WatchPools implements gdsv1.PoolServiceServer.
func (s *Server) WatchPools(req *gdsv1.WatchPoolsRequest, stream grpc.ServerStreamingServer[gdsv1.WatchPoolsResponse]) error {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	f := poolFilter(&v, req)
	if err := v.err(); err != nil {
		return err
	}
	return watchError(s.hub.Watch(stream.Context(), req.GetClusterset(), f, req.GetResumeRevision(), func(ev view.WatchEvent) error {
		resp := &gdsv1.WatchPoolsResponse{Type: ev.Type, Revision: ev.Revision}
		if ev.Object != nil {
			resp.Pool = ev.Object.(*gdsv1.Pool)
		}
		return stream.Send(resp)
	}))
}

func routerFilter(v *validator, region, cluster string) view.RouterFilter {
	v.optionalDNSLabel("cluster", cluster)
	return view.RouterFilter{Region: region, Cluster: cluster}
}

// ListRouters implements gdsv1.PoolServiceServer.
func (s *Server) ListRouters(_ context.Context, req *gdsv1.ListRoutersRequest) (*gdsv1.ListRoutersResponse, error) {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	f := routerFilter(&v, req.GetRegion(), req.GetCluster())
	if err := v.err(); err != nil {
		return nil, err
	}
	msgs, rev := s.view.List(req.GetClusterset(), f)
	resp := &gdsv1.ListRoutersResponse{Revision: rev}
	for _, m := range msgs {
		resp.Routers = append(resp.Routers, m.(*gdsv1.Router))
	}
	return resp, nil
}

// WatchRouters implements gdsv1.PoolServiceServer.
func (s *Server) WatchRouters(req *gdsv1.WatchRoutersRequest, stream grpc.ServerStreamingServer[gdsv1.WatchRoutersResponse]) error {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	f := routerFilter(&v, req.GetRegion(), req.GetCluster())
	if err := v.err(); err != nil {
		return err
	}
	return watchError(s.hub.Watch(stream.Context(), req.GetClusterset(), f, req.GetResumeRevision(), func(ev view.WatchEvent) error {
		resp := &gdsv1.WatchRoutersResponse{Type: ev.Type, Revision: ev.Revision}
		if ev.Object != nil {
			resp.Router = ev.Object.(*gdsv1.Router)
		}
		return stream.Send(resp)
	}))
}

// GetServerInfo implements gdsv1.ServerInfoServiceServer.
func (s *Server) GetServerInfo(context.Context, *gdsv1.GetServerInfoRequest) (*gdsv1.GetServerInfoResponse, error) {
	return &gdsv1.GetServerInfoResponse{
		Region:     s.opts.Region,
		Version:    s.opts.Version,
		ViewEpoch:  s.view.Epoch(),
		Endpoints:  s.opts.Endpoints,
		ServerTime: convert.Timestamp(s.now()),
	}, nil
}
