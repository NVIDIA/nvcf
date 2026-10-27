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
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// PoolCacheConfig configures a PoolCache.
type PoolCacheConfig struct {
	// Filter selects the pools; Clusterset is required. The watch uses the
	// same filters.
	Filter *gdsv1.ListPoolsRequest
	// TTL is the interval of the anti-entropy full list. Default 30 s.
	TTL time.Duration
	// StaleAfter marks the cache stale when no list or watch event
	// succeeded for this long. Default 2 x TTL.
	StaleAfter time.Duration
	// IdleTimeout reopens a watch that delivered nothing, not even a
	// bookmark, for this long. Default DefaultWatchIdleTimeout; negative
	// disables it.
	IdleTimeout time.Duration
	// Name labels the watch and cache metrics. Default "pools". Give each
	// cache of a process its own name.
	Name string
}

// PoolState is an immutable snapshot of a PoolCache. Do not modify the
// pools; they are shared with other readers.
type PoolState struct {
	// Pools sorted by pool id.
	Pools []*gdsv1.Pool
	// Revision of the last list, snapshot, delta or bookmark applied.
	Revision string
	// UpdatedAt is the last time a list or watch event confirmed the data;
	// zero before the first success.
	UpdatedAt time.Time
	// Synced is set once the first list succeeded.
	Synced bool
	// Stale is set when nothing succeeded for StaleAfter; the data is the
	// last known state.
	Stale bool
}

// Get returns the pool with the id, or nil.
func (s *PoolState) Get(poolID string) *gdsv1.Pool {
	i, ok := slices.BinarySearchFunc(s.Pools, poolID, func(p *gdsv1.Pool, id string) int {
		return strings.Compare(poolKey(p), id)
	})
	if !ok {
		return nil
	}
	return s.Pools[i]
}

// ByModel returns the pools serving a model, matched by model id or alias.
func (s *PoolState) ByModel(model string) []*gdsv1.Pool {
	return filterPools(s.Pools, func(p *gdsv1.Pool) bool {
		for _, m := range p.GetModels() {
			if m.GetModelId() == model || slices.Contains(m.GetAliases(), model) {
				return true
			}
		}
		return false
	})
}

// ByCluster returns the pools located in a cluster.
func (s *PoolState) ByCluster(cluster string) []*gdsv1.Pool {
	return filterPools(s.Pools, func(p *gdsv1.Pool) bool { return p.GetLocation().GetCluster() == cluster })
}

// Active returns the pools in state ACTIVE, the ones routers may choose.
func (s *PoolState) Active() []*gdsv1.Pool {
	return filterPools(s.Pools, func(p *gdsv1.Pool) bool { return p.GetState() == gdsv1.PoolState_POOL_STATE_ACTIVE })
}

func filterPools(pools []*gdsv1.Pool, keep func(*gdsv1.Pool) bool) []*gdsv1.Pool {
	var out []*gdsv1.Pool
	for _, p := range pools {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

// poolKey is the pool id, or cluster/name for a pool without one.
func poolKey(p *gdsv1.Pool) string {
	if id := p.GetPoolId(); id != "" {
		return id
	}
	return p.GetLocation().GetCluster() + "/" + p.GetName()
}

// PoolCache keeps the pools matching a filter: an initial ListPools, then
// WatchPools deltas, a full ListPools every TTL that replaces the state, and
// a Stale flag after StaleAfter without success. This is the Global
// Router's view of every pool, refreshed as pools join, drain and leave.
type PoolCache struct {
	core  *cache[*gdsv1.Pool]
	state atomic.Pointer[PoolState]
}

// NewPoolCache returns a PoolCache over c. Start it with Run.
func NewPoolCache(c *Client, cfg PoolCacheConfig) (*PoolCache, error) {
	if cfg.Filter.GetClusterset() == "" {
		return nil, errors.New("gds: PoolCacheConfig.Filter.Clusterset is required")
	}
	if cfg.Name == "" {
		cfg.Name = WatchPools
	}
	list := proto.CloneOf(cfg.Filter)
	watch := &gdsv1.WatchPoolsRequest{
		Clusterset:       list.GetClusterset(),
		Region:           list.GetRegion(),
		Dc:               list.GetDc(),
		Cluster:          list.GetCluster(),
		HardwareClass:    list.GetHardwareClass(),
		Model:            list.GetModel(),
		States:           slices.Clone(list.GetStates()),
		Selector:         proto.CloneOf(list.GetSelector()),
		IncludeEndpoints: list.GetIncludeEndpoints(),
	}
	pc := &PoolCache{}
	pc.core = newCache(c, cacheOptions[*gdsv1.Pool]{
		name:        cfg.Name,
		ttl:         cfg.TTL,
		staleAfter:  cfg.StaleAfter,
		idleTimeout: cfg.IdleTimeout,
		key:         poolKey,
		list: func(ctx context.Context) ([]*gdsv1.Pool, string, error) {
			resp, err := c.Pools().List(ctx, list)
			return resp.GetPools(), resp.GetRevision(), err
		},
		watch: c.Pools().Watcher(watch),
		publish: func(items []*gdsv1.Pool, m cacheMeta) {
			pc.state.Store(&PoolState{Pools: items, Revision: m.revision, UpdatedAt: m.updatedAt, Synced: m.synced, Stale: m.stale})
		},
	})
	return pc, nil
}

// Run keeps the cache up to date until ctx is done, then returns nil. With
// controller-runtime, add it as manager.RunnableFunc(cache.Run).
func (pc *PoolCache) Run(ctx context.Context) error { return pc.core.run(ctx) }

// Snapshot returns the current state. It never returns nil.
func (pc *PoolCache) Snapshot() *PoolState { return pc.state.Load() }

// Updates returns a channel that receives a value after the state changed
// (pools, Synced or Stale; not bookmarks). Changes coalesce: read Snapshot
// after each receive. The channel is closed when Run returns. Each call
// subscribes a new channel; call it once per consumer.
func (pc *PoolCache) Updates() <-chan struct{} { return pc.core.updates() }

// WaitSynced waits until the first list succeeded.
func (pc *PoolCache) WaitSynced(ctx context.Context) error { return pc.core.waitSynced(ctx) }

// Refresh lists now, outside the TTL schedule.
func (pc *PoolCache) Refresh(ctx context.Context) error { return pc.core.refresh(ctx) }

// LastError returns the error of the last list, or nil after a success.
func (pc *PoolCache) LastError() error { return pc.core.lastError() }
