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

package translateutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewBoolPtr(t *testing.T) {
	got := NewBoolPtr(true)
	assert.NotNil(t, got)
	assert.True(t, *got)
}

func TestMergeMaps(t *testing.T) {
	got := MergeMaps(
		map[string]string{"left": "keep", "shared": "old"},
		map[string]string{"right": "add", "shared": "new"},
	)

	assert.Equal(t, map[string]string{
		"left":   "keep",
		"right":  "add",
		"shared": "new",
	}, got)
}
