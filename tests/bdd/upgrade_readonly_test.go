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
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

// TestUpgradeReadOnlyChecks exercises the downloaded scripts against an existing installation.
func TestUpgradeReadOnlyChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	if os.Getenv("BDD_CLEANUP_MODE") != "" {
		t.Fatal("read-only checks require BDD_CLEANUP_MODE to be unset")
	}
	runLiveFeature(t, "upgrade-readonly-checks.feature")
}

func TestUpgradeReadOnlyChecksFeatureFileWiresToSteps(t *testing.T) {
	for _, name := range []string{"REPO_ROOT", "CONTROL_KUBECONFIG", "CONTROL_CONTEXT",
		"COMPUTE_KUBECONFIG", "COMPUTE_CONTEXT", "EXPECTED_CONTROL_STACK_VERSION"} {
		t.Setenv(name, "/synthetic/"+name)
	}
	runner := newFakeRunner(nil)
	runner.respond = func(command string) (harness.Result, bool) {
		if strings.HasPrefix(command, "env EXPECTED_CONTROL_STACK_VERSION=not-the-selected-release ") || strings.Contains(command, "missing-upgrade-test-context") {
			return harness.Result{ExitCode: 1}, true
		}
		return harness.Result{ExitCode: 0}, true
	}
	suite := newWiringSuite(t, runner)
	sc := steps.NewScenarioContext(suite)
	var out strings.Builder
	status := godog.TestSuite{
		Name:                "upgrade-readonly-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) { steps.RegisterAll(ctx, sc) },
		Options:             &godog.Options{Format: "pretty", Paths: []string{mustResolveFeaturePath(t, "upgrade-readonly-checks.feature")}, Strict: true, Output: &out},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	if !commandRanThatContains(runner.runs, "fern/assets/upgrade-checks/check-upgrade-receipt.py") {
		t.Fatal("downloaded receipt check was not invoked")
	}
}
