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

package portable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPlanPreservesOrderedPhasesAndTags(t *testing.T) {
	root := seedFeatures(t, "setup.feature", "smoke/a.feature", "smoke/b.feature")
	path := writePlan(t, root, `
version: 1
name: local-smoke
phases:
  - name: setup
    feature: setup.feature
    tags: "~@skip"
    mutatesTopology: true
    consent:
      environment: BDD_ALLOW_TOPOLOGY_MUTATION
      equalsTargetEnvironment: BDD_TARGET_APPROVAL
  - name: smoke-a
    feature: smoke/a.feature
    mutatesTopology: false
  - name: smoke-b
    feature: smoke/b.feature
    mutatesTopology: false
`)
	plan, err := LoadPlan(root, path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(plan.Phases) != 3 || filepath.Base(plan.Phases[0].Feature) != "setup.feature" {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Phases[0].Tags != "~@skip" || !plan.Phases[0].MutatesTopology {
		t.Fatalf("first phase=%+v", plan.Phases[0])
	}
}

func TestLoadPlanRejectsInvalidComposition(t *testing.T) {
	root := seedFeatures(t, "provider.feature", "smoke.feature")
	for name, body := range map[string]string{
		"missing phases": `version: 1
name: empty
phases: []
`,
		"duplicate phase": `version: 1
name: duplicate
phases:
  - name: first
    feature: smoke.feature
    mutatesTopology: false
  - name: first
    feature: provider.feature
    mutatesTopology: false
`,
		"path traversal": `version: 1
name: escape
phases:
  - name: escape
    feature: ../outside.feature
    mutatesTopology: false
`,
		"topology without consent": `version: 1
name: unsafe
phases:
  - name: provider
    feature: provider.feature
    mutatesTopology: true
`,
		"unknown field": `version: 1
name: unknown
phases:
  - name: smoke
    feature: smoke.feature
    mutatesTopology: false
    role: smoke
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadPlan(root, writePlan(t, root, body)); err == nil {
				t.Fatal("expected plan error")
			}
		})
	}
}

func TestPlanValidatesInvocationConsentAgainstTarget(t *testing.T) {
	plan := Plan{Phases: []Phase{{
		Name: "maintenance",
		Consent: &Consent{
			Environment:             "BDD_ALLOW_CLUSTER_MAINTENANCE",
			EqualsTargetEnvironment: "BDD_COMPUTE_CLUSTER",
		},
	}}}
	target := Target{Env: map[string]string{"BDD_COMPUTE_CLUSTER": "compute-1"}}
	if err := plan.ValidateConsent(target); err == nil {
		t.Fatal("expected missing consent error")
	}
	t.Setenv("BDD_ALLOW_CLUSTER_MAINTENANCE", "other")
	if err := plan.ValidateConsent(target); err == nil {
		t.Fatal("expected mismatched consent error")
	}
	t.Setenv("BDD_ALLOW_CLUSTER_MAINTENANCE", "compute-1")
	if err := plan.ValidateConsent(target); err != nil {
		t.Fatalf("validate consent: %v", err)
	}
	target.Env["BDD_ALLOW_CLUSTER_MAINTENANCE"] = "compute-1"
	if err := plan.ValidateConsent(target); err == nil {
		t.Fatal("expected target-supplied consent error")
	}
}

func TestCommittedPlansMatchSchema(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join("..", "plans", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no committed plans")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			absolutePath, err := filepath.Abs(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPlan(repoRoot, absolutePath); err != nil {
				t.Fatalf("load committed plan: %v", err)
			}
		})
	}
}

func writePlan(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, "tests", "bdd", "plans", "plan.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
