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
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

var clientSeq atomic.Uint64

// Client is a GDS client: one gRPC ClientConn with endpoint failover,
// typed sub-clients, metrics and reconnect hooks. It is safe for concurrent
// use. Close it when done.
type Client struct {
	cfg            Config
	targets        []target
	creds          credentials.TransportCredentials
	resolverScheme string
	conn           *grpc.ClientConn
	metrics        *metrics
	logger         *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	reconnectCh  chan struct{}
	serverInfoCh chan struct{}

	mu               sync.Mutex
	res              *gdsResolver
	fallbacks        []target
	hooks            []func(url string)
	current          *connInfo
	lastURL          string
	everConnected    bool
	pendingReconnect string
	watchRestart     chan struct{}
	closeOnce        sync.Once
	closeErr         error

	clusters       *ClusterClient
	serviceExports *ServiceExportClient
	serviceImports *ServiceImportClient
	pools          *PoolClient
	routers        *RouterClient
	serverInfo     *ServerInfoClient
}

// New validates cfg and returns a Client. It does not wait for a
// connection: the client connects in the background, and calls wait for it
// within their deadline. ctx only bounds construction.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, targets, err := normalize(cfg)
	if err != nil {
		return nil, err
	}
	creds, err := transportCredentials(cfg, targets[0].scheme)
	if err != nil {
		return nil, err
	}
	urls := make([]string, len(targets))
	for i, t := range targets {
		urls[i] = t.url
	}
	m, err := newMetrics(cfg.Registerer, urls)
	if err != nil {
		return nil, fmt.Errorf("gds: register metrics: %w", err)
	}
	c := &Client{
		cfg:            cfg,
		targets:        targets,
		creds:          creds,
		resolverScheme: "gds-client-" + strconv.FormatUint(clientSeq.Add(1), 10),
		metrics:        m,
		logger:         cfg.Logger,
		reconnectCh:    make(chan struct{}, 1),
		serverInfoCh:   make(chan struct{}, 1),
		watchRestart:   make(chan struct{}),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	opts := append(c.baseDialOptions(),
		grpc.WithResolvers(&resolverBuilder{c: c}),
		grpc.WithStatsHandler(&connTracker{c: c}),
		grpc.WithDefaultServiceConfig(serviceConfig(cfg)),
		grpc.WithChainUnaryInterceptor(c.unaryInterceptor),
		grpc.WithChainStreamInterceptor(c.streamInterceptor),
	)
	opts = append(opts, cfg.DialOptions...)
	conn, err := grpc.NewClient(c.resolverScheme+":///gds", opts...)
	if err != nil {
		c.cancel()
		return nil, fmt.Errorf("gds: create client: %w", err)
	}
	c.conn = conn
	c.clusters = &ClusterClient{c: gdsv1.NewClusterServiceClient(conn)}
	c.serviceExports = &ServiceExportClient{c: gdsv1.NewServiceExportServiceClient(conn)}
	c.serviceImports = &ServiceImportClient{c: gdsv1.NewServiceImportServiceClient(conn)}
	pools := gdsv1.NewPoolServiceClient(conn)
	c.pools = &PoolClient{c: pools}
	c.routers = &RouterClient{c: pools}
	c.serverInfo = &ServerInfoClient{c: gdsv1.NewServerInfoServiceClient(conn)}
	c.wg.Add(3)
	go c.hookLoop()
	go c.serverInfoLoop()
	go c.primaryLoop()
	conn.Connect()
	return c, nil
}

// baseDialOptions are shared by the main connection and primary probes.
func (c *Client) baseDialOptions() []grpc.DialOption {
	bc := backoff.Config{
		BaseDelay:  c.cfg.Backoff.Initial,
		Multiplier: c.cfg.Backoff.Multiplier,
		Jitter:     c.cfg.Backoff.Jitter,
		MaxDelay:   c.cfg.Backoff.Max,
	}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(c.creds),
		grpc.WithContextDialer(c.dial),
		grpc.WithUserAgent(c.cfg.UserAgent),
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: bc, MinConnectTimeout: c.cfg.ConnectTimeout}),
	}
}

// serviceConfig returns the default service config: pick_first in address
// order and, unless MaxAttempts is 1, a retry policy on UNAVAILABLE for
// every unary GDS method. Watches are not retried by gRPC; WatchLoop
// reconnects them.
func serviceConfig(cfg Config) string {
	sc := map[string]any{
		"loadBalancingConfig": []any{map[string]any{"pick_first": map[string]any{"shuffleAddressList": false}}},
	}
	if cfg.MaxAttempts > 1 {
		var names []map[string]string
		for _, sd := range serviceDescs {
			for _, m := range sd.Methods {
				names = append(names, map[string]string{"service": sd.ServiceName, "method": m.MethodName})
			}
		}
		seconds := func(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s" }
		sc["methodConfig"] = []any{map[string]any{
			"name": names,
			"retryPolicy": map[string]any{
				"maxAttempts":          cfg.MaxAttempts,
				"initialBackoff":       seconds(cfg.Backoff.Initial),
				"maxBackoff":           seconds(cfg.Backoff.Max),
				"backoffMultiplier":    cfg.Backoff.Multiplier,
				"retryableStatusCodes": []string{"UNAVAILABLE"},
			},
		}}
	}
	b, err := json.Marshal(sc)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Close closes the connection and stops the background work. Running
// watches and caches fail; stop them first.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.closeErr = c.conn.Close()
		c.wg.Wait()
	})
	return c.closeErr
}

// Conn returns the underlying connection, for generated clients the
// sub-clients do not cover.
func (c *Client) Conn() *grpc.ClientConn { return c.conn }

// Clusters returns the ClusterService client.
func (c *Client) Clusters() *ClusterClient { return c.clusters }

// ServiceExports returns the ServiceExportService client.
func (c *Client) ServiceExports() *ServiceExportClient { return c.serviceExports }

// ServiceImports returns the ServiceImportService client.
func (c *Client) ServiceImports() *ServiceImportClient { return c.serviceImports }

// Pools returns the pool calls of the PoolService.
func (c *Client) Pools() *PoolClient { return c.pools }

// Routers returns the router calls of the PoolService.
func (c *Client) Routers() *RouterClient { return c.routers }

// ServerInfo returns the ServerInfoService client.
func (c *Client) ServerInfo() *ServerInfoClient { return c.serverInfo }

// OnReconnect registers fn to run after every connection the client
// establishes after its first one, with the URL it connected to. Hooks run
// one at a time on a background goroutine; reconnects that happen while
// hooks run are coalesced into one call with the latest URL. Writers use it
// to resend their state, readers to resync.
func (c *Client) OnReconnect(fn func(url string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hooks = append(c.hooks, fn)
}

// CurrentURL returns the canonical URL (scheme://host:port) of the
// connection in use, or "" while disconnected.
func (c *Client) CurrentURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return ""
	}
	return c.current.url
}

// URLs returns the canonical configured URLs followed by the fallbacks
// learned from GetServerInfo, in dial order.
func (c *Client) URLs() []string {
	var urls []string
	for _, t := range c.allTargets() {
		urls = append(urls, t.url)
	}
	return urls
}

func (c *Client) allTargets() []target {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(slices.Clone(c.targets), c.fallbacks...)
}

func (c *Client) knownURLsLocked() []string {
	var urls []string
	for _, t := range c.targets {
		urls = append(urls, t.url)
	}
	for _, t := range c.fallbacks {
		urls = append(urls, t.url)
	}
	return urls
}

func (c *Client) setResolver(r *gdsResolver) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.res = r
}

func (c *Client) clearResolver(r *gdsResolver) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.res == r {
		c.res = nil
	}
}

func (c *Client) resolverInstance() *gdsResolver {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.res
}

// watchRestarted returns a channel closed when the client returns to its
// primary URL, so that WatchLoops move their streams too.
func (c *Client) watchRestarted() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watchRestart
}

func (c *Client) restartWatches() {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.watchRestart)
	c.watchRestart = make(chan struct{})
}

func (c *Client) hookLoop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.reconnectCh:
		}
		c.mu.Lock()
		url, hooks := c.pendingReconnect, slices.Clone(c.hooks)
		c.mu.Unlock()
		for _, h := range hooks {
			h(url)
		}
	}
}

func (c *Client) serverInfoLoop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.serverInfoCh:
			c.refreshFallbacks(c.ctx)
		}
	}
}

// refreshFallbacks asks the connected replica for the GDS endpoints of
// every region and keeps the gRPC URLs that are new, valid and of the
// client's scheme as fallbacks after the configured URLs.
func (c *Client) refreshFallbacks(ctx context.Context) {
	info, err := c.serverInfo.Get(ctx)
	if err != nil {
		c.logger.Debug("GetServerInfo failed; keeping the fallback URLs", "error", err)
		return
	}
	seen := map[string]bool{}
	for _, t := range c.targets {
		seen[t.url] = true
	}
	var fallbacks []target
	for _, ep := range info.GetEndpoints() {
		if ep.GetGrpcUrl() == "" {
			continue
		}
		t, err := parseTarget(ep.GetGrpcUrl(), c.cfg.AllowInsecureHTTP)
		if err != nil || t.scheme != c.targets[0].scheme {
			c.logger.Debug("ignoring advertised GDS endpoint", "url", ep.GetGrpcUrl(), "error", err)
			continue
		}
		if seen[t.url] {
			continue
		}
		seen[t.url] = true
		fallbacks = append(fallbacks, t)
	}
	c.mu.Lock()
	changed := !slices.Equal(fallbacks, c.fallbacks)
	if changed {
		c.fallbacks = fallbacks
		for _, t := range fallbacks {
			if c.current == nil || c.current.url != t.url {
				c.metrics.connected.WithLabelValues(t.url).Set(0)
			}
		}
	}
	r := c.res
	c.mu.Unlock()
	if changed && r != nil {
		c.logger.Info("GDS fallback URLs updated", "fallbacks", len(fallbacks))
		r.ResolveNow(resolver.ResolveNowOptions{})
	}
}

func (c *Client) primaryLoop() {
	defer c.wg.Done()
	if c.cfg.PrimaryRetryInterval <= 0 {
		return
	}
	t := time.NewTicker(c.cfg.PrimaryRetryInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			c.returnToPrimary(c.ctx)
		}
	}
}

// returnToPrimary re-resolves the URLs and, when the client is connected to
// another URL than the primary and the primary answers a probe, moves the
// connection and the watches back to the primary. It reports whether it
// moved.
func (c *Client) returnToPrimary(ctx context.Context) bool {
	r := c.resolverInstance()
	if r == nil {
		return false
	}
	r.ResolveNow(resolver.ResolveNowOptions{})
	cur, primary := c.CurrentURL(), c.targets[0]
	if cur == "" || cur == primary.url {
		return false
	}
	addrs, err := c.addresses(ctx, []target{primary})
	if err != nil {
		c.logger.Debug("primary GDS URL does not resolve", "url", primary.url, "error", err)
		return false
	}
	if err := c.probe(ctx, addrs); err != nil {
		c.logger.Debug("primary GDS URL is not serving yet", "url", primary.url, "error", err)
		return false
	}
	c.logger.Info("returning to the primary GDS URL", "from", cur, "to", primary.url)
	r.returnTo(addrs)
	c.restartWatches()
	return true
}

// probe calls GetServerInfo on addrs over a separate connection.
func (c *Client) probe(ctx context.Context, addrs []resolver.Address) error {
	mb := manual.NewBuilderWithScheme(c.resolverScheme + "-probe")
	mb.InitialState(resolver.State{Addresses: addrs})
	conn, err := grpc.NewClient(mb.Scheme()+":///probe", append(c.baseDialOptions(), grpc.WithResolvers(mb))...)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	_, err = gdsv1.NewServerInfoServiceClient(conn).GetServerInfo(ctx, &gdsv1.GetServerInfoRequest{}, grpc.WaitForReady(true))
	return err
}
