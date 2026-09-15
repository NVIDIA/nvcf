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

package custommetadata

import (
	"strconv"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name       string
		input      map[string]string
		shouldPass bool
	}{
		{
			"valid",
			map[string]string{
				"foo": "abc",
				"bar": "def",
				"baz": "ghi",
			},
			true,
		},
		{
			"too_many_keys",
			func() map[string]string {
				cm := make(map[string]string)

				for i := 0; i < maxKeyLength+1; i++ {
					s := strconv.Itoa(i)
					cm[s] = s
				}

				return cm
			}(),
			false,
		},
		{
			"key_too_long",
			map[string]string{
				strings.Repeat("a", maxKeyLength+1): "abc",
			},
			false,
		},
		{
			"value_too_long",
			map[string]string{
				"foo": strings.Repeat("a", maxValueLength+1),
			},
			false,
		},
		{
			"unprintable_key",
			map[string]string{
				"unprint\u200bable": "abc",
			},
			false,
		},
		{
			"unprintable_value",
			map[string]string{
				"foo": "unprint\u200bable",
			},
			false,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := Validate(tc.input)

			if tc.shouldPass && err != nil {
				t.Fatalf("expected validation to pass, input: %#v, err: %v", tc.input, err)
			}

			if !tc.shouldPass && err == nil {
				t.Fatalf("expected validation to fail, input: %#v, err: %v", tc.input, err)
			}
		})
	}
}
