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
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestNVCFCLINonlocalFixtureMatchesCLITemplate asserts every top-level
// key in tests/bdd/fixtures/nvcf-cli-nonlocal.yaml.template is also
// declared (active or commented documentation) in the canonical CLI
// template at src/clis/nvcf-cli/.nvcf-cli.yaml.template.
//
// The BDD fixture is intentionally a trimmed subset of the CLI
// template (chart-level constants only, no production URLs, no
// inline docs). If the CLI renames or removes a key the BDD fixture
// references, the runtime CLI config the feature builds at suite
// runtime would silently lose that key. This test catches the
// rename/remove at unit-test time so the wiring is forced to update
// in lockstep.
//
// The CLI template's commented-out documentation blocks (e.g.
// `# api_keys_host: api-keys.nvcf.example.com`) are sufficient to
// pass the assertion. The contract is "the CLI knows about this key",
// not "the CLI defaults it".
func TestNVCFCLINonlocalFixtureMatchesCLITemplate(t *testing.T) {
	const (
		fixturePath     = "fixtures/nvcf-cli-nonlocal.yaml.template"
		cliTemplatePath = "../../src/clis/nvcf-cli/.nvcf-cli.yaml.template"
	)

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}
	var fixture map[string]any
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse fixture %s: %v", fixturePath, err)
	}
	if len(fixture) == 0 {
		t.Fatalf("fixture %s has no top-level keys", fixturePath)
	}

	cliTemplateBytes, err := os.ReadFile(cliTemplatePath)
	if err != nil {
		t.Fatalf("read CLI template %s: %v", cliTemplatePath, err)
	}
	cliBody := string(cliTemplateBytes)

	for key := range fixture {
		// Match either an active key (`<key>:`) or a documentation
		// line that mentions the key explicitly (`# api_keys_host:`,
		// `# Config key: api_keys_host`). Anchor on word boundaries
		// so a substring of another key cannot satisfy the match.
		pattern := regexp.MustCompile(`(?m)(^|[^\w])` + regexp.QuoteMeta(key) + `(\s*:|\b)`)
		if !pattern.MatchString(cliBody) {
			t.Errorf("BDD fixture key %q not referenced in CLI template at %s; the CLI may have renamed or removed it", key, cliTemplatePath)
		}
	}
}

func TestNVCFCLILocalFixtureTargetsLocalGRPCGateway(t *testing.T) {
	fixtureBytes, err := os.ReadFile("fixtures/nvcf-cli-local.yaml")
	if err != nil {
		t.Fatalf("read local CLI fixture: %v", err)
	}
	var fixture map[string]any
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse local CLI fixture: %v", err)
	}
	if got, want := fixture["base_grpc_url"], "localhost:10081"; got != want {
		t.Fatalf("base_grpc_url = %v, want %q", got, want)
	}
}

func TestSelfManagedLocalBDDMultiFixtureWiresGRPCWorkerCallback(t *testing.T) {
	fixtureBytes, err := os.ReadFile("fixtures/self-managed-local-bdd-multi.yaml")
	if err != nil {
		t.Fatalf("read multi-cluster stack fixture: %v", err)
	}
	fixture := string(fixtureBytes)
	for _, want := range []string{
		"workerConnectBaseURL: http://grpc.nvcf.svc.cluster.local:10086",
		"chart: ../../../helm/gateway-routes/chart",
		`version: ""`,
		"grpcWorker:",
		"enabled: true",
		"listenerName: worker-tcp",
	} {
		if !strings.Contains(fixture, want) {
			t.Fatalf("multi-cluster stack fixture missing %q", want)
		}
	}
}

func TestNVCTTaskSmokeUsesTaskSimpleSample(t *testing.T) {
	for _, path := range []string{
		"../../examples/task-samples/task-simple-sample/Dockerfile",
		"../../examples/task-samples/task-simple-sample/main.py",
		"../../examples/task-samples/task-simple-sample/requirements.txt",
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("task-simple-sample fixture missing %s: %v", path, err)
		}
	}

	scriptBytes, err := os.ReadFile("scripts/run-nvct-task-smoke.sh")
	if err != nil {
		t.Fatalf("read NVCT task smoke script: %v", err)
	}
	script := string(scriptBytes)
	for _, want := range []string{
		"task-simple-sample",
		"NVCT_BDD_TASK_IMAGE_TAG:-local",
		"containerEnvironment",
		"NUM_OF_RESULTS",
		"DELAY_BETWEEN_RESULTS_IN_MINUTES",
		".token // empty",
		"audience_service_ids",
		"account-tasks",
		"Key-Issuer-Service",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("NVCT task smoke script does not reference %q", want)
		}
	}
	if strings.Contains(script, ".apiKey // empty") {
		t.Fatal("NVCT task smoke script reads the function API key from nvcf-cli state")
	}
	if strings.Contains(script, "task_simple_sample") {
		t.Fatal("NVCT task smoke script uses the unpublished underscore image name")
	}
	if strings.Contains(script, "docker.io/library/busybox") {
		t.Fatal("NVCT task smoke script still uses the synthetic busybox sample")
	}
}

func TestNVCFGatewayFixtureDefinesReferencedGatewayClass(t *testing.T) {
	const fixturePath = "fixtures/nvcf-gateway.yaml"

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(fixtureBytes))
	gatewayClasses := map[string]bool{}
	var gatewayClassName string
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("parse fixture %s: %v", fixturePath, err)
		}
		if len(doc) == 0 {
			continue
		}

		kind, _ := doc["kind"].(string)
		metadata, _ := doc["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		switch kind {
		case "GatewayClass":
			gatewayClasses[name] = true
		case "Gateway":
			if name != "nvcf-gateway" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			gatewayClassName, _ = spec["gatewayClassName"].(string)
		}
	}

	if gatewayClassName == "" {
		t.Fatalf("fixture %s does not define gateway/nvcf-gateway with spec.gatewayClassName", fixturePath)
	}
	if !gatewayClasses[gatewayClassName] {
		t.Fatalf("fixture %s references GatewayClass %q but does not define it", fixturePath, gatewayClassName)
	}
}
