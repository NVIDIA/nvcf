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

package framework

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
)

func TestWAL(t *testing.T) {
	s := new(logical.InmemStorage)

	ctx := context.Background()

	// WAL should be empty to start
	keys, err := ListWAL(ctx, s)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if len(keys) > 0 {
		t.Fatalf("bad: %#v", keys)
	}

	// Write an entry to the WAL
	id, err := PutWAL(ctx, s, "foo", "bar")
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// The key should be in the WAL
	keys, err = ListWAL(ctx, s)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if !reflect.DeepEqual(keys, []string{id}) {
		t.Fatalf("bad: %#v", keys)
	}

	// Should be able to get the value
	entry, err := GetWAL(ctx, s, id)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if entry.Kind != "foo" {
		t.Fatalf("bad: %#v", entry)
	}
	if entry.Data != "bar" {
		t.Fatalf("bad: %#v", entry)
	}

	// Should be able to delete the value
	if err := DeleteWAL(ctx, s, id); err != nil {
		t.Fatalf("err: %s", err)
	}
	entry, err = GetWAL(ctx, s, id)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if entry != nil {
		t.Fatalf("bad: %#v", entry)
	}
}
