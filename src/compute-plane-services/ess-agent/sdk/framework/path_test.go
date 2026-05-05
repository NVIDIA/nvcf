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
	"testing"

	"github.com/go-test/deep"
)

func TestPath_Regex(t *testing.T) {
	tests := []struct {
		pattern   string
		input     string
		pathMatch bool
		captures  map[string]string
	}{
		{
			pattern:   "a/b/" + GenericNameRegex("val"),
			input:     "a/b/foo",
			pathMatch: true,
			captures:  map[string]string{"val": "foo"},
		},
		{
			pattern:   "a/b/" + GenericNameRegex("val"),
			input:     "a/b/foo/more",
			pathMatch: false,
			captures:  nil,
		},
		{
			pattern:   "a/b/" + GenericNameRegex("val"),
			input:     "a/b/abc-.123",
			pathMatch: true,
			captures:  map[string]string{"val": "abc-.123"},
		},
		{
			pattern:   "a/b/" + GenericNameRegex("val") + "/c/d",
			input:     "a/b/foo/c/d",
			pathMatch: true,
			captures:  map[string]string{"val": "foo"},
		},
		{
			pattern:   "a/b/" + GenericNameRegex("val") + "/c/d",
			input:     "a/b/foo/c/d/e",
			pathMatch: false,
			captures:  nil,
		},
		{
			pattern:   "a/b" + OptionalParamRegex("val"),
			input:     "a/b",
			pathMatch: true,
			captures:  map[string]string{"val": ""},
		},
		{
			pattern:   "a/b" + OptionalParamRegex("val"),
			input:     "a/b/foo",
			pathMatch: true,
			captures:  map[string]string{"val": "foo"},
		},
		{
			pattern:   "foo/" + MatchAllRegex("val"),
			input:     "foos/ball",
			pathMatch: false,
			captures:  nil,
		},
		{
			pattern:   "foos/" + MatchAllRegex("val"),
			input:     "foos/ball",
			pathMatch: true,
			captures:  map[string]string{"val": "ball"},
		},
		{
			pattern:   "foos/ball/" + MatchAllRegex("val"),
			input:     "foos/ball/with/more/stuff/at_the/end",
			pathMatch: true,
			captures:  map[string]string{"val": "with/more/stuff/at_the/end"},
		},
		{
			pattern:   MatchAllRegex("val"),
			input:     "foos/ball/with/more/stuff/at_the/end",
			pathMatch: true,
			captures:  map[string]string{"val": "foos/ball/with/more/stuff/at_the/end"},
		},
	}

	for i, test := range tests {
		b := Backend{
			Paths: []*Path{{Pattern: test.pattern}},
		}
		path, captures := b.route(test.input)
		pathMatch := path != nil
		if pathMatch != test.pathMatch {
			t.Fatalf("[%d] unexpected path match result (%s): expected %t, actual %t", i, test.pattern, test.pathMatch, pathMatch)
		}
		if diff := deep.Equal(captures, test.captures); diff != nil {
			t.Fatal(diff)
		}
	}
}
