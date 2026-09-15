// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import "testing"

func TestValidateTagAllowsLegacyReleaseTags(t *testing.T) {
	tests := []string{
		"v0.1.0",
		"v1.9.9",
		"v2.52.3",
		"v2.52.3-rc.1",
		"v2.52.3-dev.2",
		"v3.0.0",
		"v3.0.12",
		"v3.0.0-rc.27",
		"v3.0.0-dev.1",
	}

	for _, tag := range tests {
		t.Run(tag, func(t *testing.T) {
			if err := validateTag(tag); err != nil {
				t.Fatalf("validateTag(%q) returned unexpected error: %v", tag, err)
			}
		})
	}
}

func TestValidateTagRejectsUnsupportedReleaseTags(t *testing.T) {
	tests := []string{
		"",
		"3.0.0",
		"v3",
		"v3.0",
		"v3.0.x",
		"v3.1.0",
		"v3.1.0-rc.1",
		"v3.2.0",
		"v3.3.0-rc.1",
		"v3.3.0-dev.1",
		"v4.0.0",
		"v10.0.0",
		"v2.52.3-beta.1",
		"foo",
	}

	for _, tag := range tests {
		t.Run(tag, func(t *testing.T) {
			if err := validateTag(tag); err == nil {
				t.Fatalf("validateTag(%q) returned nil error", tag)
			}
		})
	}
}
