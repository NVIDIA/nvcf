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

package lastcluster

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/olrictest"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

func TestOlricStoreTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded Olric test in -short mode")
	}
	ctx := context.Background()
	node := olrictest.StartCluster(t, 1)[0]
	dm, err := node.Client.NewDMap(DMapName)
	if err != nil {
		t.Fatalf("NewDMap() error = %v", err)
	}
	store := NewOlricStore(dm)

	if _, ok, err := store.Get(ctx, "missing"); ok || err != nil {
		t.Fatalf("Get(missing) = %v, %v; want miss without error", ok, err)
	}
	if err := store.Set(ctx, "k", "cluster-b", 300*time.Millisecond); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if value, ok, err := store.Get(ctx, "k"); err != nil || !ok || value != "cluster-b" {
		t.Fatalf("Get() = %q, %v, %v; want cluster-b", value, ok, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, ok, err := store.Get(ctx, "k")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("entry did not expire after its TTL")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTwoReplicasShareLastCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-node Olric test in -short mode")
	}
	ctx := context.Background()
	nodes := olrictest.StartCluster(t, 2)
	trackers := make([]*Tracker, len(nodes))
	for i, node := range nodes {
		dm, err := node.Client.NewDMap(DMapName)
		if err != nil {
			t.Fatalf("NewDMap() on node %d error = %v", i, err)
		}
		trackers[i] = NewTracker(NewOlricStore(dm), Options{TTL: time.Minute, LookupTimeout: time.Second})
	}
	reqCtx := sessionCtx(requestctx.SessionSourcePromptCacheKey)

	key, clusterID := trackers[0].Lookup(ctx, reqCtx, reqCtx.Model)
	if clusterID != "" {
		t.Fatalf("first lookup cluster = %q, want miss", clusterID)
	}
	trackers[0].Remember(ctx, reqCtx, reqCtx.Model, key, http.StatusOK, "cluster-b")
	trackers[0].Drain(ctx)

	if _, clusterID := trackers[1].Lookup(ctx, reqCtx, reqCtx.Model); clusterID != "cluster-b" {
		t.Fatalf("replica 2 lookup cluster = %q, want cluster-b", clusterID)
	}
}
