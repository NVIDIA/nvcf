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
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// stubKey identifies one registration, as GDS keys them.
type stubKey struct {
	cluster, pool string
	role          gdsv1.FrontendRole
	agentID       string
}

// agentStub implements the pool agent RPCs of the PoolService with the
// registration, epoch, generation and error rules of GDS: registrations are
// keyed by (cluster, pool name, role, agent id), each with its own lease,
// and the agent of a role mirrors the registration whose lease expires
// last. It stands in for pkg/fakegds, which does not serve the agent RPCs
// yet.
type agentStub struct {
	gdsv1.UnimplementedPoolServiceServer

	mu sync.Mutex
	// ttl is the lease the stub grants; 0 grants the requested one.
	ttl   time.Duration
	epoch uint64
	// genFloor is a generation every registration must exceed, as one
	// stored before a restart of the writer.
	genFloor uint64
	regs     map[stubKey]*gdsv1.PoolAgentRegistration
	// pools maps pool ids to their (cluster, pool name).
	pools map[string][2]string
	// failHeartbeats fails that many heartbeats with INTERNAL;
	// staleRegisters answers that many registrations with STALE_GENERATION;
	// omitRegistration leaves registration out of RegisterPoolAgent
	// responses.
	failHeartbeats   int
	staleRegisters   int
	failRegister     error
	omitRegistration bool

	registers   []*gdsv1.RegisterPoolAgentRequest
	heartbeats  []*gdsv1.HeartbeatPoolAgentRequest
	okBeats     map[string]int
	deregisters []*gdsv1.DeregisterPoolAgentRequest
}

const stubPoolID = "pool-blue"

func stubError(code codes.Code, reason string, metadata map[string]string) error {
	st, err := status.New(code, reason).WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: api.ErrorDomain, Metadata: metadata})
	if err != nil {
		panic(err)
	}
	return st.Err()
}

// agentLocked returns the merged agent of a role, or nil without a
// registration.
func (s *agentStub) agentLocked(cluster, pool string, role gdsv1.FrontendRole) *gdsv1.PoolAgent {
	var regs []*gdsv1.PoolAgentRegistration
	for k, r := range s.regs {
		if k.cluster == cluster && k.pool == pool && k.role == role {
			regs = append(regs, proto.CloneOf(r))
		}
	}
	if len(regs) == 0 {
		return nil
	}
	slices.SortFunc(regs, func(a, b *gdsv1.PoolAgentRegistration) int { return strings.Compare(a.GetAgentId(), b.GetAgentId()) })
	freshest := regs[0]
	for _, r := range regs[1:] {
		if r.GetLease().GetExpiresAt().AsTime().After(freshest.GetLease().GetExpiresAt().AsTime()) {
			freshest = r
		}
	}
	return &gdsv1.PoolAgent{
		Role:                    role,
		Source:                  gdsv1.AgentSource_AGENT_SOURCE_API,
		DialMode:                gdsv1.DialMode_DIAL_MODE_OUTBOUND,
		Live:                    true,
		Lease:                   proto.CloneOf(freshest.GetLease()),
		AgentId:                 freshest.GetAgentId(),
		Attributes:              maps.Clone(freshest.GetAttributes()),
		Generation:              freshest.GetGeneration(),
		Registrations:           regs,
		ActiveRegistrationCount: int32(len(regs)),
	}
}

func renew(l *gdsv1.Lease) {
	now := time.Now()
	l.RenewedAt = timestamppb.New(now)
	l.ExpiresAt = timestamppb.New(now.Add(l.GetTtl().AsDuration()))
}

func (s *agentStub) RegisterPoolAgent(_ context.Context, req *gdsv1.RegisterPoolAgentRequest) (*gdsv1.RegisterPoolAgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registers = append(s.registers, proto.CloneOf(req))
	if s.failRegister != nil {
		return nil, s.failRegister
	}
	if req.GetAgent().GetAgentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "agent.agent_id is required")
	}
	key := stubKey{req.GetCluster(), req.GetPoolName(), req.GetAgent().GetRole(), req.GetAgent().GetAgentId()}
	if stored := max(s.genFloor, s.regs[key].GetGeneration()); s.staleRegisters > 0 || req.GetGeneration() <= stored {
		s.staleRegisters = max(s.staleRegisters-1, 0)
		return nil, stubError(codes.FailedPrecondition, api.ReasonStaleGeneration,
			map[string]string{api.MetadataCurrentGeneration: strconv.FormatUint(stored, 10)})
	}
	s.epoch++
	ttl := req.GetLeaseTtl()
	if s.ttl > 0 {
		ttl = durationpb.New(s.ttl)
	}
	reg := &gdsv1.PoolAgentRegistration{
		AgentId:    key.agentID,
		Lease:      &gdsv1.Lease{Epoch: s.epoch, Ttl: ttl, State: gdsv1.LeaseState_LEASE_STATE_ACTIVE},
		Attributes: maps.Clone(req.GetAgent().GetAttributes()),
		Generation: req.GetGeneration(),
	}
	renew(reg.Lease)
	s.regs[key] = reg
	poolID := "pool-" + key.pool
	s.pools[poolID] = [2]string{key.cluster, key.pool}
	resp := &gdsv1.RegisterPoolAgentResponse{
		PoolId:    poolID,
		Agent:     s.agentLocked(key.cluster, key.pool, key.role),
		PoolState: gdsv1.PoolState_POOL_STATE_ACTIVE,
	}
	if !s.omitRegistration {
		resp.Registration = proto.CloneOf(reg)
	}
	return resp, nil
}

func (s *agentStub) HeartbeatPoolAgent(_ context.Context, req *gdsv1.HeartbeatPoolAgentRequest) (*gdsv1.HeartbeatPoolAgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats = append(s.heartbeats, proto.CloneOf(req))
	if s.failHeartbeats > 0 {
		s.failHeartbeats--
		return nil, status.Error(codes.Internal, "injected")
	}
	if req.GetAgentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	pool := s.pools[req.GetPoolId()]
	reg := s.regs[stubKey{pool[0], pool[1], req.GetRole(), req.GetAgentId()}]
	if reg == nil {
		return nil, status.Error(codes.NotFound, "pool agent registration not found")
	}
	if req.GetEpoch() != reg.GetLease().GetEpoch() {
		return nil, stubError(codes.Aborted, api.ReasonLeaseEpochMismatch, nil)
	}
	if len(req.GetAttributes()) > 0 {
		reg.Attributes = maps.Clone(req.GetAttributes())
	}
	renew(reg.Lease)
	s.okBeats[req.GetAgentId()]++
	return &gdsv1.HeartbeatPoolAgentResponse{Lease: proto.CloneOf(reg.GetLease()), PoolState: gdsv1.PoolState_POOL_STATE_ACTIVE}, nil
}

func (s *agentStub) DeregisterPoolAgent(_ context.Context, req *gdsv1.DeregisterPoolAgentRequest) (*gdsv1.DeregisterPoolAgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deregisters = append(s.deregisters, proto.CloneOf(req))
	if req.GetAgentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	pool := s.pools[req.GetPoolId()]
	delete(s.regs, stubKey{pool[0], pool[1], req.GetRole(), req.GetAgentId()})
	return &gdsv1.DeregisterPoolAgentResponse{}, nil
}

// findLocked returns the registration of an agent id in any role.
func (s *agentStub) findLocked(agentID string) *gdsv1.PoolAgentRegistration {
	for k, r := range s.regs {
		if k.agentID == agentID {
			return r
		}
	}
	return nil
}

// supersede mints a new epoch for a registration, as a registration by
// another process under the same agent id would.
func (s *agentStub) supersede(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	s.findLocked(agentID).Lease.Epoch = s.epoch
}

// forget drops a registration, as an expired lease or DeregisterPool would.
func (s *agentStub) forget(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.regs {
		if k.agentID == agentID {
			delete(s.regs, k)
		}
	}
}

func (s *agentStub) set(fn func(s *agentStub)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

type stubCounts struct{ registers, heartbeats, okBeats, deregisters int }

func (s *agentStub) counts() stubCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := 0
	for _, n := range s.okBeats {
		ok += n
	}
	return stubCounts{len(s.registers), len(s.heartbeats), ok, len(s.deregisters)}
}

func (s *agentStub) okBeatsOf(agentID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.okBeats[agentID]
}

func (s *agentStub) register(i int) *gdsv1.RegisterPoolAgentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registers[i]
}

func (s *agentStub) deregister(i int) *gdsv1.DeregisterPoolAgentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deregisters[i]
}

func (s *agentStub) lastHeartbeat() *gdsv1.HeartbeatPoolAgentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeats[len(s.heartbeats)-1]
}

// stored returns the registration of an agent id, or nil.
func (s *agentStub) stored(agentID string) *gdsv1.PoolAgentRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.CloneOf(s.findLocked(agentID))
}

// agent returns the merged agent of a role of pool a/blue, or nil.
func (s *agentStub) agent(role gdsv1.FrontendRole) *gdsv1.PoolAgent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentLocked("a", "blue", role)
}

// startAgentStub serves an agentStub over bufconn and returns a client of
// it.
func startAgentStub(t *testing.T) (*agentStub, *Client) {
	t.Helper()
	stub := &agentStub{regs: map[stubKey]*gdsv1.PoolAgentRegistration{}, pools: map[string][2]string{}, okBeats: map[string]int{}}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	gdsv1.RegisterPoolServiceServer(gs, stub)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	c := newTestClient(t, Config{
		URLs:                 []string{"http://127.0.0.1"},
		Dialer:               func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) },
		PrimaryRetryInterval: -1,
	})
	return stub, c
}

func pylonSpec() AgentSpec {
	return AgentSpec{
		Clusterset: testClusterset,
		Cluster:    "a",
		PoolName:   "blue",
		Role:       rolePylon,
		AgentID:    "pylon-0",
		Attributes: map[string]string{"models": "llama-3"},
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		case <-timeout:
			t.Fatal("updates channel not closed")
		}
	}
}

// TestAgentRegistrationLifecycle registers a Pylon agent, expects
// heartbeats at a third of the granted lease, an attribute change on the
// next heartbeat, and Close to stop the heartbeat and deregister.
func TestAgentRegistrationLifecycle(t *testing.T) {
	stub, c := startAgentStub(t)
	stub.set(func(s *agentStub) { s.ttl = 30 * time.Millisecond })
	spec := pylonSpec()
	spec.LeaseTTL = 45 * time.Second
	reg, err := RegisterAgent(testCtx(t), c, spec)
	require.NoError(t, err)
	spec.Attributes["models"] = "changed"
	assert.Equal(t, "llama-3", stub.stored("pylon-0").GetAttributes()["models"], "the spec's map is copied")

	st := reg.State()
	assert.Equal(t, stubPoolID, st.PoolID)
	assert.Equal(t, uint64(1), st.Epoch)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, st.PoolState)
	assert.Equal(t, 30*time.Millisecond, st.Lease.GetTtl().AsDuration())
	assert.False(t, st.UpdatedAt.IsZero())
	assert.Zero(t, st.Failures)
	assert.NoError(t, st.LastError)
	assert.False(t, st.Stopped)

	first := stub.register(0)
	assert.Equal(t, testClusterset, first.GetClusterset())
	assert.Equal(t, "a", first.GetCluster())
	assert.Equal(t, "blue", first.GetPoolName())
	assert.Empty(t, first.GetPoolId(), "GDS resolves the pool by name")
	assert.Equal(t, rolePylon, first.GetAgent().GetRole())
	assert.Equal(t, "pylon-0", first.GetAgent().GetAgentId())
	assert.Equal(t, map[string]string{"models": "llama-3"}, first.GetAgent().GetAttributes())
	assert.Equal(t, 45*time.Second, first.GetLeaseTtl().AsDuration())
	assert.GreaterOrEqual(t, first.GetGeneration(), uint64(time.Now().Add(-time.Minute).UnixMilli()))

	// Heartbeats renew the lease at a third of the granted 30 ms; plain
	// renewals do not wake subscribers.
	updates := reg.Updates()
	require.Eventually(t, func() bool { return stub.counts().okBeats >= 3 }, 5*time.Second, time.Millisecond)
	hb := stub.lastHeartbeat()
	assert.Equal(t, testClusterset, hb.GetClusterset())
	assert.Equal(t, stubPoolID, hb.GetPoolId())
	assert.Equal(t, rolePylon, hb.GetRole())
	assert.Equal(t, "pylon-0", hb.GetAgentId())
	assert.Equal(t, uint64(1), hb.GetEpoch())
	assert.Empty(t, hb.GetAttributes(), "unchanged attributes are not resent")
	assert.Equal(t, 1, stub.counts().registers)
	select {
	case <-updates:
		t.Fatal("a plain renewal notified the subscribers")
	default:
	}

	// SetAttributes applies on the next heartbeat, once.
	reg.SetAttributes(map[string]string{"models": "llama-3"})
	reg.SetAttributes(map[string]string{"models": "llama-3,mistral"})
	require.Eventually(t, func() bool { return stub.stored("pylon-0").GetAttributes()["models"] == "llama-3,mistral" }, 5*time.Second, time.Millisecond)
	beats := stub.counts().heartbeats
	require.Eventually(t, func() bool { return stub.counts().heartbeats >= beats+2 }, 5*time.Second, time.Millisecond)
	assert.Empty(t, stub.lastHeartbeat().GetAttributes(), "applied attributes are not resent")
	assert.Equal(t, 1, stub.counts().registers)

	// Close stops the heartbeat and removes the registration.
	require.NoError(t, reg.Close(testCtx(t)))
	n := stub.counts()
	require.Equal(t, 1, n.deregisters)
	dereg := stub.deregister(0)
	assert.Equal(t, testClusterset, dereg.GetClusterset())
	assert.Equal(t, stubPoolID, dereg.GetPoolId())
	assert.Equal(t, rolePylon, dereg.GetRole())
	assert.Equal(t, "pylon-0", dereg.GetAgentId())
	assert.Nil(t, stub.stored("pylon-0"))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, n.heartbeats, stub.counts().heartbeats, "no heartbeat after Close")
	st = reg.State()
	assert.True(t, st.Stopped)
	assert.True(t, st.Deregistered)
	assert.NoError(t, st.LastError)
	waitClosed(t, updates)
	_, open := <-reg.Updates()
	assert.False(t, open, "Updates after Close is closed")
	assert.NoError(t, reg.Close(testCtx(t)), "Close is idempotent")
	assert.Equal(t, 1, stub.counts().deregisters)
}

func agentIDs(regs []*gdsv1.PoolAgentRegistration) []string {
	var out []string
	for _, r := range regs {
		out = append(out, r.GetAgentId())
	}
	return out
}

// TestAgentRegistrationsOfOneRole runs two registrations of the PYLON role
// under different agent ids and expects both heartbeats to keep succeeding,
// each with its own epoch, and Close of one to leave the other.
func TestAgentRegistrationsOfOneRole(t *testing.T) {
	stub, c := startAgentStub(t)
	spec0 := pylonSpec()
	spec0.HeartbeatInterval = 5 * time.Millisecond
	spec0.LeaseTTL = 45 * time.Second
	spec1 := pylonSpec()
	spec1.AgentID = "pylon-1"
	spec1.Attributes = map[string]string{"models": "mistral"}
	spec1.HeartbeatInterval = 5 * time.Millisecond
	spec1.LeaseTTL = 2 * time.Minute

	// pylon-1's lease expires last, so the merged agent mirrors it even in
	// the response to pylon-0's registration; pylon-0 reads its own epoch
	// from the response's registration.
	reg1, err := RegisterAgent(testCtx(t), c, spec1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg1.Close(context.Background()) })
	reg0, err := RegisterAgent(testCtx(t), c, spec0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg0.Close(context.Background()) })
	assert.Equal(t, uint64(1), reg1.State().Epoch)
	assert.Equal(t, uint64(2), reg0.State().Epoch)
	assert.Equal(t, 45*time.Second, reg0.State().Lease.GetTtl().AsDuration())

	merged := stub.agent(rolePylon)
	assert.Equal(t, "pylon-1", merged.GetAgentId(), "the agent mirrors the freshest registration")
	assert.Equal(t, int32(2), merged.GetActiveRegistrationCount())
	pool := &gdsv1.Pool{Agents: []*gdsv1.PoolAgent{merged}}
	assert.Equal(t, []string{"pylon-0", "pylon-1"}, agentIDs(Registrations(pool, rolePylon)))
	assert.Equal(t, map[string]string{"models": "llama-3"}, Registrations(pool, rolePylon)[0].GetAttributes())

	require.Eventually(t, func() bool { return stub.okBeatsOf("pylon-0") >= 5 && stub.okBeatsOf("pylon-1") >= 5 },
		5*time.Second, time.Millisecond)
	assert.Equal(t, 2, stub.counts().registers, "registrations of one role do not fence each other")
	for _, reg := range []*AgentRegistration{reg0, reg1} {
		st := reg.State()
		assert.Zero(t, st.Failures)
		assert.NoError(t, st.LastError)
	}
	assert.Equal(t, uint64(2), reg0.State().Epoch)
	assert.Equal(t, uint64(1), reg1.State().Epoch)

	// Close of one process leaves the other's registration.
	require.NoError(t, reg0.Close(testCtx(t)))
	assert.Equal(t, "pylon-0", stub.deregister(0).GetAgentId())
	assert.Nil(t, stub.stored("pylon-0"))
	assert.NotNil(t, stub.stored("pylon-1"))
	beats := stub.okBeatsOf("pylon-1")
	require.Eventually(t, func() bool { return stub.okBeatsOf("pylon-1") >= beats+3 }, 5*time.Second, time.Millisecond)
	assert.Zero(t, reg1.State().Failures)
	assert.Equal(t, 2, stub.counts().registers)
	require.NoError(t, reg1.Close(testCtx(t)))
	assert.Nil(t, stub.agent(rolePylon))
}

// TestAgentRegistrationReregisters expects a new registration after an
// epoch mismatch and after NOT_FOUND, and a re-registration to clear the
// attributes.
func TestAgentRegistrationReregisters(t *testing.T) {
	stub, c := startAgentStub(t)
	spec := pylonSpec()
	spec.Role = roleKVRelay
	spec.HeartbeatInterval = 5 * time.Millisecond
	reg, err := RegisterAgent(testCtx(t), c, spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close(context.Background()) })
	updates := reg.Updates()
	require.Eventually(t, func() bool { return stub.counts().okBeats >= 1 }, 5*time.Second, time.Millisecond)

	// A registration under the same agent id minted epoch 2: the next
	// heartbeat fails with LEASE_EPOCH_MISMATCH and the agent registers
	// again (epoch 3).
	stub.supersede("pylon-0")
	require.Eventually(t, func() bool { return reg.State().Epoch == 3 }, 5*time.Second, time.Millisecond)
	waitUpdate(t, updates)
	assert.Equal(t, 2, stub.counts().registers)
	assert.Greater(t, stub.register(1).GetGeneration(), stub.register(0).GetGeneration(), "every registration has a new generation")
	assert.Equal(t, map[string]string{"models": "llama-3"}, stub.register(1).GetAgent().GetAttributes())
	beats := stub.counts().okBeats
	require.Eventually(t, func() bool { return stub.counts().okBeats > beats }, 5*time.Second, time.Millisecond)
	assert.Equal(t, uint64(3), stub.lastHeartbeat().GetEpoch())

	// GDS lost the registration: NOT_FOUND, then a new registration.
	stub.forget("pylon-0")
	require.Eventually(t, func() bool { return reg.State().Epoch == 4 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, 3, stub.counts().registers)
	assert.Zero(t, reg.State().Failures)

	// A heartbeat cannot clear attributes; an empty map registers again.
	reg.SetAttributes(map[string]string{"models": "mistral"})
	require.Eventually(t, func() bool { return stub.stored("pylon-0").GetAttributes()["models"] == "mistral" }, 5*time.Second, time.Millisecond)
	assert.Equal(t, 3, stub.counts().registers)
	reg.SetAttributes(nil)
	require.Eventually(t, func() bool { return stub.counts().registers == 4 }, 5*time.Second, time.Millisecond)
	assert.Empty(t, stub.register(3).GetAgent().GetAttributes())
	require.Eventually(t, func() bool { return reg.State().Epoch == 5 && stub.stored("pylon-0").GetAttributes() == nil }, 5*time.Second, time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, 4, stub.counts().registers, "the attributes are cleared once")
	require.NoError(t, reg.Close(testCtx(t)))
}

// TestAgentRegistrationEpochFallback expects the agent's lease to serve
// when a response carries no registration.
func TestAgentRegistrationEpochFallback(t *testing.T) {
	stub, c := startAgentStub(t)
	stub.set(func(s *agentStub) { s.omitRegistration = true })
	spec := pylonSpec()
	spec.HeartbeatInterval = 5 * time.Millisecond
	reg, err := RegisterAgent(testCtx(t), c, spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close(context.Background()) })
	assert.Equal(t, stub.stored("pylon-0").GetLease().GetEpoch(), reg.State().Epoch)
	require.Eventually(t, func() bool { return stub.okBeatsOf("pylon-0") >= 3 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, 1, stub.counts().registers)
}

// TestAgentRegistrationHeartbeatFailures expects failed heartbeats to show
// in the state with backoff and no re-registration, and a recovery to clear
// them.
func TestAgentRegistrationHeartbeatFailures(t *testing.T) {
	stub, c := startAgentStub(t)
	spec := pylonSpec()
	spec.HeartbeatInterval = 5 * time.Millisecond
	reg, err := RegisterAgent(testCtx(t), c, spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close(context.Background()) })
	updates := reg.Updates()

	stub.set(func(s *agentStub) { s.failHeartbeats = 1 << 20 })
	require.Eventually(t, func() bool { return reg.State().Failures >= 2 }, 5*time.Second, time.Millisecond)
	waitUpdate(t, updates)
	st := reg.State()
	assert.Equal(t, codes.Internal, status.Code(st.LastError))
	assert.False(t, st.Stopped)
	assert.Equal(t, 1, stub.counts().registers, "other errors are retried, not re-registered")

	stub.set(func(s *agentStub) { s.failHeartbeats = 0 })
	require.Eventually(t, func() bool { st := reg.State(); return st.Failures == 0 && st.LastError == nil }, 5*time.Second, time.Millisecond)
	waitUpdate(t, updates)
	assert.Equal(t, uint64(1), reg.State().Epoch)
}

// TestAgentRegistrationClientClosed expects the heartbeat to stop with the
// client and Close to report the failed deregistration.
func TestAgentRegistrationClientClosed(t *testing.T) {
	stub, c := startAgentStub(t)
	spec := pylonSpec()
	spec.HeartbeatInterval = 5 * time.Millisecond
	reg, err := RegisterAgent(testCtx(t), c, spec)
	require.NoError(t, err)
	updates := reg.Updates()

	require.NoError(t, c.Close())
	waitClosed(t, updates)
	st := reg.State()
	assert.True(t, st.Stopped)
	assert.False(t, st.Deregistered)
	assert.Error(t, reg.Close(testCtx(t)))
	assert.Error(t, reg.State().LastError)
	assert.Zero(t, stub.counts().deregisters)
}

func TestRegisterAgentErrors(t *testing.T) {
	stub, c := startAgentStub(t)
	for name, mutate := range map[string]func(*AgentSpec){
		"no clusterset":  func(s *AgentSpec) { s.Clusterset = "" },
		"no cluster":     func(s *AgentSpec) { s.Cluster = "" },
		"no pool name":   func(s *AgentSpec) { s.PoolName = "" },
		"no agent id":    func(s *AgentSpec) { s.AgentID = "" },
		"long agent id":  func(s *AgentSpec) { s.AgentID = strings.Repeat("a", MaxAgentIDLength+1) },
		"no role":        func(s *AgentSpec) { s.Role = gdsv1.FrontendRole_FRONTEND_ROLE_UNSPECIFIED },
		"unknown role":   func(s *AgentSpec) { s.Role = 42 },
		"negative lease": func(s *AgentSpec) { s.LeaseTTL = -time.Second },
	} {
		spec := pylonSpec()
		mutate(&spec)
		_, err := RegisterAgent(testCtx(t), c, spec)
		assert.Error(t, err, name)
	}
	_, err := RegisterAgent(testCtx(t), c, AgentSpec{})
	assert.EqualError(t, err, "gds: AgentSpec.Clusterset, AgentSpec.Cluster, AgentSpec.PoolName, AgentSpec.AgentID are required")
	_, err = RegisterAgent(testCtx(t), c, AgentSpec{Clusterset: testClusterset, Cluster: "a", PoolName: "blue"})
	assert.EqualError(t, err, "gds: AgentSpec.AgentID is required")
	assert.Zero(t, stub.counts().registers, "invalid specs are not sent")

	// A failed first registration is returned.
	stub.set(func(s *agentStub) {
		s.failRegister = stubError(codes.AlreadyExists, api.ReasonPoolNameTaken, nil)
	})
	_, err = RegisterAgent(testCtx(t), c, pylonSpec())
	assert.Equal(t, api.ReasonPoolNameTaken, Reason(err))
	stub.set(func(s *agentStub) { s.failRegister = nil })

	// STALE_GENERATION is retried once above the stored generation.
	stored := uint64(time.Now().Add(time.Hour).UnixMilli())
	stub.set(func(s *agentStub) { s.genFloor = stored; s.registers = nil })
	spec := pylonSpec()
	spec.LeaseTTL = 0
	reg, err := RegisterAgent(testCtx(t), c, spec)
	require.NoError(t, err)
	require.Equal(t, 2, stub.counts().registers)
	assert.Equal(t, stored+1, stub.register(1).GetGeneration())
	assert.Equal(t, DefaultAgentLeaseTTL, stub.register(1).GetLeaseTtl().AsDuration())
	require.NoError(t, reg.Close(testCtx(t)))

	stub.set(func(s *agentStub) { s.staleRegisters = 2; s.registers = nil })
	_, err = RegisterAgent(testCtx(t), c, pylonSpec())
	_, stale := StaleGeneration(err)
	assert.True(t, stale, "a second STALE_GENERATION is returned")
	assert.Equal(t, 2, stub.counts().registers)
}

func TestAgentHeartbeatInterval(t *testing.T) {
	spec := AgentSpec{LeaseTTL: 60 * time.Second}
	assert.Equal(t, 20*time.Second, agentHeartbeatInterval(spec, nil), "a third of the requested lease")
	granted := &gdsv1.Lease{Ttl: durationpb.New(15 * time.Second)}
	assert.Equal(t, 5*time.Second, agentHeartbeatInterval(spec, granted), "a third of the granted lease")
	spec.HeartbeatInterval = time.Second
	assert.Equal(t, time.Second, agentHeartbeatInterval(spec, granted), "the configured interval")
	assert.Equal(t, time.Millisecond, agentHeartbeatInterval(AgentSpec{}, nil), "never zero")
}
