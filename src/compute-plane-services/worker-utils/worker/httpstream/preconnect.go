/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package httpstream

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/tracing"
)

type callbackPostKey struct{}
type callbackRouteKey struct{}

type callbackRoute struct {
	key     string
	scheme  string
	address string
}

type preconnectConfig struct {
	capacity      int
	setupTimeout  time.Duration
	idleTimeout   time.Duration
	retryInterval time.Duration
}

func defaultPreconnectConfig() preconnectConfig {
	return preconnectConfig{capacity: 16, setupTimeout: 2 * time.Second, idleTimeout: 30 * time.Second, retryInterval: time.Second}
}

// callbackTransport leaves HTTP, proxy authentication and connection reuse to
// net/http. Only its first-hop dial hooks may claim a prepared connection.
type callbackTransport struct {
	base *http.Transport
	rt   http.RoundTripper
	pool *preconnectPool
}

func newCallbackTransport(base *http.Transport, config preconnectConfig) *callbackTransport {
	rawDial := base.DialContext
	if rawDial == nil {
		// Match net/http's zero-value dialer for this transport. Only
		// speculative setup receives the separate preparation timeout.
		dialer := &net.Dialer{}
		rawDial = dialer.DialContext
	}
	tlsDial := base.DialTLSContext
	if tlsDial == nil {
		tlsDial = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := rawDial(ctx, network, address)
			if err != nil {
				return nil, err
			}
			config := &tls.Config{}
			if base.TLSClientConfig != nil {
				config = base.TLSClientConfig.Clone()
			}
			if config.ServerName == "" {
				config.ServerName, _, _ = net.SplitHostPort(address)
			}
			// The callback transport deliberately uses HTTP/1, including proxies.
			config.NextProtos = []string{"http/1.1"}
			tlsConn := tls.Client(conn, config)
			handshakeCtx := ctx
			if base.TLSHandshakeTimeout > 0 {
				var cancel context.CancelFunc
				handshakeCtx, cancel = context.WithTimeout(ctx, base.TLSHandshakeTimeout)
				defer cancel()
			}
			if err := callbackTLSHandshake(handshakeCtx, tlsConn); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return tlsConn, nil
		}
	}
	p := &preconnectPool{config: config, entries: make(map[string]*preparedConnection), recent: make(map[string]time.Time)}
	p.dial = func(ctx context.Context, route callbackRoute) (net.Conn, error) {
		if route.scheme == "https" {
			return tlsDial(ctx, "tcp", route.address)
		}
		return rawDial(ctx, "tcp", route.address)
	}
	wrapDial := func(scheme string, fallback func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
		return func(ctx context.Context, network, address string) (net.Conn, error) {
			if route, ok := ctx.Value(callbackRouteKey{}).(callbackRoute); ok && route.scheme == scheme && route.address == address {
				if conn := p.take(ctx, route.key); conn != nil {
					callbackConnectionClaimed(ctx)
					return conn, nil
				}
			}
			return fallback(ctx, network, address)
		}
	}
	base.DialContext = wrapDial("http", rawDial)
	base.DialTLSContext = wrapDial("https", tlsDial)
	return &callbackTransport{base: base, rt: tracing.NewOtelTransport(base), pool: p}
}

func (t *callbackTransport) route(req *http.Request) (callbackRoute, bool) {
	first := req.URL
	proxyIdentity := ""
	if t.base.Proxy != nil {
		proxy, err := t.base.Proxy(req)
		if err != nil {
			return callbackRoute{}, false
		}
		if proxy != nil {
			first = proxy
			proxyIdentity = proxy.String()
		}
	}
	if (first.Scheme != "http" && first.Scheme != "https") || first.Hostname() == "" ||
		(req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return callbackRoute{}, false
	}
	port := first.Port()
	if port == "" {
		port = "80"
		if first.Scheme == "https" {
			port = "443"
		}
	}
	return callbackRoute{
		key:    req.URL.Scheme + "://" + req.URL.Host + "\x00" + proxyIdentity,
		scheme: first.Scheme, address: net.JoinHostPort(first.Hostname(), port),
	}, true
}

func (t *callbackTransport) preconnect(ctx context.Context, target, requestID string) {
	attach, err := url.JoinPath(target, "/v2/nvcf/worker/request-attach")
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, attach, nil)
	if err != nil {
		return
	}
	if route, ok := t.route(req); ok {
		t.pool.start(ctx, route, requestID)
	}
}

func (t *callbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if callback, _ := req.Context().Value(callbackPostKey{}).(bool); callback {
		if route, ok := t.route(req); ok {
			req = req.WithContext(context.WithValue(req.Context(), callbackRouteKey{}, route))
		}
	}
	return t.rt.RoundTrip(req)
}

func (t *callbackTransport) CloseIdleConnections() {
	t.pool.closeIdle()
	t.base.CloseIdleConnections()
}

type preparedConnection struct {
	ready, checked chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	claimed        bool // guarded by pool.mu
	conn           net.Conn
	err            error
	readErr        error
	expires        time.Time
	detach         func() bool
}

type preconnectPool struct {
	mu      sync.Mutex
	entries map[string]*preparedConnection
	recent  map[string]time.Time
	config  preconnectConfig
	dial    func(context.Context, callbackRoute) (net.Conn, error)
}

func (p *preconnectPool) start(ctx context.Context, route callbackRoute, requestID string) {
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	if _, ok := p.entries[route.key]; ok {
		p.mu.Unlock()
		preconnectResult("coalesced")
		return
	}
	if len(p.entries) >= p.config.capacity {
		p.mu.Unlock()
		preconnectResult("capacity")
		return
	}
	now := time.Now()
	for key, until := range p.recent {
		if !now.Before(until) {
			delete(p.recent, key)
		}
	}
	if _, ok := p.recent[route.key]; ok {
		p.mu.Unlock()
		preconnectResult("cooldown")
		return
	}
	if len(p.recent) >= p.config.capacity {
		p.mu.Unlock()
		preconnectResult("capacity")
		return
	}
	p.recent[route.key] = now.Add(p.config.retryInterval)
	ctx, cancel := context.WithCancel(ctx)
	e := &preparedConnection{ctx: ctx, cancel: cancel, ready: make(chan struct{}), checked: make(chan struct{})}
	p.entries[route.key] = e
	p.mu.Unlock()
	preconnectResult("started")
	go p.prepare(e, route, requestID)
}

func (p *preconnectPool) prepare(e *preparedConnection, route callbackRoute, requestID string) {
	setupCtx, finish := preconnectSpan(e.ctx, requestID)
	setupCtx, cancel := context.WithTimeout(setupCtx, p.config.setupTimeout)
	e.conn, e.err = p.dial(setupCtx, route)
	cancel()
	finish(e.err)
	if e.err != nil {
		close(e.ready)
		close(e.checked)
		p.remove(route.key, e)
		e.cancel()
		return
	}
	e.expires = time.Now().Add(p.config.idleTimeout)
	e.err = e.conn.SetReadDeadline(e.expires)
	e.detach = context.AfterFunc(e.ctx, func() { _ = e.conn.Close() })
	close(e.ready)
	if e.err == nil {
		var data [1]byte
		var n int
		n, e.readErr = e.conn.Read(data[:])
		if n != 0 {
			e.readErr = errors.New("unexpected application data before callback")
		}
	}
	close(e.checked)
	p.mu.Lock()
	claimed := e.claimed
	if p.entries[route.key] == e {
		delete(p.entries, route.key)
	}
	p.mu.Unlock()
	if !claimed {
		result := "discarded"
		if e.ctx.Err() != nil {
			result = "canceled"
		} else if !time.Now().Before(e.expires) {
			result = "expired"
		}
		preconnectResult(result)
		_ = e.conn.Close()
		e.detach()
		e.cancel()
	}
}

// take gives the transport exclusive ownership. The read monitor must finish
// before net/http can read from the socket; its interrupting deadline is cleared.
func (p *preconnectPool) take(ctx context.Context, key string) net.Conn {
	p.mu.Lock()
	e := p.entries[key]
	if e != nil {
		e.claimed = true
		delete(p.entries, key)
	}
	p.mu.Unlock()
	if e == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		e.cancel()
		return nil
	case <-e.ready:
	}
	if e.err != nil {
		e.cancel()
		if e.conn != nil {
			_ = e.conn.Close()
		}
		return nil
	}
	defer e.cancel()
	valid := e.detach()
	if err := e.conn.SetReadDeadline(time.Now()); err != nil {
		valid = false
		_ = e.conn.Close()
	}
	select {
	case <-ctx.Done():
		valid = false
		_ = e.conn.Close()
	case <-e.checked:
	}
	var timeout net.Error
	if valid && ctx.Err() == nil && e.ctx.Err() == nil && time.Now().Before(e.expires) &&
		errors.As(e.readErr, &timeout) && timeout.Timeout() {
		if err := e.conn.SetReadDeadline(time.Time{}); err == nil {
			preconnectResult("claimed")
			return e.conn
		}
	}
	preconnectResult("discarded")
	_ = e.conn.Close()
	return nil
}

func (p *preconnectPool) remove(key string, e *preparedConnection) {
	p.mu.Lock()
	if p.entries[key] == e {
		delete(p.entries, key)
	}
	p.mu.Unlock()
}

func (p *preconnectPool) closeIdle() {
	p.mu.Lock()
	old := p.entries
	p.entries = make(map[string]*preparedConnection)
	p.mu.Unlock()
	for _, e := range old {
		e.cancel()
	}
}
