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
	"io"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

// TestChartImageValues runs direct Helm upgrades against an isolated QA cluster.
func TestChartImageValues(t *testing.T) {
	if testing.Short() {
		t.Skip("live Helm upgrades skipped under -short")
	}
	runLiveFeature(t, "chart-image-values.feature")
}

// TestChartImageValuesWiring verifies the feature uses existing steps.
func TestChartImageValuesWiring(t *testing.T) {
	t.Setenv("REPO_ROOT", "/repo")
	t.Setenv("IMAGE_VALUES_CHART_ROOT", "/charts")
	t.Setenv("IMAGE_VALUES_QA_ROOT", "/qa")
	t.Setenv("IMAGE_VALUES_MIRROR_PREFIX", "fixture-mirror.example")
	runner := newFakeRunner(nil)
	runner.respond = func(string) (harness.Result, bool) {
		return harness.Result{ExitCode: 0, Stdout: "fixture-mirror.example/renamed/server:v1"}, true
	}
	suite := newWiringSuite(t, runner)
	scenario := steps.NewScenarioContext(suite)
	status := godog.TestSuite{
		Name: "chart-image-values-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, scenario)
		},
		Options: &godog.Options{
			Format: "progress",
			Paths:  []string{mustResolveFeaturePath(t, "chart-image-values.feature")},
			Strict: true,
			Output: io.Discard,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("image catalog feature wiring failed: %d", status)
	}
}
