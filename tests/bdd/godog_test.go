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
`
	writeArtifact(t, repoRoot, "control-plane-profile.yaml", body)
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
	writeArtifact(t, repoRoot, "control-plane-profile.yaml", body)
}

// writeMulticlusterComputeRegisterValues seeds the per-compute-cluster
// register-values handoff the multi-cluster install scenario reads.
func writeMulticlusterComputeRegisterValues(t *testing.T, repoRoot, cluster string) {
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
	writeArtifact(t, repoRoot, cluster+"-register-values.yaml", body)
}

// writeSingleClusterComputeRegisterValues seeds the register-values
// handoff the single-cluster CLI feature reads after compute-plane
// register. compute-plane register picks the in-cluster service
// hostnames in single-cluster topology (worker and CP share the same
// k3d cluster, so the in-cluster URLs are the directly reachable
// ones). The multi-cluster equivalent in writeMulticlusterComputeRegisterValues
// uses the compute-reachable .test hostnames instead.
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
	writeArtifact(t, repoRoot, "ncp-local-register-values.yaml", body)
}

// writeHelmfileRegisterValues seeds the register-values handoff the
// single-cluster-helmfile.feature register scenario reads. The stack
// Makefile passes CLUSTER_NAME separately to helmfile, so the file
// produced by `make register-cluster` does not carry clusterName at
// the top level and the selfManaged URLs use the compute-reachable
// localhost hostnames.
func writeHelmfileRegisterValues(t *testing.T, repoRoot string) {
	t.Helper()
	body := `clusterID: 11111111-2222-3333-4444-555555555555
clusterGroupID: aaaa-bbbb-cccc-dddd
ncaID: nvcf-default
region: us-west-1
selfManaged:
  identitySource: psat
  icmsServiceURL: http://sis.localhost:8080
  revalServiceURL: http://reval.localhost:8080
  natsURL: nats://nats.localhost:4222
`
	writeArtifact(t, repoRoot, "ncp-local-register-values.yaml", body)
}

// seedStackSecretsTemplate writes a minimal stand-in for
// deploy/stacks/self-managed/secrets/secrets.yaml.template at the
// suite's RepoRoot. The body is not a faithful copy of the real
// stack template (which has a richer schema with several placeholders
// across openbao and api blocks); it only carries the single
// REPLACE_WITH_BASE64_DOCKER_CREDENTIAL token the feature substitutes,
// which is sufficient to exercise the I copy and I substitute steps
// against a fake CommandRunner.
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

func writeArtifact(t *testing.T, repoRoot, name, body string) {
	t.Helper()
	dir := filepath.Join(repoRoot, "deploy", "stacks", "self-managed", "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir handoff: %v", err)
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
// helm-list canned outputs carry --kube-context k3d-ncp-local so the
// control-plane and nvca-operator json-rows assertions have something to
// parse; the conflict-precheck k3d-get returns exit 1.
func TestSingleClusterUpOneClickFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"helm list --all-namespaces --kube-context k3d-ncp-local -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		"helm list -n nvca-operator --kube-context k3d-ncp-local -o json": {ExitCode: 0, Stdout: helmListNVCAJSON()},
		// Conflict precheck: feature asserts the multi-cluster
		// control-plane is absent.
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
	}))
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)

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
// nvcf-control-plane.test hostnames that the install command produces.
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
	writeMulticlusterComputeRegisterValues(t, suite.Config.RepoRoot, "ncp-local-compute-1")
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)

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
// fake runner is pre-loaded with canned JSON for the `helm list` step
// so the json-rows assertion has something to parse.
func TestSingleClusterHelmfileFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"helm list --all-namespaces -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		"helm list -n nvca-operator -o json": {ExitCode: 0, Stdout: helmListNVCAJSON()},
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --request-body '{\"message\":\"bdd-echo\",\"repeats\":1}' --timeout 120 --poll-duration 5": {
			ExitCode: 0,
			Stdout:   "Function invocation completed!\n\nResponse:\n{\"rawResponse\":\"bdd-echo\"}\n",
		},
		// Conflict precheck: feature asserts the conflicting
		// multi-cluster control-plane is absent.
		"k3d cluster get ncp-local-cp": {ExitCode: 1},
	}))
	seedHelmfileLocalBDDFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
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
}

// TestMultiClusterHelmfileFeatureFileWiresToSteps runs
// multi-cluster-helmfile.feature against a fake runner. The same
// fixture seeds and canned helm-list outputs cover both scenarios;
// the helm list canned keys carry --kube-context because the
// multi-cluster feature targets the cp and compute clusters
// explicitly.
func TestMultiClusterHelmfileFeatureFileWiresToSteps(t *testing.T) {
	t.Setenv("NGC_API_KEY", "test-key")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		"helm list --all-namespaces --kube-context k3d-ncp-local-cp -o json":        {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		"helm list -n nvca-operator --kube-context k3d-ncp-local-compute-1 -o json": {ExitCode: 0, Stdout: helmListNVCAJSON()},
		// Conflict precheck: feature asserts the conflicting
		// single-cluster is absent.
		"k3d cluster get ncp-local": {ExitCode: 1},
	}))
	seedHelmfileLocalBDDMultiFixture(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	writeMulticlusterComputeRegisterValues(t, suite.Config.RepoRoot, "ncp-local-compute-1")

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
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install-nvca-operator") {
		t.Fatal("install-nvca-operator make target was never invoked")
	}
}

// helmListAllNamespacesJSON returns canned helm-list output covering
// every release the helmfile install scenario asserts.
func helmListAllNamespacesJSON() string {
	return `[
{"name":"nats","namespace":"nats-system","status":"deployed"},
{"name":"cert-manager","namespace":"cert-manager","status":"deployed"},
{"name":"openbao-server","namespace":"vault-system","status":"deployed"},
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
{"name":"llm-api-gateway","namespace":"nvcf","status":"deployed"}
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
addons:
  llm:
    enabled: true
`)
}

// seedHelmfileLocalBDDMultiFixture writes the multi-cluster variant
// the multi-cluster helmfile feature copies onto the env file. Its
// nvcaOperator.selfManaged URLs target the .test hostnames the
// compute cluster aliases to the control-plane LB; single-cluster
// in-cluster URLs do not resolve across k3d clusters.
func seedHelmfileLocalBDDMultiFixture(t *testing.T, repoRoot string) {
	t.Helper()
	writeFixture(t, repoRoot, "self-managed-local-bdd-multi.yaml", `global:
  storageClass: local-path
  nvcaOperator:
    selfManaged:
      icmsServiceURL: http://sis.nvcf-control-plane.test:8080
      revalServiceURL: http://reval.nvcf-control-plane.test:8080
      natsURL: nats://nats.nvcf-control-plane.test:4222
addons:
  llm:
    enabled: true
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

// writeEKSRegisterValues seeds the register-values handoff the
// @nvca-registration scenario reads back after register-cluster.sh.
// The wiring test's fakeRunner does not actually run the script, so
// the file must be pre-seeded with the same shape the assertions
// expect: ncaID, region matching EKS_REGION, identitySource=psat, and
// non-empty clusterID + clusterGroupID. The selfManaged URL fields are
// rewritten in-place by the chart Host-header workaround step, so
// their initial values do not have to match the EKS gateway.
func writeEKSRegisterValues(t *testing.T, repoRoot, clusterName, region string) {
	t.Helper()
	body := `clusterID: 11111111-2222-3333-4444-555555555555
clusterGroupID: aaaa-bbbb-cccc-dddd
ncaID: nvcf-default
region: ` + region + `
selfManaged:
  identitySource: psat
  icmsServiceURL: http://wiring-elb.example.test
  revalServiceURL: http://wiring-elb.example.test
  natsURL: nats://wiring-elb.example.test:4222
`
	writeArtifact(t, repoRoot, clusterName+"-register-values.yaml", body)
}

// TestSingleClusterEKSHelmfileFeatureFileWiresToSteps runs the EKS
// Helmfile feature against a fake CommandRunner. The fakeRunner
// returns ExitCode 0 for unknown commands by default, so only the
// commands with assertion-driven output (gateway-address jsonpath,
// helm list JSON, httproute hostname) need canned responses. The
// I export step records the gateway jsonpath stdout into the env
// Ledger; subsequent ${EKS_GATEWAY_ADDR} interpolations then use the
// exported value, which is what the @control-plane httproute
// assertion expects to see.
func TestSingleClusterEKSHelmfileFeatureFileWiresToSteps(t *testing.T) {
	const (
		eksContext      = "arn:aws:eks:us-east-1:000000000000:cluster/wiring-test"
		eksClusterName  = "wiring-test"
		eksRegion       = "us-east-1"
		wiringGatewayLB = "wiring-elb.example.test"
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
		// @control-plane: httproute jsonpath assertion expects api.<gw>.
		"kubectl --context " + eksContext + " get httproute nvcf-api -n envoy-gateway -o jsonpath={.spec.hostnames[0]}": {ExitCode: 0, Stdout: "api." + wiringGatewayLB},
		// @nvca-registration: helm list confirms nvca-operator deployed.
		"helm list -n nvca-operator --kube-context " + eksContext + " -o json": {ExitCode: 0, Stdout: helmListNVCAJSON()},
	}))
	seedStackBaseYaml(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedNVCFCLINonlocalTemplate(t, suite.Config.RepoRoot)
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
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install HELMFILE_ENV=eks-bdd") {
		t.Fatal("helmfile install make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "cluster register --name") {
		t.Fatal("nvcf-cli cluster register was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install-nvca-operator") {
		t.Fatal("install-nvca-operator make target was never invoked")
	}
}

// TestMultiClusterEKSHelmfileFeatureFileWiresToSteps runs the
// multi-cluster EKS Helmfile feature against a fake CommandRunner.
// Canned outputs cover the control-plane gateway-address jsonpath, the
// control-plane helm-list, the api HTTPRoute hostname, the compute
// nvca-operator helm-list, and the function invoke. The helm-list keys
// carry distinct --kube-context values so the test exercises the
// control-plane vs compute split. @gateway-setup's export step captures
// EKS_GATEWAY_ADDR from the canned gateway stdout.
func TestMultiClusterEKSHelmfileFeatureFileWiresToSteps(t *testing.T) {
	const (
		cpContext          = "arn:aws:eks:us-east-1:000000000000:cluster/wiring-cp"
		computeContext     = "arn:aws:eks:us-east-1:000000000000:cluster/wiring-compute"
		computeClusterName = "wiring-compute"
		eksRegion          = "us-east-1"
		wiringGatewayLB    = "wiring-cp-elb.example.test"
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
	// EKS_GATEWAY_ADDR is intentionally NOT preset: @gateway-setup's
	// export step captures it from the canned gateway stdout below.

	suite := newWiringSuite(t, newFakeRunner(map[string]harness.Result{
		// @gateway-setup: control-plane gateway address -> EKS_GATEWAY_ADDR.
		"kubectl --context " + cpContext + " get gateway nvcf-gateway -n envoy-gateway -o jsonpath={.status.addresses[0].value}": {ExitCode: 0, Stdout: wiringGatewayLB},
		// control-plane helm list assertion.
		"helm list --all-namespaces --kube-context " + cpContext + " -o json": {ExitCode: 0, Stdout: helmListAllNamespacesJSON()},
		// control-plane api HTTPRoute hostname assertion.
		"kubectl --context " + cpContext + " get httproute nvcf-api -n envoy-gateway -o jsonpath={.spec.hostnames[0]}": {ExitCode: 0, Stdout: "api." + wiringGatewayLB},
		// compute nvca-operator helm list assertion.
		"helm list -n nvca-operator --kube-context " + computeContext + " -o json": {ExitCode: 0, Stdout: helmListNVCAJSON()},
		// @function-lifecycle: function invoke returns the echo payload.
		"/usr/bin/nvcf-cli --config /repo-root-placeholder/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml function invoke --request-body '{\"message\":\"bdd-echo\",\"repeats\":1}' --timeout 120 --poll-duration 5": {
			ExitCode: 0,
			Stdout:   "Function invocation completed!\n\nResponse:\n{\"rawResponse\":\"bdd-echo\"}\n",
		},
	}))
	seedStackBaseYaml(t, suite.Config.RepoRoot)
	seedStackSecretsTemplate(t, suite.Config.RepoRoot)
	seedNVCFCLINonlocalTemplate(t, suite.Config.RepoRoot)
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
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install HELMFILE_ENV=eks-bdd-multi") {
		t.Fatal("helmfile install make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "cluster register --name") {
		t.Fatal("nvcf-cli cluster register was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "install-nvca-operator") {
		t.Fatal("install-nvca-operator make target was never invoked")
	}
	if !commandRanThatContains(suite.Runner.(*fakeRunner).runs, "function invoke") {
		t.Fatal("function invoke CLI command was never invoked")
	}
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
// cluster. Skipped under -short.
//
// The @function-lifecycle scenario is excluded from the live run via
// ~@skip: cross-cluster function execution is not supported by the
// current nvca-operator chart/agent (worker pods receive the control
// plane's in-cluster service FQDNs, which do not resolve on a separate
// compute cluster). The wiring test still exercises that scenario
// against the fake runner. Drop the tag filter once worker FQDNs can be
// externalized.
func TestMultiClusterEKSHelmfile(t *testing.T) {
	if testing.Short() {
		t.Skip("live run skipped under -short")
	}
	runLiveFeatureTags(t, "multi-cluster-eks-helmfile.feature", "~@skip")
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
	defer func() {
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

// mustResolveFeaturePath returns the feature file path relative to the
// package directory. `go test` invokes test binaries from the package
// directory, so a plain relative path is sufficient; the helper exists
// only so the test reads as "give me the feature named X" rather than
// inlining filepath.Join everywhere.
func mustResolveFeaturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("features", name)
}
