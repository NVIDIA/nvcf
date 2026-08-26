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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTargetInterpolatesAndExportsOpenEnvironment(t *testing.T) {
	t.Setenv("SAMPLE_NGC_ORG", "sample-org")
	t.Setenv("SAMPLE_NGC_TEAM", "sample-team")
	path := writeYAML(t, "target.yaml", `
version: 1
name: local-multi
env:
  BDD_NVCF_CLI_CONFIG: tests/bdd/fixtures/nvcf-cli-local.yaml
  BDD_COMPUTE_CONTEXT: k3d-ncp-local-compute-1
  BDD_FUTURE_INPUT: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/sample:1
`)
	target, err := LoadTarget(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	env := target.Environment("run-123")
	if env["BDD_FUTURE_INPUT"] != "nvcr.io/sample-org/sample-team/sample:1" {
		t.Fatalf("future input=%q", env["BDD_FUTURE_INPUT"])
	}
	if env["BDD_RUN_ID"] != "run-123" || env["BDD_TARGET_NAME"] != "local-multi" {
		t.Fatalf("env=%v", env)
	}
}

func TestLoadTargetRejectsInvalidStructureAndReservedEnvironment(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `version: 1
name: target
env:
  BDD_NVCF_CLI_CONFIG: config.yaml
unexpected: true
`,
		"missing cli config": `version: 1
name: target
env:
  BDD_COMPUTE_CONTEXT: context
`,
		"unsupported version": `version: 2
name: target
env:
  BDD_NVCF_CLI_CONFIG: config.yaml
`,
		"reserved runner value": `version: 1
name: target
env:
  BDD_NVCF_CLI_CONFIG: config.yaml
  BDD_RUN_ID: fixed
`,
		"invalid env name": `version: 1
name: target
env:
  BDD_NVCF_CLI_CONFIG: config.yaml
  ? "BAD=NAME"
  : value
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadTarget(writeYAML(t, "target.yaml", body)); err == nil {
				t.Fatal("expected target error")
			}
		})
	}
}

func TestTargetEnvironmentPreservesEmptyFeatureInputs(t *testing.T) {
	target := Target{
		Version: 1,
		Name:    "api-only",
		Env: map[string]string{
			cliConfigEnvironment: "prod.yaml",
			"BDD_OPTIONAL_INPUT": "",
		},
	}
	env := target.Environment("run")
	if value, ok := env["BDD_OPTIONAL_INPUT"]; !ok || value != "" {
		t.Fatalf("empty feature input was changed: %v", env)
	}
}

func TestCommittedLocalTargetsMatchSchema(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/kubeconfig")
	t.Setenv("SAMPLE_NGC_ORG", "sample-org")
	t.Setenv("SAMPLE_NGC_TEAM", "sample-team")
	paths, err := filepath.Glob(filepath.Join("targets", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no committed smoke targets")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			target, err := LoadTarget(path)
			if err != nil {
				t.Fatalf("load committed target: %v", err)
			}
			if target.Env["BDD_COMPUTE_KUBECONFIG"] != "/tmp/kubeconfig" || target.Env["BDD_FUNCTION_IMAGE"] == "" {
				t.Fatalf("target=%+v", target)
			}
		})
	}
}

func writeYAML(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	return path
}
