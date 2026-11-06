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

package bdd_tmp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/steps"
)

// TestUpgradeProfileExistingInstall checks the export handoff without changing
// an installed stack. Use an isolated stack directory: export writes its profile.
func TestUpgradeProfileExistingInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	if os.Getenv("BDD_CLEANUP_MODE") != "" {
		t.Fatal("existing-install validation requires BDD_CLEANUP_MODE to be unset")
	}
	runLiveFeature(t, "upgrade-profile.feature")
}

func TestUpgradeProfileFeatureFileWiresToSteps(t *testing.T) {
	runner := newFakeRunner(nil)
	suite := newWiringSuite(t, runner)
	stack := filepath.Join(suite.Config.RepoRoot, "stack")
	if err := os.MkdirAll(filepath.Join(stack, "environments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stack, "environments", "upgrade.yaml"), []byte("addons:\n  llm:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"NVCF_CLI":           "/usr/bin/nvcf-cli",
		"UPGRADE_CLI_CONFIG": "/site/cli.yaml",
		"UPGRADE_STACK":      stack,
		"UPGRADE_ENV":        "upgrade",
		"UPGRADE_CLUSTER":    "upgrade-test",
		"UPGRADE_KUBECONFIG": "/site/kubeconfig",
		"UPGRADE_CONTEXT":    "upgrade-context",
	} {
		t.Setenv(key, value)
	}
	sc := steps.NewScenarioContext(suite)
	var output strings.Builder
	status := godog.TestSuite{
		Name: "upgrade-profile-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{mustResolveFeaturePath(t, "upgrade-profile.feature")},
			Strict: true,
			Output: &output,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, output.String())
	}
	if !commandRanThatContains(runner.runs, `profile validate --file "`+stack+`/out/control-plane-profile.yaml" --require compute-reachable`) {
		t.Fatal("fresh exported profile was not validated")
	}
}
