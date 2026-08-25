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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTargetInterpolatesAndExportsCoordinates(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("SAMPLE_NGC_ORG", "sample-org")
	t.Setenv("SAMPLE_NGC_TEAM", "sample-team")
	path := writeTarget(t, `
version: 1
name: local-multi
nvcf:
  cliConfig: tests/bdd/fixtures/nvcf-cli-local.yaml
  cliState: ${HOME}/.nvcf-cli.nvcf-cli-local.state
compute:
  kubeconfig: /tmp/local-kubeconfig
  context: k3d-ncp-local-compute-1
  cluster: ncp-local-compute-1
  backendNamespace: nvca-operator
  systemNamespace: nvca-system
  region: us-west-1
workload:
  gpu: H100
  instanceType: NCP.GPU.H100_1x
  functionImage: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/sample:1
`)
	target, err := LoadTarget(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if target.NVCF.CLIState != "/home/tester/.nvcf-cli.nvcf-cli-local.state" {
		t.Fatalf("CLIState=%q", target.NVCF.CLIState)
	}
	env := target.Environment("run-123")
	if env["BDD_FUNCTION_IMAGE"] != "nvcr.io/sample-org/sample-team/sample:1" {
		t.Fatalf("function image=%q", env["BDD_FUNCTION_IMAGE"])
	}
	if env["BDD_RUN_ID"] != "run-123" || env["BDD_COMPUTE_CONTEXT"] != "k3d-ncp-local-compute-1" {
		t.Fatalf("env=%v", env)
	}
}

func TestLoadTargetRejectsUnknownAndMissingStructuralFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `version: 1
name: target
nvcf:
  cliConfig: config.yaml
  cliState: /tmp/state
unexpected: true
`,
		"missing state": `version: 1
name: target
nvcf:
  cliConfig: config.yaml
`,
		"unsupported version": `version: 2
name: target
nvcf:
  cliConfig: config.yaml
  cliState: /tmp/state
`,
		"relative state": `version: 1
name: target
nvcf:
  cliConfig: config.yaml
  cliState: relative.state
`,
		"missing required interpolation": `version: 1
name: target
nvcf:
  cliConfig: config.yaml
  cliState: ${BDD_MISSING_HOME}/state
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadTarget(writeTarget(t, body)); err == nil {
				t.Fatal("expected target error")
			}
		})
	}
}

func TestTargetEnvironmentOmitsOptionalEmptyValues(t *testing.T) {
	target := Target{Version: 1, Name: "api-only", NVCF: NVCFTarget{CLIConfig: "prod.yaml", CLIState: "/tmp/prod.state"}}
	env := target.Environment("run")
	if _, ok := env["BDD_COMPUTE_CONTEXT"]; ok {
		t.Fatalf("optional empty compute context was exported: %v", env)
	}
}

func TestCommittedLocalTargetsMatchSchema(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("KUBECONFIG", "/tmp/kubeconfig")
	t.Setenv("SAMPLE_NGC_ORG", "sample-org")
	t.Setenv("SAMPLE_NGC_TEAM", "sample-team")
	for _, name := range []string{"local-single.yaml", "local-multi.yaml"} {
		t.Run(name, func(t *testing.T) {
			target, err := LoadTarget(filepath.Join("..", "targets", name))
			if err != nil {
				t.Fatalf("load committed target: %v", err)
			}
			if target.Compute.Kubeconfig != "/tmp/kubeconfig" || target.Workload.FunctionImage == "" {
				t.Fatalf("target=%+v", target)
			}
		})
	}
}

func writeTarget(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "target.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	return path
}
