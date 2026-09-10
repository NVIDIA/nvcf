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

import (
	"strings"
	"testing"
)

func TestRequireEnvironmentVariables(t *testing.T) {
	t.Setenv("BDD_ENV_PRESENT_ONE", "one")
	t.Setenv("BDD_ENV_PRESENT_TWO", "two")
	if err := RequireEnvironmentVariables([]string{"BDD_ENV_PRESENT_ONE", "BDD_ENV_PRESENT_TWO"}); err != nil {
		t.Fatalf("require present variables: %v", err)
	}
}

func TestRequireEnvironmentVariablesReportsMissingName(t *testing.T) {
	t.Setenv("BDD_ENV_PRESENT", "present")
	t.Setenv("BDD_ENV_MISSING_EXACT", "")
	err := RequireEnvironmentVariables([]string{"BDD_ENV_PRESENT", "BDD_ENV_MISSING_EXACT"})
	if err == nil {
		t.Fatal("expected missing-variable error")
	}
	if !strings.Contains(err.Error(), `"BDD_ENV_MISSING_EXACT"`) {
		t.Fatalf("error %q does not name the missing variable", err)
	}
}

func TestRequireEnvironmentVariablesRejectsEmptyNames(t *testing.T) {
	for _, names := range [][]string{nil, {""}, {"  "}} {
		if err := RequireEnvironmentVariables(names); err == nil {
			t.Fatalf("RequireEnvironmentVariables(%q) succeeded, want error", names)
		}
	}
}
