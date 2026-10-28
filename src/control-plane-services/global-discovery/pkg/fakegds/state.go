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
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// kind is a watchable object kind. Each kind has its own broadcast and
// history per clusterset.
type kind int

const (
	kindClusters kind = iota
	kindImports
	kindPools
	kindRouters
	numKinds
)

func (k kind) String() string {
	return [...]string{"clusters", "service_imports", "pools", "routers"}[k]
}

type clusterRec struct {
	// c is the stored cluster; Lease.State and Lease.ExpiresAt are filled
	// when it is read.
	c   *gdsv1.Cluster
	ttl time.Duration
}

type exportKey struct {
	cluster, namespace, name string
}

type endpointRec struct {
	endpoints  []*gdsv1.Endpoint
	generation uint64
}

type poolRec struct {
	// p holds the writer-owned fields, the pool id, generation, owner,
	// timestamps and the lease (state filled when read).
	p       *gdsv1.Pool
	ttl     time.Duration
	started bool
}

// poolKey is the (cluster, pool name) every pool source merges on.
type poolKey struct {
	cluster, name string
}

// agentKey identifies one pool agent registration.
type agentKey struct {
	cluster, poolID string
	role            gdsv1.FrontendRole
}

type agentRec struct {
	// cluster and pool are the (cluster, pool name) of the registration;
	// poolID is the pool id it resolved to.
	cluster, pool, poolID string
	// a holds the writer-owned fields (role, agent_id, attributes), the
	// generation, the timestamps and the lease (state filled when read).
	a   *gdsv1.PoolAgent
	ttl time.Duration
}

// event is one change of one object, in the order the server made it.
type event struct {
	typ gdsv1.EventType
	key string
	obj proto.Message
	seq uint64
}

// history keeps the latest events of one kind of one clusterset for resume.
type history struct {
	events []event
	// truncated is the highest sequence number dropped from events; every
	// event after it is still present.
	truncated uint64
}

// clusterSet holds the records of one clusterset and its last computed
// views, from which watch events are diffed.
type clusterSet struct {
	name      string
	clusters  map[string]*clusterRec
	exports   map[exportKey]*gdsv1.ServiceExport
	endpoints map[exportKey]*endpointRec
	pools     map[string]*poolRec
	agents    map[agentKey]*agentRec
	// started holds the pools that have been live once (registry
	// PoolStateInput.Started), updated whenever the views are computed.
	started map[poolKey]bool
	views   [numKinds]map[string]proto.Message
	history [numKinds]history
}

func (s *Server) setLocked(name string) *clusterSet {
	cs := s.sets[name]
	if cs == nil {
		cs = &clusterSet{
			name:      name,
			clusters:  map[string]*clusterRec{},
			exports:   map[exportKey]*gdsv1.ServiceExport{},
			endpoints: map[exportKey]*endpointRec{},
			pools:     map[string]*poolRec{},
			agents:    map[agentKey]*agentRec{},
			started:   map[poolKey]bool{},
		}
		for k := range cs.views {
			cs.views[k] = map[string]proto.Message{}
		}
		s.sets[name] = cs
	}
	return cs
}

// commitLocked recomputes the views of cs at the server clock and
// broadcasts every difference to the previous views: UPSERT for new and
// changed objects, DELETE (with the last object) for removed ones.
func (s *Server) commitLocked(cs *clusterSet) {
	next := s.computeLocked(cs)
	for k := kind(0); k < numKinds; k++ {
		prev := cs.views[k]
		for _, key := range slices.Sorted(maps.Keys(next[k])) {
			obj := next[k][key]
			if old, ok := prev[key]; !ok || !proto.Equal(old, obj) {
				s.emitLocked(cs, k, gdsv1.EventType_EVENT_TYPE_UPSERT, key, obj)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(prev)) {
			if _, ok := next[k][key]; !ok {
				s.emitLocked(cs, k, gdsv1.EventType_EVENT_TYPE_DELETE, key, tombstone(prev[key]))
			}
		}
		cs.views[k] = next[k]
	}
}

// tombstone returns the object a DELETE event carries: the last object, with
// state GONE for a pool.
func tombstone(obj proto.Message) proto.Message {
	if p, ok := obj.(*gdsv1.Pool); ok {
		p = proto.CloneOf(p)
		p.State = gdsv1.PoolState_POOL_STATE_GONE
		return p
	}
	return obj
}

func (s *Server) emitLocked(cs *clusterSet, k kind, typ gdsv1.EventType, key string, obj proto.Message) {
	s.seq++
	ev := event{typ: typ, key: key, obj: obj, seq: s.seq}
	h := &cs.history[k]
	h.events = append(h.events, ev)
	if over := len(h.events) - s.historySize; over > 0 {
		h.truncated = h.events[over-1].seq
		h.events = slices.Clone(h.events[over:])
	}
	for sub := range s.subs {
		if sub.cs != cs.name || sub.kind != k {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			select {
			case sub.lag <- struct{}{}:
			default:
			}
		}
	}
}

// refreshedLocked returns cs with its views re-evaluated at the server
// clock, so reads see current lease states.
func (s *Server) refreshedLocked(name string) *clusterSet {
	cs := s.setLocked(name)
	s.commitLocked(cs)
	return cs
}

// sortedViews returns the objects of one view that match, in key order,
// passed through view.
func sortedViews[T proto.Message](cs *clusterSet, k kind, match func(T) bool) []T {
	var out []T
	for _, key := range slices.Sorted(maps.Keys(cs.views[k])) {
		obj := cs.views[k][key].(T)
		if match == nil || match(obj) {
			out = append(out, obj)
		}
	}
	return out
}
