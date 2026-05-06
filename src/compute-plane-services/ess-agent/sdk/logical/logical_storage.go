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

	"github.com/hashicorp/vault/sdk/physical"
)

type LogicalStorage struct {
	underlying physical.Backend
}

func (s *LogicalStorage) Get(ctx context.Context, key string) (*StorageEntry, error) {
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

func (s *LogicalStorage) Put(ctx context.Context, entry *StorageEntry) error {
	return s.underlying.Put(ctx, &physical.Entry{
		Key:      entry.Key,
		Value:    entry.Value,
		SealWrap: entry.SealWrap,
	})
}

func (s *LogicalStorage) Delete(ctx context.Context, key string) error {
	return s.underlying.Delete(ctx, key)
}

func (s *LogicalStorage) List(ctx context.Context, prefix string) ([]string, error) {
	return s.underlying.List(ctx, prefix)
}

func (s *LogicalStorage) Underlying() physical.Backend {
	return s.underlying
}

func NewLogicalStorage(underlying physical.Backend) *LogicalStorage {
	return &LogicalStorage{
		underlying: underlying,
	}
}
