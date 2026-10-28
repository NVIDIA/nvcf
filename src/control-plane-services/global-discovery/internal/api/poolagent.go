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
	"maps"

	"go.uber.org/zap"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/convert"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

// agentPoolID returns the pool id an agent registration uses, as
// poolIDFor does for pools. Agent registrations hold no name reservation
// in the store, so an explicit pool id is also checked against the pool
// that holds (cluster, name) and against the name of the pool that has the
// id. A pool this replica does not know yet is looked up after a reload,
// so that a pool registered through another replica keeps its id.
func (s *Server) agentPoolID(ctx context.Context, clusterSet, cluster, name, requested string) (string, error) {
	if _, ok := s.view.PoolByName(clusterSet, cluster, name); !ok {
		s.refresh(ctx, clusterSet)
	}
	id, err := s.poolIDFor(clusterSet, cluster, name, requested)
	if err != nil || requested == "" {
		return id, err
	}
	if p, ok := s.view.PoolByName(clusterSet, cluster, name); ok && p.GetPoolId() != requested {
		return "", poolNameTaken(fmt.Sprintf("pool %q of cluster %q has pool id %q, not %q", name, cluster, p.GetPoolId(), requested))
	}
	if o, ok := s.view.Get(clusterSet, view.KindPool, requested); ok {
		if p := o.Full.(*gdsv1.Pool); p.GetState() != gdsv1.PoolState_POOL_STATE_GONE && p.GetName() != name {
			return "", poolNameTaken(fmt.Sprintf("pool id %q belongs to pool %q", requested, p.GetName()))
		}
	}
	return requested, nil
}

// putPoolAgent writes an agent registration, retrying once with a fresh
// generation when the server assigned the generation and the view was
// behind.
func (s *Server) putPoolAgent(ctx context.Context, rec store.PoolAgentRecord, assigned bool, opts store.PutPoolAgentOptions) (store.PoolAgentRecord, error) {
	out, err := s.store.PutPoolAgent(ctx, rec, opts)
	if cur, stale := store.StaleGeneration(err); stale && assigned {
		rec.Generation = registry.NextGeneration(cur, opts.Now)
		out, err = s.store.PutPoolAgent(ctx, rec, opts)
	}
	if cur, stale := store.StaleGeneration(err); stale {
		return out, staleGeneration("pool agent", rec.Generation, cur)
	}
	if err != nil {
		return out, storeError(err, "put pool agent")
	}
	return out, nil
}

// poolAgentRecord resolves a registration, reloading the clusterset once
// when this replica has not seen it.
func (s *Server) poolAgentRecord(ctx context.Context, clusterSet, poolID string, role store.FrontendRole, agentID string) (store.PoolAgentRecord, bool) {
	if rec, ok := s.view.PoolAgentRecord(clusterSet, poolID, role, agentID); ok {
		return rec, true
	}
	s.refresh(ctx, clusterSet)
	return s.view.PoolAgentRecord(clusterSet, poolID, role, agentID)
}

// agentPool returns the derived pool a registration belongs to: the pool
// of its (cluster, pool name), else the pool with its pool id.
func (s *Server) agentPool(rec store.PoolAgentRecord) *gdsv1.Pool {
	if p, ok := s.view.PoolByName(rec.Key.ClusterSet, rec.Key.Cluster, rec.PoolName); ok {
		return p
	}
	return s.poolMessage(rec.Key.ClusterSet, rec.Key.PoolID, false)
}

// mergedAgent returns the merged agent of a registration's role from its
// pool, or one built from the registration alone.
func mergedAgent(p *gdsv1.Pool, rec store.PoolAgentRecord, reg *gdsv1.PoolAgentRegistration) *gdsv1.PoolAgent {
	role := convert.FrontendRoleToProto(rec.Key.Role)
	for _, a := range p.GetAgents() {
		if a.GetRole() == role {
			return a
		}
	}
	return &gdsv1.PoolAgent{
		Role:                    role,
		Source:                  gdsv1.AgentSource_AGENT_SOURCE_API,
		DialMode:                convert.DialModeToProto(convert.DialModeOf(rec.Key.Role)),
		Lease:                   reg.GetLease(),
		AgentId:                 reg.GetAgentId(),
		Attributes:              reg.GetAttributes(),
		Generation:              reg.GetGeneration(),
		RegisteredAt:            reg.GetRegisteredAt(),
		UpdatedAt:               reg.GetUpdatedAt(),
		Live:                    true,
		Registrations:           []*gdsv1.PoolAgentRegistration{reg},
		ActiveRegistrationCount: 1,
	}
}

// RegisterPoolAgent implements gdsv1.PoolServiceServer.
func (s *Server) RegisterPoolAgent(ctx context.Context, req *gdsv1.RegisterPoolAgentRequest) (*gdsv1.RegisterPoolAgentResponse, error) {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	v.dnsLabel("cluster", req.GetCluster())
	v.dnsLabel("pool_name", req.GetPoolName())
	if id := req.GetPoolId(); id != "" && !registry.ValidPoolID(id) {
		v.add("pool_id", "%q is not a valid pool id", id)
	}
	a := req.GetAgent()
	if a == nil {
		v.add("agent", "is required")
	} else {
		v.poolRole("agent.role", a.GetRole())
		v.agentID("agent.agent_id", a.GetAgentId())
		v.agentAttributes("agent.attributes", a.GetAttributes())
	}
	ttl := v.leaseTTL("lease_ttl", req.GetLeaseTtl(), registry.DefaultPoolAgentLeaseTTL)
	if err := v.err(); err != nil {
		return nil, err
	}
	cs, cluster, name := req.GetClusterset(), req.GetCluster(), req.GetPoolName()
	id, err := s.agentPoolID(ctx, cs, cluster, name, req.GetPoolId())
	if err != nil {
		return nil, err
	}
	now := s.now()
	key := store.PoolAgentKey{
		ClusterSet: cs, Cluster: cluster, PoolID: id, Role: convert.FrontendRoleFromProto(a.GetRole()), AgentID: a.GetAgentId(),
	}
	var storedGen uint64
	if existing, ok := s.view.PoolAgentRecord(cs, id, key.Role, key.AgentID); ok && existing.Key.Cluster == cluster {
		storedGen = existing.Generation
	}
	gen, assigned := generation(req.GetGeneration(), storedGen, now)
	rec := store.PoolAgentRecord{
		Key:        key,
		PoolName:   name,
		Attributes: maps.Clone(a.GetAttributes()),
		Lease:      store.Lease{TTL: ttl},
		Generation: gen,
	}
	out, err := s.putPoolAgent(ctx, rec, assigned, store.PutPoolAgentOptions{Now: now, MintLease: true})
	if err != nil {
		return nil, err
	}
	s.view.ApplyPoolAgent(out)
	s.log.Info("pool agent registered", zap.String("clusterset", cs), zap.String("cluster", cluster), zap.String("pool_id", id),
		zap.String("pool", name), zap.String("role", string(key.Role)), zap.String("agent_id", key.AgentID),
		zap.Uint64("epoch", out.Lease.Epoch))
	reg := convert.PoolAgentRegistrationToProto(out, registry.LeaseStateActive)
	p := s.agentPool(out)
	return &gdsv1.RegisterPoolAgentResponse{
		PoolId:       id,
		Agent:        mergedAgent(p, out, reg),
		Registration: reg,
		PoolState:    p.GetState(),
		ServerTime:   convert.Timestamp(now),
	}, nil
}

// validateAgentKey checks the fields that address one registration.
func validateAgentKey(v *validator, clusterSet, poolID string, role gdsv1.FrontendRole, agentID string) {
	validatePoolKey(v, clusterSet, poolID)
	v.poolRole("role", role)
	v.agentID("agent_id", agentID)
}

// HeartbeatPoolAgent implements gdsv1.PoolServiceServer.
func (s *Server) HeartbeatPoolAgent(ctx context.Context, req *gdsv1.HeartbeatPoolAgentRequest) (*gdsv1.HeartbeatPoolAgentResponse, error) {
	var v validator
	validateAgentKey(&v, req.GetClusterset(), req.GetPoolId(), req.GetRole(), req.GetAgentId())
	if req.GetEpoch() == 0 {
		v.add("epoch", "is required")
	}
	v.agentAttributes("attributes", req.GetAttributes())
	if err := v.err(); err != nil {
		return nil, err
	}
	cs, id, role := req.GetClusterset(), req.GetPoolId(), convert.FrontendRoleFromProto(req.GetRole())
	rec, ok := s.poolAgentRecord(ctx, cs, id, role, req.GetAgentId())
	if !ok {
		return nil, notFound("no registration of agent %q in role %s of pool %q in clusterset %q", req.GetAgentId(), role, id, cs)
	}
	now := s.now()
	out, err := s.store.RenewPoolAgentLease(ctx, rec.Key, req.GetEpoch(), now, store.PoolAgentHeartbeatPatch{
		Attributes: maps.Clone(req.GetAttributes()),
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("no registration of agent %q in role %s of pool %q in clusterset %q", req.GetAgentId(), role, id, cs)
	case err != nil:
		return nil, storeError(err, "heartbeat pool agent")
	}
	s.view.ApplyPoolAgent(out)
	return &gdsv1.HeartbeatPoolAgentResponse{
		Lease:      convert.LeaseToProto(out.Lease, registry.LeaseStateActive),
		PoolState:  s.agentPool(out).GetState(),
		ServerTime: convert.Timestamp(now),
	}, nil
}

// DeregisterPoolAgent implements gdsv1.PoolServiceServer.
func (s *Server) DeregisterPoolAgent(ctx context.Context, req *gdsv1.DeregisterPoolAgentRequest) (*gdsv1.DeregisterPoolAgentResponse, error) {
	var v validator
	validateAgentKey(&v, req.GetClusterset(), req.GetPoolId(), req.GetRole(), req.GetAgentId())
	if err := v.err(); err != nil {
		return nil, err
	}
	rec, ok := s.poolAgentRecord(ctx, req.GetClusterset(), req.GetPoolId(), convert.FrontendRoleFromProto(req.GetRole()), req.GetAgentId())
	if !ok {
		return &gdsv1.DeregisterPoolAgentResponse{}, nil
	}
	if err := s.store.DeletePoolAgent(ctx, rec.Key); err != nil {
		return nil, storeError(err, "deregister pool agent")
	}
	s.view.ApplyPoolAgentDelete(rec.Key)
	s.log.Info("pool agent deregistered", zap.String("clusterset", rec.Key.ClusterSet), zap.String("cluster", rec.Key.Cluster),
		zap.String("pool_id", rec.Key.PoolID), zap.String("pool", rec.PoolName), zap.String("role", string(rec.Key.Role)),
		zap.String("agent_id", rec.Key.AgentID))
	return &gdsv1.DeregisterPoolAgentResponse{}, nil
}
