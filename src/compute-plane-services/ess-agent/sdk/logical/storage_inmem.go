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

package logical

import (
	"context"
	"sync"

	"github.com/hashicorp/vault/sdk/physical"
	"github.com/hashicorp/vault/sdk/physical/inmem"
)

// InmemStorage implements Storage and stores all data in memory. It is
// basically a straight copy of physical.Inmem, but it prevents backends from
// having to load all of physical's dependencies (which are legion) just to
// have some testing storage.
type InmemStorage struct {
	underlying physical.Backend
	once       sync.Once
}

func (s *InmemStorage) Get(ctx context.Context, key string) (*StorageEntry, error) {
	s.once.Do(s.init)

	entry, err := s.underlying.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	return &StorageEntry{
		Key:      entry.Key,
		Value:    entry.Value,
		SealWrap: entry.SealWrap,
	}, nil
}

func (s *InmemStorage) Put(ctx context.Context, entry *StorageEntry) error {
	s.once.Do(s.init)

	return s.underlying.Put(ctx, &physical.Entry{
		Key:      entry.Key,
		Value:    entry.Value,
		SealWrap: entry.SealWrap,
	})
}

func (s *InmemStorage) Delete(ctx context.Context, key string) error {
	s.once.Do(s.init)

	return s.underlying.Delete(ctx, key)
}

func (s *InmemStorage) List(ctx context.Context, prefix string) ([]string, error) {
	s.once.Do(s.init)

	return s.underlying.List(ctx, prefix)
}

func (s *InmemStorage) Underlying() *inmem.InmemBackend {
	s.once.Do(s.init)

	return s.underlying.(*inmem.InmemBackend)
}

func (s *InmemStorage) FailPut(fail bool) *InmemStorage {
	s.Underlying().FailPut(fail)
	return s
}

func (s *InmemStorage) FailGet(fail bool) *InmemStorage {
	s.Underlying().FailGet(fail)
	return s
}

func (s *InmemStorage) FailDelete(fail bool) *InmemStorage {
	s.Underlying().FailDelete(fail)
	return s
}

func (s *InmemStorage) FailList(fail bool) *InmemStorage {
	s.Underlying().FailList(fail)
	return s
}

func (s *InmemStorage) init() {
	s.underlying, _ = inmem.NewInmem(nil, nil)
}
