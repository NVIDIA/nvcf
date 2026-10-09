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
	"container/list"
	"context"
	"sync"
	"time"
)

// LocalStore is an in-process LRU with per-entry TTL, used when Olric is off.
// It holds at most maxEntries entries and evicts the least recently used one
// first. Reads refresh recency but not the TTL; only writes reset the TTL.
type LocalStore struct {
	mu         sync.Mutex
	maxEntries int
	now        func() time.Time
	order      *list.List // front is most recently used
	entries    map[string]*list.Element
}

type localEntry struct {
	key       string
	value     string
	expiresAt time.Time
}

// LocalOption configures a LocalStore.
type LocalOption func(*LocalStore)

// WithClock overrides the clock used for TTL checks. Tests only.
func WithClock(now func() time.Time) LocalOption {
	return func(s *LocalStore) { s.now = now }
}

// NewLocalStore returns an empty LocalStore. maxEntries below 1 is treated as 1.
func NewLocalStore(maxEntries int, opts ...LocalOption) *LocalStore {
	if maxEntries < 1 {
		maxEntries = 1
	}
	s := &LocalStore{
		maxEntries: maxEntries,
		now:        time.Now,
		order:      list.New(),
		entries:    make(map[string]*list.Element),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *LocalStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	elem, ok := s.entries[key]
	if !ok {
		return "", false, nil
	}
	entry := elem.Value.(*localEntry)
	if !s.now().Before(entry.expiresAt) {
		s.order.Remove(elem)
		delete(s.entries, key)
		return "", false, nil
	}
	s.order.MoveToFront(elem)
	return entry.value, true, nil
}

func (s *LocalStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt := s.now().Add(ttl)
	if elem, ok := s.entries[key]; ok {
		entry := elem.Value.(*localEntry)
		entry.value = value
		entry.expiresAt = expiresAt
		s.order.MoveToFront(elem)
		return nil
	}
	s.entries[key] = s.order.PushFront(&localEntry{key: key, value: value, expiresAt: expiresAt})
	for s.order.Len() > s.maxEntries {
		oldest := s.order.Back()
		s.order.Remove(oldest)
		delete(s.entries, oldest.Value.(*localEntry).key)
	}
	return nil
}

// Len returns the number of entries held, including expired entries that
// have not been read or evicted yet.
func (s *LocalStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}
