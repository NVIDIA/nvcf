// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

func TestNewReportsBackendNotCompiledIn(t *testing.T) {
	_, err := New(config.Config{Backend: config.BackendKratos})
	if err == nil {
		t.Fatal("New() error = nil, want not-compiled-in error")
	}
	if !strings.Contains(err.Error(), "not compiled into this build") {
		t.Fatalf("New() error = %v, want not-compiled-in error", err)
	}
}

func TestRegisterAndNew(t *testing.T) {
	t.Cleanup(func() {
		registryMu.Lock()
		delete(registry, config.BackendObjectStore)
		registryMu.Unlock()
	})

	Register(config.BackendObjectStore, func(config.Config) (Client, error) { return nil, nil })
	if _, err := New(config.Config{Backend: config.BackendObjectStore}); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := Registered(); len(got) != 1 || got[0] != string(config.BackendObjectStore) {
		t.Fatalf("Registered() = %v, want [objectstore]", got)
	}
}

func TestNewWrapsFactoryError(t *testing.T) {
	t.Cleanup(func() {
		registryMu.Lock()
		delete(registry, config.BackendObjectStore)
		registryMu.Unlock()
	})

	sentinel := errors.New("missing credentials")
	Register(config.BackendObjectStore, func(config.Config) (Client, error) { return nil, sentinel })

	_, err := New(config.Config{Backend: config.BackendObjectStore})
	if err == nil {
		t.Fatal("New() error = nil, want factory error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("New() error = %v, want it to wrap the factory error", err)
	}
	if !strings.Contains(err.Error(), string(config.BackendObjectStore)) {
		t.Fatalf("New() error = %v, want it to name the backend", err)
	}
}
