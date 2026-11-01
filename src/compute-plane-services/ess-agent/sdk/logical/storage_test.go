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
	"testing"

	"github.com/go-test/deep"
)

var keyList = []string{
	"a",
	"b",
	"d",
	"foo",
	"foo42",
	"foo/a/b/c",
	"c/d/e/f/g",
}

func TestScanView(t *testing.T) {
	s := prepKeyStorage(t)

	keys := make([]string, 0)
	err := ScanView(context.Background(), s, func(path string) {
		keys = append(keys, path)
	})
	if err != nil {
		t.Fatal(err)
	}

	if diff := deep.Equal(keys, keyList); diff != nil {
		t.Fatal(diff)
	}
}

func TestScanView_CancelContext(t *testing.T) {
	s := prepKeyStorage(t)

	ctx, cancelCtx := context.WithCancel(context.Background())
	var i int
	err := ScanView(ctx, s, func(path string) {
		cancelCtx()
		i++
	})

	if err == nil {
		t.Error("Want context cancel err, got none")
	}
	if i != 1 {
		t.Errorf("Want i==1, got %d", i)
	}
}

func TestCollectKeys(t *testing.T) {
	s := prepKeyStorage(t)

	keys, err := CollectKeys(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	if diff := deep.Equal(keys, keyList); diff != nil {
		t.Fatal(diff)
	}
}

func TestCollectKeysPrefix(t *testing.T) {
	s := prepKeyStorage(t)

	keys, err := CollectKeysWithPrefix(context.Background(), s, "foo")
	if err != nil {
		t.Fatal(err)
	}

	exp := []string{
		"foo",
		"foo42",
		"foo/a/b/c",
	}

	if diff := deep.Equal(keys, exp); diff != nil {
		t.Fatal(diff)
	}
}

func prepKeyStorage(t *testing.T) Storage {
	t.Helper()
	s := &InmemStorage{}

	for _, key := range keyList {
		if err := s.Put(context.Background(), &StorageEntry{
			Key:      key,
			Value:    nil,
			SealWrap: false,
		}); err != nil {
			t.Fatal(err)
		}
	}

	return s
}
