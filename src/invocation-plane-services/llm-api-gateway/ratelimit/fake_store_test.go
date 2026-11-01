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

package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// fakeStore is an in-process OlricStore for driving CAS-based transitions
// without spinning up an Olric node. A process-wide mutex simulates
// cluster-wide serialization on a single partition owner.
type fakeStore struct {
	mu      sync.Mutex
	buckets map[string]bucketState
	exists  map[string]bool

	failGet bool
	failCAS bool

	// forcedMismatches makes the next n CompareAndSetBucket calls decline
	// the swap as if a concurrent writer had won the race. Used by tests to
	// drive the retry loop deterministically.
	forcedMismatches int

	getCalls int
	casCalls int
	casSwaps int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		buckets: map[string]bucketState{},
		exists:  map[string]bool{},
	}
}

func (f *fakeStore) GetBucket(
	_ context.Context,
	key string,
) (bucketState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.getCalls++

	if f.failGet {
		return bucketState{}, false, errors.New("fake get error")
	}

	if !f.exists[key] {
		return bucketState{}, false, nil
	}
	return f.buckets[key], true, nil
}

func (f *fakeStore) CompareAndSetBucket(
	_ context.Context,
	key string,
	expected *bucketState,
	state bucketState,
	_ time.Duration,
) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.casCalls++

	if f.failCAS {
		return false, errors.New("fake cas error")
	}

	if f.forcedMismatches > 0 {
		f.forcedMismatches--
		return false, nil
	}

	cur, exists := f.buckets[key], f.exists[key]

	if expected == nil {
		if exists {
			return false, nil
		}
	} else {
		if !exists {
			return false, nil
		}
		if *expected != cur {
			return false, nil
		}
	}

	f.buckets[key] = state
	f.exists[key] = true
	f.casSwaps++
	return true, nil
}

func (f *fakeStore) Delete(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.buckets, k)
		delete(f.exists, k)
	}
	return nil
}

// mustContain panics if the bucket is not present.
func (f *fakeStore) mustContain(key string) bucketState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists[key] {
		panic(fmt.Sprintf("fakeStore: expected key %q to be present", key))
	}
	return f.buckets[key]
}

// setForcedMismatches primes the CAS path to reject the next n
// CompareAndSetBucket calls as if a concurrent writer had won the race.
func (f *fakeStore) setForcedMismatches(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forcedMismatches = n
}
