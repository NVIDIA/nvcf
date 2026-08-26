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

package smoke

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

type featureFlag []string

func (f *featureFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *featureFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("smoke feature path must not be empty")
	}
	*f = append(*f, value)
	return nil
}

var (
	targetFlag   string
	planFlag     string
	tagsFlag     string
	featureFlags featureFlag
)

func init() {
	flag.StringVar(&targetFlag, "bdd-target", "", "path to a smoke target YAML")
	flag.StringVar(&planFlag, "bdd-plan", "", "path to a committed smoke plan YAML")
	flag.StringVar(&tagsFlag, "bdd-tags", "", "Godog tag filter for direct feature selection")
	flag.Var(&featureFlags, "bdd-feature", "safe smoke feature path; repeat to select several")
}

// TestLive runs a committed plan or directly selected safe features against
// one target through a shared, isolated suite. It skips under -short.
func TestLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	config, err := harness.ResolveConfig()
	if err != nil {
		t.Fatalf("resolve harness config: %v", err)
	}
	targetPath := strings.TrimSpace(targetFlag)
	if targetPath == "" {
		t.Fatal("-bdd-target must name a smoke target")
	}
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(config.RepoRoot, targetPath)
	}
	target, err := LoadTarget(targetPath)
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	plan, err := LoadSelection(config.RepoRoot, planFlag, featureFlags, tagsFlag)
	if err != nil {
		t.Fatalf("load smoke selection: %v", err)
	}
	if err := plan.ValidateConsent(target); err != nil {
		t.Fatalf("validate plan consent: %v", err)
	}
	if strings.TrimSpace(os.Getenv("BDD_CLEANUP_MODE")) != "" {
		t.Fatal("BDD_CLEANUP_MODE is install-specific and must be unset for the smoke suite")
	}

	suite, err := harness.NewSuiteWithOptions(t, harness.SuiteOptions{
		CLIConfigPath:   target.Env[cliConfigEnvironment],
		IsolateCLIState: true,
	})
	if err != nil {
		t.Fatalf("new smoke suite: %v", err)
	}
	defer func() {
		if err := suite.Teardown(); err != nil {
			t.Errorf("teardown: %v", err)
		}
	}()
	runID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	environment := target.Environment(runID)
	environment[cliConfigEnvironment] = suite.CLIConfigPath
	for name, value := range environment {
		t.Setenv(name, value)
	}

	for index, phase := range plan.Phases {
		runFeature(t, suite, fmt.Sprintf("phase-%d-%s", index+1, phase.Name), phase.Feature, phase.Tags)
		restoreFeatureState(t, suite)
	}
}

func runFeature(t *testing.T, suite *harness.Suite, name, path, tags string) {
	t.Helper()
	scenario := steps.NewScenarioContext(suite)
	harness.RunFeature(t, harness.FeatureRunOptions{
		Name: "bdd-smoke-" + name,
		Path: path,
		Tags: tags,
	}, func(ctx *godog.ScenarioContext) {
		steps.RegisterSmoke(ctx, scenario)
	})
	fmt.Fprintf(os.Stderr, ">>> completed %s phase\n", name)
}

// restoreFeatureState prevents exported values and file mutations from
// becoming undocumented inputs to the next independently selectable feature.
// The built CLI remains suite-scoped. Isolated CLI state, the successful-command
// cache, and feature-owned ledgers return to their baseline between phases.
func restoreFeatureState(t *testing.T, suite *harness.Suite) {
	t.Helper()
	if err := suite.RestoreFeatureState(); err != nil {
		t.Fatalf("restore feature state: %v", err)
	}
}
