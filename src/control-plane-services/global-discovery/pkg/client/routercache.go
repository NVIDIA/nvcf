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
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// Defaults of the router seed and derived Service URLs.
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
	// ClusterDomain of DerivedServiceURL. Default "cluster.local". Seed
	// URLs do not use it.
	ClusterDomain string
	// DefaultScheme of the seed and derived Service URLs of routers without
	// a dial scheme: http or https. Default https.
	DefaultScheme string
}

// RouterState is an immutable snapshot of a RouterCache. Do not modify the
// routers.
type RouterState struct {
	// Routers sorted by cluster, namespace and name. Each carries the named
	// ports (grpc_port, http_port, quic_port) and, in Ports, every port of
	// the exported Service as GDS received it.
	Routers []*gdsv1.Router
	// Revision of the last list, snapshot, delta or bookmark applied.
	Revision string
	// UpdatedAt is the last time a list or watch event confirmed the data.
	UpdatedAt time.Time
	// Synced is set once the first list succeeded.
	Synced bool
	// Stale is set when nothing succeeded for StaleAfter.
	Stale bool
	// ClusterDomain and DefaultScheme shape the seed and derived Service
	// URLs; empty values take DefaultClusterDomain and DefaultDialScheme.
	ClusterDomain string
	DefaultScheme string
}

// SeedURLs returns the seeds a pylon dials: the RouterSeedURLs of every
// router whose cluster lease has not expired, in router order (cluster,
// namespace, name), each URL once. A router cluster contributes one URL per
// ready replica of its router Service (its stargate-k8s-router or Stargate
// pods), so a seed always reaches the routers of one cluster, even when
// several clusters export the same namespace and name into one shared
// import.
func (s *RouterState) SeedURLs() []string {
	var urls []string
	seen := map[string]bool{}
	for _, r := range s.Routers {
		if r.GetClusterLeaseState() == gdsv1.LeaseState_LEASE_STATE_EXPIRED {
			continue
		}
		for _, u := range s.RouterSeedURLs(r) {
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	return urls
}

// SeedRouters returns the routers that contribute to SeedURLs: those whose
// cluster lease has not expired and that have at least one seed URL, in
// router order.
func (s *RouterState) SeedRouters() []*gdsv1.Router {
	var out []*gdsv1.Router
	for _, r := range s.Routers {
		if r.GetClusterLeaseState() != gdsv1.LeaseState_LEASE_STATE_EXPIRED && len(s.RouterSeedURLs(r)) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// RouterSeedURLs returns the seed URLs of one router, one per ready
// replica, sorted and without duplicates:
// <scheme>://<replica address>:<grpc_port>, with IPv6 addresses in
// brackets. The scheme is the router's dial scheme, else DefaultScheme,
// else DefaultDialScheme. It returns nil when the router has no gRPC port or
// the scheme is neither http nor https. Replicas that are not ready or whose
// address is empty, an IPv6 address with a zone or otherwise contains a
// colon are skipped. The cluster lease is not checked; SeedURLs checks it.
//
// The port is the router Service's gRPC port, so the Service must use the
// same number for the port and its target port, as the llm-request-router
// chart does.
func (s *RouterState) RouterSeedURLs(r *gdsv1.Router) []string {
	scheme, ok := s.scheme(r)
	if !ok || r.GetGrpcPort() <= 0 {
		return nil
	}
	port := strconv.Itoa(int(r.GetGrpcPort()))
	var urls []string
	for _, replica := range r.GetReplicas() {
		if !replica.GetReady() {
			continue
		}
		host, ok := seedHost(replica.GetAddress())
		if !ok {
			continue
		}
		urls = append(urls, scheme+"://"+net.JoinHostPort(host, port))
	}
	slices.Sort(urls)
	return slices.Compact(urls)
}

// RelayURLs returns the seeds a Dynamo PoolRelay dials: the
// RouterRelayURLs of every router whose cluster lease has not expired, in
// router order (cluster, namespace, name), each URL once. Today they equal
// SeedURLs, since the PoolRelay opens its state streams over gRPC to the
// same router listener as Pylon's WatchStargates. Use RelayURLs for the
// relay and SeedURLs for Pylon, so that a relay-specific listener or scheme
// can be introduced without touching Pylon.
func (s *RouterState) RelayURLs() []string {
	var urls []string
	seen := map[string]bool{}
	for _, r := range s.Routers {
		if r.GetClusterLeaseState() == gdsv1.LeaseState_LEASE_STATE_EXPIRED {
			continue
		}
		for _, u := range s.RouterRelayURLs(r) {
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	return urls
}

// RouterRelayURLs returns the relay seeds of one router,
// <scheme>://<replica address>:<grpc_port> per ready replica, sorted and
// without duplicates. Today it returns what RouterSeedURLs returns, with the
// same scheme, port and replica rules; it is a separate function so that
// the relay listener can diverge from Pylon's. The cluster lease is not
// checked; RelayURLs checks it.
func (s *RouterState) RouterRelayURLs(r *gdsv1.Router) []string {
	return s.RouterSeedURLs(r)
}

// RouterQUICAddrs returns the reverse tunnel addresses of one router,
// <replica address>:<quic_port> per ready replica (IPv6 addresses in
// brackets), sorted and without duplicates. It returns nil when the router
// has no QUIC port. Replicas are filtered as in RouterSeedURLs; the dial
// scheme and the cluster lease are not checked.
//
// The QUIC port is informational for Pylon, whose tunnel address comes from
// the registration ack. Consumers of a port GDS does not name read
// Router.Ports, every port of the exported Service with its name and
// protocol.
func (s *RouterState) RouterQUICAddrs(r *gdsv1.Router) []string {
	if r.GetQuicPort() <= 0 {
		return nil
	}
	port := strconv.Itoa(int(r.GetQuicPort()))
	var addrs []string
	for _, replica := range r.GetReplicas() {
		if !replica.GetReady() {
			continue
		}
		if host, ok := seedHost(replica.GetAddress()); ok {
			addrs = append(addrs, net.JoinHostPort(host, port))
		}
	}
	slices.Sort(addrs)
	return slices.Compact(addrs)
}

// DerivedServiceURL returns the URL of the derived Service of the router's
// import in the caller's cluster,
// <scheme>://derived-<hash>.<namespace>.svc.<cluster domain>:<grpc_port>.
// Every cluster that exports the router's namespace and name joins that one
// import, so this VIP spreads over the router replicas of all of them. It is
// not a pylon seed (a pylon could reach another cluster's k8s-router than
// the Stargate it registers with); use it only where the merged VIP is
// wanted. It returns "" when the router has no gRPC port, namespace or name,
// or the scheme is neither http nor https.
func (s *RouterState) DerivedServiceURL(r *gdsv1.Router) string {
	scheme, ok := s.scheme(r)
	if !ok || r.GetGrpcPort() <= 0 || r.GetNamespace() == "" || r.GetName() == "" {
		return ""
	}
	domain := strings.Trim(s.ClusterDomain, ".")
	if domain == "" {
		domain = DefaultClusterDomain
	}
	host := api.DerivedServiceName(r.GetNamespace(), r.GetName()) + "." + r.GetNamespace() + ".svc." + domain
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(int(r.GetGrpcPort())))
}

// scheme returns the router's dial scheme, else the state's default scheme,
// else DefaultDialScheme, lower-cased; ok is false unless it is http or
// https.
func (s *RouterState) scheme(r *gdsv1.Router) (string, bool) {
	scheme := strings.ToLower(strings.TrimSpace(r.GetDialScheme()))
	if scheme == "" {
		scheme = strings.ToLower(strings.TrimSpace(s.DefaultScheme))
	}
	if scheme == "" {
		scheme = DefaultDialScheme
	}
	return scheme, scheme == api.DialSchemeHTTP || scheme == api.DialSchemeHTTPS
}

// seedHost returns the host of a replica address for a URL: an IP address
// or a host name, unbracketed. ok is false for an empty address, an IPv6
// address with a zone, or a non-IP address that contains a colon.
func seedHost(address string) (string, bool) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", false
	}
	if ip, err := netip.ParseAddr(address); err == nil {
		return address, ip.Zone() == ""
	}
	return address, !strings.Contains(address, ":")
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
// use it for router seeds (RouterState.SeedURLs), the Dynamo PoolRelay for
// its relay seeds (RouterState.RelayURLs).
type RouterCache struct {
	core  *cache[*gdsv1.Router]
	state atomic.Pointer[RouterState]
}

// NewRouterCache returns a RouterCache over c. Start it with Run.
func NewRouterCache(c *Client, cfg RouterCacheConfig) (*RouterCache, error) {
	if cfg.Filter.GetClusterset() == "" {
		return nil, errors.New("gds: RouterCacheConfig.Filter.Clusterset is required")
	}
	if ds := strings.ToLower(strings.TrimSpace(cfg.DefaultScheme)); ds != "" && ds != api.DialSchemeHTTP && ds != api.DialSchemeHTTPS {
		return nil, fmt.Errorf("gds: RouterCacheConfig.DefaultScheme %q is neither http nor https", cfg.DefaultScheme)
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
