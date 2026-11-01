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

package harness

import (
	"strings"
	"testing"
)

func TestCheckExternalToolsNilSucceeds(t *testing.T) {
	if err := CheckExternalTools(nil); err != nil {
		t.Fatalf("CheckExternalTools(nil) returned error: %v", err)
	}
}

func TestCheckExternalToolsFoundTool(t *testing.T) {
	// "sh" is present on every supported platform.
	if err := CheckExternalTools([]string{"sh"}); err != nil {
		t.Fatalf("CheckExternalTools reported sh as missing: %v", err)
	}
}

func TestCheckExternalToolsMissingToolReportsName(t *testing.T) {
	const absent = "nvcf-bdd-tool-that-does-not-exist"
	err := CheckExternalTools([]string{absent})
	if err == nil {
		t.Fatal("CheckExternalTools returned nil for a missing tool")
	}
	if !strings.Contains(err.Error(), absent) {
		t.Fatalf("error does not contain tool name %q: %v", absent, err)
	}
}

func TestCheckExternalToolsReportsAllMissing(t *testing.T) {
	names := []string{"nvcf-bdd-absent-1", "nvcf-bdd-absent-2"}
	err := CheckExternalTools(names)
	if err == nil {
		t.Fatal("CheckExternalTools returned nil for missing tools")
	}
	for _, name := range names {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not contain tool name %q: %v", name, err)
		}
	}
}
