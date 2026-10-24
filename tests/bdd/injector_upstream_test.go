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
	"github.com/cucumber/godog"
	"io"
	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
	"testing"
)

// TestOpenBaoInjectorUpstream exercises a prepared isolated backend and the
// published wrapper. The operator owns fixture setup and cluster cleanup.
func TestOpenBaoInjectorUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "openbao-injector-upstream.feature")
}

func TestOpenBaoInjectorUpstreamFeatureFileWiresToSteps(t *testing.T) {
	for name, value := range map[string]string{
		"KUBECONFIG": "/isolated/config", "INJECTOR_CONTEXT": "isolated",
		"INJECTOR_STACK_DIR": "/isolated/stack", "INJECTOR_ENV": "injector-test",
		"INJECTOR_EXPECTED_IMAGE": "docker.io/hashicorp/vault-k8s:fixture",
		"INJECTOR_AGENT_IMAGE":    "mirror.example.com/nvcf-openbao:fixture",
		"INJECTOR_PULL_SECRET":    "registry",
	} {
		t.Setenv(name, value)
	}
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"kubectl --context isolated -n vault-system get deployment openbao-server-agent-injector -o=jsonpath={.spec.template.spec.containers[0].image}": {Stdout: "docker.io/hashicorp/vault-k8s:fixture"},
		"kubectl --context isolated -n injector-workload get pod injected-consumer -o=jsonpath={.spec.initContainers[0].image}":                         {Stdout: "mirror.example.com/nvcf-openbao:fixture"},
		`kubectl --context isolated -n injector-workload get pod injected-consumer -o 'jsonpath={.spec.containers[?(@.name=="vault-agent")].image}'`:    {Stdout: "mirror.example.com/nvcf-openbao:fixture"},
	}))
	sc := steps.NewScenarioContext(suite)
	status := godog.TestSuite{
		Name:                "openbao-injector-upstream-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) { steps.RegisterAll(ctx, sc) },
		Options: &godog.Options{Format: "progress", Strict: true, Output: io.Discard,
			Paths: []string{mustResolveFeaturePath(t, "openbao-injector-upstream.feature")}},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d", status)
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "--selector name=openbao-server sync") {
		t.Fatal("published OpenBao Helmfile install was never invoked")
	}
}
