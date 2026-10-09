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
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestKeyIsPrefixedHashOfLengthPrefixedParts(t *testing.T) {
	key := Key("routing-key", "model", "mt:v1:session:abc")
	if !strings.HasPrefix(key, "lc:v1:") || len(key) != len("lc:v1:")+64 {
		t.Fatalf("Key() = %q, want lc:v1: plus 64 hex characters", key)
	}
	if strings.Contains(key, "routing-key") {
		t.Fatalf("Key() = %q leaks the raw routing key", key)
	}
	if Key("ab", "c", "d") == Key("a", "bc", "d") {
		t.Fatal("Key() collides for different splits of the same bytes")
	}
	if Key("r", "m1", "k") == Key("r", "m2", "k") || Key("r1", "m", "k") == Key("r2", "m", "k") {
		t.Fatal("Key() must differ by model and routing key")
	}
	if Key("r", "m", "k") != Key("r", "m", "k") {
		t.Fatal("Key() must be deterministic")
	}
}

func TestValidClusterID(t *testing.T) {
	cases := map[string]bool{
		"":                                   false,
		"cluster-b":                          true,
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

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestLocalStoreTTL(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Unix(1000, 0)}
	store := NewLocalStore(10, WithClock(clock.Now))
	ttl := 10 * time.Minute

	if err := store.Set(ctx, "k", "B", ttl); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	clock.Advance(ttl - time.Nanosecond)
	if value, ok, _ := store.Get(ctx, "k"); !ok || value != "B" {
		t.Fatalf("Get() before TTL = %q, %v; want B, true", value, ok)
	}
	clock.Advance(time.Nanosecond)
	if value, ok, _ := store.Get(ctx, "k"); ok {
		t.Fatalf("Get() at TTL = %q, true; want miss", value)
	}
	if store.Len() != 0 {
		t.Fatalf("Len() = %d after expired read, want 0", store.Len())
	}
}

func TestLocalStoreWriteResetsTTL(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Unix(1000, 0)}
	store := NewLocalStore(10, WithClock(clock.Now))
	ttl := time.Minute

	_ = store.Set(ctx, "k", "A", ttl)
	clock.Advance(ttl / 2)
	_ = store.Set(ctx, "k", "B", ttl)
	clock.Advance(ttl/2 + ttl/4)
	if value, ok, _ := store.Get(ctx, "k"); !ok || value != "B" {
		t.Fatalf("Get() after rewrite = %q, %v; want B, true", value, ok)
	}
	// A read does not extend the TTL.
	clock.Advance(ttl / 4)
	if _, ok, _ := store.Get(ctx, "k"); ok {
		t.Fatal("Get() after rewrite TTL = hit, want miss")
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

	if _, ok, _ := store.Get(ctx, "b"); ok {
		t.Fatal("Get(b) = hit, want b evicted as least recently used")
	}
	for _, key := range []string{"a", "c"} {
		if _, ok, _ := store.Get(ctx, key); !ok {
			t.Fatalf("Get(%s) = miss, want hit", key)
		}
	}
}

func TestLocalStoreNeverExceedsCap(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(100)
	for i := 0; i < 1000; i++ {
		_ = store.Set(ctx, fmt.Sprintf("k%d", i), "B", time.Minute)
		if store.Len() > 100 {
			t.Fatalf("Len() = %d after %d writes, want <= 100", store.Len(), i+1)
		}
	}
	if store.Len() != 100 {
		t.Fatalf("Len() = %d, want 100", store.Len())
	}
	if _, ok, _ := store.Get(ctx, "k999"); !ok {
		t.Fatal("newest entry evicted")
	}
	if _, ok, _ := store.Get(ctx, "k0"); ok {
		t.Fatal("oldest entry still present")
	}
}
