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
	"reflect"
	"time"

	testing "github.com/mitchellh/go-testing-interface"

	log "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/sdk/helper/logging"
)

// TestRequest is a helper to create a purely in-memory Request struct.
func TestRequest(t testing.T, op Operation, path string) *Request {
	return &Request{
		Operation:  op,
		Path:       path,
		Data:       make(map[string]interface{}),
		Storage:    new(InmemStorage),
		Connection: &Connection{},
	}
}

// TestStorage is a helper that can be used from unit tests to verify
// the behavior of a Storage impl.
func TestStorage(t testing.T, s Storage) {
	keys, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list error: %s", err)
	}
	if len(keys) > 0 {
		t.Fatalf("should have no keys to start: %#v", keys)
	}

	entry := &StorageEntry{Key: "foo", Value: []byte("bar")}
	if err := s.Put(context.Background(), entry); err != nil {
		t.Fatalf("put error: %s", err)
	}

	actual, err := s.Get(context.Background(), "foo")
	if err != nil {
		t.Fatalf("get error: %s", err)
	}
	if !reflect.DeepEqual(actual, entry) {
		t.Fatalf("wrong value. Expected: %#v\nGot: %#v", entry, actual)
	}

	keys, err = s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list error: %s", err)
	}
	if !reflect.DeepEqual(keys, []string{"foo"}) {
		t.Fatalf("bad keys: %#v", keys)
	}

	if err := s.Delete(context.Background(), "foo"); err != nil {
		t.Fatalf("put error: %s", err)
	}

	keys, err = s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list error: %s", err)
	}
	if len(keys) > 0 {
		t.Fatalf("should have no keys to start: %#v", keys)
	}
}

func TestSystemView() *StaticSystemView {
	defaultLeaseTTLVal := time.Hour * 24
	maxLeaseTTLVal := time.Hour * 24 * 2
	return &StaticSystemView{
		DefaultLeaseTTLVal: defaultLeaseTTLVal,
		MaxLeaseTTLVal:     maxLeaseTTLVal,
		VersionString:      "testVersionString",
	}
}

func TestBackendConfig() *BackendConfig {
	bc := &BackendConfig{
		Logger: logging.NewVaultLogger(log.Trace),
		System: TestSystemView(),
		Config: make(map[string]string),
	}

	return bc
}
