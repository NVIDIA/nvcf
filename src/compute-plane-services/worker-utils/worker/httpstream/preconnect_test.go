/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package httpstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testPool(t *testing.T, capacity int, ttl time.Duration, dial func(context.Context, callbackRoute) (net.Conn, error)) *preconnectPool {
	t.Helper()
	p := &preconnectPool{config: preconnectConfig{capacity: capacity, setupTimeout: time.Second, idleTimeout: ttl}, entries: make(map[string]*preparedConnection), recent: make(map[string]time.Time), dial: dial}
	t.Cleanup(p.closeIdle)
	return p
}

func entryFor(t *testing.T, p *preconnectPool, key string) *preparedConnection {
	t.Helper()
	p.mu.Lock()
	e := p.entries[key]
	p.mu.Unlock()
	require.NotNil(t, e)
	select {
	case <-e.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("preparation did not finish")
	}
	return e
}

func poolSize(p *preconnectPool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

func TestPreconnectCoalescingCapacityAndCancellation(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 2)
	p := testPool(t, 2, time.Second, func(ctx context.Context, _ callbackRoute) (net.Conn, error) {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	for i := 0; i < 100; i++ {
		p.start(t.Context(), callbackRoute{key: "same"}, "test")
	}
	p.start(t.Context(), callbackRoute{key: "second"}, "test")
	p.start(t.Context(), callbackRoute{key: "over-capacity"}, "test")
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("dial not started")
		}
	}
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, 2, poolSize(p))
	p.mu.Lock()
	entries := []*preparedConnection{p.entries["same"], p.entries["second"]}
	p.mu.Unlock()
	p.closeIdle()
	for _, e := range entries {
		select {
		case <-e.ready:
			require.ErrorIs(t, e.err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("dial not canceled")
		}
	}
	require.Zero(t, poolSize(p))
}

func TestPreconnectHandoffHasOneOwnerAndClearsDeadline(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = b.Close() })
	p := testPool(t, 1, time.Second, func(context.Context, callbackRoute) (net.Conn, error) { return a, nil })
	p.start(t.Context(), callbackRoute{key: "route"}, "test")
	entryFor(t, p, "route")
	var winners atomic.Int32
	var won net.Conn
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			if c := p.take(t.Context(), "route"); c != nil {
				winners.Add(1)
				mu.Lock()
				won = c
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	require.NotNil(t, won)
	defer won.Close()
	go func() { _, _ = b.Write([]byte("ok")) }()
	buf := make([]byte, 2)
	_, err := io.ReadFull(won, buf)
	require.NoError(t, err)
	require.Equal(t, "ok", string(buf))
}

func TestPreconnectExpiryPeerCloseAndOwnerCancel(t *testing.T) {
	for _, mode := range []string{"expiry", "peer-close", "cancel", "unexpected-data", "client-close"} {
		t.Run(mode, func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := testPool(t, 1, 30*time.Millisecond, func(context.Context, callbackRoute) (net.Conn, error) { return a, nil })
			p.start(ctx, callbackRoute{key: "route"}, "test")
			e := entryFor(t, p, "route")
			switch mode {
			case "peer-close":
				_ = b.Close()
			case "cancel":
				cancel()
			case "unexpected-data":
				_, _ = b.Write([]byte("bad"))
			case "client-close":
				p.closeIdle()
			}
			select {
			case <-e.checked:
			case <-time.After(time.Second):
				t.Fatal("parked socket monitor did not stop")
			}
			require.Eventually(t, func() bool { return poolSize(p) == 0 }, time.Second, time.Millisecond)
			require.Nil(t, p.take(t.Context(), "route"))
		})
	}
}

func TestPreconnectFailedAndCanceledClaim(t *testing.T) {
	t.Run("setup-timeout", func(t *testing.T) {
		finished := make(chan error, 1)
		p := testPool(t, 1, time.Second, func(ctx context.Context, _ callbackRoute) (net.Conn, error) {
			<-ctx.Done()
			finished <- ctx.Err()
			return nil, ctx.Err()
		})
		p.config.setupTimeout = 10 * time.Millisecond
		p.start(t.Context(), callbackRoute{key: "route"}, "test")
		select {
		case err := <-finished:
			require.ErrorIs(t, err, context.DeadlineExceeded)
		case <-time.After(time.Second):
			t.Fatal("setup exceeded its budget")
		}
	})
	t.Run("failure", func(t *testing.T) {
		p := testPool(t, 1, time.Second, func(context.Context, callbackRoute) (net.Conn, error) { return nil, errors.New("synthetic failure") })
		p.start(t.Context(), callbackRoute{key: "route"}, "test")
		require.Eventually(t, func() bool { return poolSize(p) == 0 }, time.Second, time.Millisecond)
		require.Nil(t, p.take(t.Context(), "route"))
	})
	t.Run("pending-claim-cancel", func(t *testing.T) {
		finished := make(chan struct{})
		p := testPool(t, 1, time.Second, func(ctx context.Context, _ callbackRoute) (net.Conn, error) {
			<-ctx.Done()
			close(finished)
			return nil, ctx.Err()
		})
		p.start(t.Context(), callbackRoute{key: "route"}, "test")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.Nil(t, p.take(ctx, "route"))
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("claimed setup leaked after callback cancellation")
		}
	})
}

func TestPreconnectRouteIsolationAndDefault(t *testing.T) {
	require.NotNil(t, NewProxiedClient())
	_, enabled := ((*http.Client)(NewProxiedClient())).Transport.(*callbackTransport)
	require.False(t, enabled)
	client := (*http.Client)(NewProxiedClient(WithCallbackPreconnect(true)))
	defer client.CloseIdleConnections()
	transport := client.Transport.(*callbackTransport)
	keys := map[string]bool{}
	for _, target := range []string{"https://one.invalid", "http://one.invalid", "https://two.invalid"} {
		for _, proxy := range []string{"", "https://alice:one@proxy.invalid:443", "https://alice:two@proxy.invalid:443"} {
			ctx := t.Context()
			if proxy != "" {
				u, err := url.Parse(proxy)
				require.NoError(t, err)
				ctx = context.WithValue(ctx, proxyURLKey, u)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
			require.NoError(t, err)
			route, ok := transport.route(req)
			require.True(t, ok)
			require.False(t, keys[route.key], "route identities collided")
			keys[route.key] = true
		}
	}
	transport.preconnect(t.Context(), "://malformed", "test")
	require.Zero(t, poolSize(transport.pool))
}

func TestPreconnectConcurrentLifecycle(t *testing.T) {
	var peersMu sync.Mutex
	var peers []net.Conn
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := testPool(t, 4, time.Second, func(context.Context, callbackRoute) (net.Conn, error) {
		a, b := net.Pipe()
		peersMu.Lock()
		peers = append(peers, b)
		peersMu.Unlock()
		return a, nil
	})
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Go(func() {
			for i := 0; i < 20; i++ {
				key := fmt.Sprintf("route-%d", i%4)
				p.start(ctx, callbackRoute{key: key}, "synthetic")
				if conn := p.take(ctx, key); conn != nil {
					_ = conn.Close()
				}
				if i%3 == 0 {
					p.closeIdle()
				}
			}
		})
	}
	wg.Wait()
	p.closeIdle()
	cancel()
	peersMu.Lock()
	for _, peer := range peers {
		_ = peer.Close()
	}
	peersMu.Unlock()
	require.Zero(t, poolSize(p))
}

func TestPreconnectCooldownBoundsRepeatedFailure(t *testing.T) {
	var calls atomic.Int32
	p := testPool(t, 1, time.Second, func(context.Context, callbackRoute) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("synthetic failure")
	})
	p.config.retryInterval = 50 * time.Millisecond
	p.start(t.Context(), callbackRoute{key: "route"}, "test")
	require.Eventually(t, func() bool { return poolSize(p) == 0 }, time.Second, time.Millisecond)
	for range 100 {
		p.start(t.Context(), callbackRoute{key: "route"}, "test")
		p.start(t.Context(), callbackRoute{key: "different-route"}, "test")
	}
	require.EqualValues(t, 1, calls.Load())
	require.Eventually(t, func() bool {
		p.start(t.Context(), callbackRoute{key: "route"}, "test")
		return calls.Load() == 2
	}, time.Second, time.Millisecond)
}
