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

// Package bdd_tmp holds the live BDD entry points and the wiring
// tests that exercise feature files against fake collaborators.
package bdd_tmp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

// fakeRunner is a CommandRunner stand-in used by wiring tests. It
// records every command and returns canned results so the wiring tests
// can run the full feature file without a live cluster or CLI binary.
type fakeRunner struct {
	results map[string]harness.Result
	runs    []string
}

func (f *fakeRunner) Run(_ context.Context, command string) (harness.Result, error) {
	f.runs = append(f.runs, command)
	if result, ok := f.results[command]; ok {
		return result, nil
	}
	return harness.Result{ExitCode: 0}, nil
}

// RunWithSensitiveStdin records only the command. Sensitive input must never
// enter wiring-test diagnostics or command assertions.
func (f *fakeRunner) RunWithSensitiveStdin(
	ctx context.Context,
	command,
	_ string,
) (harness.Result, error) {
	return f.Run(ctx, command)
}

// RunWithTTY records and resolves identically to Run; the fake does not
// allocate a pty.
func (f *fakeRunner) RunWithTTY(ctx context.Context, command string) (harness.Result, error) {
	return f.Run(ctx, command)
}

// newFakeRunner returns a runner pre-loaded with canned responses for
// specific commands. Commands not in the map resolve to ExitCode 0
// with empty streams. Pass nil for an all-zero-exit runner.
func newFakeRunner(canned map[string]harness.Result) *fakeRunner {
	return &fakeRunner{results: canned, runs: nil}
}

// newWiringSuite builds a Suite tailored for the wiring tests: a
// fake CommandRunner, a real Ledger and CommandCache, and a Config
// whose RepoRoot is a temp directory seeded with the fixtures the
// feature file references.
func newWiringSuite(t *testing.T, runner harness.CommandRunner) *harness.Suite {
	t.Helper()
	repoRoot := t.TempDir()
	cfg := harness.Config{
		RepoRoot:  repoRoot,
		OutDir:    filepath.Join(repoRoot, "tests", "bdd", "out", "wiring"),
		LedgerDir: filepath.Join(repoRoot, "tests", "bdd", "out", "wiring", "originals"),
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	return &harness.Suite{
		Config:    cfg,
		Runner:    runner,
		Ledger:    harness.NewLedger(cfg.LedgerDir),
		EnvLedger: harness.NewEnvLedger(),
		Cache:     harness.NewCommandCache(),
	}
}

// writeProfileHandoffArtifact seeds the control-plane-profile.yaml the
// validate scenario reads back. The values match the assertions in
// features/single-cluster-up.feature.
func writeProfileHandoffArtifact(t *testing.T, repoRoot string) {
	t.Helper()
	body := `apiVersion: nvcf.nvidia.com/v1alpha1
kind: ControlPlaneProfile
controlPlane:
  clusterName: ncp-local
  ncaID: nvcf-default
  region: us-west-1
  endpoints:
    inCluster:
      icmsURL: http://api.sis.svc.cluster.local:8080
      revalURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222
    computeReachable:
      icmsURL: http://sis.localhost:8080
      revalURL: http://reval.localhost:8080
      natsURL: nats://nats.localhost:4222
  gateway:
    httpURL: http://api.localhost:8080
    grpcURL: grpc.localhost:10081
  hosts:
    api: api.localhost
    apiKeys: api-keys.localhost
    sis: sis.localhost
    reval: reval.localhost
    nats: nats.localhost
    invocation: invocation.localhost
managementTls:
  trustMode: bundle
  caBundlePem: test-ca-bundle
transportTls:
  trustMode: bundle
  trustBundleFingerprint: sha256:test-fingerprint
  trustBundlePem: test-ca-bundle
`
	writeArtifact(t, repoRoot, "self-managed", "control-plane-profile.yaml", body)
}

// writeMulticlusterProfileHandoffArtifact seeds the profile that the
// multi-cluster validate scenario reads. Endpoints carry the localhost
// hostnames emitted by the local split-cluster install.
func writeMulticlusterProfileHandoffArtifact(t *testing.T, repoRoot string) {
	t.Helper()
	body := `apiVersion: nvcf.nvidia.com/v1alpha1
kind: ControlPlaneProfile
controlPlane:
  clusterName: ncp-local-cp
  ncaID: nvcf-default
  region: us-west-1
  endpoints:
    inCluster:
      icmsURL: http://api.sis.svc.cluster.local:8080
      revalURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222
    computeReachable:
      icmsURL: http://sis.localhost:8080
      revalURL: http://reval.localhost:8080
      natsURL: nats://nats.localhost:4222
  gateway:
    httpURL: http://api.localhost:8080
    grpcURL: grpc.localhost:10081
  hosts:
    api: api.localhost
    apiKeys: api-keys.localhost
    sis: sis.localhost
    reval: reval.localhost
    nats: nats.localhost
    invocation: invocation.localhost
`
	writeArtifact(t, repoRoot, "self-managed", "control-plane-profile.yaml", body)
}

// writeMulticlusterComputeRegisterValues seeds the per-compute-cluster
// register-values handoff the multi-cluster install scenario reads.
func writeMulticlusterComputeRegisterValues(t *testing.T, repoRoot, stackDir, cluster string) {
	t.Helper()
	body := `clusterName: ` + cluster + `
clusterID: 99999999-aaaa-bbbb-cccc-dddddddddddd
clusterGroupID: cccc-dddd-eeee-ffff
ncaID: nvcf-default
region: us-west-1
selfManaged:
  identitySource: psat
  icmsServiceURL: http://sis.localhost:8080
  revalServiceURL: http://reval.localhost:8080
  natsURL: nats://nats.localhost:4222
`
	writeArtifact(t, repoRoot, stackDir, cluster+"-register-values.yaml", body)
	writeRegistrationArtifact(t, repoRoot, stackDir, cluster+"-register-values.yaml", body)
}

// writeSingleClusterComputeRegisterValues seeds the register-values
// handoff the single-cluster CLI feature reads after compute-plane
// register. compute-plane register picks the in-cluster service
// hostnames in single-cluster topology (worker and CP share the same
// k3d cluster, so the in-cluster URLs are the directly reachable
// ones). The multi-cluster equivalent in writeMulticlusterComputeRegisterValues
// uses compute-reachable gateway hostnames instead.
func writeSingleClusterComputeRegisterValues(t *testing.T, repoRoot string) {
	t.Helper()
	body := `clusterName: ncp-local
clusterID: 11111111-2222-3333-4444-555555555555
clusterGroupID: aaaa-bbbb-cccc-dddd
ncaID: nvcf-default
region: us-west-1
selfManaged:
  identitySource: psat
  icmsServiceURL: http://api.sis.svc.cluster.local:8080
  revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
  natsURL: nats://nats.nats-system.svc.cluster.local:4222
`
	writeArtifact(t, repoRoot, "nvcf-compute-plane", "ncp-local-register-values.yaml", body)
	writeRegistrationArtifact(t, repoRoot, "nvcf-compute-plane", "ncp-local-register-values.yaml", body)
}

// writeHelmfileRegisterValues seeds the compute-plane register-values handoff
// the single-cluster-helmfile.feature register scenario reads. Profile-driven
// registration records clusterName and selects in-cluster endpoints when the
// compute target is the control-plane cluster.
func writeHelmfileRegisterValues(t *testing.T, repoRoot string) {
	t.Helper()
	body := `clusterName: ncp-local
clusterID: 11111111-2222-3333-4444-555555555555
clusterGroupID: aaaa-bbbb-cccc-dddd
ncaID: nvcf-default
region: us-west-1
selfManaged:
  identitySource: psat
  icmsServiceURL: http://api.sis.svc.cluster.local:8080
  revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
  natsURL: nats://nats.nats-system.svc.cluster.local:4222
`
	writeArtifact(t, repoRoot, "nvcf-compute-plane", "ncp-local-register-values.yaml", body)
	writeRegistrationArtifact(t, repoRoot, "nvcf-compute-plane", "ncp-local-register-values.yaml", body)
}

// seedStackSecretsTemplate writes minimal stand-ins for the split stack
// templates under deploy/stacks/self-managed/secrets/secrets.yaml.template at
// the suite's RepoRoot. The body is not a faithful copy of the real
// stack templates (which have richer schemas with several placeholders);
// it only carries the single REPLACE_WITH_BASE64_DOCKER_CREDENTIAL token
// the self-managed secrets step renders, which is sufficient to exercise
// the file preparation path against a fake CommandRunner.
func seedStackSecretsTemplate(t *testing.T, repoRoot string) {
	t.Helper()
	templatePath := filepath.Join(repoRoot, "deploy", "stacks", "self-managed", "secrets", "secrets.yaml.template")
	if err := os.MkdirAll(filepath.Dir(templatePath), 0o755); err != nil {
		t.Fatalf("mkdir secrets dir: %v", err)
	}
	if err := os.WriteFile(templatePath, []byte("token: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
}

func writeArtifact(t *testing.T, repoRoot, stackDir, name, body string) {
	t.Helper()
	dir := filepath.Join(repoRoot, "deploy", "stacks", stackDir, "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir handoff: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func writeRegistrationArtifact(t *testing.T, repoRoot, stackDir, name, body string) {
	t.Helper()
	dir := filepath.Join(repoRoot, "deploy", "stacks", stackDir, "registration")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir registration handoff: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestSingleClusterUpFeatureFileWiresToSteps runs the live feature
// file from tests/bdd/features against a fake CommandRunner. The
// test asserts the suite reaches the end without unresolved steps and
// that the expected destructive command (self-hosted up) was invoked
// at least once. Recorded call counts are intentionally not asserted
// per AGENTS.md guidance against deep-equality wiring tests.
func TestSingleClusterUpFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		// Conflict precheck: the feature asserts the conflicting
		// multi-cluster control-plane is absent. Mimic k3d v5 "not
		// found" by returning ExitCode 1 with no error so the
		// `command exit code should be 1` assertion passes.
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
	}))
	writeProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeSingleClusterComputeRegisterValues(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDFixture(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "single-cluster-up.feature")
	status := godog.TestSuite{
		Name: "single-cluster-up-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "progress",
			Paths:  []string{featurePath},
			Strict: true,
			Output: io.Discard,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d", status)
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "self-hosted") {
		t.Fatal("self-hosted CLI command was never invoked")
	}
}

// TestSingleClusterUpOneClickFeatureFileWiresToSteps runs the
// self-hosted up one-click feature against a fake CommandRunner. The
// helm-list canned output carries --kube-context k3d-ncp-local so the
// control-plane and nvca-operator assertions have something to parse;
// the conflict-precheck k3d-get returns exit 1.
func TestSingleClusterUpOneClickFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"helm list --all-namespaces --kube-context k3d-ncp-local -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		"kubectl --context k3d-ncp-local get configmap/nvcf-api-remote-config -n nvcf -o yaml": {
			ExitCode: 0,
			Stdout:   "data:\n  nvcf-api.yaml: |\n    nvcf:\n      sidecars:\n        llm-router-client-image: nvcr.io/test-org/test-team/pylon:0.14.1\n",
		},
		// Conflict precheck: feature asserts the multi-cluster
		// control-plane is absent.
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
	}))
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDFixture(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "single-cluster-up-oneclick.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "single-cluster-up-oneclick-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "up --cluster-name ncp-local") {
		t.Fatal("self-hosted up CLI command was never invoked")
	}
}

// TestMultiClusterUpFeatureFileWiresToSteps runs multi-cluster-up.feature
// against a fake CommandRunner, exercising the same handler chain as
// the live run. The seeded handoff artifacts use the split-cluster
// service-DNS hostnames that the install command produces.
func TestMultiClusterUpFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		// Conflict precheck: feature asserts the conflicting
		// single-cluster is absent. Mimic k3d v5 "not found".
		"k3d cluster get ncp-local": {ExitCode: 1},
	}))
	writeMulticlusterProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeMulticlusterComputeRegisterValues(t, suite.Config.RepoRoot, "nvcf-compute-plane", "ncp-local-compute-1")
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedHelmfileLocalBDDMultiFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDMultiFixture(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "multi-cluster-up.feature")
	status := godog.TestSuite{
		Name: "multi-cluster-up-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "progress",
			Paths:  []string{featurePath},
			Strict: true,
			Output: io.Discard,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d", status)
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "compute-plane install") {
		t.Fatal("compute-plane install CLI command was never invoked")
	}
}

// TestSingleClusterHelmfileFeatureFileWiresToSteps runs
// single-cluster-helmfile.feature against a fake runner. The fixture
// the feature copies from is seeded into the wiring suite's RepoRoot
// so the I copy / I update yaml chain has a real source file. The
// fake runner is pre-loaded with canned JSON for the Helm release assertion.
func TestSingleClusterHelmfileFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"helm list --all-namespaces --kube-context k3d-ncp-local -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		"kubectl --context k3d-ncp-local get configmap/nvcf-api-remote-config -n nvcf -o yaml": {
			ExitCode: 0,
			Stdout:   "data:\n  nvcf-api.yaml: |\n    nvcf:\n      sidecars:\n        llm-router-client-image: nvcr.io/test-org/test-team/pylon:0.14.1\n",
		},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --request-body '{\"message\":\"bdd-echo\",\"repeats\":1}' --timeout 120 --poll-duration 5": {
			ExitCode: 0,
			Stdout:   "Function invocation completed!\n\nResponse:\n{\"rawResponse\":\"bdd-echo\"}\n",
		},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke" +
			" --grpc --grpc-plaintext --grpc-service Echo --grpc-method EchoMessage" +
			" --request-body '{\"message\":\"bdd-grpc-echo\"}' --timeout 120 --poll-duration 5": {
			ExitCode: 0,
			Stdout:   "Function invocation completed!\n\nResponse:\n{\"message\":\"bdd-grpc-echo\"}\n",
		},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke" +
			" --inference-url /v1/chat/completions --model-name openai-compatible-sample" +
			" --request-body '{\"messages\":[{\"role\":\"user\",\"content\":\"bdd-llm-echo\"}]}' --timeout 120": {
			ExitCode: 0,
			Stdout: "Function invocation completed!\n\nResponse:\n" +
				`{"object":"chat.completion","choices":[{"message":{"content":"This is a fixed 128-byte response from an NVCF-hosted OpenAI-compatible sample, used for load testing and throughput benchmarks."}}]}` +
				"\n",
		},
		`curl -s -o /dev/null -w "%{http_code}" -X POST http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"unauthenticated/check","messages":[]}'`: {
			ExitCode: 0,
			Stdout:   "401",
		},
		// Conflict precheck: feature asserts the conflicting
		// multi-cluster control-plane is absent.
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
	}))
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	writeProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeHelmfileRegisterValues(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "single-cluster-helmfile.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "single-cluster-helmfile-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install HELMFILE_ENV") {
		t.Fatal("helmfile install make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke") {
		t.Fatal("function invoke CLI command was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke --grpc --grpc-plaintext") {
		t.Fatal("gRPC function invoke CLI command was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "bdd-grpc-load-tester-supreme") {
		t.Fatal("gRPC sample function was never created or deployed")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"function create --name bdd-grpc-load-tester-supreme",
		"load_tester_supreme:0.0.8",
		"--health-protocol GRPC --health-uri / --health-port 8001") {
		t.Fatal("gRPC sample function was not configured with a gRPC health endpoint")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"api-key generate --for function",
		"--description bdd-load-tester-supreme") {
		t.Fatal("HTTP sample function API key was not generated for the function service")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"api-key generate --for function",
		"--description bdd-grpc-load-tester-supreme") {
		t.Fatal("gRPC sample function API key was not generated for the function service")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"function create --name bdd-openai-compatible-sample",
		"nvcf-openai-compatible-sample:local",
		"--function-type LLM",
		"--llm-model") {
		t.Fatal("LLM sample function was not created with the LLM function type and model config")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke --inference-url /v1/chat/completions --model-name openai-compatible-sample") {
		t.Fatal("LLM function invoke CLI command was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function delete --deployment-only") {
		t.Fatal("function deployment cleanup was never invoked")
	}
	assertFunctionDeploymentsUseInstanceType(t, suite.Runner.(*fakeRunner).runs, "NCP.GPU.H100_1x", 3)
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "http://llm.localhost:8080/v1/chat/completions") {
		t.Fatal("unauthenticated LLM gateway check was never invoked")
	}
}

// TestSingleClusterHelmfileLLMPKIFeatureFileWiresToSteps runs the
// LLM PKI Helmfile feature against a fake runner, with canned results
// for the LLM invoke and the no-auth curl.
func TestSingleClusterHelmfileLLMPKIFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"helm list --all-namespaces --kube-context k3d-ncp-local -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		"kubectl --context k3d-ncp-local get configmap/nvcf-api-remote-config -n nvcf -o yaml": {
			ExitCode: 0,
			Stdout: "data:\n  nvcf-api.yaml: |\n    nvcf:\n" +
				"      llm-request-router:\n        worker-address: llm-request-router.nvcf.svc.cluster.local:50071\n" +
				"      sidecars:\n        llm-router-client-image: nvcr.io/test-org/test-team/pylon:0.14.1\n",
		},
		"helm get values nvca-operator --namespace nvca-operator --kube-context k3d-ncp-local -o yaml": {
			ExitCode: 0,
			Stdout:   "agentConfig:\n  mergeConfig: |\n    workload:\n      stargateQUICInsecure: false\n      transportTLS:\n        trustMode: bundle\n        trustBundleFingerprint: sha256:test\n",
		},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke" +
			" --inference-url /v1/chat/completions --model-name openai-compatible-sample" +
			" --request-body '{\"messages\":[{\"role\":\"user\",\"content\":\"bdd-pki-llm\"}]}' --timeout 120": {
			ExitCode: 0,
			Stdout: "Function invocation completed!\n\nResponse:\n" +
				`{"object":"chat.completion","choices":[{"message":{"content":"This is a fixed 128-byte response from an NVCF-hosted OpenAI-compatible sample, used for routing and response-contract validation, not token-generation capacity."}}]}` +
				"\n",
		},
		`curl -s --connect-timeout 5 --max-time 30 -o /dev/null -w "%{http_code}" -X POST ` +
			`http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" ` +
			`-H "traceparent: 00-00000000000000000000000000001076-0000000000001076-01" ` +
			`-d '{"model":"unauthenticated/check","messages":[]}'`: {
			ExitCode: 0,
			Stdout:   "401",
		},
		// Conflict precheck: feature asserts the conflicting
		// multi-cluster control-plane is absent.
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
	}))
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	writeProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeHelmfileRegisterValues(t, suite.Config.RepoRoot)
	seedPKIRenderOutput(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "single-cluster-helmfile-llm-pki.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "single-cluster-helmfile-llm-pki-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	runs := suite.Runner.(*fakeRunner).runs
	if !commandRanThatContains(runs, "install HELMFILE_ENV=local-bdd-pki") {
		t.Fatal("PKI helmfile install make target was never invoked")
	}
	profileExport := "/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml" +
		" self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-pki" +
		" control-plane profile export --cluster-name ncp-local"
	initCommand := "/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml init >/dev/null"
	profileExportIndex := -1
	initIndex := -1
	registerIndex := -1
	for index, command := range runs {
		if strings.Contains(command, profileExport) {
			profileExportIndex = index
		}
		if strings.Contains(command, initCommand) {
			initIndex = index
		}
		if strings.Contains(command, "register-cluster CLUSTER_NAME=ncp-local") {
			registerIndex = index
			if !strings.Contains(command, "CONTROL_PLANE_PROFILE=/repo-root-placeholder/deploy/stacks/self-managed/out/control-plane-profile.yaml") {
				t.Fatalf("compute-plane registration did not use the exported profile: %s", command)
			}
			if !strings.Contains(command, "COMPUTE_KUBE_CONTEXT=k3d-ncp-local") {
				t.Fatalf("compute-plane registration did not select the local cluster context: %s", command)
			}
			if !strings.Contains(command, "NVCF_CLI_CONFIG=/repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml") {
				t.Fatalf("compute-plane registration did not select the initialized CLI config: %s", command)
			}
		}
	}
	if profileExportIndex < 0 {
		t.Fatal("selected Helmfile environment was not exported to a control-plane profile")
	}
	if initIndex < 0 {
		t.Fatal("local admin credentials were not initialized before compute-plane registration")
	}
	if registerIndex < 0 {
		t.Fatal("compute-plane register-cluster make target was never invoked")
	}
	if profileExportIndex >= initIndex || initIndex >= registerIndex {
		t.Fatal("profile export and credential initialization did not precede compute-plane registration")
	}
	assertFunctionDeploymentsUseInstanceType(t, runs, "NCP.GPU.H100_1x", 1)
}

// TestObservabilityControlFeatureFileWiresToSteps runs the live-install
// observability-control feature against a fake runner. It checks the
// single-cluster Helmfile path renders and verifies the profile-selected
// shared releases and monitor resources through explicit local context calls.
func TestObservabilityControlFeatureFileWiresToSteps(t *testing.T) {
	const (
		registryLoginCommand    = "helm registry login nvcr.io --username '$oauthtoken' --password-stdin"
		serviceMonitorCommand   = "kubectl get servicemonitor/nvcf-default-monitors-state-metrics --namespace monitoring --context k3d-ncp-local -o name"
		absentPodMonitorCommand = "kubectl get podmonitor/nvcf-default-monitors-worker --namespace monitoring --context k3d-ncp-local --ignore-not-found -o name"
		collectorYAMLCommand    = "kubectl get opentelemetrycollector/nvcf-observability --namespace monitoring --context k3d-ncp-local -o yaml"
	)
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		registryLoginCommand:           {ExitCode: 0},
		serviceMonitorCommand:          {ExitCode: 0},
		absentPodMonitorCommand:        {ExitCode: 0},
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
		"helm list --all-namespaces --kube-context k3d-ncp-local -o json": {
			ExitCode: 0,
			Stdout:   observabilityControlHelmListJSON(),
		},
		collectorYAMLCommand: {
			ExitCode: 0,
			Stdout:   observabilityCollectorYAML(),
		},
	}))
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "observability-control.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "observability-control-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	runs := suite.Runner.(*fakeRunner).runs
	for _, command := range []string{
		registryLoginCommand,
		serviceMonitorCommand,
		absentPodMonitorCommand,
	} {
		if !commandRanExactly(runs, command) {
			t.Fatalf("exact command was never invoked: %s", command)
		}
	}
	if !commandRanThatContains(runs, "install HELMFILE_ENV=local-bdd-observability-control") {
		t.Fatal("control-profile Helmfile install command was never invoked")
	}
	environmentPath := filepath.Join(suite.Config.RepoRoot, "deploy", "stacks", "self-managed", "environments", "local-bdd-observability-control.yaml")
	imageTag, found, err := dsl.ReadYAMLKey(environmentPath, "functionAutoscaler.image.tag")
	if err != nil {
		t.Fatalf("read control-profile autoscaler image tag: %v", err)
	}
	if !found || imageTag != "1.18.10" {
		t.Fatalf("control-profile autoscaler image tag = %q, found = %t; want 1.18.10", imageTag, found)
	}
}

func observabilityControlHelmListJSON() string {
	return `[
{"name":"prometheus-operator-crds","namespace":"monitoring","status":"deployed"},
{"name":"opentelemetry-operator","namespace":"monitoring","status":"deployed"},
{"name":"victoria-metrics","namespace":"monitoring","status":"deployed"},
{"name":"otel-collector","namespace":"monitoring","status":"deployed"},
{"name":"default-monitors","namespace":"monitoring","status":"deployed"}
]`
}

func observabilityCollectorYAML() string {
	return `apiVersion: opentelemetry.io/v1beta1
kind: OpenTelemetryCollector
metadata:
  name: nvcf-observability
  namespace: monitoring
spec:
  targetAllocator:
    enabled: true
`
}

// TestObservabilityComputeFeatureFileWiresToSteps runs the live-install
// observability-compute feature against a fake runner. It checks that every
// cluster operation is explicitly routed to the local control or compute
// cluster and that the compute profile verifies its releases and monitors.
func TestObservabilityComputeFeatureFileWiresToSteps(t *testing.T) {
	const (
		registryLoginCommand        = "helm registry login nvcr.io --username '$oauthtoken' --password-stdin"
		serviceMonitorCommand       = "kubectl get servicemonitor/nvcf-default-monitors-nvca --namespace monitoring --context k3d-ncp-local-compute-1 -o name"
		podMonitorCommand           = "kubectl get podmonitor/nvcf-default-monitors-worker --namespace monitoring --context k3d-ncp-local-compute-1 -o name"
		absentServiceMonitorCommand = "kubectl get servicemonitor/nvcf-default-monitors-state-metrics --namespace monitoring --context k3d-ncp-local-compute-1 --ignore-not-found -o name"
		collectorValuesCommand      = "helm get values nvca-operator --namespace nvca-operator --kube-context k3d-ncp-local-compute-1 -o yaml"
		collectorYAMLCommand        = "kubectl get opentelemetrycollector/nvcf-observability --namespace monitoring --context k3d-ncp-local-compute-1 -o yaml"
		serviceKeyCommand           = `bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" |` +
			` kubectl --context k3d-ncp-local-compute-1 create secret generic ngc-service-api-key` +
			` --namespace nvca-system --from-file=ngc-service-api-key=/dev/stdin --dry-run=client -o yaml |` +
			` kubectl --context k3d-ncp-local-compute-1 apply -f -'`
		restartNVCACommand = "kubectl --context k3d-ncp-local-compute-1 delete pod --namespace nvca-system --selector app.kubernetes.io/name=nvca --wait=false"
	)
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		registryLoginCommand:        {ExitCode: 0},
		"k3d cluster get ncp-local": {ExitCode: 1},
		serviceMonitorCommand:       {ExitCode: 0},
		podMonitorCommand:           {ExitCode: 0},
		absentServiceMonitorCommand: {ExitCode: 0},
		collectorValuesCommand:      {ExitCode: 0, Stdout: "selfManaged:\n  otelCollector:\n    enabled: true\n    imageRepository: nvcr.io/test-org/test-team/nvcf-otel-collector\n"},
		"helm list --all-namespaces --kube-context k3d-ncp-local-compute-1 -o json": {
			ExitCode: 0,
			Stdout:   observabilityComputeHelmListJSON(),
		},
		collectorYAMLCommand: {
			ExitCode: 0,
			Stdout:   observabilityCollectorYAML(),
		},
		"helm status function-autoscaler --namespace nvcf --kube-context k3d-ncp-local-compute-1": {
			ExitCode: 1,
			Stderr:   "Error: release: not found\n",
		},
	}))
	seedHelmfileLocalBDDMultiFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDMultiFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	writeMulticlusterProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeMulticlusterComputeRegisterValues(t, suite.Config.RepoRoot, "nvcf-compute-plane", "ncp-local-compute-1")

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "observability-compute.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "observability-compute-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}

	runs := suite.Runner.(*fakeRunner).runs
	for _, command := range []string{serviceMonitorCommand, podMonitorCommand, absentServiceMonitorCommand, collectorValuesCommand} {
		if !commandRanExactly(runs, command) {
			t.Fatalf("exact command was never invoked: %s", command)
		}
	}
	if !commandRanThatContains(runs, "kubectl --context k3d-ncp-local-compute-1 delete pod --namespace nvca-system") {
		t.Fatal("NVCA restart command was never invoked")
	}
	for _, run := range runs {
		if strings.HasPrefix(run, "kubectl apply -f ") {
			t.Fatalf("manifest apply relied on the ambient kube context: %s", run)
		}
		if strings.Contains(run, "test-key") {
			t.Fatalf("NGC API key leaked into command arguments: %s", run)
		}
	}

	for _, stack := range []string{"self-managed", "observability", "nvcf-compute-plane"} {
		environmentPath := filepath.Join(suite.Config.RepoRoot, "deploy", "stacks", stack, "environments", "local-bdd-observability-compute.yaml")
		profile, found, err := dsl.ReadYAMLKey(environmentPath, "observability.profile")
		if err != nil {
			t.Fatalf("read %s observability profile: %v", stack, err)
		}
		want := "compute"
		if stack == "self-managed" {
			want = "disabled"
		}
		if !found || profile != want {
			t.Fatalf("%s observability profile = %q, found = %t; want %q", stack, profile, found, want)
		}
	}
	computeEnvironmentPath := filepath.Join(suite.Config.RepoRoot, "deploy", "stacks", "nvcf-compute-plane", "environments", "local-bdd-observability-compute.yaml")
	for key, want := range map[string]string{
		"global.nvcaOperator.selfManaged.otelCollector.enabled": "true",
	} {
		got, found, err := dsl.ReadYAMLKey(computeEnvironmentPath, key)
		if err != nil {
			t.Fatalf("read compute-profile override %s: %v", key, err)
		}
		if !found || got != want {
			t.Fatalf("compute-profile override %s = %q, found = %t; want %q", key, got, found, want)
		}
	}
}

func observabilityComputeHelmListJSON() string {
	return `[
{"name":"prometheus-operator-crds","namespace":"monitoring","status":"deployed"},
{"name":"opentelemetry-operator","namespace":"monitoring","status":"deployed"},
{"name":"victoria-metrics","namespace":"monitoring","status":"deployed"},
{"name":"otel-collector","namespace":"monitoring","status":"deployed"},
{"name":"default-monitors","namespace":"monitoring","status":"deployed"},
{"name":"nvca-operator","namespace":"nvca-operator","status":"deployed"}
]`
}

// TestObservabilityAllFeatureFileWiresToSteps runs the all-profile feature
// against a fake runner. It keeps every cluster operation on the explicit
// local context and verifies that one shared stack serves both monitor sets.
func TestObservabilityAllFeatureFileWiresToSteps(t *testing.T) {
	const (
		registryLoginCommand   = "helm registry login nvcr.io --username '$oauthtoken' --password-stdin"
		serviceMonitorCommand  = "kubectl get servicemonitor/nvcf-default-monitors-state-metrics --namespace monitoring --context k3d-ncp-local -o name"
		podMonitorCommand      = "kubectl get podmonitor/nvcf-default-monitors-worker --namespace monitoring --context k3d-ncp-local -o name"
		collectorValuesCommand = "helm get values nvca-operator --namespace nvca-operator --kube-context k3d-ncp-local -o yaml"
		collectorYAMLCommand   = "kubectl get opentelemetrycollector/nvcf-observability --namespace monitoring --context k3d-ncp-local -o yaml"
		serviceKeyCommand      = `bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" |` +
			` kubectl --context k3d-ncp-local create secret generic ngc-service-api-key` +
			` --namespace nvca-system --from-file=ngc-service-api-key=/dev/stdin --dry-run=client -o yaml |` +
			` kubectl --context k3d-ncp-local apply -f -'`
		restartNVCACommand = "kubectl --context k3d-ncp-local delete pod --namespace nvca-system --selector app.kubernetes.io/name=nvca --wait=false"
	)
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		registryLoginCommand:           {ExitCode: 0},
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
		serviceMonitorCommand:          {ExitCode: 0},
		podMonitorCommand:              {ExitCode: 0},
		collectorValuesCommand:         {ExitCode: 0, Stdout: "selfManaged:\n  otelCollector:\n    enabled: true\n    imageRepository: nvcr.io/test-org/test-team/nvcf-otel-collector\n"},
		serviceKeyCommand:              {ExitCode: 0},
		restartNVCACommand:             {ExitCode: 0},
		"helm list --all-namespaces --kube-context k3d-ncp-local -o json": {
			ExitCode: 0,
			Stdout:   observabilityAllHelmListJSON(),
		},
		collectorYAMLCommand: {
			ExitCode: 0,
			Stdout:   observabilityCollectorYAML(),
		},
	}))
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	writeProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeHelmfileRegisterValues(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "observability-all.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "observability-all-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}

	runs := suite.Runner.(*fakeRunner).runs
	for _, command := range []string{
		registryLoginCommand,
		serviceMonitorCommand,
		podMonitorCommand,
		collectorValuesCommand,
		serviceKeyCommand,
		restartNVCACommand,
	} {
		if !commandRanExactly(runs, command) {
			t.Fatalf("exact command was never invoked: %s", command)
		}
	}
	for _, commandFragment := range []string{
		"deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-observability-all KUBECONFIG_FILE=/repo-root-placeholder/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml",
		"register-cluster CLUSTER_NAME=ncp-local" +
			" CONTROL_PLANE_PROFILE=/repo-root-placeholder/deploy/stacks/self-managed/out/control-plane-profile.yaml" +
			" COMPUTE_KUBE_CONTEXT=k3d-ncp-local" +
			" KUBECONFIG_FILE=/repo-root-placeholder/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml",
		"deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-observability-all KUBECONFIG_FILE=/repo-root-placeholder/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml",
	} {
		if !commandRanThatContains(runs, commandFragment) {
			t.Fatalf("command containing %q was never invoked", commandFragment)
		}
	}
	for _, run := range runs {
		if strings.HasPrefix(run, "kubectl apply -f ") {
			t.Fatalf("manifest apply relied on the ambient kube context: %s", run)
		}
		if strings.Contains(run, "test-key") {
			t.Fatalf("NGC API key leaked into command arguments: %s", run)
		}
	}

	for _, stack := range []string{"self-managed", "observability", "nvcf-compute-plane"} {
		environmentPath := filepath.Join(suite.Config.RepoRoot, "deploy", "stacks", stack, "environments", "local-bdd-observability-all.yaml")
		profile, found, err := dsl.ReadYAMLKey(environmentPath, "observability.profile")
		if err != nil {
			t.Fatalf("read %s observability profile: %v", stack, err)
		}
		if !found || profile != "all" {
			t.Fatalf("%s observability profile = %q, found = %t; want all", stack, profile, found)
		}
	}

	assertions := []struct {
		stack string
		key   string
		want  string
	}{
		{stack: "self-managed", key: "functionAutoscaler.image.tag", want: "1.18.10"},
		{stack: "nvcf-compute-plane", key: "global.nvcaOperator.selfManaged.otelCollector.enabled", want: "true"},
	}
	for _, assertion := range assertions {
		environmentPath := filepath.Join(suite.Config.RepoRoot, "deploy", "stacks", assertion.stack, "environments", "local-bdd-observability-all.yaml")
		got, found, err := dsl.ReadYAMLKey(environmentPath, assertion.key)
		if err != nil {
			t.Fatalf("read %s override %s: %v", assertion.stack, assertion.key, err)
		}
		if !found || got != assertion.want {
			t.Fatalf("%s override %s = %q, found = %t; want %q", assertion.stack, assertion.key, got, found, assertion.want)
		}
	}
}

func observabilityAllHelmListJSON() string {
	return `[
{"name":"prometheus-operator-crds","namespace":"monitoring","revision":"1","status":"deployed"},
{"name":"opentelemetry-operator","namespace":"monitoring","revision":"1","status":"deployed"},
{"name":"victoria-metrics","namespace":"monitoring","revision":"1","status":"deployed"},
{"name":"otel-collector","namespace":"monitoring","revision":"1","status":"deployed"},
{"name":"default-monitors","namespace":"monitoring","revision":"1","status":"deployed"},
{"name":"nvca-operator","namespace":"nvca-operator","revision":"1","status":"deployed"}
]`
}

// TestMultiClusterHelmfileFeatureFileWiresToSteps runs
// multi-cluster-helmfile.feature against a fake runner. The same
// fixture seeds and canned helm-list outputs cover the scenarios;
// the helm list canned keys carry --kube-context because the
// multi-cluster feature targets the cp and compute clusters
// explicitly.
func TestMultiClusterHelmfileFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	const taskSmokeCommand = "env NVCT_BDD_TASK_INSTANCE_TYPE=NCP.GPU.H100_1x tests/bdd/scripts/run-nvct-task-smoke.sh"
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"helm list --all-namespaces --kube-context k3d-ncp-local-cp -o json":        {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		"helm list --all-namespaces --kube-context k3d-ncp-local-compute-1 -o json": {ExitCode: 0, Stdout: helmListNVCAJSON()},
		"kubectl --context k3d-ncp-local-cp get configmap/nvcf-api-remote-config -n nvcf -o yaml": {
			ExitCode: 0,
			Stdout: "data:\n  nvcf-api.yaml: |\n    nvcf:\n" +
				"      llm-request-router:\n        worker-address: https://llm-request-router.nvcf.svc.cluster.local:50071\n" +
				"      sidecars:\n        llm-router-client-image: nvcr.io/test-org/test-team/pylon:0.14.1\n",
		},
		"kubectl get certificate/stargate-quic-tls --namespace nvcf --context k3d-ncp-local-cp -o yaml": {
			ExitCode: 0,
			Stdout:   "spec:\n  secretName: stargate-quic-tls\n  dnsNames:\n    - llm-request-router.nvcf.svc.cluster.local\n    - '*.llm-request-router-headless.nvcf.svc.cluster.local'\n",
		},
		"kubectl get certificate/llm-request-router-grpc-tls --namespace envoy-gateway-system --context k3d-ncp-local-cp -o yaml": {
			ExitCode: 0,
			Stdout:   "spec:\n  secretName: llm-request-router-grpc-tls\n  dnsNames:\n    - llm-request-router.nvcf.svc.cluster.local\n  issuerRef:\n    kind: ClusterIssuer\n    name: nvcf-openbao-pki\n",
		},
		"kubectl get gateway/grpc-gw --namespace envoy-gateway-system --context k3d-ncp-local-cp -o yaml": {
			ExitCode: 0,
			Stdout: "spec:\n  gatewayClassName: eg\n  listeners:\n" +
				"    - name: tcp\n      protocol: TCP\n      port: 10081\n      allowedRoutes:\n        namespaces:\n          from: All\n" +
				"    - name: worker-tcp\n      protocol: TCP\n      port: 10086\n      allowedRoutes:\n        namespaces:\n          from: All\n" +
				"    - name: llm-grpc\n" +
				"      protocol: HTTPS\n" +
				"      port: 50071\n" +
				"      tls:\n" +
				"        mode: Terminate\n" +
				"        certificateRefs:\n" +
				"          - group: \"\"\n" +
				"            kind: Secret\n" +
				"            name: llm-request-router-grpc-tls\n" +
				"      allowedRoutes:\n" +
				"        namespaces:\n" +
				"          from: All\n" +
				"    - name: llm-quic\n      protocol: UDP\n      port: 50072\n      allowedRoutes:\n        namespaces:\n          from: All\n",
		},
		"kubectl get backendtrafficpolicy/llm-worker-grpc-streams --namespace envoy-gateway-system --context k3d-ncp-local-cp -o yaml": {
			ExitCode: 0,
			Stdout:   "spec:\n  targetRefs:\n    - group: gateway.networking.k8s.io\n      kind: GRPCRoute\n      name: llm-worker-grpc\n  timeout:\n    http:\n      requestTimeout: 0s\n      maxStreamDuration: 0s\n",
		},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --request-body '{\"message\":\"bdd-echo\",\"repeats\":1}' --timeout 120 --poll-duration 5": {
			ExitCode: 0,
			Stdout:   "Function invocation completed!\n\nResponse:\n{\"rawResponse\":\"bdd-echo\"}\n",
		},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke" +
			" --grpc --grpc-plaintext --grpc-service Echo --grpc-method EchoMessage" +
			" --request-body '{\"message\":\"bdd-grpc-echo\"}' --timeout 120 --poll-duration 5": {
			ExitCode: 0,
			Stdout:   "Function invocation completed!\n\nResponse:\n{\"message\":\"bdd-grpc-echo\"}\n",
		},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke" +
			" --inference-url /v1/chat/completions --model-name openai-compatible-sample" +
			" --request-body '{\"messages\":[{\"role\":\"user\",\"content\":\"bdd-multi-llm-echo\"}]}' --timeout 120": {
			ExitCode: 0,
			Stdout: "Function invocation completed!\n\nResponse:\n" +
				`{"object":"chat.completion","choices":[{"message":{"content":"This is a fixed 128-byte response for routing and contract validation, not token-generation capacity."}}]}` +
				"\n",
		},
		`curl -s --connect-timeout 5 --max-time 30 -o /dev/null -w "%{http_code}" -X POST ` +
			`http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" ` +
			`-H "traceparent: 00-00000000000000000000000000001019-0000000000001019-01" ` +
			`-d '{"model":"unauthenticated/check","messages":[]}'`: {
			ExitCode: 0,
			Stdout:   "401",
		},
		taskSmokeCommand: {
			ExitCode: 0,
			Stdout:   "Task bdd-nvct-task-smoke status: COMPLETED\n",
		},
		// Conflict precheck: feature asserts the conflicting
		// single-cluster is absent.
		"k3d cluster get ncp-local": {ExitCode: 1},
	}))
	seedHelmfileLocalBDDMultiFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDMultiFixture(t, suite.Config.RepoRoot)
	assertFileContains(t, filepath.Join(suite.Config.RepoRoot, "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml"),
		"workerConnectBaseURL: http://grpc.nvcf.svc.cluster.local:10086",
		"chartPath: ../../../helm/gateway-routes/chart",
		"chartPath: ../../../helm/llm-request-router/llm-request-router",
		"llmRequestRouterAddress: https://llm-request-router.nvcf.svc.cluster.local:50071",
		"secretName: llm-request-router-grpc-tls",
		"grpcWorker:",
		"llmWorker:",
		"enabled: true",
		"listenerName: worker-tcp",
	)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	writeMulticlusterProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeMulticlusterComputeRegisterValues(t, suite.Config.RepoRoot, "nvcf-compute-plane", "ncp-local-compute-1")

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "multi-cluster-helmfile.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "multi-cluster-helmfile-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	environmentPath := filepath.Join(suite.Config.RepoRoot, "deploy", "stacks", "self-managed", "environments", "local-bdd.yaml")
	for _, assertion := range []struct {
		key  string
		want string
	}{
		{key: "global.workerEndpoints.llmRequestRouterAddress", want: "https://llm-request-router.nvcf.svc.cluster.local:50071"},
		{key: "addons.llm.requestRouter.chartPath", want: "../../../helm/llm-request-router/llm-request-router"},
		{key: "addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress", want: "https://llm-request-router.nvcf.svc.cluster.local:50071"},
		{key: "addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress", want: "llm-request-router.nvcf.svc.cluster.local:50072"},
		{key: "addons.llm.requestRouter.grpcTls.enabled", want: "true"},
		{key: "addons.llm.requestRouter.grpcTls.mode", want: "certManager"},
		{key: "addons.llm.requestRouter.grpcTls.secretName", want: "llm-request-router-grpc-tls"},
		{key: "addons.llm.requestRouter.grpcTls.dnsNames[0]", want: "llm-request-router.nvcf.svc.cluster.local"},
		{key: "addons.llm.pki.allowedDomains", want: "cluster.local"},
		{key: "addons.llm.pki.dnsNames[0]", want: "llm-request-router.nvcf.svc.cluster.local"},
		{key: "addons.llm.pki.dnsNames[1]", want: "*.llm-request-router-headless.nvcf.svc.cluster.local"},
		{key: "ingress.gatewayApi.chartPath", want: "../../../helm/gateway-routes/chart"},
		{key: "ingress.gatewayApi.routes.llmWorker.enabled", want: "true"},
		{key: "ingress.gatewayApi.routes.llmWorker.backend.namespace", want: "nvcf"},
		{key: "ingress.gatewayApi.gateways.llmGrpc.listenerName", want: "llm-grpc"},
		{key: "ingress.gatewayApi.gateways.llmQuic.listenerName", want: "llm-quic"},
	} {
		got, found, err := dsl.ReadYAMLKey(environmentPath, assertion.key)
		if err != nil {
			t.Fatalf("read multi-cluster override %s: %v", assertion.key, err)
		}
		if !found || got != assertion.want {
			t.Fatalf("multi-cluster override %s = %q, found = %t; want %q", assertion.key, got, found, assertion.want)
		}
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "deploy/stacks/nvcf-compute-plane install") {
		t.Fatal("compute-plane install make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke") {
		t.Fatal("function invoke CLI command was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke --grpc --grpc-plaintext") {
		t.Fatal("gRPC function invoke CLI command was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "bdd-grpc-load-tester-supreme") {
		t.Fatal("gRPC sample function was never created or deployed")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"function create --name bdd-grpc-load-tester-supreme",
		"load_tester_supreme:0.0.8",
		"--health-protocol GRPC --health-uri / --health-port 8001") {
		t.Fatal("gRPC sample function was not configured with a gRPC health endpoint")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"api-key generate --for function",
		"--description bdd-load-tester-supreme") {
		t.Fatal("HTTP sample function API key was not generated for the function service")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"api-key generate --for function",
		"--description bdd-grpc-load-tester-supreme") {
		t.Fatal("gRPC sample function API key was not generated for the function service")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"function create --name bdd-multi-openai-compatible-sample",
		"nvcf-openai-compatible-sample:local",
		"--function-type LLM",
		"--llm-model") {
		t.Fatal("multi-cluster LLM sample function was not created with the LLM function type and model config")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke --inference-url /v1/chat/completions --model-name openai-compatible-sample") {
		t.Fatal("multi-cluster LLM function invoke CLI command was never invoked")
	}
	if !commandRanThatContainsAll(suite.Runner.(*fakeRunner).runs,
		"api-key generate --for function",
		"--description bdd-multi-openai-compatible-sample") {
		t.Fatal("multi-cluster LLM sample function API key was not generated for the function service")
	}
	cleanupCount := 0
	for _, command := range suite.Runner.(*fakeRunner).runs {
		if strings.Contains(command, "function delete --deployment-only") {
			cleanupCount++
		}
	}
	if cleanupCount != 3 {
		t.Fatalf("function deployment cleanup commands = %d, want 3", cleanupCount)
	}
	if commandRanThatContains(suite.Runner.(*fakeRunner).runs, "api-key generate --description bdd-nvct-task-smoke") {
		t.Fatal("NVCT task smoke should not use nvcf-cli api-key generate because it emits function resources")
	}
	if !commandRanExactly(suite.Runner.(*fakeRunner).runs, taskSmokeCommand) {
		t.Fatal("NVCT task API smoke script was not invoked with the local instance type")
	}
	assertFunctionDeploymentsUseInstanceType(t, suite.Runner.(*fakeRunner).runs, "NCP.GPU.H100_1x", 3)
}

// TestSingleClusterHelmfileUpstreamImagesFeatureFileWiresToSteps runs the
// focused upstream-image feature against a fake runner. The seeded global
// template contains the exact documentation blocks so the ledger-backed
// substitutions exercise real file writes.
func TestSingleClusterHelmfileUpstreamImagesFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	upstreamReloader := "docker.io/natsio/nats-server-config-reloader:0.23.0"
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
		"helm list --all-namespaces --kube-context k3d-ncp-local -o json": {
			ExitCode: 0,
			Stdout:   helmListAllNamespacesJSON(),
		},
		`kubectl get statefulset nats -n nats-system -o 'jsonpath={.spec.template.spec.containers[?(@.name=="reloader")].image}'`: {
			ExitCode: 0,
			Stdout:   upstreamReloader,
		},
	}))
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedUpstreamImageStackInputs(t, suite.Config.RepoRoot)
	seedUpstreamImageRenderOutput(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "single-cluster-helmfile-upstream-images.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "single-cluster-helmfile-upstream-images-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	runs := suite.Runner.(*fakeRunner).runs
	if !commandRanThatContains(runs, "template HELMFILE_ENV=local-bdd") {
		t.Fatal("helmfile template make target was never invoked")
	}
	for _, selector := range []string{
		"HELMFILE_SELECTOR=release-group=dependencies",
		"HELMFILE_SELECTOR=name=ess-api",
		"HELMFILE_SELECTOR=name=nats-auth-callout-service",
		"HELMFILE_SELECTOR=name=api",
	} {
		if !commandRanThatContains(runs, "install HELMFILE_ENV=local-bdd "+selector) {
			t.Fatalf("helmfile install selector %q was never invoked", selector)
		}
	}
	if commandRanThatContains(runs, "rg --fixed-strings") {
		t.Fatal("rendered manifest assertions should not invoke rg")
	}
}

// TestObservabilityDisabledFeatureFileWiresToSteps runs the render-only
// observability-disabled feature against a fake runner. The fixture setup
// mirrors the local Helmfile inputs while the feature asserts disabled profile
// renders omit observability resources from both stacks.
func TestObservabilityDisabledFeatureFileWiresToSteps(t *testing.T) {
	const registryLoginCommand = "helm registry login nvcr.io --username '$oauthtoken' --password-stdin"
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	suite := newWiringSuite(t, newFakeRunner(nil))
	t.Setenv("REPO_ROOT", suite.Config.RepoRoot)
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedComputePlaneLocalBDDFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedObservabilityDisabledRegistrationValuesFixture(t, suite.Config.RepoRoot)
	seedObservabilityDisabledRenderOutput(t, suite.Config.RepoRoot)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "observability-disabled.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "observability-disabled-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	runs := suite.Runner.(*fakeRunner).runs
	if !commandRanExactly(runs, registryLoginCommand) {
		t.Fatal("Helm OCI registry login command was never invoked")
	}
	if !commandRanThatContains(runs, "deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-observability-disabled") {
		t.Fatal("control-plane Helmfile template command was never invoked")
	}
	if !commandRanThatContains(runs, "deploy/stacks/nvcf-compute-plane template CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-observability-disabled") {
		t.Fatal("compute-plane Helmfile template command was never invoked")
	}
}

func seedObservabilityDisabledRegistrationValuesFixture(t *testing.T, repoRoot string) {
	t.Helper()
	fixturePath := filepath.Join("fixtures", "ncp-local-register-values.yaml")
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read registration fixture %s: %v", fixturePath, err)
	}
	writeFixture(t, repoRoot, "ncp-local-register-values.yaml", string(body))
}

func seedObservabilityDisabledRenderOutput(t *testing.T, repoRoot string) {
	t.Helper()
	for _, relativePath := range []string{
		filepath.Join("control-plane", "api.yaml"),
		filepath.Join("compute-plane", "nvca.yaml"),
	} {
		path := filepath.Join(repoRoot, "tests", "bdd", "out", "observability-disabled", relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create rendered output directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("kind: Deployment\n"), 0o644); err != nil {
			t.Fatalf("write rendered output %s: %v", relativePath, err)
		}
	}
}

// helmListAllNamespacesJSON returns canned helm-list output covering
// every release the helmfile install scenario asserts.
func helmListAllNamespacesJSON() string {
	return `[
{"name":"nats","namespace":"nats-system","status":"deployed"},
{"name":"cert-manager","namespace":"cert-manager","status":"deployed"},
{"name":"openbao-server","namespace":"vault-system","status":"deployed"},
{"name":"nvcf-pki","namespace":"cert-manager","status":"deployed"},
{"name":"cassandra","namespace":"cassandra-system","status":"deployed"},
{"name":"api-keys","namespace":"api-keys","status":"deployed"},
{"name":"sis","namespace":"sis","status":"deployed"},
{"name":"api","namespace":"nvcf","status":"deployed"},
{"name":"nvct-api","namespace":"nvcf","status":"deployed"},
{"name":"invocation-service","namespace":"nvcf","status":"deployed"},
{"name":"grpc-proxy","namespace":"nvcf","status":"deployed"},
{"name":"ess-api","namespace":"ess","status":"deployed"},
{"name":"notary-service","namespace":"nvcf","status":"deployed"},
{"name":"admin-issuer-proxy","namespace":"api-keys","status":"deployed"},
{"name":"reval","namespace":"nvcf","status":"deployed"},
{"name":"nats-auth-callout-service","namespace":"nats-system","status":"deployed"},
{"name":"ingress","namespace":"envoy-gateway-system","status":"deployed"},
{"name":"llm-request-router","namespace":"nvcf","status":"deployed"},
{"name":"llm-api-gateway","namespace":"nvcf","status":"deployed"},
{"name":"nvca-operator","namespace":"nvca-operator","status":"deployed"}
]`
}

// helmListNVCAJSON returns canned helm-list output for the
// nvca-operator namespace.
func helmListNVCAJSON() string {
	return `[{"name":"nvca-operator","namespace":"nvca-operator","status":"deployed"}]`
}

// seedHelmfileLocalBDDFixture writes the tests/bdd/fixtures/
// self-managed-local-bdd.yaml fixture the single-cluster helmfile
// feature copies onto the stack's environment file. The body matches
// what the real fixture in the repo ships so the wiring test does
// not depend on the real fixture tree.
func seedHelmfileLocalBDDFixture(t *testing.T, repoRoot string) {
	t.Helper()
	writeFixture(t, repoRoot, "self-managed-local-bdd.yaml", `global:
  storageClass: local-path
  workerEndpoints:
    essServiceURL: http://ess-api.ess.svc.cluster.local:8080
    invocationServiceURL: http://invocation.nvcf.svc.cluster.local:8080
addons:
  llm:
    enabled: true
`)
}

// seedUpstreamImageStackInputs writes the two stack files the focused
// upstream-image feature copies and edits in its wiring test.
func seedUpstreamImageStackInputs(t *testing.T, repoRoot string) {
	t.Helper()
	clusterSecretsDir := filepath.Join(repoRoot, "tools", "ncp-local-cluster", "secrets")
	if err := os.MkdirAll(clusterSecretsDir, 0o755); err != nil {
		t.Fatalf("mkdir cluster secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clusterSecretsDir, "docker-config.json"), []byte(`{
  "auths": {
    "nvcr.io": {}
  }
}`), 0o600); err != nil {
		t.Fatalf("write docker config: %v", err)
	}
	stackDir := filepath.Join(repoRoot, "deploy", "stacks", "self-managed")
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		t.Fatalf("mkdir stack dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stackDir, "Makefile.dist"), []byte("template:\n\t@true\ninstall:\n\t@true\n"), 0o644); err != nil {
		t.Fatalf("write Makefile.dist: %v", err)
	}
	global := `nats:
  reloader:
    image:
      registry: {{ .Values.global.image.registry }}
      repository: {{ .Values.global.image.repository }}/nats-server-config-reloader
      tag: "0.23.0"
api:
  accountBootstrap:
    image:
      registry: {{ .Values.global.image.registry }}
      repository: {{ .Values.global.image.repository }}/alpine-k8s
      tag: 1.36.1
      pullPolicy: IfNotPresent
`
	if err := os.WriteFile(filepath.Join(stackDir, "global.yaml.gotmpl"), []byte(global), 0o644); err != nil {
		t.Fatalf("write global template: %v", err)
	}
}

// seedUpstreamImageRenderOutput writes representative render directories for
// the positive fixed-string assertions in the upstream-image feature.
func seedUpstreamImageRenderOutput(t *testing.T, repoRoot string) {
	t.Helper()
	manifests := map[string]string{
		"01-nats/templates/nats.yaml": `# Source: helm-nvcf-nats/templates/nkey-secret.yaml
image: docker.io/natsio/nats-server-config-reloader:0.23.0
`,
		"02-cassandra/templates/cassandra.yaml": "image: nvcf-cassandra-migrations:latest\n",
		"03-api/templates/api.yaml":             "image: docker.io/alpine/k8s:1.36.1\n",
	}
	root := filepath.Join(repoRoot, "deploy", "stacks", "self-managed", "out")
	for relativePath, body := range manifests {
		filePath := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatalf("mkdir rendered manifest dir: %v", err)
		}
		if err := os.WriteFile(filePath, []byte(body), 0o644); err != nil {
			t.Fatalf("write rendered manifest: %v", err)
		}
	}
}

// seedPKIRenderOutput writes the representative PKI resources asserted by the
// focused Helmfile feature wiring test.
func seedPKIRenderOutput(t *testing.T, repoRoot string) {
	t.Helper()
	manifest := `kind: ClusterIssuer
metadata:
  name: "nvcf-openbao-pki"
spec:
  dnsNames:
    - llm-request-router.nvcf.svc.cluster.local
env:
  - name: ADDONS_LLM_ENABLED
    value: "true"
  - name: NVCF_SERVICE_PKI_ALLOWED_DOMAINS
    value: "nvcf.svc.cluster.local"
image: nvcr.io/test-org/test-team/nvcf-openbao-migrations:0.16.2
`
	filePath := filepath.Join(repoRoot, "deploy", "stacks", "self-managed", "out", "01-pki", "templates", "pki.yaml")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("mkdir rendered PKI manifest dir: %v", err)
	}
	if err := os.WriteFile(filePath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write rendered PKI manifest: %v", err)
	}
}

// seedHelmfileLocalBDDMultiFixture writes the multi-cluster variant
// the multi-cluster helmfile feature copies onto the env file. Its
// workerEndpoints and nvcaOperator.selfManaged URLs use the service
// DNS names from the local stack. In multi-cluster local runs those
// names resolve to alias Services in the compute cluster.
func seedHelmfileLocalBDDMultiFixture(t *testing.T, repoRoot string) {
	t.Helper()
	writeFixture(t, repoRoot, "self-managed-local-bdd-multi.yaml", `global:
  storageClass: local-path
  workerEndpoints:
    essServiceURL: http://ess-api.ess.svc.cluster.local:8080
    invocationServiceURL: http://invocation.nvcf.svc.cluster.local:8080
    llmRequestRouterAddress: https://llm-request-router.nvcf.svc.cluster.local:50071
  nvcaOperator:
    selfManaged:
      icmsServiceURL: http://api.sis.svc.cluster.local:8080
      revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222
addons:
  llm:
    enabled: true
    requestRouter:
      chartPath: ../../../helm/llm-request-router/llm-request-router
      grpcTls:
        enabled: true
        mode: certManager
        secretName: llm-request-router-grpc-tls
        dnsNames:
          - llm-request-router.nvcf.svc.cluster.local
      backendRouter:
        pylonGrpcDialAddress: https://llm-request-router.nvcf.svc.cluster.local:50071
        pylonReverseTunnelDialAddress: llm-request-router.nvcf.svc.cluster.local:50072
    pki:
      enabled: true
      allowedDomains: cluster.local
      dnsNames:
        - llm-request-router.nvcf.svc.cluster.local
        - "*.llm-request-router-headless.nvcf.svc.cluster.local"
grpcproxy:
  workerConnectBaseURL: http://grpc.nvcf.svc.cluster.local:10086
ingress:
  gatewayApi:
    chartPath: ../../../helm/gateway-routes/chart
    gateways:
      llmGrpc:
        listenerName: llm-grpc
      llmQuic:
        listenerName: llm-quic
    routes:
      grpcWorker:
        enabled: true
        listenerName: worker-tcp
      llmWorker:
        enabled: true
        backend:
          namespace: nvcf
`)
}

func seedComputePlaneLocalBDDFixture(t *testing.T, repoRoot string) {
	t.Helper()
	writeFixture(t, repoRoot, "nvcf-compute-plane-local-bdd.yaml", `global:
  nodeSelectors:
    enabled: false
  nvcaOperator:
    selfManaged:
      icmsServiceURL: http://api.sis.svc.cluster.local:8080
      revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222
agentConfig:
  mergeConfig: |
    cluster:
      validationPolicy:
        name: Unrestricted
    workload:
      stargateQUICInsecure: false
`)
}

func seedComputePlaneLocalBDDMultiFixture(t *testing.T, repoRoot string) {
	t.Helper()
	writeFixture(t, repoRoot, "nvcf-compute-plane-local-bdd-multi.yaml", `global:
  nodeSelectors:
    enabled: false
  nvcaOperator:
    selfManaged:
      icmsServiceURL: http://api.sis.svc.cluster.local:8080
      revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222
agentConfig:
  mergeConfig: |
    cluster:
      validationPolicy:
        name: Unrestricted
    workload:
      stargateQUICInsecure: false
`)
}

func writeFixture(t *testing.T, repoRoot, name, body string) {
	t.Helper()
	fixturePath := filepath.Join(repoRoot, "tests", "bdd", "fixtures", name)
	if err := os.MkdirAll(filepath.Dir(fixturePath), 0o755); err != nil {
		t.Fatalf("mkdir fixtures: %v", err)
	}
	if err := os.WriteFile(fixturePath, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

// seedStackBaseYaml writes a minimal stand-in for
// deploy/stacks/self-managed/environments/base.yaml so the EKS
// Helmfile feature's `I copy ... base.yaml to eks-bdd.yaml` step has a
// source and the `I update yaml file ... eks-bdd.yaml with keys:` step
// can upsert dotted paths into it. The body is intentionally minimal:
// dsl/yamledit.go upserts missing intermediate maps, so an empty file
// suffices.
func seedStackBaseYaml(t *testing.T, repoRoot string) {
	t.Helper()
	envPath := filepath.Join(repoRoot, "deploy", "stacks", "self-managed", "environments", "base.yaml")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatalf("mkdir env dir: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("global: {}\n"), 0o644); err != nil {
		t.Fatalf("write base.yaml: %v", err)
	}
}

func seedComputePlaneBaseYaml(t *testing.T, repoRoot string) {
	t.Helper()
	envPath := filepath.Join(repoRoot, "deploy", "stacks", "nvcf-compute-plane", "environments", "base.yaml")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatalf("mkdir compute env dir: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("global: {}\n"), 0o644); err != nil {
		t.Fatalf("write compute base.yaml: %v", err)
	}
}

// seedNVCFCLINonlocalTemplate writes a minimal stand-in for
// tests/bdd/fixtures/nvcf-cli-nonlocal.yaml.template. The EKS feature's
// @nvca-registration scenario copies this into tests/bdd/out/ and
// patches the URL + Host fields. The body needs to be valid YAML so
// the dotted-path upserts can extend it.
func seedNVCFCLINonlocalTemplate(t *testing.T, repoRoot string) {
	t.Helper()
	writeFixture(t, repoRoot, "nvcf-cli-nonlocal.yaml.template", `api_keys_service_id: "wiring-test-service-id"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"
client_id: "nvcf-default"
`)
}

// writeEKSRegisterValues seeds the register-values handoff the EKS
// @nvca-registration scenarios read back. The wiring test's fakeRunner
// does not actually run the registration command, so the file must be
// pre-seeded with the same shape the assertions expect: ncaID, region
// matching EKS_REGION, identitySource=psat, and non-empty clusterID +
// clusterGroupID.
func writeEKSRegisterValues(t *testing.T, repoRoot, clusterName, region string) {
	t.Helper()
	body := `clusterID: 11111111-2222-3333-4444-555555555555
clusterGroupID: aaaa-bbbb-cccc-dddd
ncaID: nvcf-default
region: ` + region + `
selfManaged:
  identitySource: psat
  icmsServiceURL: http://wiring-elb.example.invalid
  revalServiceURL: http://wiring-elb.example.invalid
  natsURL: nats://wiring-elb.example.invalid:4222
`
	writeArtifact(t, repoRoot, "nvcf-compute-plane", clusterName+"-register-values.yaml", body)
	writeRegistrationArtifact(t, repoRoot, "nvcf-compute-plane", clusterName+"-register-values.yaml", body)
}

// TestSingleClusterEKSHelmfileFeatureFileWiresToSteps runs the EKS
// Helmfile feature against a fake CommandRunner. The fakeRunner
// returns ExitCode 0 for unknown commands by default, so only the
// commands with assertion-driven output (gateway-address jsonpath,
// helm list JSON, HTTPRoute YAML) need canned responses. The
// I export step records the gateway jsonpath stdout into the env
// Ledger; subsequent ${EKS_GATEWAY_ADDR} interpolations then use the
// exported value, which is what the @control-plane httproute
// assertion expects to see.
func TestSingleClusterEKSHelmfileFeatureFileWiresToSteps(t *testing.T) {
	const (
		eksContext           = "arn:aws:eks:us-east-1:000000000000:cluster/wiring-test"
		eksClusterName       = "wiring-test"
		eksRegion            = "us-east-1"
		wiringGatewayLB      = "wiring-elb.example.invalid"
		registryLoginCommand = "helm registry login nvcr.io --username '$oauthtoken' --password-stdin"
	)
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	t.Setenv("EKS_CONTEXT", eksContext)
	t.Setenv("EKS_CLUSTER_NAME", eksClusterName)
	t.Setenv("EKS_REGION", eksRegion)
	// EKS_GATEWAY_ADDR is intentionally NOT preset: @gateway-setup's
	// `I export command output to environment variable` step is the
	// place that captures it from the canned `kubectl get gateway`
	// stdout below. Pre-setting would mask whether the export step is
	// wired into the suite.

	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		// @gateway-setup: kubectl get gateway returns the ELB hostname.
		// The export step captures this into EKS_GATEWAY_ADDR.
		"kubectl --context " + eksContext + " get gateway nvcf-gateway -n envoy-gateway -o jsonpath={.status.addresses[0].value}": {ExitCode: 0, Stdout: wiringGatewayLB},
		// @control-plane: helm list assertion covers the 16 deployed releases.
		"helm list --all-namespaces --kube-context " + eksContext + " -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		// @control-plane: HTTPRoute subset assertion expects api.<gw>.
		"kubectl get httproute/nvcf-api --namespace envoy-gateway --context " + eksContext + " -o yaml": {ExitCode: 0, Stdout: "spec:\n  hostnames:\n    - api." + wiringGatewayLB + "\n"},
	}))
	seedStackBaseYaml(t, suite.Config.RepoRoot)
	seedComputePlaneBaseYaml(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedNVCFCLINonlocalTemplate(t, suite.Config.RepoRoot)
	writeProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeEKSRegisterValues(t, suite.Config.RepoRoot, eksClusterName, eksRegion)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "single-cluster-eks-helmfile.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "single-cluster-eks-helmfile-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	if !commandRanExactly(suite.Runner.(*fakeRunner).runs, registryLoginCommand) {
		t.Fatal("Helm OCI registry login command was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install HELMFILE_ENV=eks-bdd") {
		t.Fatal("helmfile install make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "register-cluster CLUSTER_NAME="+eksClusterName) {
		t.Fatal("compute-plane register-cluster make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "deploy/stacks/nvcf-compute-plane install") {
		t.Fatal("compute-plane install make target was never invoked")
	}
}

// TestMultiClusterEKSHelmfileFeatureFileWiresToSteps runs the
// multi-cluster EKS Helmfile feature against a fake CommandRunner.
// Canned outputs cover the control-plane gateway-address jsonpath, the
// control-plane helm-list, the API HTTPRoute and ConfigMaps, the compute
// nvca-operator helm-list, and the function invoke. The helm-list keys
// carry distinct --kube-context values so the test exercises the
// control-plane vs compute split. @gateway-setup's export step captures
// EKS_GATEWAY_ADDR from the canned gateway stdout.
func TestMultiClusterEKSHelmfileFeatureFileWiresToSteps(t *testing.T) {
	const (
		cpContext            = "arn:aws:eks:us-east-1:000000000000:cluster/wiring-cp"
		computeContext       = "arn:aws:eks:us-east-1:000000000000:cluster/wiring-compute"
		computeClusterName   = "wiring-compute"
		eksRegion            = "us-east-1"
		wiringGatewayLB      = "wiring-cp-elb.example.invalid"
		wiringGatewayDomain  = "192-0-2-10.nip.io"
		registryLoginCommand = "helm registry login nvcr.io --username '$oauthtoken' --password-stdin"
	)
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	t.Setenv("EKS_CONTEXT", cpContext)
	t.Setenv("EKS_COMPUTE_CONTEXT", computeContext)
	t.Setenv("EKS_COMPUTE_CLUSTER_NAME", computeClusterName)
	t.Setenv("EKS_REGION", eksRegion)
	t.Setenv("HOME", "/home/wiring")
	// EKS_GATEWAY_ADDR is intentionally NOT preset: @gateway-setup's
	// export step captures it from the canned gateway stdout below. The
	// gateway-domain export is likewise exercised from its canned result.
	taskSmokeCommand := strings.Join([]string{
		"env",
		"NVCT_BDD_STATE_PATH=/home/wiring/.nvcf-cli.nvcf-cli-eks-bdd-multi.state",
		"NVCT_BDD_API_KEYS_URL=http://" + wiringGatewayLB + "/v1/keys",
		"NVCT_BDD_API_KEYS_HOST=api-keys." + wiringGatewayDomain,
		"NVCT_BDD_TASKS_URL=http://" + wiringGatewayLB + "/v1/nvct/tasks",
		"NVCT_BDD_TASKS_HOST=tasks." + wiringGatewayDomain,
		"NVCT_BDD_TASK_BACKEND=" + computeClusterName,
		"NVCT_BDD_TASK_INSTANCE_TYPE=NCP.GPU.H100_8x",
		"tests/bdd/scripts/run-nvct-task-smoke.sh",
	}, " ")
	pullSecretCommand := "kubectl get secret/nvcr-pull-secret --namespace nvca-system --context " + computeContext + " -o name"

	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		// @gateway-setup: control-plane gateway address -> EKS_GATEWAY_ADDR.
		"kubectl --context " + cpContext + " get gateway nvcf-gateway -n envoy-gateway -o jsonpath={.status.addresses[0].value}": {ExitCode: 0, Stdout: wiringGatewayLB},
		"tests/bdd/scripts/resolve-gateway-domain.sh " + wiringGatewayLB:                                                         {ExitCode: 0, Stdout: wiringGatewayDomain},
		// control-plane helm list assertion.
		"helm list --all-namespaces --kube-context " + cpContext + " -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		// control-plane API HTTPRoute hostname assertion.
		"kubectl get httproute/nvcf-api --namespace envoy-gateway --context " + cpContext + " -o yaml": {ExitCode: 0, Stdout: "spec:\n  hostnames:\n    - api." + wiringGatewayDomain + "\n"},
		// control-plane API environment config assertions.
		"kubectl get configmap/nvcf-api-env --namespace nvcf --context " + cpContext + " -o yaml": {
			ExitCode: 0,
			Stdout: "data:\n" +
				"  NVCF_FQDN: http://api." + wiringGatewayDomain + "\n" +
				"  NVCF_GLOBAL_FQDN_GRPC: http://worker-api." + wiringGatewayDomain + "\n" +
				"  NVCF_NATS_WORKER_URL: nats://" + wiringGatewayLB + ":4222\n",
		},
		"kubectl get configmap/nvct-api-env --namespace nvcf --context " + cpContext + " -o yaml": {
			ExitCode: 0,
			Stdout: "data:\n" +
				"  NVCT_FQDN: http://tasks." + wiringGatewayDomain + "\n" +
				"  NVCT_GLOBAL_FQDN_GRPC: http://worker-tasks." + wiringGatewayDomain + "\n",
		},
		// compute nvca-operator helm list assertion.
		"helm list --all-namespaces --kube-context " + computeContext + " -o json": {ExitCode: 0, Stdout: helmListNVCAJSON()},
		pullSecretCommand: {ExitCode: 0},
		// @function-lifecycle: function invoke returns the echo payload.
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml function invoke --request-body '{\"message\":\"bdd-echo\",\"repeats\":1}' --timeout 120 --poll-duration 5": {
			ExitCode: 0,
			Stdout:   "Function invocation completed!\n\nResponse:\n{\"rawResponse\":\"bdd-echo\"}\n",
		},
		taskSmokeCommand: {
			ExitCode: 0,
			Stdout:   "Task bdd-nvct-task-smoke status: COMPLETED\n",
		},
	}))
	seedStackBaseYaml(t, suite.Config.RepoRoot)
	seedComputePlaneBaseYaml(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedNVCFCLINonlocalTemplate(t, suite.Config.RepoRoot)
	writeMulticlusterProfileHandoffArtifact(t, suite.Config.RepoRoot)
	writeEKSRegisterValues(t, suite.Config.RepoRoot, computeClusterName, eksRegion)

	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, "multi-cluster-eks-helmfile.feature")
	var out strings.Builder
	status := godog.TestSuite{
		Name: "multi-cluster-eks-helmfile-wiring",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths:  []string{featurePath},
			Strict: true,
			Output: &out,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d\n%s", status, out.String())
	}
	if !commandRanExactly(suite.Runner.(*fakeRunner).runs, registryLoginCommand) {
		t.Fatal("Helm OCI registry login command was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install HELMFILE_ENV=eks-bdd-multi") {
		t.Fatal("helmfile install make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "register-cluster CLUSTER_NAME="+computeClusterName) {
		t.Fatal("compute-plane register-cluster make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "KUBECONFIG_FILE=/repo-root-placeholder/tests/bdd/out/eks-compute-kubeconfig.yaml") {
		t.Fatal("compute-plane register/install did not use the generated compute kubeconfig")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "deploy/stacks/nvcf-compute-plane install") {
		t.Fatal("compute-plane install make target was never invoked")
	}
	if !commandRanExactly(suite.Runner.(*fakeRunner).runs, pullSecretCommand) {
		t.Fatal("compute-plane pull-secret propagation assertion was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke") {
		t.Fatal("function invoke CLI command was never invoked")
	}
	if !commandRanExactly(suite.Runner.(*fakeRunner).runs, taskSmokeCommand) {
		t.Fatal("NVCT task API smoke script was not invoked with the EKS instance type")
	}
	assertFunctionDeploymentsUseInstanceType(t, suite.Runner.(*fakeRunner).runs, "NCP.GPU.H100_8x", 1)
}

// TestSingleClusterUp is the live entry point for the single-cluster
// CLI feature. Skipped under -short.
func TestSingleClusterUp(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "single-cluster-up.feature")
}

// TestMultiClusterUp is the live entry point for the multi-cluster
// feature. Skipped under -short.
func TestMultiClusterUp(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "multi-cluster-up.feature")
}

// TestSingleClusterUpOneClick is the live entry point for the
// self-hosted up one-click feature on a local k3d single cluster.
// Skipped under -short.
func TestSingleClusterUpOneClick(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "single-cluster-up-oneclick.feature")
}

// TestSingleClusterHelmfile is the live entry point for the helmfile
// feature. Skipped under -short.
func TestSingleClusterHelmfile(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "single-cluster-helmfile.feature")
}

// TestSingleClusterHelmfileLLMPKI is the live entry point for the
// PKI-secured LLM transport Helmfile feature. Skipped under -short.
func TestSingleClusterHelmfileLLMPKI(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "single-cluster-helmfile-llm-pki.feature")
}

// TestObservabilityControl is the live entry point for the control
// observability profile feature. Skipped under -short.
func TestObservabilityControl(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "observability-control.feature")
}

// TestObservabilityCompute is the live entry point for the compute
// observability profile on the local split-cluster topology. Skipped under
// -short.
func TestObservabilityCompute(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "observability-compute.feature")
}

// TestObservabilityAll is the live entry point for both observability planes
// on the local single-cluster topology. Skipped under -short.
func TestObservabilityAll(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "observability-all.feature")
}

// TestSingleClusterHelmfileUpstreamImages is the live entry point for the
// focused Docker Hub supporting-image override feature. Skipped under -short.
func TestSingleClusterHelmfileUpstreamImages(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "single-cluster-helmfile-upstream-images.feature")
}

// TestObservabilityDisabled is the live entry point for the render-only
// disabled observability profile feature. Skipped under -short.
func TestObservabilityDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "observability-disabled.feature")
}

// TestMultiClusterHelmfile is the live entry point for the
// multi-cluster Helmfile feature: control-plane install on
// k3d-ncp-local-cp followed by register + NVCA install on the
// compute cluster. Skipped under -short.
func TestMultiClusterHelmfile(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "multi-cluster-helmfile.feature")
}

// TestSingleClusterEKSHelmfile is the live entry point for the
// single-cluster EKS Helmfile feature. Skipped under -short.
func TestSingleClusterEKSHelmfile(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "single-cluster-eks-helmfile.feature")
}

// TestMultiClusterEKSHelmfile is the live entry point for the
// multi-cluster EKS Helmfile feature: control-plane install on one EKS
// cluster, then register + NVCA install on a separate compute EKS
// cluster, then execute a function and an NVCT task there. Skipped under
// -short.
func TestMultiClusterEKSHelmfile(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeature(t, "multi-cluster-eks-helmfile.feature")
}

// runLiveFeature is the shared live-run path: build CLI, register
// every step, drive the feature, restore the ledger. Most live entry
// points differ only in the feature file they name.
func runLiveFeature(t *testing.T, feature string) {
	t.Helper()
	runLiveFeatureTags(t, feature, "")
}

// runLiveFeatureTags is runLiveFeature with an optional godog tag
// expression (for example "~@skip" to exclude a scenario from the
// live run while leaving it in the feature file for documentation and
// for the wiring test). An empty tags string runs every scenario.
func runLiveFeatureTags(t *testing.T, feature, tags string) {
	t.Helper()
	suite, err := harness.NewSuite(t)
	if err != nil {
		t.Fatalf("new suite: %v", err)
	}
	stopSignalCleanup := suite.InstallSignalCleanup()
	defer func() {
		stopSignalCleanup()
		if err := suite.Teardown(); err != nil {
			t.Errorf("teardown: %v", err)
		}
	}()
	sc := steps.NewScenarioContext(suite)
	featurePath := mustResolveFeaturePath(t, feature)
	status := godog.TestSuite{
		Name: "bdd-live-" + feature,
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.RegisterAll(ctx, sc)
			stepHooks := ctx.StepContext()
			stepHooks.Before(func(stepContext context.Context, _ *godog.Step) (context.Context, error) {
				return suite.BeginSignalSafeStep(stepContext)
			})
			stepHooks.After(func(
				stepContext context.Context,
				_ *godog.Step,
				_ godog.StepResultStatus,
				_ error,
			) (context.Context, error) {
				return suite.EndSignalSafeStep(stepContext), nil
			})
		},
		Options: &godog.Options{
			Format:        "pretty",
			Paths:         []string{featurePath},
			Tags:          tags,
			Strict:        true,
			StopOnFailure: true,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("godog suite status = %d", status)
	}
}

// commandRanThatContains scans the captured fake runs for a substring
// match. Used for behavior-level wiring assertions only.
func commandRanThatContains(runs []string, needle string) bool {
	for _, run := range runs {
		if strings.Contains(run, needle) {
			return true
		}
	}
	return false
}

func assertFunctionDeploymentsUseInstanceType(t *testing.T, runs []string, want string, wantCount int) {
	t.Helper()
	count := 0
	for _, command := range runs {
		if !strings.Contains(command, " function deploy create ") {
			continue
		}
		count++
		if !commandOptionEquals(command, "--instance-type", want) {
			t.Fatalf("function deployment command did not use instance type %s: %s", want, command)
		}
	}
	if count != wantCount {
		t.Fatalf("function deployment commands = %d, want %d", count, wantCount)
	}
}

func commandOptionEquals(command, option, want string) bool {
	fields := strings.Fields(command)
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] == option {
			return fields[index+1] == want
		}
	}
	return false
}

func commandRanExactly(runs []string, want string) bool {
	for _, run := range runs {
		if run == want {
			return true
		}
	}
	return false
}

func commandRanThatContainsAll(runs []string, needles ...string) bool {
	for _, run := range runs {
		matched := true
		for _, needle := range needles {
			if !strings.Contains(run, needle) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func assertFileContains(t *testing.T, path string, needles ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			t.Fatalf("%s does not contain %q", path, needle)
		}
	}
}

// mustResolveFeaturePath returns the feature file path relative to the
// package directory. `go test` invokes test binaries from the package
// directory, so a plain relative path is sufficient; the helper exists
// only so the test reads as "give me the feature named X" rather than
// inlining filepath.Join everywhere.
func mustResolveFeaturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("features", name)
}
