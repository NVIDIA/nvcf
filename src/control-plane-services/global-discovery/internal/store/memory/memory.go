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

// Package memory is an in-process store.Store. It has the conditional
// semantics of the DynamoDB store and backs development, single-replica
// deployments, the k3d end-to-end tests and unit tests. Nothing is persisted.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// Store is an in-memory store.Store. It is safe for concurrent use.
type Store struct {
	mu          sync.RWMutex
	closed      bool
	clusterSets map[string]*clusterSet
}

type poolName struct {
	cluster string
	name    string
}

// clusterSet holds the records of one clusterset.
type clusterSet struct {
	clusters  map[string]store.ClusterRecord
	exports   map[store.ExportKey]store.ExportRecord
	endpoints map[store.ExportKey]store.EndpointSetRecord
	pools     map[store.PoolKey]store.PoolRecord
	// poolNames maps (cluster, name) to the pool id that owns the name.
	poolNames map[poolName]string
	agents    map[store.PoolAgentKey]store.PoolAgentRecord
}

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{clusterSets: map[string]*clusterSet{}}
}

// read runs fn under the read lock after the context and closed checks.
func (s *Store) read(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return store.ErrUnavailable
	}
	return fn()
}

// write runs fn under the write lock after the context and closed checks.
func (s *Store) write(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.ErrUnavailable
	}
	return fn()
}

// set returns the clusterset, creating it when create is true; otherwise it
// returns nil for an unknown clusterset.
func (s *Store) set(name string, create bool) *clusterSet {
	cs := s.clusterSets[name]
	if cs == nil && create {
		cs = &clusterSet{
			clusters:  map[string]store.ClusterRecord{},
			exports:   map[store.ExportKey]store.ExportRecord{},
			endpoints: map[store.ExportKey]store.EndpointSetRecord{},
			pools:     map[store.PoolKey]store.PoolRecord{},
			poolNames: map[poolName]string{},
			agents:    map[store.PoolAgentKey]store.PoolAgentRecord{},
		}
		s.clusterSets[name] = cs
	}
	return cs
}

// PutCluster implements store.ClusterStore.
func (s *Store) PutCluster(ctx context.Context, rec store.ClusterRecord, now time.Time) (store.ClusterRecord, error) {
	var out store.ClusterRecord
	err := s.write(ctx, func() error {
		cs := s.set(rec.Key.ClusterSet, true)
		stored, exists := cs.clusters[rec.Key.Cluster]
		out = rec.Clone()
		out.Lease = store.Lease{
			Epoch:     store.NextEpoch(stored.Lease.Epoch, now),
			TTL:       rec.Lease.TTL,
			RenewedAt: now,
		}
		out.Generation = stored.Generation + 1
		out.RegisteredAt = now
		if exists {
			out.RegisteredAt = stored.RegisteredAt
		}
		out.UpdatedAt = now
		cs.clusters[rec.Key.Cluster] = out
		out = out.Clone()
		return nil
	})
	return out, err
}

// RenewClusterLease implements store.ClusterStore.
func (s *Store) RenewClusterLease(ctx context.Context, key store.ClusterKey, epoch uint64, now time.Time) (store.ClusterRecord, error) {
	var out store.ClusterRecord
	err := s.write(ctx, func() error {
		cs := s.set(key.ClusterSet, false)
		if cs == nil {
			return store.ErrNotFound
		}
		stored, ok := cs.clusters[key.Cluster]
		if !ok {
			return store.ErrNotFound
		}
		if epoch != stored.Lease.Epoch {
			return store.ErrLeaseEpochMismatch
		}
		stored.Lease.RenewedAt = now
		cs.clusters[key.Cluster] = stored
		out = stored.Clone()
		return nil
	})
	return out, err
}

// GetCluster implements store.ClusterStore.
func (s *Store) GetCluster(ctx context.Context, key store.ClusterKey) (store.ClusterRecord, error) {
	var out store.ClusterRecord
	err := s.read(ctx, func() error {
		cs := s.set(key.ClusterSet, false)
		if cs == nil {
			return store.ErrNotFound
		}
		stored, ok := cs.clusters[key.Cluster]
		if !ok {
			return store.ErrNotFound
		}
		out = stored.Clone()
		return nil
	})
	return out, err
}

// DeleteCluster implements store.ClusterStore.
func (s *Store) DeleteCluster(ctx context.Context, key store.ClusterKey) error {
	return s.write(ctx, func() error {
		cs := s.set(key.ClusterSet, false)
		if cs == nil {
			return nil
		}
		delete(cs.clusters, key.Cluster)
		for k := range cs.exports {
			if k.Cluster == key.Cluster {
				delete(cs.exports, k)
			}
		}
		for k := range cs.endpoints {
			if k.Cluster == key.Cluster {
				delete(cs.endpoints, k)
			}
		}
		for k := range cs.pools {
			if k.Cluster == key.Cluster {
				delete(cs.pools, k)
			}
		}
		for n := range cs.poolNames {
			if n.cluster == key.Cluster {
				delete(cs.poolNames, n)
			}
		}
		for k := range cs.agents {
			if k.Cluster == key.Cluster {
				delete(cs.agents, k)
			}
		}
		return nil
	})
}

// PutExport implements store.ExportStore.
func (s *Store) PutExport(ctx context.Context, rec store.ExportRecord) error {
	return s.write(ctx, func() error {
		cs := s.set(rec.Key.ClusterSet, true)
		if stored, ok := cs.exports[rec.Key]; ok && rec.Generation <= stored.Generation {
			return store.ErrStaleGeneration{Current: stored.Generation}
		}
		cs.exports[rec.Key] = rec.Clone()
		return nil
	})
}

// DeleteExport implements store.ExportStore.
func (s *Store) DeleteExport(ctx context.Context, key store.ExportKey) error {
	return s.write(ctx, func() error {
		if cs := s.set(key.ClusterSet, false); cs != nil {
			delete(cs.exports, key)
			delete(cs.endpoints, key)
		}
		return nil
	})
}

// PutEndpointSet implements store.EndpointStore.
func (s *Store) PutEndpointSet(ctx context.Context, rec store.EndpointSetRecord) error {
	return s.write(ctx, func() error {
		if len(rec.Endpoints) > store.MaxEndpointsPerSet {
			return store.ErrTooLarge
		}
		cs := s.set(rec.Key.ClusterSet, true)
		if stored, ok := cs.endpoints[rec.Key]; ok && rec.Generation <= stored.Generation {
			return store.ErrStaleGeneration{Current: stored.Generation}
		}
		cs.endpoints[rec.Key] = rec.Clone()
		return nil
	})
}

// DeleteEndpointSet implements store.EndpointStore.
func (s *Store) DeleteEndpointSet(ctx context.Context, key store.ExportKey) error {
	return s.write(ctx, func() error {
		if cs := s.set(key.ClusterSet, false); cs != nil {
			delete(cs.endpoints, key)
		}
		return nil
	})
}

// PutPool implements store.PoolStore.
func (s *Store) PutPool(ctx context.Context, rec store.PoolRecord, opts store.PutPoolOptions) (store.PoolRecord, error) {
	var out store.PoolRecord
	err := s.write(ctx, func() error {
		cs := s.set(rec.Key.ClusterSet, true)
		stored, exists := cs.pools[rec.Key]
		if exists && rec.Generation <= stored.Generation {
			return store.ErrStaleGeneration{Current: stored.Generation}
		}
		name := poolName{cluster: rec.Key.Cluster, name: rec.Name}
		if owner, ok := cs.poolNames[name]; ok && owner != rec.Key.PoolID {
			return store.ErrPoolNameTaken
		}

		out = rec.Clone()
		out.Spec.Location.Cluster = rec.Key.Cluster
		switch {
		case opts.MintLease:
			out.Lease = store.Lease{
				Epoch:     store.NextEpoch(stored.Lease.Epoch, opts.Now),
				TTL:       rec.Lease.TTL,
				RenewedAt: opts.Now,
			}
		case exists:
			out.Lease = stored.Lease
		default:
			out.Lease = store.Lease{}
		}
		out.RegisteredAt = opts.Now
		if exists {
			out.RegisteredAt = stored.RegisteredAt
			if stored.Name != rec.Name {
				delete(cs.poolNames, poolName{cluster: rec.Key.Cluster, name: stored.Name})
			}
		}
		out.UpdatedAt = opts.Now

		cs.poolNames[name] = rec.Key.PoolID
		cs.pools[rec.Key] = out
		out = out.Clone()
		return nil
	})
	return out, err
}

// RenewPoolLease implements store.PoolStore.
func (s *Store) RenewPoolLease(ctx context.Context, key store.PoolKey, epoch uint64, now time.Time, patch store.PoolHeartbeatPatch) (store.PoolRecord, error) {
	var out store.PoolRecord
	err := s.write(ctx, func() error {
		cs := s.set(key.ClusterSet, false)
		if cs == nil {
			return store.ErrNotFound
		}
		stored, ok := cs.pools[key]
		if !ok {
			return store.ErrNotFound
		}
		if stored.Lease.Epoch == 0 || epoch != stored.Lease.Epoch {
			return store.ErrLeaseEpochMismatch
		}
		stored.Lease.RenewedAt = now
		if patch.Apply(&stored.Spec) {
			stored.UpdatedAt = now
		}
		cs.pools[key] = stored
		out = stored.Clone()
		return nil
	})
	return out, err
}

// DeletePool implements store.PoolStore.
func (s *Store) DeletePool(ctx context.Context, key store.PoolKey) error {
	return s.write(ctx, func() error {
		cs := s.set(key.ClusterSet, false)
		if cs == nil {
			return nil
		}
		for k := range cs.agents {
			if k.PoolKey() == key {
				delete(cs.agents, k)
			}
		}
		stored, ok := cs.pools[key]
		if !ok {
			return nil
		}
		delete(cs.pools, key)
		name := poolName{cluster: key.Cluster, name: stored.Name}
		if cs.poolNames[name] == key.PoolID {
			delete(cs.poolNames, name)
		}
		return nil
	})
}

// PutPoolAgent implements store.PoolAgentStore.
func (s *Store) PutPoolAgent(ctx context.Context, rec store.PoolAgentRecord, opts store.PutPoolAgentOptions) (store.PoolAgentRecord, error) {
	var out store.PoolAgentRecord
	err := s.write(ctx, func() error {
		cs := s.set(rec.Key.ClusterSet, true)
		stored, exists := cs.agents[rec.Key]
		if exists && rec.Generation <= stored.Generation {
			return store.ErrStaleGeneration{Current: stored.Generation}
		}
		out = rec.Clone()
		switch {
		case opts.MintLease:
			out.Lease = store.Lease{
				Epoch:     store.NextEpoch(stored.Lease.Epoch, opts.Now),
				TTL:       rec.Lease.TTL,
				RenewedAt: opts.Now,
			}
		case exists:
			out.Lease = stored.Lease
		default:
			out.Lease = store.Lease{}
		}
		out.RegisteredAt = opts.Now
		if exists {
			out.RegisteredAt = stored.RegisteredAt
		}
		out.UpdatedAt = opts.Now
		cs.agents[rec.Key] = out
		out = out.Clone()
		return nil
	})
	return out, err
}

// RenewPoolAgentLease implements store.PoolAgentStore.
func (s *Store) RenewPoolAgentLease(ctx context.Context, key store.PoolAgentKey, epoch uint64, now time.Time, patch store.PoolAgentHeartbeatPatch) (store.PoolAgentRecord, error) {
	var out store.PoolAgentRecord
	err := s.write(ctx, func() error {
		cs := s.set(key.ClusterSet, false)
		if cs == nil {
			return store.ErrNotFound
		}
		stored, ok := cs.agents[key]
		if !ok {
			return store.ErrNotFound
		}
		if stored.Lease.Epoch == 0 || epoch != stored.Lease.Epoch {
			return store.ErrLeaseEpochMismatch
		}
		stored.Lease.RenewedAt = now
		if patch.Apply(&stored) {
			stored.UpdatedAt = now
		}
		cs.agents[key] = stored
		out = stored.Clone()
		return nil
	})
	return out, err
}

// DeletePoolAgent implements store.PoolAgentStore.
func (s *Store) DeletePoolAgent(ctx context.Context, key store.PoolAgentKey) error {
	return s.write(ctx, func() error {
		if cs := s.set(key.ClusterSet, false); cs != nil {
			delete(cs.agents, key)
		}
		return nil
	})
}

// ListClusterSets implements store.Store.
func (s *Store) ListClusterSets(ctx context.Context) ([]string, error) {
	var out []string
	err := s.read(ctx, func() error {
		out = make([]string, 0, len(s.clusterSets))
		for name := range s.clusterSets {
			out = append(out, name)
		}
		sort.Strings(out)
		return nil
	})
	return out, err
}

// LoadClusterSet implements store.Store.
func (s *Store) LoadClusterSet(ctx context.Context, clusterSet string) (store.ClusterSetSnapshot, error) {
	snap := store.ClusterSetSnapshot{ClusterSet: clusterSet}
	err := s.read(ctx, func() error {
		cs := s.set(clusterSet, false)
		if cs == nil {
			return nil
		}
		for _, r := range cs.clusters {
			snap.Clusters = append(snap.Clusters, r.Clone())
		}
		for _, r := range cs.exports {
			snap.Exports = append(snap.Exports, r.Clone())
		}
		for _, r := range cs.endpoints {
			snap.EndpointSets = append(snap.EndpointSets, r.Clone())
		}
		for _, r := range cs.pools {
			snap.Pools = append(snap.Pools, r.Clone())
		}
		for _, r := range cs.agents {
			snap.PoolAgents = append(snap.PoolAgents, r.Clone())
		}
		return nil
	})
	if err != nil {
		return store.ClusterSetSnapshot{}, err
	}
	snap.Sort()
	return snap, nil
}

// Ping implements store.Store. It fails only after Close.
func (s *Store) Ping(ctx context.Context) error {
	return s.read(ctx, func() error { return nil })
}

// Close implements store.Store. Later calls return store.ErrUnavailable.
// Close is idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.clusterSets = map[string]*clusterSet{}
	return nil
}
