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

package dsl

import "testing"

func TestInterpolateBracedOnly(t *testing.T) {
	t.Setenv("FOO", "alpha")
	t.Setenv("BAR_QUX", "beta")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single braced", "${FOO}", "alpha"},
		{"multi braced", "${FOO}/${BAR_QUX}", "alpha/beta"},
		{"missing var expands empty", "${MISSING}", ""},
		{"bare dollar word stays literal", "$oauthtoken:${FOO}", "$oauthtoken:alpha"},
		{"braced inside path", "nvcr.io/${FOO}/${BAR_QUX}/client:1.0", "nvcr.io/alpha/beta/client:1.0"},
		{"no interpolation needed", "plain string", "plain string"},
		{"lowercase var not interpolated", "${foo}", "${foo}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Interpolate(tc.in)
			if got != tc.want {
				t.Fatalf("Interpolate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
