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

package live

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlanSeparatesProviderAndSmokes(t *testing.T) {
	root := seedFeatures(t, "provider.feature", "smoke/a.feature", "smoke/b.feature")
	t.Setenv("BDD_PROVIDER_FEATURE", "provider.feature")
	t.Setenv("BDD_SMOKE_FEATURES", "smoke/a.feature, smoke/b.feature")
	plan, err := LoadPlan(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if filepath.Base(plan.ProviderFeature) != "provider.feature" || len(plan.SmokeFeatures) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestLoadPlanSupportsAttachMode(t *testing.T) {
	root := seedFeatures(t, "smoke.feature")
	t.Setenv("BDD_PROVIDER_FEATURE", "")
	t.Setenv("BDD_SMOKE_FEATURES", "smoke.feature")
	plan, err := LoadPlan(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if plan.ProviderFeature != "" || len(plan.SmokeFeatures) != 1 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestLoadPlanRejectsMissingDuplicateAndEscapingFeatures(t *testing.T) {
	root := seedFeatures(t, "provider.feature", "smoke.feature")
	for name, selection := range map[string][2]string{
		"missing smokes": {"", ""},
		"duplicate":      {"provider.feature", "provider.feature"},
		"path traversal": {"", "../outside.feature"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BDD_PROVIDER_FEATURE", selection[0])
			t.Setenv("BDD_SMOKE_FEATURES", selection[1])
			if _, err := LoadPlan(root); err == nil {
				t.Fatal("expected plan error")
			}
		})
	}
}

func seedFeatures(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		path := filepath.Join(root, "tests", "bdd", "features", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("Feature: test\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}
