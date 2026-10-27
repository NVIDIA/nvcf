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

// Package storefactory opens the store backend that the configuration
// selects. It is the seam where store implementations plug into gds-server
// and gds-admin: the DynamoDB store adds its opener to openers (or calls
// Register from its own package) when it is built into the binaries.
package storefactory

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/memory"
)

// Opener opens a store from the configuration (--store, --dynamodb-table,
// --dynamodb-endpoint, --region).
type Opener func(ctx context.Context, cfg *config.Config) (store.Store, error)

var (
	mu      sync.RWMutex
	openers = map[string]Opener{
		config.StoreMemory: func(context.Context, *config.Config) (store.Store, error) { return memory.New(), nil },
	}
)

// Register makes a backend available under kind, replacing any previous
// opener of that kind.
func Register(kind string, open Opener) {
	mu.Lock()
	defer mu.Unlock()
	openers[kind] = open
}

// Kinds returns the registered backend kinds, sorted.
func Kinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	kinds := make([]string, 0, len(openers))
	for k := range openers {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// Open opens the backend named by cfg.Store.
func Open(ctx context.Context, cfg *config.Config) (store.Store, error) {
	mu.RLock()
	open, ok := openers[cfg.Store]
	mu.RUnlock()
	switch {
	case ok:
		return open(ctx, cfg)
	case cfg.Store == config.StoreDynamoDB:
		return nil, fmt.Errorf("--store=%s: the DynamoDB store is not built into this binary yet; use --store=%s",
			config.StoreDynamoDB, config.StoreMemory)
	}
	return nil, fmt.Errorf("--store=%s: unknown store; available: %v", cfg.Store, Kinds())
}
