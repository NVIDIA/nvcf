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

func TestString(t *testing.T) {
	t.Cleanup(func() {
		Version = ""
		GitHash = ""
		Dirty = ""
		ReleaseTag = ""
	})

	type test struct {
		inputVersion string
		inputGitHash string
		inputDirty   string
		want         string
	}

	tests := []test{
		{
			inputVersion: "1.0.0",
			inputGitHash: "abcd1234",
			inputDirty:   "",
			want:         "1.0.0+abcd1234",
		},
		{
			inputVersion: "1.0.1",
			inputGitHash: "",
			inputDirty:   "",
			want:         "1.0.1",
		},
		{
			inputVersion: "1.0.2",
			inputGitHash: "",
			inputDirty:   "true",
			want:         "1.0.2",
		},
		{
			inputVersion: "1.0.3",
			inputGitHash: "abcd1234",
			inputDirty:   "true",
			want:         "1.0.3+abcd1234.dirty",
		},
	}

	for _, tc := range tests {
		t.Run("want="+tc.want, func(t *testing.T) {
			Version = tc.inputVersion
			GitHash = tc.inputGitHash
			Dirty = tc.inputDirty
			assert.Equal(t, tc.want, String())
		})
	}
}

func TestReleaseString(t *testing.T) {
	t.Cleanup(func() {
		Version = ""
		GitHash = ""
		Dirty = ""
		ReleaseTag = ""
	})

	type test struct {
		inputVersion    string
		inputGitHash    string
		inputDirty      string
		inputReleaseTag string
		want            string
	}

	tests := []test{
		{
			inputVersion: "1.0.0",
			inputGitHash: "abcd1234",
			inputDirty:   "",
			want:         "1.0.0+abcd1234",
		},
		{
			inputVersion: "1.0.1",
			inputGitHash: "",
			inputDirty:   "",
			want:         "1.0.1",
		},
		{
			inputVersion: "1.0.2",
			inputGitHash: "",
			inputDirty:   "true",
			want:         "1.0.2",
		},
		{
			inputVersion: "1.0.3",
			inputGitHash: "abcd1234",
			inputDirty:   "true",
			want:         "1.0.3+abcd1234",
		},
		{
			inputVersion:    "1.0.3",
			inputGitHash:    "abcd1234",
			inputDirty:      "true",
			inputReleaseTag: "v1.0.3",
			want:            "1.0.3",
		},
	}

	for _, tc := range tests {
		t.Run("want="+tc.want, func(t *testing.T) {
			Version = tc.inputVersion
			GitHash = tc.inputGitHash
			Dirty = tc.inputDirty
			ReleaseTag = tc.inputReleaseTag
			assert.Equal(t, tc.want, ReleaseString())
		})
	}
}
