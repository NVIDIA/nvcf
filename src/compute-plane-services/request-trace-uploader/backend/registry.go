// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"fmt"
	"sort"
	"sync"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

// Factory builds one backend from the loaded configuration.
type Factory func(config.Config) (Client, error)

var (
	registryMu sync.RWMutex
	registry   = map[config.Backend]Factory{}
)

// Register makes a backend available to New. Implementations call this from an
// init function, so a build that does not link the implementation does not
// carry its dependencies. Registering the same backend twice panics, because
// that can only be a build wiring mistake.
func Register(name config.Backend, factory Factory) {
	if factory == nil {
		panic(fmt.Sprintf("backend %q registered with a nil factory", name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("backend %q registered twice", name))
	}
	registry[name] = factory
}

// New builds the configured backend. A backend that is valid configuration but
// absent from this build reports that it was not compiled in, so an operator
// can tell a wiring mistake from a bad setting.
func New(cfg config.Config) (Client, error) {
	registryMu.RLock()
	factory, ok := registry[cfg.Backend]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("backend %q is not compiled into this build (available: %v)", cfg.Backend, Registered())
	}
	return factory(cfg)
}

// Registered lists the backends linked into this build, sorted for stable
// error messages and logs.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, string(name))
	}
	sort.Strings(names)
	return names
}
