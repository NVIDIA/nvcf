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
	"fmt"
	"maps"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
)

func errAgentRole(field string) error {
	return errInvalid(field, "must be FRONTEND_ROLE_OPENAI, FRONTEND_ROLE_KV_RELAY or FRONTEND_ROLE_PYLON")
}

func validAgentID(field, id string) error {
	if len(id) == 0 || len(id) > api.MaxAgentIDBytes {
		return errInvalid(field, fmt.Sprintf("must have 1 to %d bytes", api.MaxAgentIDBytes))
	}
	return nil
}

// validateAgentKey checks the registration key of a heartbeat or a
// deregistration.
func validateAgentKey(clusterset, poolID string, role gdsv1.FrontendRole, agentID string) error {
	if err := require("clusterset", clusterset, "pool_id", poolID); err != nil {
		return err
	}
	if !validAgentRole(role) {
		return errAgentRole("role")
	}
	return validAgentID("agent_id", agentID)
}

func validAgentAttributes(field string, attrs map[string]string) error {
	if len(attrs) > api.MaxAgentAttributes {
		return errInvalid(field, fmt.Sprintf("has %d entries; at most %d are allowed", len(attrs), api.MaxAgentAttributes))
	}
	return nil
}

// validateAgentRequest checks a RegisterPoolAgentRequest.
func validateAgentRequest(req *gdsv1.RegisterPoolAgentRequest) error {
	if err := require("clusterset", req.GetClusterset(), "cluster", req.GetCluster(), "pool_name", req.GetPoolName()); err != nil {
		return err
	}
	agent := req.GetAgent()
	switch {
	case !registry.IsDNSLabel(req.GetPoolName()):
		return errInvalid("pool_name", fmt.Sprintf("%q is not a DNS-1123 label", req.GetPoolName()))
	case req.GetPoolId() != "" && !registry.ValidPoolID(req.GetPoolId()):
		return errInvalid("pool_id", fmt.Sprintf("%q is not a valid pool id", req.GetPoolId()))
	case agent == nil:
		return errInvalid("agent", "required")
	case !validAgentRole(agent.GetRole()):
		return errAgentRole("agent.role")
	}
	if err := validAgentID("agent.agent_id", agent.GetAgentId()); err != nil {
		return err
	}
	return validAgentAttributes("agent.attributes", agent.GetAttributes())
}

// labelPoolLocked reports whether a labelled export names the pool.
func labelPoolLocked(cs *clusterSet, cluster, name string) bool {
	for k, e := range cs.exports {
		if k.cluster == cluster && e.GetSpec().GetLabels()[api.LabelPool] == name && len(exportRoles(e)) > 0 {
			return true
		}
	}
	return false
}

// agentPoolIDLocked resolves the pool id of an agent registration: the id
// of the pool holding (cluster, name), else the requested one, else the
// deterministic one. A requested id that disagrees with an existing pool,
// or belongs to another pool, fails with POOL_NAME_TAKEN.
func agentPoolIDLocked(cs *clusterSet, cluster, name, requested string) (string, error) {
	holder := poolHolderLocked(cs, cluster, name)
	if holder == "" && labelPoolLocked(cs, cluster, name) {
		holder = LabelPoolID(cs.name, cluster, name)
	}
	switch {
	case holder != "" && requested != "" && requested != holder:
		return "", errPoolNameTaken(cluster, name, holder)
	case holder != "":
		return holder, nil
	case requested == "":
		return LabelPoolID(cs.name, cluster, name), nil
	case poolIDInUseLocked(cs, requested):
		return "", errPoolIDTaken(requested)
	}
	return requested, nil
}

// agentLocked returns the registration of a pool id, role and agent id.
func agentLocked(cs *clusterSet, poolID string, role gdsv1.FrontendRole, agentID string) (agentKey, *agentRec) {
	for k, rec := range cs.agents {
		if k.poolID == poolID && k.role == role && k.agentID == agentID {
			return k, rec
		}
	}
	return agentKey{}, nil
}

// poolStateLocked returns the derived state of a pool from the last
// computed view, UNSPECIFIED when the pool is not in it.
func poolStateLocked(cs *clusterSet, poolID string) gdsv1.PoolState {
	if p, ok := cs.views[kindPools][poolID].(*gdsv1.Pool); ok {
		return p.GetState()
	}
	return gdsv1.PoolState_POOL_STATE_UNSPECIFIED
}

func (ps *poolService) RegisterPoolAgent(_ context.Context, req *gdsv1.RegisterPoolAgentRequest) (*gdsv1.RegisterPoolAgentResponse, error) {
	if err := validateAgentRequest(req); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cs := s.setLocked(req.GetClusterset())
	cluster, name, role, agentID := req.GetCluster(), req.GetPoolName(), req.GetAgent().GetRole(), req.GetAgent().GetAgentId()
	id, err := agentPoolIDLocked(cs, cluster, name, req.GetPoolId())
	if err != nil {
		return nil, err
	}
	key := agentKey{cluster: cluster, poolID: id, role: role, agentID: agentID}
	var storedGen, prevEpoch uint64
	registered := now
	if old := cs.agents[key]; old != nil {
		storedGen, prevEpoch = old.r.GetGeneration(), old.r.GetLease().GetEpoch()
		registered = old.r.GetRegisteredAt().AsTime()
	}
	gen := req.GetGeneration()
	switch {
	case gen == 0:
		gen = registry.NextGeneration(storedGen, now)
	case gen <= storedGen:
		return nil, errStaleGeneration("pool agent", gen, storedGen)
	}
	ttl := registry.ClampLeaseTTL(req.GetLeaseTtl().AsDuration(), registry.DefaultPoolLeaseTTL)
	rec := &agentRec{
		cluster: cluster,
		pool:    name,
		poolID:  id,
		role:    role,
		ttl:     ttl,
		r: &gdsv1.PoolAgentRegistration{
			AgentId:      agentID,
			Attributes:   maps.Clone(req.GetAgent().GetAttributes()),
			Generation:   gen,
			RegisteredAt: timestamppb.New(registered),
			UpdatedAt:    timestamppb.New(now),
			Lease: &gdsv1.Lease{
				Epoch:     registry.NextGeneration(prevEpoch, now),
				Ttl:       durationpb.New(ttl),
				RenewedAt: timestamppb.New(now),
			},
		},
	}
	cs.agents[key] = rec
	s.commitLocked(cs)
	resp := &gdsv1.RegisterPoolAgentResponse{
		PoolId:       id,
		Registration: registrationView(rec, now),
		PoolState:    poolStateLocked(cs, id),
		ServerTime:   timestamppb.New(now),
	}
	if p, ok := cs.views[kindPools][id].(*gdsv1.Pool); ok {
		for _, ag := range p.GetAgents() {
			if ag.GetRole() == role {
				resp.Agent = ag
			}
		}
	}
	return resp, nil
}

func (ps *poolService) HeartbeatPoolAgent(_ context.Context, req *gdsv1.HeartbeatPoolAgentRequest) (*gdsv1.HeartbeatPoolAgentResponse, error) {
	if err := validateAgentKey(req.GetClusterset(), req.GetPoolId(), req.GetRole(), req.GetAgentId()); err != nil {
		return nil, err
	}
	if err := validAgentAttributes("attributes", req.GetAttributes()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cs := s.setLocked(req.GetClusterset())
	_, rec := agentLocked(cs, req.GetPoolId(), req.GetRole(), req.GetAgentId())
	if rec == nil {
		return nil, errNotFound("registration %q of agent %s of pool %s not found in clusterset %q",
			req.GetAgentId(), req.GetRole(), req.GetPoolId(), req.GetClusterset())
	}
	if cur := rec.r.GetLease().GetEpoch(); req.GetEpoch() != cur {
		return nil, errEpochMismatch("pool agent", req.GetEpoch(), cur)
	}
	next := proto.CloneOf(rec.r)
	next.Lease.RenewedAt = timestamppb.New(now)
	next.UpdatedAt = timestamppb.New(now)
	if len(req.GetAttributes()) > 0 {
		next.Attributes = maps.Clone(req.GetAttributes())
	}
	rec.r = next
	s.commitLocked(cs)
	return &gdsv1.HeartbeatPoolAgentResponse{
		Lease:      leaseView(next.GetLease(), rec.ttl, now),
		PoolState:  poolStateLocked(cs, rec.poolID),
		ServerTime: timestamppb.New(now),
	}, nil
}

func (ps *poolService) DeregisterPoolAgent(_ context.Context, req *gdsv1.DeregisterPoolAgentRequest) (*gdsv1.DeregisterPoolAgentResponse, error) {
	if err := validateAgentKey(req.GetClusterset(), req.GetPoolId(), req.GetRole(), req.GetAgentId()); err != nil {
		return nil, err
	}
	s := ps.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(req.GetClusterset())
	if key, rec := agentLocked(cs, req.GetPoolId(), req.GetRole(), req.GetAgentId()); rec != nil {
		delete(cs.agents, key)
		s.commitLocked(cs)
	}
	return &gdsv1.DeregisterPoolAgentResponse{}, nil
}
