// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ratelimiter

import (
	"context"
	"testing"
	"time"

	"github.com/olric-data/olric"
	olricConfig "github.com/olric-data/olric/config"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	cfg := olricConfig.New("local")
	cfg.DMaps.EvictionPolicy = olricConfig.LRUEviction
	cfg.DMaps.MaxInuse = 100_000_000

	started := make(chan struct{})
	cfg.Started = func() { close(started) }

	db, err := olric.New(cfg)
	require.NoError(t, err)

	go func() {
		if err := db.Start(); err != nil {
			t.Errorf("olric failed to start: %v", err)
		}
	}()

	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("olric did not start in time")
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Shutdown(ctx)
	})

	dmap, err := db.NewEmbeddedClient().NewDMap("test-limiter")
	require.NoError(t, err)

	return &Store{Prefix: "test-limiter", dmap: dmap}
}

func TestDrainReplicatesEveryKeyWithoutChangingCounts(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	require.NoError(t, store.dmap.Put(ctx, "a", 4, olric.EX(time.Hour)))
	require.NoError(t, store.dmap.Put(ctx, "b", 9, olric.EX(time.Hour)))

	ttlBefore := map[string]int64{}
	for _, key := range []string{"a", "b"} {
		entry, err := store.dmap.Get(ctx, key)
		require.NoError(t, err)
		ttlBefore[key] = entry.TTL()
	}

	drained, failed, err := store.Drain(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, drained)
	require.Zero(t, failed)

	for key, want := range map[string]int{"a": 4, "b": 9} {
		got, err := store.dmap.Get(ctx, key)
		require.NoError(t, err)
		value, err := got.Int()
		require.NoError(t, err)
		require.Equal(t, want, value, "drain must not change the counter for %q", key)
		// The window must end when it would have without the drain, so compare
		// against the deadline recorded before draining rather than just
		// asserting some TTL survived.
		require.InDelta(t, ttlBefore[key], got.TTL(), float64(2*time.Second/time.Millisecond),
			"drain must keep the window deadline for %q", key)
	}
}

func TestDrainOnEmptyStore(t *testing.T) {
	store := newTestStore(t)

	drained, failed, err := store.Drain(context.Background())
	require.NoError(t, err)
	require.Zero(t, drained)
	require.Zero(t, failed)
}

func TestRedactKeyHidesSubject(t *testing.T) {
	// Non per-user keys carry no caller identity and pass through untouched.
	require.Equal(t, "limiter:nca-1:version-1:10-H", redactKey("limiter:nca-1:version-1:10-H"))

	// Real per-user keys are prefixed by the store, so the sentinel is not first.
	got := redactKey("limiter:user:caller@example.com:nca-1:version-1:10-H")
	require.NotContains(t, got, "caller@example.com")
	require.Equal(t, "limiter:user:"+redactSubject("caller@example.com")+":nca-1:version-1:10-H", got)
}
