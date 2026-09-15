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

	log "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/sdk/helper/logging"
	"github.com/hashicorp/vault/sdk/physical"
)

func TestPhysicalView_impl(t *testing.T) {
	var _ physical.Backend = new(physical.View)
}

func newInmemTestBackend() (physical.Backend, error) {
	logger := logging.NewVaultLogger(log.Debug)
	return NewInmem(nil, logger)
}

func TestPhysicalView_BadKeysKeys(t *testing.T) {
	backend, err := newInmemTestBackend()
	if err != nil {
		t.Fatal(err)
	}
	view := physical.NewView(backend, "foo/")

	_, err = view.List(context.Background(), "../")
	if err == nil {
		t.Fatalf("expected error")
	}

	_, err = view.Get(context.Background(), "../")
	if err == nil {
		t.Fatalf("expected error")
	}

	err = view.Delete(context.Background(), "../foo")
	if err == nil {
		t.Fatalf("expected error")
	}

	le := &physical.Entry{
		Key:   "../foo",
		Value: []byte("test"),
	}
	err = view.Put(context.Background(), le)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPhysicalView(t *testing.T) {
	backend, err := newInmemTestBackend()
	if err != nil {
		t.Fatal(err)
	}

	view := physical.NewView(backend, "foo/")

	// Write a key outside of foo/
	entry := &physical.Entry{Key: "test", Value: []byte("test")}
	if err := backend.Put(context.Background(), entry); err != nil {
		t.Fatalf("bad: %v", err)
	}

	// List should have no visibility
	keys, err := view.List(context.Background(), "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("bad: %v", err)
	}

	// Get should have no visibility
	out, err := view.Get(context.Background(), "test")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != nil {
		t.Fatalf("bad: %v", out)
	}

	// Try to put the same entry via the view
	if err := view.Put(context.Background(), entry); err != nil {
		t.Fatalf("err: %v", err)
	}

	// Check it is nested
	entry, err = backend.Get(context.Background(), "foo/test")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if entry == nil {
		t.Fatalf("missing nested foo/test")
	}

	// Delete nested
	if err := view.Delete(context.Background(), "test"); err != nil {
		t.Fatalf("err: %v", err)
	}

	// Check the nested key
	entry, err = backend.Get(context.Background(), "foo/test")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if entry != nil {
		t.Fatalf("nested foo/test should be gone")
	}

	// Check the non-nested key
	entry, err = backend.Get(context.Background(), "test")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if entry == nil {
		t.Fatalf("root test missing")
	}
}
