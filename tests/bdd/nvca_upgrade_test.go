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
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

// nvcaUpgradeHTTPSequence supplies canned observations for step wiring only.
// Product behavior is exercised by TestNVCAUpgrade against the real lab.
type nvcaUpgradeHTTPSequence struct {
	bodies []string
	calls  int
}

func (f *nvcaUpgradeHTTPSequence) Do(_ context.Context, _, _ string, _ map[string]string, _ string) (harness.HTTPResponse, error) {
	if f.calls >= len(f.bodies) {
		return harness.HTTPResponse{}, fmt.Errorf("unexpected HTTP call %d", f.calls)
	}
	body := f.bodies[f.calls]
	f.calls++
	return harness.HTTPResponse{Status: 200, Body: body}, nil
}

func TestNVCAUpgradeFeatureFileWiresToSteps(t *testing.T) {
	for name, value := range map[string]string{
		"REPO_ROOT": "/repo", "NVCA_BDD_KUBECONFIG": "/lab/kubeconfig",
		"NVCA_BDD_GATEWAY": "http://fixture", "NVCA_BDD_OPERATOR_REPOSITORY": "local/operator",
		"NVCA_BDD_PROBE_REPOSITORY": "local/probe", "NVCA_BDD_VERSION_A": "0.0.1",
		"NVCA_BDD_VERSION_B": "0.0.2",
	} {
		t.Setenv(name, value)
	}
	runner := newFakeRunner(nil)
	runner.respond = func(command string) (harness.Result, bool) {
		if strings.Contains(command, "get deployment nvca -o") {
			return harness.Result{ExitCode: 0, Stdout: "local/probe:0.0.1 local/probe:0.0.2"}, true
		}
		if strings.Contains(command, "get nvcfbackend bdd-upgrade") {
			return harness.Result{ExitCode: 0, Stdout: "0.0.1 0.0.2"}, true
		}
		return harness.Result{}, false
	}
	suite := newWiringSuite(t, runner)
	t.Cleanup(func() {
		if err := suite.Teardown(); err != nil {
			t.Error(err)
		}
	})
	suite.HTTP = &nvcaUpgradeHTTPSequence{bodies: []string{
		`{"clusterId":"cluster"}`,
		`{}`, `{"nvcaVersion":"0.0.2"}`, `{}`, `{"nvcaVersion":"0.0.2"}`, `{}`, `{"nvcaVersion":"0.0.2"}`,
		`{}`, `{"nvcaVersion":"0.0.1"}`, `{}`, `{"nvcaVersion":"0.0.1"}`,
		`{"clusterId":"helm-cluster"}`, `{}`,
		`{"clusterSource":"helm-managed","nvcaVersion":"0.0.2"}`,
	}}
	sc := steps.NewScenarioContext(suite)
	status := godog.TestSuite{
		Name: "nvca-upgrade-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{mustResolveFeaturePath(t, "nvca-upgrade.feature")},
			Strict: true, StopOnFailure: true, Output: io.Discard,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("feature wiring status = %d", status)
	}
	if !commandRanThatContains(runner.runs, "--replicas=0") ||
		!commandRanThatContains(runner.runs, "--replicas=1") {
		t.Fatal("operator pause/resume commands were not exercised")
	}
}
