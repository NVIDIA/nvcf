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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/olric-data/olric"
)

// bucketState is the single serialized value stored per bucket key.
// The full struct is read and written atomically via Olric's CompareAndSwap
// so the continuous-refill transition stays consistent across the cluster
// without a server-side script or a distributed lock.
type bucketState struct {
	Value      int64 `json:"v"`
	LastRefill int64 `json:"t"`
}

// OlricStore is the minimum surface the rate-limit algorithm needs from Olric.
// It is intentionally small so the limiter can be unit tested against a fake.
//
// The contract is an optimistic-concurrency read-modify-write:
//
//  1. GetBucket reads the current state. ok=false means the key is absent.
//  2. The caller computes the post-transition state locally.
//  3. CompareAndSetBucket publishes the new state only if the currently stored
//     state still matches what GetBucket returned (or, when expected is nil,
//     only if the key is still absent). On mismatch the caller re-reads and
//     retries.
type OlricStore interface {
	// GetBucket reads the current bucket state for the key.
	// ok=false, state={} when the key is absent.
	GetBucket(ctx context.Context, key string) (state bucketState, ok bool, err error)

	// CompareAndSetBucket writes newState with the given TTL, but only if the
	// currently stored state still matches expected. When expected is nil the
	// swap succeeds only if the key is absent (insert-if-absent semantics).
	//
	// swapped=true on success. swapped=false signals a concurrent update; the
	// caller should re-read via GetBucket and retry.
	CompareAndSetBucket(
		ctx context.Context,
		key string,
		expected *bucketState,
		newState bucketState,
		ttl time.Duration,
	) (swapped bool, err error)

	// Delete removes one or more bucket keys.
	Delete(ctx context.Context, keys ...string) error
}

type olricDMapStore struct {
	dmap olric.DMap
}

// NewOlricStore returns an OlricStore backed by the given Olric DMap.
func NewOlricStore(dmap olric.DMap) OlricStore {
	return &olricDMapStore{dmap: dmap}
}

func (s *olricDMapStore) GetBucket(
	ctx context.Context,
	key string,
) (bucketState, bool, error) {
	resp, err := s.dmap.Get(ctx, key)
	if err != nil {
		if errors.Is(err, olric.ErrKeyNotFound) {
			return bucketState{}, false, nil
		}
		return bucketState{}, false, fmt.Errorf("olric get: %w", err)
	}

	decoded, err := resp.Byte()
	if err != nil {
		return bucketState{}, false, fmt.Errorf("olric get decode: %w", err)
	}

	var state bucketState
	if err := json.Unmarshal(decoded, &state); err != nil {
		return bucketState{}, false, fmt.Errorf("olric get unmarshal: %w", err)
	}
	return state, true, nil
}

func (s *olricDMapStore) CompareAndSetBucket(
	ctx context.Context,
	key string,
	expected *bucketState,
	newState bucketState,
	ttl time.Duration,
) (bool, error) {
	newBytes, err := json.Marshal(newState)
	if err != nil {
		return false, fmt.Errorf("olric cas marshal new: %w", err)
	}

	var expectedBytes []byte
	if expected != nil {
		b, err := json.Marshal(*expected)
		if err != nil {
			return false, fmt.Errorf("olric cas marshal expected: %w", err)
		}
		expectedBytes = b
	}

	swapped, _, err := s.dmap.CompareAndSwap(ctx, key, expectedBytes, newBytes, olric.PX(ttl))
	if err != nil {
		return false, fmt.Errorf("olric cas: %w", err)
	}
	return swapped, nil
}

func (s *olricDMapStore) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if _, err := s.dmap.Delete(ctx, keys...); err != nil {
		return fmt.Errorf("olric delete: %w", err)
	}
	return nil
}
