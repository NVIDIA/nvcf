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

package api

import "testing"

func TestIsSudoPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		path     string
		expected bool
	}{
		{
			"/not/in/sudo/paths/list",
			false,
		},
		{
			"/sys/raw/single-node-path",
			true,
		},
		{
			"/sys/raw/multiple/nodes/path",
			true,
		},
		{
			"/sys/raw/WEIRD(but_still_valid!)p4Th?🗿笑",
			true,
		},
		{
			"/sys/auth/path/in/middle/tune",
			true,
		},
		{
			"/sys/plugins/catalog/some-type",
			true,
		},
		{
			"/sys/plugins/catalog/some/type/or/name/with/slashes",
			false,
		},
		{
			"/sys/plugins/catalog/some-type/some-name",
			true,
		},
		{
			"/sys/plugins/catalog/some-type/some/name/with/slashes",
			false,
		},
	}

	for _, tc := range testCases {
		result := IsSudoPath(tc.path)
		if result != tc.expected {
			t.Fatalf("expected api.IsSudoPath to return %v for path %s but it returned %v", tc.expected, tc.path, result)
		}
	}
}
