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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsDNSLabel(t *testing.T) {
	for _, ok := range []string{"a", "use1-a", "0", strings.Repeat("a", 63)} {
		assert.True(t, IsDNSLabel(ok), ok)
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a.b", "a_b", strings.Repeat("a", 64)} {
		assert.False(t, IsDNSLabel(bad), bad)
	}
}
