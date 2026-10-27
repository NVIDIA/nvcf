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

package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeysAndValues(t *testing.T) {
	kv := KeysAndValues()
	assert.Len(t, kv, 8)
	assert.Equal(t, []any{"service", Service, "version", Version, "gitHash", GitHash, "releaseTag", ReleaseTag}, kv)
}

func TestString(t *testing.T) {
	oldVersion, oldHash := Version, GitHash
	t.Cleanup(func() { Version, GitHash = oldVersion, oldHash })

	Version, GitHash = "1.2.3", "abc123"
	assert.Equal(t, "1.2.3 (abc123)", String())
}
