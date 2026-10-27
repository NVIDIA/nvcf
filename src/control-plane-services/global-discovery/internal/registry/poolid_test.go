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

package registry

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

var uuidV5 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestPoolID(t *testing.T) {
	id := PoolID("prod", "use1-a", "llama-blue")
	assert.Regexp(t, uuidV5, id)
	// Golden value, cross-checked with Python uuid.uuid5: the id is part of
	// the API (clients persist it).
	assert.Equal(t, "a9ca9a69-b33e-5ff3-9e69-9670d2b01e06", id)
	assert.Equal(t, id, PoolID("prod", "use1-a", "llama-blue"))
	assert.NotEqual(t, id, PoolID("prod", "use1-b", "llama-blue"))
	assert.NotEqual(t, id, PoolID("dev", "use1-a", "llama-blue"))
}

func TestValidPoolID(t *testing.T) {
	for _, ok := range []string{"p", "7f1c-ab", PoolID("a", "b", "c"), "pool_1.x"} {
		assert.True(t, ValidPoolID(ok), ok)
	}
	for _, bad := range []string{"", "-a", "a/b", "a b", string(make([]byte, 129))} {
		assert.False(t, ValidPoolID(bad), bad)
	}
}
