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
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

func TestClusterMaintenanceFeatureWiresToSharedSteps(t *testing.T) {
	for name, value := range map[string]string{
		"NVCF_CLI":                      "/usr/bin/nvcf-cli",
		"BDD_NVCF_CLI_CONFIG":           "/tmp/nvcf.yaml",
		"BDD_COMPUTE_KUBECONFIG":        "/tmp/kubeconfig",
		"BDD_COMPUTE_CONTEXT":           "compute-context",
		"BDD_COMPUTE_CLUSTER":           "compute-cluster",
		"BDD_NVCA_BACKEND_NAMESPACE":    "nvca-operator",
		"BDD_NVCA_SYSTEM_NAMESPACE":     "nvca-system",
		"BDD_COMPUTE_REGION":            "us-west-1",
		"BDD_WORKLOAD_GPU":              "H100",
		"BDD_WORKLOAD_INSTANCE_TYPE":    "NCP.GPU.H100_1x",
		"BDD_FUNCTION_IMAGE":            "nvcr.io/example/sample:1",
		"BDD_RUN_ID":                    "run-1",
		"BDD_ALLOW_CLUSTER_MAINTENANCE": "compute-cluster",
	} {
		t.Setenv(name, value)
	}
	runner := &maintenanceFakeRunner{}
	temp := t.TempDir()
	suite := &harness.Suite{
		Config: harness.Config{
			RepoRoot:      temp,
			CommandLogDir: filepath.Join(temp, "logs"),
		},
		Runner:    runner,
		Ledger:    harness.NewLedger(filepath.Join(temp, "originals")),
		EnvLedger: harness.NewEnvLedger(),
		Cache:     harness.NewCommandCache(),
	}
	scenario := steps.NewScenarioContext(suite)
	var output bytes.Buffer
	status := godog.TestSuite{
		Name: "cluster-maintenance-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, scenario)
		},
		Options: &godog.Options{
			Format:        "progress",
			Output:        &output,
			Paths:         []string{filepath.Join("..", "features", "smoke", "cluster-maintenance.feature")},
			Strict:        true,
			StopOnFailure: true,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, output.String())
	}
	for _, required := range []string{
		`--kubeconfig "/tmp/kubeconfig" --compute-plane-context "compute-context"`,
		"cluster agent cordon-and-drain",
		"cluster agent uncordon",
		"function delete",
		`.phase == \"ACTIVE\" and .instanceCount > 0`,
		"get nvcfbackend 'compute-cluster'",
		"maintenanceMode: None",
	} {
		if !runner.contains(required) {
			t.Fatalf("runner never received %q: %v", required, runner.runs)
		}
	}
}

type maintenanceFakeRunner struct {
	runs []string
}

func (r *maintenanceFakeRunner) Run(_ context.Context, command string) (harness.Result, error) {
	r.runs = append(r.runs, command)
	result := harness.Result{ExitCode: 0}
	switch {
	case strings.Contains(command, "--dry-run"):
		result.Stdout = `{"dryRun":true,"configChanged":true}`
	case strings.Contains(command, "bdd-before-maintenance"):
		result.Stdout = "bdd-before-maintenance"
	case strings.Contains(command, "--json function list"):
		result.Stdout = "function-1\n"
	case strings.Contains(command, "cluster agent cordon-and-drain"):
		result.Stdout = `{"mode":"CordonAndDrain","configChanged":true,"rolloutComplete":true}`
	case strings.Contains(command, "cluster agent uncordon"):
		result.Stdout = `{"configChanged":true,"rolloutComplete":true}`
	case strings.Contains(command, "bdd-after-maintenance"):
		result.Stdout = "bdd-after-maintenance"
	}
	return result, nil
}

func (r *maintenanceFakeRunner) RunWithSensitiveStdin(ctx context.Context, command, _ string) (harness.Result, error) {
	return r.Run(ctx, command)
}

func (r *maintenanceFakeRunner) RunWithTTY(ctx context.Context, command string) (harness.Result, error) {
	return r.Run(ctx, command)
}

func (r *maintenanceFakeRunner) contains(fragment string) bool {
	for _, command := range r.runs {
		if strings.Contains(command, fragment) {
			return true
		}
	}
	return false
}
