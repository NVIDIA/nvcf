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
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/resolver"
)

// minResolveInterval spaces re-resolutions requested by gRPC, which asks
// after every failed connection attempt.
var minResolveInterval = time.Second

// addrSep separates the URL from host:port in a resolver address. The
// prefix makes every address an "unknown family" to pick_first, which then
// keeps the URL order instead of interleaving IPv4 and IPv6, and lets the
// dialer attribute each connection to its URL.
const addrSep = "|"

func encodeAddr(url, hostport string) string { return url + addrSep + hostport }

func decodeAddr(addr string) (url, hostport string) {
	i := strings.LastIndex(addr, addrSep)
	if i < 0 {
		return "", addr
	}
	return addr[:i], addr[i+len(addrSep):]
}

// lookup resolves the host of t with the configured Resolver. An IP literal
// resolves to itself.
func (c *Client) lookup(ctx context.Context, t target) ([]string, error) {
	if ip, err := netip.ParseAddr(t.host); err == nil {
		return []string{ip.String()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	ips, err := c.cfg.Resolver.LookupHost(ctx, t.host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", t.host)
	}
	return ips, nil
}

// addresses resolves targets in order into resolver addresses, skipping
// targets that do not resolve. It fails only when none resolves.
func (c *Client) addresses(ctx context.Context, targets []target) ([]resolver.Address, error) {
	var addrs []resolver.Address
	var errs []error
	for _, t := range targets {
		ips, err := c.lookup(ctx, t)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve %s: %w", t.url, err))
			continue
		}
		for _, ip := range ips {
			addrs = append(addrs, resolver.Address{
				Addr:       encodeAddr(t.url, net.JoinHostPort(ip, t.port)),
				ServerName: t.serverName,
			})
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("gds: no GDS URL resolved: %w", errors.Join(errs...))
	}
	if len(errs) > 0 {
		c.logger.Debug("some GDS URLs did not resolve", "error", errors.Join(errs...))
	}
	return addrs, nil
}

// resolverBuilder builds the per-client resolver. Its scheme is unique per
// Client, and it is passed with grpc.WithResolvers, so clients never share
// resolver state.
type resolverBuilder struct {
	c *Client
}

func (b *resolverBuilder) Scheme() string { return b.c.resolverScheme }

func (b *resolverBuilder) Build(_ resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	ctx, cancel := context.WithCancel(b.c.ctx)
	r := &gdsResolver{c: b.c, cc: cc, ctx: ctx, cancel: cancel, trigger: make(chan struct{}, 1)}
	b.c.setResolver(r)
	b.c.wg.Add(1)
	go r.run()
	return r, nil
}

// gdsResolver resolves every configured URL, then the fallbacks advertised
// by GetServerInfo, in order on start and on every ResolveNow, and pushes
// the addresses to pick_first.
type gdsResolver struct {
	c       *Client
	cc      resolver.ClientConn
	ctx     context.Context
	cancel  context.CancelFunc
	trigger chan struct{}

	// mu serialises resolutions and state pushes.
	mu   sync.Mutex
	last []resolver.Address
}

func (r *gdsResolver) ResolveNow(resolver.ResolveNowOptions) {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *gdsResolver) Close() {
	r.cancel()
	r.c.clearResolver(r)
}

func (r *gdsResolver) run() {
	defer r.c.wg.Done()
	var last time.Time
	for {
		if wait := minResolveInterval - time.Since(last); !last.IsZero() && wait > 0 {
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		last = time.Now()
		r.resolve(false)
		select {
		case <-r.ctx.Done():
			return
		case <-r.trigger:
		}
	}
}

// resolve re-resolves every target and pushes the result when it changed,
// or always with force.
func (r *gdsResolver) resolve(force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	addrs, err := r.c.addresses(r.ctx, r.c.allTargets())
	if r.ctx.Err() != nil {
		return
	}
	if err != nil {
		r.c.logger.Warn("GDS name resolution failed", "error", err)
		r.cc.ReportError(err)
		return
	}
	if !force && slices.EqualFunc(addrs, r.last, func(a, b resolver.Address) bool { return a.Equal(b) }) {
		return
	}
	r.last = addrs
	if err := r.cc.UpdateState(resolver.State{Addresses: addrs}); err != nil {
		r.c.logger.Debug("GDS resolver state rejected", "error", err)
	}
}

// returnTo pushes only the given addresses, which makes pick_first drop its
// current connection and connect to them, then the full list again, which
// keeps the new connection.
func (r *gdsResolver) returnTo(addrs []resolver.Address) {
	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		return
	}
	_ = r.cc.UpdateState(resolver.State{Addresses: addrs})
	r.last = nil
	r.mu.Unlock()
	r.resolve(true)
}
