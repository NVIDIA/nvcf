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
	"strings"
	"testing"
	"time"
)

func TestKey(t *testing.T) {
	key := Key("routing-key", "model", "mt:v1:session:abc")
	if !strings.HasPrefix(key, "lc:v1:") || len(key) != len("lc:v1:")+64 {
		t.Fatalf("Key() = %q, want lc:v1: plus 64 hex characters", key)
	}
	if Key("ab", "c", "d") == Key("a", "bc", "d") {
		t.Fatal("Key() collides for different splits of the same bytes")
	}
}

func TestValidClusterID(t *testing.T) {
	cases := map[string]bool{
		"":                                   false,
		"cluster b":                          true,
		strings.Repeat("a", MaxValueBytes):   true,
		strings.Repeat("a", MaxValueBytes+1): false,
		"bad\nvalue":                         false,
		"caf\xc3\xa9":                        false,
	}
	for value, want := range cases {
		if got := validClusterID(value); got != want {
			t.Errorf("validClusterID(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestLocalStoreTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	store := NewLocalStore(10)
	store.now = func() time.Time { return now }
	ttl := time.Minute

	_ = store.Set(ctx, "k", "A", ttl)
	now = now.Add(ttl / 2)
	_ = store.Set(ctx, "k", "B", ttl)
	now = now.Add(ttl/2 + ttl/4)
	if value, ok, _ := store.Get(ctx, "k"); !ok || value != "B" {
		t.Fatalf("Get() past the first TTL = %q, %v; want B, true since the rewrite reset it", value, ok)
	}
	// The read above does not extend the TTL.
	now = now.Add(ttl / 4)
	if _, ok, _ := store.Get(ctx, "k"); ok || store.Len() != 0 {
		t.Fatalf("Get() at TTL = hit=%v, Len()=%d; want a miss that drops the entry", ok, store.Len())
	}
}

func TestLocalStoreEvictsLeastRecentlyUsed(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(2)
	_ = store.Set(ctx, "a", "A", time.Minute)
	_ = store.Set(ctx, "b", "B", time.Minute)
	if _, ok, _ := store.Get(ctx, "a"); !ok {
		t.Fatal("Get(a) = miss, want hit")
	}
	_ = store.Set(ctx, "c", "C", time.Minute)

	if store.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", store.Len())
	}
	for key, want := range map[string]bool{"a": true, "b": false, "c": true} {
		if _, ok, _ := store.Get(ctx, key); ok != want {
			t.Fatalf("Get(%s) hit = %v, want %v", key, ok, want)
		}
	}
}
