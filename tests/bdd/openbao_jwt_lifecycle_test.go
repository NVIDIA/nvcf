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
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

func TestOpenBaoJWTPluginLifecycleFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("OPENBAO_JWT_TEST_CONTAINER", "jwt-lifecycle-wiring")
	t.Setenv("OPENBAO_JWT_RECOVERY_CONTAINER", "jwt-recovery-wiring")
	runner := newFakeRunner(nil)
	runner.respond = func(command string) (harness.Result, bool) {
		if strings.Contains(command, "bao plugin info") {
			return harness.Result{ExitCode: 2}, true
		}
		for _, action := range []string{"upgrade", "check-cleanup", "cleanup", "verify"} {
			if strings.HasSuffix(command, "runtime-bao.sh jwt-plugin-lifecycle "+action) {
				return harness.Result{ExitCode: 1}, true
			}
		}
		return harness.Result{ExitCode: 0}, true
	}
	suite := newWiringSuite(t, runner)
	sc := steps.NewScenarioContext(suite)
	var out strings.Builder
	status := godog.TestSuite{
		Name: "openbao-jwt-lifecycle-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{mustResolveFeaturePath(t, "openbao-jwt-plugin-lifecycle.feature")},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	if !commandRanThatContains(runner.runs, "jwt-plugin-lifecycle rollback") {
		t.Fatal("rollback command was never invoked")
	}
}

// TestOpenBaoJWTPluginLifecycle uses only the explicitly supplied disposable
// container. The feature never installs a stack or removes Docker resources.
func TestOpenBaoJWTPluginLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("live OpenBao lifecycle requires a prepared isolated container")
	}
	t.Setenv("BDD_CLEANUP_MODE", "")
	runLiveFeature(t, "openbao-jwt-plugin-lifecycle.feature")
}
