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

func TestPolicyMap(t *testing.T) {
	p := &PolicyMap{}
	p.PathMap.Name = "foo"
	s := new(logical.InmemStorage)

	ctx := context.Background()

	p.Put(ctx, s, "foo", map[string]interface{}{"value": "bar"})
	p.Put(ctx, s, "bar", map[string]interface{}{"value": "foo,baz "})

	// Read via API
	actual, err := p.Policies(ctx, s, "foo", "bar")
	if err != nil {
		t.Fatalf("bad: %#v", err)
	}

	expected := []string{"bar", "baz", "foo"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: %#v", actual)
	}
}
