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

package inmem

import (
	"context"
	"testing"

	"github.com/armon/go-metrics"
	log "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/sdk/helper/logging"
	"github.com/hashicorp/vault/sdk/physical"
)

func TestCache(t *testing.T) {
	logger := logging.NewVaultLogger(log.Debug)

	inm, err := NewInmem(nil, logger)
	if err != nil {
		t.Fatal(err)
	}

	cache := physical.NewCache(inm, 0, logger, &metrics.BlackholeSink{})
	cache.SetEnabled(true)
	physical.ExerciseBackend(t, cache)
	physical.ExerciseBackend_ListPrefix(t, cache)
}

func TestCache_Purge(t *testing.T) {
	logger := logging.NewVaultLogger(log.Debug)

	inm, err := NewInmem(nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	cache := physical.NewCache(inm, 0, logger, &metrics.BlackholeSink{})
	cache.SetEnabled(true)

	ent := &physical.Entry{
		Key:   "foo",
		Value: []byte("bar"),
	}
	err = cache.Put(context.Background(), ent)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Delete from under
	inm.Delete(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}

	// Read should work
	out, err := cache.Get(context.Background(), "foo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out == nil {
		t.Fatalf("should have key")
	}

	// Clear the cache
	cache.Purge(context.Background())

	// Read should fail
	out, err = cache.Get(context.Background(), "foo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != nil {
		t.Fatalf("should not have key")
	}
}

func TestCache_Disable(t *testing.T) {
	logger := logging.NewVaultLogger(log.Debug)

	inm, err := NewInmem(nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	cache := physical.NewCache(inm, 0, logger, &metrics.BlackholeSink{})

	disabledTests := func() {
		ent := &physical.Entry{
			Key:   "foo",
			Value: []byte("bar"),
		}
		err = inm.Put(context.Background(), ent)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read should work
		out, err := cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}

		err = inm.Delete(context.Background(), ent.Key)
		if err != nil {
			t.Fatal(err)
		}

		// Should not work
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out != nil {
			t.Fatalf("should not have key")
		}

		// Put through the cache and try again
		err = cache.Put(context.Background(), ent)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read should work in both
		out, err = inm.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}

		err = inm.Delete(context.Background(), ent.Key)
		if err != nil {
			t.Fatal(err)
		}

		// Should not work
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out != nil {
			t.Fatalf("should not have key")
		}
	}

	enabledTests := func() {
		ent := &physical.Entry{
			Key:   "foo",
			Value: []byte("bar"),
		}
		err = inm.Put(context.Background(), ent)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read should work
		out, err := cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}

		err = inm.Delete(context.Background(), ent.Key)
		if err != nil {
			t.Fatal(err)
		}

		// Should work
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}

		// Put through the cache and try again
		err = cache.Put(context.Background(), ent)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read should work for both
		out, err = inm.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}

		err = inm.Delete(context.Background(), ent.Key)
		if err != nil {
			t.Fatal(err)
		}

		// Should work
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}

		// Put through the cache
		err = cache.Put(context.Background(), ent)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read should work for both
		out, err = inm.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out == nil {
			t.Fatalf("should have key")
		}

		// Delete via cache
		err = cache.Delete(context.Background(), ent.Key)
		if err != nil {
			t.Fatal(err)
		}

		// Read should not work for either
		out, err = inm.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out != nil {
			t.Fatalf("should not have key")
		}
		out, err = cache.Get(context.Background(), "foo")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out != nil {
			t.Fatalf("should not have key")
		}
	}

	disabledTests()
	cache.SetEnabled(true)
	enabledTests()
	cache.SetEnabled(false)
	disabledTests()
}

func TestCache_Refresh(t *testing.T) {
	logger := logging.NewVaultLogger(log.Debug)

	inm, err := NewInmem(nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	cache := physical.NewCache(inm, 0, logger, &metrics.BlackholeSink{})
	cache.SetEnabled(true)

	ent := &physical.Entry{
		Key:   "foo",
		Value: []byte("bar"),
	}
	err = cache.Put(context.Background(), ent)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	ent2 := &physical.Entry{
		Key:   "foo",
		Value: []byte("baz"),
	}
	// Update below cache
	err = inm.Put(context.Background(), ent2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	r, err := cache.Get(context.Background(), "foo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if string(r.Value) != "bar" {
		t.Fatalf("expected value bar, got %s", string(r.Value))
	}

	// Refresh the cache
	r, err = cache.Get(physical.CacheRefreshContext(context.Background(), true), "foo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if string(r.Value) != "baz" {
		t.Fatalf("expected value baz, got %s", string(r.Value))
	}

	// Make sure new value is in cache
	r, err = cache.Get(context.Background(), "foo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(r.Value) != "baz" {
		t.Fatalf("expected value baz, got %s", string(r.Value))
	}
}
