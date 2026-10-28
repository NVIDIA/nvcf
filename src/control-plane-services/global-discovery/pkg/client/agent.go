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
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// DefaultAgentLeaseTTL is the lease of an AgentSpec without LeaseTTL. GDS
// clamps agent leases to [10 s, 5 m].
const DefaultAgentLeaseTTL = 60 * time.Second

// MaxAgentIDLength is the longest AgentID GDS accepts, in bytes.
const MaxAgentIDLength = 253

// agentHeartbeatJitter shortens each heartbeat interval by up to this
// fraction, so agents registered together spread their heartbeats.
const agentHeartbeatJitter = 0.2

// AgentSpec describes the registration an AgentRegistration makes: one
// process instance of a pool agent, the role a process plays for a pool
// (design 4.6).
type AgentSpec struct {
	// Clusterset of the pool; required.
	Clusterset string
	// Cluster is the cluster id of the pool; required.
	Cluster string
	// PoolName is the pool's name, a DNS label unique within the cluster;
	// required. GDS creates the pool when it does not exist, under the
	// deterministic pool id of (clusterset, cluster, pool name), so the
	// registration and a labelled export of the pool converge on one record.
	PoolName string
	// Role of the agent: OPENAI, KV_RELAY or PYLON; required.
	Role gdsv1.FrontendRole
	// AgentID identifies the registering process instance, for example a
	// Pylon inference_server_id or a relay pod name; required, at most
	// MaxAgentIDLength bytes. GDS keys registrations by (cluster, pool name,
	// role, agent id): every process instance registers under its own id,
	// and registrations of one role with different ids coexist.
	AgentID string
	// Attributes are writer-owned facts of this registration, at most 16
	// entries, for example the model list. SetAttributes replaces them later.
	Attributes map[string]string
	// LeaseTTL is the agent lease. Default DefaultAgentLeaseTTL.
	LeaseTTL time.Duration
	// HeartbeatInterval spaces the heartbeats. Default a third of the lease
	// GDS granted, else of LeaseTTL.
	HeartbeatInterval time.Duration
}

func (s AgentSpec) validate() error {
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"Clusterset", s.Clusterset},
		{"Cluster", s.Cluster},
		{"PoolName", s.PoolName},
		{"AgentID", s.AgentID},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	switch len(missing) {
	case 0:
	case 1:
		return fmt.Errorf("gds: AgentSpec.%s is required", missing[0])
	default:
		return fmt.Errorf("gds: AgentSpec.%s are required", strings.Join(missing, ", AgentSpec."))
	}
	switch s.Role {
	case gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON:
	default:
		return fmt.Errorf("gds: AgentSpec.Role %s is not OPENAI, KV_RELAY or PYLON", s.Role)
	}
	if len(s.AgentID) > MaxAgentIDLength {
		return fmt.Errorf("gds: AgentSpec.AgentID is longer than %d bytes", MaxAgentIDLength)
	}
	if s.LeaseTTL < 0 || s.HeartbeatInterval < 0 {
		return errors.New("gds: AgentSpec.LeaseTTL and HeartbeatInterval must not be negative")
	}
	return nil
}

// agentHeartbeatInterval returns the configured interval, else a third of
// the granted lease, else a third of the requested lease.
func agentHeartbeatInterval(spec AgentSpec, granted *gdsv1.Lease) time.Duration {
	if spec.HeartbeatInterval > 0 {
		return spec.HeartbeatInterval
	}
	ttl := spec.LeaseTTL
	if g := granted.GetTtl().AsDuration(); g > 0 {
		ttl = g
	}
	return max(ttl/3, time.Millisecond)
}

// AgentRegistrationState is the state of an AgentRegistration.
type AgentRegistrationState struct {
	// PoolID is the id of the agent's pool as GDS returned it.
	PoolID string
	// Epoch is the lease epoch of this process's registration. It changes
	// on every registration.
	Epoch uint64
	// PoolState is the derived pool state GDS reported with the last
	// successful registration or heartbeat.
	PoolState gdsv1.PoolState
	// Lease is this registration's lease as of the last successful
	// registration or heartbeat. Do not modify it.
	Lease *gdsv1.Lease
	// UpdatedAt is the time of the last successful registration or
	// heartbeat.
	UpdatedAt time.Time
	// Failures counts the heartbeats, re-registrations included, that failed
	// since the last success.
	Failures int
	// LastError is the error of the last failed heartbeat, re-registration
	// or deregistration; nil after a success.
	LastError error
	// Stopped is set once the heartbeat stopped: after Close, or when the
	// Client was closed. Without a deregistration the lease then expires.
	Stopped bool
	// Deregistered is set when Close deregistered the agent.
	Deregistered bool
}

// changed reports whether s differs from prev in a field subscribers act
// on; a plain lease renewal does not count.
func (s AgentRegistrationState) changed(prev AgentRegistrationState) bool {
	return s.PoolID != prev.PoolID || s.Epoch != prev.Epoch || s.PoolState != prev.PoolState ||
		s.Failures != prev.Failures || (s.LastError == nil) != (prev.LastError == nil) ||
		s.Stopped != prev.Stopped || s.Deregistered != prev.Deregistered
}

// AgentRegistration is one process instance's registration of a pool agent,
// made with RegisterPoolAgent and kept alive by a heartbeat goroutine, the
// Go counterpart of the Rust gds-client AgentRegistration. Pylon Operator
// and other Go processes use it to register as a PYLON, KV_RELAY or OPENAI
// agent of a pool.
//
// The heartbeat calls HeartbeatPoolAgent every HeartbeatInterval (a third
// of the lease, shortened by up to 20 percent of jitter). When GDS answers
// NOT_FOUND (the registration is gone) or ABORTED with LEASE_EPOCH_MISMATCH
// (a registration under the same agent id minted a new epoch), it registers
// again with the current spec. Failed heartbeats are retried after the client's Backoff,
// at most every interval. Registrations use a Generation and retry once
// after STALE_GENERATION.
//
// Make one registration per process instance. GDS merges the registrations
// of a role into one PoolAgent, live while any of them is, so N Pylon pods
// are N AgentRegistrations with N agent ids. Two registrations that share an
// agent id take each other's epoch on every heartbeat.
//
// It is safe for concurrent use. Close it before closing the Client.
type AgentRegistration struct {
	c      *Client
	logger *slog.Logger
	gen    Generation

	// ctx ends with Close or with the Client; it bounds the heartbeat calls.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu   sync.Mutex
	spec AgentSpec
	// attrsVersion counts SetAttributes changes; attrsApplied is the
	// version GDS confirmed.
	attrsVersion uint64
	attrsApplied uint64
	state        AgentRegistrationState
	subs         map[chan struct{}]struct{}
	subsClosed   bool
	closing      bool

	closeOnce sync.Once
	closeErr  error
}

// RegisterAgent registers the agent with RegisterPoolAgent and starts the
// heartbeat. ctx bounds the first registration only; the heartbeat runs
// until Close or until the Client is closed. It fails when the spec is
// invalid or the first registration fails after the client's retries.
func RegisterAgent(ctx context.Context, c *Client, spec AgentSpec) (*AgentRegistration, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	spec.Attributes = maps.Clone(spec.Attributes)
	if spec.LeaseTTL == 0 {
		spec.LeaseTTL = DefaultAgentLeaseTTL
	}
	logger := c.logger.With("clusterset", spec.Clusterset, "cluster", spec.Cluster, "pool", spec.PoolName,
		"role", spec.Role.String(), "agent_id", spec.AgentID)
	a := &AgentRegistration{
		c:      c,
		logger: logger,
		spec:   spec,
		done:   make(chan struct{}),
		subs:   map[chan struct{}]struct{}{},
	}
	if err := a.register(ctx); err != nil {
		return nil, err
	}
	a.ctx, a.cancel = context.WithCancel(c.ctx)
	go a.run()
	return a, nil
}

// State returns the current state.
func (a *AgentRegistration) State() AgentRegistrationState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// Updates returns a channel that receives a value after the state changed:
// a new epoch, pool id or pool state, a failure or a recovery, the stop and
// the deregistration; plain lease renewals do not count. Changes coalesce:
// read State after each receive. The channel is closed when Close returned
// or the Client was closed. Each call subscribes a new channel; call it once
// per consumer.
func (a *AgentRegistration) Updates() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	ch := make(chan struct{}, 1)
	if a.subsClosed {
		close(ch)
		return ch
	}
	a.subs[ch] = struct{}{}
	return ch
}

// SetAttributes replaces the agent's attributes. The next heartbeat sends
// them, and later registrations carry them. Since a heartbeat replaces the
// attributes only with a non-empty map, clearing them (nil or empty)
// registers the agent again on the next heartbeat, which mints a new epoch.
func (a *AgentRegistration) SetAttributes(attrs map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if maps.Equal(attrs, a.spec.Attributes) {
		return
	}
	a.spec.Attributes = maps.Clone(attrs)
	a.attrsVersion++
}

// Close stops the heartbeat and removes this registration with
// DeregisterPoolAgent; ctx bounds that call. The agent stays while its
// export or another registration remains, the pool while an export, the
// pool registration or another agent remains. When the deregistration fails,
// Close returns its error and the lease expires after its TTL. Later calls
// return the first result.
func (a *AgentRegistration) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closing = true
		a.mu.Unlock()
		a.cancel()
		<-a.done

		a.mu.Lock()
		req := &gdsv1.DeregisterPoolAgentRequest{
			Clusterset: a.spec.Clusterset,
			PoolId:     a.state.PoolID,
			Role:       a.spec.Role,
			AgentId:    a.spec.AgentID,
		}
		a.mu.Unlock()
		_, err := a.c.Pools().DeregisterAgent(ctx, req)

		a.mu.Lock()
		defer a.mu.Unlock()
		a.updateLocked(func(s *AgentRegistrationState) {
			s.Stopped = true
			if err != nil {
				s.LastError = err
				return
			}
			s.Deregistered = true
		})
		a.closeSubscribersLocked()
		a.closeErr = err
		if err == nil {
			a.logger.Info("GDS pool agent deregistered", "pool_id", req.GetPoolId())
		}
	})
	return a.closeErr
}

// run heartbeats until ctx ends.
func (a *AgentRegistration) run() {
	defer close(a.done)
	failures := 0
	for {
		interval := a.interval()
		delay := time.Duration(float64(interval) * (1 - agentHeartbeatJitter*rand.Float64()))
		if failures > 0 {
			delay = min(a.c.cfg.Backoff.Delay(failures-1), interval)
		}
		t := time.NewTimer(delay)
		select {
		case <-a.ctx.Done():
			t.Stop()
			a.stop()
			return
		case <-t.C:
		}
		err := a.heartbeat(a.ctx)
		if a.ctx.Err() != nil {
			a.stop()
			return
		}
		if err == nil {
			failures = 0
			continue
		}
		failures++
		a.logger.Warn("GDS pool agent heartbeat failed", "failures", failures, "error", err)
		a.mu.Lock()
		a.updateLocked(func(s *AgentRegistrationState) {
			s.Failures = failures
			s.LastError = err
		})
		a.mu.Unlock()
	}
}

// stop records the end of the heartbeat. Close publishes the
// deregistration and closes the subscribers afterwards; when the Client was
// closed, stop closes them.
func (a *AgentRegistration) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updateLocked(func(s *AgentRegistrationState) { s.Stopped = true })
	if !a.closing {
		a.logger.Info("GDS client closed; pool agent heartbeat stopped", "pool_id", a.state.PoolID)
		a.closeSubscribersLocked()
	}
}

func (a *AgentRegistration) interval() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return agentHeartbeatInterval(a.spec, a.state.Lease)
}

// register calls RegisterPoolAgent with the current spec and a new
// generation, once more after STALE_GENERATION. It mints a new epoch.
func (a *AgentRegistration) register(ctx context.Context) error {
	for retried := false; ; retried = true {
		a.mu.Lock()
		spec, version := a.spec, a.attrsVersion
		attrs := maps.Clone(spec.Attributes)
		a.mu.Unlock()
		resp, err := a.c.Pools().RegisterAgent(ctx, &gdsv1.RegisterPoolAgentRequest{
			Clusterset: spec.Clusterset,
			Cluster:    spec.Cluster,
			PoolName:   spec.PoolName,
			Agent:      &gdsv1.PoolAgent{Role: spec.Role, AgentId: spec.AgentID, Attributes: attrs},
			LeaseTtl:   durationpb.New(spec.LeaseTTL),
			Generation: a.gen.Next(),
		})
		if err != nil {
			if current, ok := StaleGeneration(err); ok && !retried {
				a.gen.Observe(current)
				continue
			}
			return err
		}
		// The registration carries this process's lease; the agent mirrors
		// the freshest registration of the role, which may be another
		// process's.
		own := resp.GetRegistration()
		if own == nil {
			own = &gdsv1.PoolAgentRegistration{Lease: resp.GetAgent().GetLease(), Generation: resp.GetAgent().GetGeneration()}
		}
		a.gen.Observe(own.GetGeneration())
		lease := own.GetLease()
		a.mu.Lock()
		a.attrsApplied = max(a.attrsApplied, version)
		a.updateLocked(func(s *AgentRegistrationState) {
			s.PoolID = resp.GetPoolId()
			s.Epoch = lease.GetEpoch()
			s.Lease = lease
			s.PoolState = resp.GetPoolState()
			s.succeeded()
		})
		a.mu.Unlock()
		a.logger.Info("GDS pool agent registered", "pool_id", resp.GetPoolId(), "epoch", lease.GetEpoch())
		return nil
	}
}

// heartbeat renews the lease, sending changed attributes, and registers
// again when GDS lost the registration or another one superseded it.
func (a *AgentRegistration) heartbeat(ctx context.Context) error {
	a.mu.Lock()
	req := &gdsv1.HeartbeatPoolAgentRequest{
		Clusterset: a.spec.Clusterset,
		PoolId:     a.state.PoolID,
		Role:       a.spec.Role,
		AgentId:    a.spec.AgentID,
		Epoch:      a.state.Epoch,
	}
	version := a.attrsVersion
	dirty := version != a.attrsApplied
	clearAttrs := dirty && len(a.spec.Attributes) == 0
	if dirty {
		req.Attributes = maps.Clone(a.spec.Attributes)
	}
	a.mu.Unlock()
	if clearAttrs {
		a.logger.Info("registering the GDS pool agent again to clear its attributes", "pool_id", req.GetPoolId())
		return a.register(ctx)
	}
	resp, err := a.c.Pools().HeartbeatAgent(ctx, req)
	if err != nil {
		if code := status.Code(err); code == codes.NotFound || code == codes.Aborted {
			a.logger.Info("GDS pool agent lease lost; registering again",
				"pool_id", req.GetPoolId(), "epoch", req.GetEpoch(), "code", code.String(), "reason", Reason(err))
			return a.register(ctx)
		}
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if dirty {
		a.attrsApplied = max(a.attrsApplied, version)
	}
	a.updateLocked(func(s *AgentRegistrationState) {
		if l := resp.GetLease(); l != nil {
			s.Lease = l
			if l.GetEpoch() != 0 {
				s.Epoch = l.GetEpoch()
			}
		}
		s.PoolState = resp.GetPoolState()
		s.succeeded()
	})
	return nil
}

func (s *AgentRegistrationState) succeeded() {
	s.UpdatedAt = time.Now()
	s.Failures = 0
	s.LastError = nil
}

// updateLocked applies fn to the state and wakes the subscribers when a
// field they act on changed.
func (a *AgentRegistration) updateLocked(fn func(*AgentRegistrationState)) {
	prev := a.state
	fn(&a.state)
	if !a.state.changed(prev) {
		return
	}
	for ch := range a.subs {
		signal(ch)
	}
}

func (a *AgentRegistration) closeSubscribersLocked() {
	if a.subsClosed {
		return
	}
	a.subsClosed = true
	for ch := range a.subs {
		close(ch)
	}
	a.subs = nil
}
