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

package harness

import "testing"

func TestCommandCacheRecordAndHas(t *testing.T) {
	cache := NewCommandCache()
	cmd := "make install HELMFILE_ENV=local-bdd"

	if cache.Has(cmd) {
		t.Fatal("empty cache reports hit")
	}
	cache.Record(cmd)
	if !cache.Has(cmd) {
		t.Fatal("cache miss after Record")
	}
}

func TestCommandCacheKeyExactness(t *testing.T) {
	cache := NewCommandCache()
	cache.Record("make install HELMFILE_ENV=local-bdd")

	if cache.Has("make install HELMFILE_ENV=other") {
		t.Fatal("cache matched on different command text")
	}
	if cache.Has("make install HELMFILE_ENV=local-bdd ") {
		t.Fatal("cache matched on trailing-space variant")
	}
}
