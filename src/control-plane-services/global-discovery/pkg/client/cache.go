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
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
)

// DefaultCacheTTL is the anti-entropy interval of the caches: a full list
// every TTL, in addition to the watch.
const DefaultCacheTTL = 30 * time.Second

// cacheMeta is the bookkeeping published with every cache snapshot.
type cacheMeta struct {
	revision  string
	updatedAt time.Time
	synced    bool
	stale     bool
}

// cacheOptions configures a cache.
type cacheOptions[T proto.Message] struct {
	name        string
	ttl         time.Duration
	staleAfter  time.Duration
	idleTimeout time.Duration
	key         func(T) string
	list        func(ctx context.Context) ([]T, string, error)
	watch       WatchFunc[T]
	// publish stores a new snapshot; items are sorted by key.
	publish func(items []T, meta cacheMeta)
}

// cache is the engine of PoolCache and RouterCache: an initial list, a
// watch applying deltas, a full list every TTL that replaces the state, and
// a stale flag once nothing succeeded for staleAfter.
type cache[T proto.Message] struct {
	c      *Client
	opts   cacheOptions[T]
	logger *slog.Logger

	mu        sync.Mutex
	items     map[string]T
	pending   map[string]T
	revision  string
	created   time.Time
	lastFresh time.Time
	synced    bool
	stale     bool
	lastErr   error
	subs      map[chan struct{}]struct{}
	closed    bool
	syncedCh  chan struct{}
	running   atomic.Bool
}

func newCache[T proto.Message](c *Client, opts cacheOptions[T]) *cache[T] {
	if opts.ttl <= 0 {
		opts.ttl = DefaultCacheTTL
	}
	if opts.staleAfter <= 0 {
		opts.staleAfter = 2 * opts.ttl
	}
	if opts.idleTimeout == 0 {
		opts.idleTimeout = DefaultWatchIdleTimeout
	}
	k := &cache[T]{
		c:        c,
		opts:     opts,
		logger:   c.logger.With("cache", opts.name),
		items:    map[string]T{},
		created:  time.Now(),
		subs:     map[chan struct{}]struct{}{},
		syncedCh: make(chan struct{}),
	}
	k.publishLocked(false)
	return k
}

// freshness returns the time the data was last confirmed, or the start
// time before the first success.
func (k *cache[T]) freshness() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.freshnessLocked()
}

func (k *cache[T]) freshnessLocked() time.Time {
	if k.lastFresh.IsZero() {
		return k.created
	}
	return k.lastFresh
}

// run lists, starts the watch after the first successful list, re-lists
// every TTL and evaluates staleness until ctx is done.
func (k *cache[T]) run(ctx context.Context) error {
	if !k.running.CompareAndSwap(false, true) {
		return errors.New("gds: cache already running")
	}
	k.c.metrics.cacheAge.set(k.opts.name, k.freshness)
	defer k.c.metrics.cacheAge.remove(k.opts.name)
	defer k.closeSubscribers()

	wctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	staleTick := time.NewTicker(max(k.opts.staleAfter/4, time.Millisecond))
	defer staleTick.Stop()
	nextList := time.NewTimer(0)
	defer nextList.Stop()
	watching := false
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-staleTick.C:
			k.checkStale()
			continue
		case <-nextList.C:
		}
		if err := k.relist(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			nextList.Reset(min(k.c.cfg.Backoff.Delay(attempt), k.opts.ttl))
			attempt++
			continue
		}
		attempt = 0
		nextList.Reset(k.opts.ttl)
		if !watching {
			watching = true
			loop := NewWatchLoop(k.c, WatchLoopConfig[T]{
				Name:        k.opts.name,
				Watch:       k.opts.watch,
				Revision:    k.currentRevision(),
				IdleTimeout: k.opts.idleTimeout,
				Handler: WatchHandler[T]{
					SnapshotStart: k.snapshotStart,
					Upsert:        k.upsert,
					Delete:        k.delete,
					Synced:        k.snapshotEnd,
					Bookmark:      k.bookmark,
				},
			})
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = loop.Run(wctx)
			}()
		}
	}
}

func (k *cache[T]) currentRevision() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.revision
}

// refresh lists now, outside the TTL schedule.
func (k *cache[T]) refresh(ctx context.Context) error {
	return k.relist(ctx)
}

// relist replaces the state with a full list.
func (k *cache[T]) relist(ctx context.Context) error {
	items, revision, err := k.opts.list(ctx)
	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil {
		k.lastErr = err
		k.logger.Warn("GDS cache list failed", "error", err)
		return err
	}
	next := make(map[string]T, len(items))
	for _, it := range items {
		next[k.opts.key(it)] = it
	}
	changed := !maps.EqualFunc(next, k.items, func(a, b T) bool { return proto.Equal(a, b) })
	k.items = next
	if revision != "" {
		k.revision = revision
	}
	k.lastErr = nil
	staleChanged := k.markFreshLocked()
	first := !k.synced
	if first {
		k.synced = true
		close(k.syncedCh)
	}
	k.publishLocked(changed || staleChanged || first)
	return nil
}

func (k *cache[T]) snapshotStart() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.pending = map[string]T{}
}

func (k *cache[T]) upsert(obj T) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pending != nil {
		k.pending[k.opts.key(obj)] = obj
		return
	}
	k.items[k.opts.key(obj)] = obj
	k.markFreshLocked()
	k.publishLocked(true)
}

func (k *cache[T]) delete(obj T) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pending != nil {
		delete(k.pending, k.opts.key(obj))
		return
	}
	delete(k.items, k.opts.key(obj))
	k.markFreshLocked()
	k.publishLocked(true)
}

func (k *cache[T]) snapshotEnd(revision string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pending == nil {
		return
	}
	changed := !maps.EqualFunc(k.pending, k.items, func(a, b T) bool { return proto.Equal(a, b) })
	k.items, k.pending = k.pending, nil
	k.revision = revision
	staleChanged := k.markFreshLocked()
	k.publishLocked(changed || staleChanged)
}

func (k *cache[T]) bookmark(revision string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if revision != "" {
		k.revision = revision
	}
	k.publishLocked(k.markFreshLocked())
}

// markFreshLocked records a success and reports whether it cleared the
// stale flag.
func (k *cache[T]) markFreshLocked() bool {
	k.lastFresh = time.Now()
	if k.stale {
		k.stale = false
		k.logger.Info("GDS cache is fresh again")
		return true
	}
	return false
}

func (k *cache[T]) checkStale() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stale || time.Since(k.freshnessLocked()) <= k.opts.staleAfter {
		return
	}
	k.stale = true
	k.logger.Warn("GDS cache is stale", "since", k.freshnessLocked(), "error", k.lastErr)
	k.publishLocked(true)
}

// publishLocked publishes a snapshot and, with notify, wakes the
// subscribers.
func (k *cache[T]) publishLocked(notify bool) {
	items := make([]T, 0, len(k.items))
	for _, key := range slices.Sorted(maps.Keys(k.items)) {
		items = append(items, k.items[key])
	}
	k.opts.publish(items, cacheMeta{
		revision:  k.revision,
		updatedAt: k.lastFresh,
		synced:    k.synced,
		stale:     k.stale,
	})
	if !notify {
		return
	}
	for ch := range k.subs {
		signal(ch)
	}
}

func (k *cache[T]) updates() <-chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	ch := make(chan struct{}, 1)
	if k.closed {
		close(ch)
		return ch
	}
	k.subs[ch] = struct{}{}
	return ch
}

func (k *cache[T]) closeSubscribers() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closed = true
	for ch := range k.subs {
		close(ch)
	}
	k.subs = nil
}

func (k *cache[T]) waitSynced(ctx context.Context) error {
	select {
	case <-k.syncedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (k *cache[T]) lastError() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.lastErr
}
