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
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// Defaults of the router seed URLs.
const (
	// DefaultClusterDomain is the Kubernetes cluster domain of derived
	// Service names.
	DefaultClusterDomain = "cluster.local"
	// DefaultDialScheme applies to routers without a dial scheme.
	DefaultDialScheme = api.DialSchemeHTTPS
)

// RouterCacheConfig configures a RouterCache.
type RouterCacheConfig struct {
	// Filter selects the routers; Clusterset is required.
	Filter *gdsv1.ListRoutersRequest
	// TTL is the interval of the anti-entropy full list. Default 30 s.
	TTL time.Duration
	// StaleAfter marks the cache stale when no list or watch event
	// succeeded for this long. Default 2 x TTL.
	StaleAfter time.Duration
	// IdleTimeout reopens a watch that delivered nothing for this long.
	// Default DefaultWatchIdleTimeout; negative disables it.
	IdleTimeout time.Duration
	// Name labels the watch and cache metrics. Default "routers".
	Name string
	// ClusterDomain of the seed URLs. Default "cluster.local".
	ClusterDomain string
	// DefaultScheme of the seed URLs of routers without a dial scheme.
	// Default https.
	DefaultScheme string
}

// RouterState is an immutable snapshot of a RouterCache. Do not modify the
// routers.
type RouterState struct {
	// Routers sorted by cluster, namespace and name.
	Routers []*gdsv1.Router
	// Revision of the last list, snapshot, delta or bookmark applied.
	Revision string
	// UpdatedAt is the last time a list or watch event confirmed the data.
	UpdatedAt time.Time
	// Synced is set once the first list succeeded.
	Synced bool
	// Stale is set when nothing succeeded for StaleAfter.
	Stale bool
	// ClusterDomain and DefaultScheme shape SeedURL; empty values take
	// DefaultClusterDomain and DefaultDialScheme.
	ClusterDomain string
	DefaultScheme string
}

// SeedURL returns the URL a pylon dials to reach a router from its own
// cluster: the derived Service of the router's import,
// <scheme>://derived-<hash>.<namespace>.svc.<cluster domain>:<grpc_port>.
// It returns "" when the router has no gRPC port.
func (s *RouterState) SeedURL(r *gdsv1.Router) string {
	if r.GetGrpcPort() <= 0 {
		return ""
	}
	scheme := r.GetDialScheme()
	if scheme == "" {
		scheme = s.DefaultScheme
	}
	if scheme == "" {
		scheme = DefaultDialScheme
	}
	domain := s.ClusterDomain
	if domain == "" {
		domain = DefaultClusterDomain
	}
	host := api.DerivedServiceName(r.GetNamespace(), r.GetName()) + "." + r.GetNamespace() + ".svc." + domain
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(int(r.GetGrpcPort())))
}

// SeedURLs returns one seed URL per router cluster, in cluster order: the
// SeedURL of the cluster's first router (by namespace and name) whose
// cluster lease has not expired and that has a gRPC port. Clusters that
// export the same namespace and name share one derived Service, so their
// URLs are equal and listed once.
func (s *RouterState) SeedURLs() []string {
	var urls []string
	seen := map[string]bool{}
	done := map[string]bool{}
	for _, r := range s.Routers {
		if done[r.GetCluster()] || r.GetClusterLeaseState() == gdsv1.LeaseState_LEASE_STATE_EXPIRED {
			continue
		}
		u := s.SeedURL(r)
		if u == "" {
			continue
		}
		done[r.GetCluster()] = true
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// ByCluster returns the routers of a cluster.
func (s *RouterState) ByCluster(cluster string) []*gdsv1.Router {
	var out []*gdsv1.Router
	for _, r := range s.Routers {
		if r.GetCluster() == cluster {
			out = append(out, r)
		}
	}
	return out
}

func routerKey(r *gdsv1.Router) string {
	return r.GetCluster() + "/" + r.GetNamespace() + "/" + r.GetName()
}

// RouterCache keeps the routers matching a filter, with the same list,
// watch, TTL and stale behaviour as PoolCache. Pylons and Pylon Operator
// use it for router seeds.
type RouterCache struct {
	core  *cache[*gdsv1.Router]
	state atomic.Pointer[RouterState]
}

// NewRouterCache returns a RouterCache over c. Start it with Run.
func NewRouterCache(c *Client, cfg RouterCacheConfig) (*RouterCache, error) {
	if cfg.Filter.GetClusterset() == "" {
		return nil, errors.New("gds: RouterCacheConfig.Filter.Clusterset is required")
	}
	if cfg.Name == "" {
		cfg.Name = WatchRouters
	}
	list := proto.CloneOf(cfg.Filter)
	watch := &gdsv1.WatchRoutersRequest{Clusterset: list.GetClusterset(), Region: list.GetRegion(), Cluster: list.GetCluster()}
	rc := &RouterCache{}
	rc.core = newCache(c, cacheOptions[*gdsv1.Router]{
		name:        cfg.Name,
		ttl:         cfg.TTL,
		staleAfter:  cfg.StaleAfter,
		idleTimeout: cfg.IdleTimeout,
		key:         routerKey,
		list: func(ctx context.Context) ([]*gdsv1.Router, string, error) {
			resp, err := c.Routers().List(ctx, list)
			return resp.GetRouters(), resp.GetRevision(), err
		},
		watch: c.Routers().Watcher(watch),
		publish: func(items []*gdsv1.Router, m cacheMeta) {
			rc.state.Store(&RouterState{
				Routers: items, Revision: m.revision, UpdatedAt: m.updatedAt, Synced: m.synced, Stale: m.stale,
				ClusterDomain: cfg.ClusterDomain, DefaultScheme: cfg.DefaultScheme,
			})
		},
	})
	return rc, nil
}

// Run keeps the cache up to date until ctx is done, then returns nil.
func (rc *RouterCache) Run(ctx context.Context) error { return rc.core.run(ctx) }

// Snapshot returns the current state. It never returns nil.
func (rc *RouterCache) Snapshot() *RouterState { return rc.state.Load() }

// Updates returns a channel that receives a value after the state changed;
// see PoolCache.Updates.
func (rc *RouterCache) Updates() <-chan struct{} { return rc.core.updates() }

// WaitSynced waits until the first list succeeded.
func (rc *RouterCache) WaitSynced(ctx context.Context) error { return rc.core.waitSynced(ctx) }

// Refresh lists now, outside the TTL schedule.
func (rc *RouterCache) Refresh(ctx context.Context) error { return rc.core.refresh(ctx) }

// LastError returns the error of the last list, or nil after a success.
func (rc *RouterCache) LastError() error { return rc.core.lastError() }
