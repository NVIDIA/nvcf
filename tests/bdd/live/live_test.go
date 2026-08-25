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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

// TestComposableLive runs an optional provider phase and one or more portable
// smoke features through one shared suite. It skips under -short.
func TestComposableLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	config, err := harness.ResolveConfig()
	if err != nil {
		t.Fatalf("resolve harness config: %v", err)
	}
	targetPath := strings.TrimSpace(os.Getenv("BDD_TARGET_FILE"))
	if targetPath == "" {
		t.Fatal("BDD_TARGET_FILE must name a live target")
	}
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(config.RepoRoot, targetPath)
	}
	target, err := LoadTarget(targetPath)
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	plan, err := LoadPlan(config.RepoRoot)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	cleanupMode, err := harness.ResolveCleanupMode()
	if err != nil {
		t.Fatalf("resolve cleanup: %v", err)
	}
	if cleanupMode != harness.CleanupNone {
		t.Fatal("BDD_CLEANUP_MODE is local-install-specific and must be unset for the portable live suite")
	}

	suite, err := harness.NewSuiteWithOptions(t, harness.SuiteOptions{
		CLIStatePath: target.NVCF.CLIState,
		CleanupMode:  cleanupMode,
	})
	if err != nil {
		t.Fatalf("new live suite: %v", err)
	}
	defer func() {
		if err := suite.Teardown(); err != nil {
			t.Errorf("teardown: %v", err)
		}
	}()
	runID := fmt.Sprintf("%s-%d", filepath.Base(suite.Config.OutDir), os.Getpid())
	for name, value := range target.Environment(runID) {
		t.Setenv(name, value)
	}

	if plan.ProviderFeature != "" {
		runFeature(t, suite, "provider", plan.ProviderFeature)
		restoreFeatureState(t, suite)
	}
	for index, path := range plan.SmokeFeatures {
		runFeature(t, suite, fmt.Sprintf("smoke-%d", index+1), path)
		restoreFeatureState(t, suite)
	}
}

func runFeature(t *testing.T, suite *harness.Suite, name, path string) {
	t.Helper()
	scenario := steps.NewScenarioContext(suite)
	status := godog.TestSuite{
		Name: "bdd-live-" + name,
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, scenario)
		},
		Options: &godog.Options{
			Format:        "pretty",
			Paths:         []string{path},
			Strict:        true,
			StopOnFailure: true,
			Concurrency:   1,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("%s phase status = %d", name, status)
	}
	fmt.Fprintf(os.Stderr, ">>> completed %s phase\n", name)
}

// restoreFeatureState prevents exported values and file mutations from
// becoming undocumented inputs to the next independently selectable feature.
// The built CLI, its state file, and successful command cache remain
// suite-scoped.
func restoreFeatureState(t *testing.T, suite *harness.Suite) {
	t.Helper()
	if err := suite.RestoreFeatureState(); err != nil {
		t.Fatalf("restore feature state: %v", err)
	}
}
