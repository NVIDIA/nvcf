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

package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
)

func TestNVCFCLIConfigStoresInterpolatedPathWithoutCheckingIt(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("BDD_CLI_CONFIG_DIR", "/missing config directory")

	if err := sc.iUseNVCFCLIConfig("${BDD_CLI_CONFIG_DIR}/config.yaml"); err != nil {
		t.Fatalf("select config: %v", err)
	}
	if sc.NVCFCLIConfig != "/missing config directory/config.yaml" {
		t.Fatalf("config = %q", sc.NVCFCLIConfig)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("selecting a config ran %d commands, want 0", len(fake.runs))
	}
}

func TestNVCFCLICreatePassesOptionsWithoutProductValidation(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "/tmp/nvcf cli")
	sc.NVCFCLIConfig = "/tmp/config file.yaml"
	fake.result = harness.Result{ExitCode: 0}
	options := docTable(t, [][]string{
		{"option", "value"},
		{"--future-option", "not-an-api-value"},
		{"--llm-model", ""},
		{"--llm-model", "name=model,uris=/v1/chat|/v1/embed,routingMethod=unknown"},
	})

	err := sc.iSuccessfullyCreateFunction(
		context.Background(),
		"function with spaces",
		"registry.example/image:tag",
		options,
	)
	if err != nil {
		t.Fatalf("create function: %v", err)
	}
	want := "'/tmp/nvcf cli' --config '/tmp/config file.yaml' function create" +
		" --name 'function with spaces' --image registry.example/image:tag" +
		" --future-option not-an-api-value --llm-model ''" +
		" --llm-model 'name=model,uris=/v1/chat|/v1/embed,routingMethod=unknown'"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %+v, want command %q", fake.runs, want)
	}
}

func TestNVCFCLIInvocationAdaptersExposeAllArguments(t *testing.T) {
	tests := []struct {
		name string
		run  func(*ScenarioContext) error
		want string
	}{
		{
			name: "HTTP",
			run: func(sc *ScenarioContext) error {
				return sc.iSuccessfullyInvokeFunctionHTTP(context.Background(), "not-seconds", "also-not-seconds", &godog.DocString{Content: `{"message":"value with spaces"}`})
			},
			want: `nvcf-cli --config config.yaml function invoke --request-body '{"message":"value with spaces"}' --timeout not-seconds --poll-duration also-not-seconds`,
		},
		{
			name: "gRPC",
			run: func(sc *ScenarioContext) error {
				return sc.iSuccessfullyInvokeFunctionGRPC(context.Background(), "Service", "Method", "120", "5", &godog.DocString{Content: `{"message":"grpc"}`})
			},
			want: `nvcf-cli --config config.yaml function invoke --grpc --grpc-plaintext --grpc-service Service --grpc-method Method --request-body '{"message":"grpc"}' --timeout 120 --poll-duration 5`,
		},
		{
			name: "model",
			run: func(sc *ScenarioContext) error {
				return sc.iSuccessfullyInvokeModel(context.Background(), "model/name", "/v1/chat/completions", "120", &godog.DocString{Content: `{"messages":[]}`})
			},
			want: `nvcf-cli --config config.yaml function invoke --inference-url /v1/chat/completions --model-name model/name --request-body '{"messages":[]}' --timeout 120`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sc, fake := newScenarioContext(t)
			t.Setenv("NVCF_CLI", "nvcf-cli")
			sc.NVCFCLIConfig = "config.yaml"
			fake.result = harness.Result{ExitCode: 0}

			if err := test.run(sc); err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if len(fake.runs) != 1 || fake.runs[0].command != test.want {
				t.Fatalf("runs = %+v, want command %q", fake.runs, test.want)
			}
		})
	}
}

func TestVanityGatewayInvocationUsesExactHostAndKeepsAPIKeyOutOfCommand(t *testing.T) {
	sc, fake := newScenarioContext(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	sc.NVCFCLIConfig = "config.yaml"
	const apiKey = "sensitive-function-api-key"
	statePath := filepath.Join(home, ".nvcf-cli.config.state")
	if err := os.WriteFile(statePath, []byte(`{"apiKey":"`+apiKey+`"}`), 0o600); err != nil {
		t.Fatalf("write CLI state: %v", err)
	}
	fake.result = harness.Result{ExitCode: 0, Stdout: `{"rawResponse":"vanity"}`}

	err := sc.iSuccessfullyInvokeFunctionThroughVanityGateway(
		context.Background(),
		"vanity.localhost",
		"/bdd/echo",
		"120",
		&godog.DocString{Content: `{"message":"vanity"}`},
	)
	if err != nil {
		t.Fatalf("invoke through Vanity Gateway: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %+v, want one request", fake.runs)
	}
	run := fake.runs[0]
	for _, expected := range []string{
		"curl --silent --show-error --fail-with-body",
		"Host: vanity.localhost",
		"Content-Type: application/json",
		`{"message":"vanity"}`,
		"--retry 24 --retry-all-errors --retry-delay 5 --retry-max-time 120",
		"http://127.0.0.1:8080/bdd/echo",
	} {
		if !strings.Contains(run.command, expected) {
			t.Fatalf("command = %q, want %q", run.command, expected)
		}
	}
	if strings.Contains(run.command, apiKey) {
		t.Fatalf("command contains function API key: %q", run.command)
	}
	if run.sensitiveStdin != apiKey {
		t.Fatalf("sensitive stdin length = %d, want %d", len(run.sensitiveStdin), len(apiKey))
	}
}

func TestFunctionAPIKeyGenerationSuppressesSecretBearingStdout(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.result = harness.Result{ExitCode: 0}
	options := docTable(t, [][]string{
		{"option", "value"},
		{"--description", "bdd key"},
	})

	if err := sc.iSuccessfullyGenerateFunctionAPIKey(context.Background(), options); err != nil {
		t.Fatalf("generate function API key: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %+v, want one request", fake.runs)
	}
	command := fake.runs[0].command
	for _, expected := range []string{"/bin/sh -c", `exec "$@" >/dev/null`, "api-key generate --for function", "--description 'bdd key'"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("command = %q, want %q", command, expected)
		}
	}
}

func TestNVCFCLIModelInvocationRetriesNoEligibleCandidates(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.runResults = []harness.Result{
		{ExitCode: 1, Stderr: `API error 404: {"code":"no_eligible_candidates"}`},
		{ExitCode: 0, Stdout: `{"object":"chat.completion"}`},
	}

	previousInterval := modelInvocationRetryInterval
	modelInvocationRetryInterval = time.Nanosecond
	t.Cleanup(func() { modelInvocationRetryInterval = previousInterval })

	err := sc.iSuccessfullyInvokeModel(
		context.Background(),
		"model/name",
		"/v1/chat/completions",
		"1",
		&godog.DocString{Content: `{"messages":[]}`},
	)
	if err != nil {
		t.Fatalf("invoke model: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(fake.runs))
	}
}

func TestNVCFCLIModelInvocationDoesNotRetryWhenWaitReachesDeadline(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.runResults = []harness.Result{
		{ExitCode: 1, Stderr: `API error 404: {"code":"no_eligible_candidates"}`},
		{ExitCode: 0, Stdout: `{"object":"chat.completion"}`},
	}

	previousInterval := modelInvocationRetryInterval
	modelInvocationRetryInterval = time.Second
	t.Cleanup(func() { modelInvocationRetryInterval = previousInterval })

	err := sc.iSuccessfullyInvokeModel(
		context.Background(),
		"model/name",
		"/v1/chat/completions",
		"0.05",
		&godog.DocString{Content: `{"messages":[]}`},
	)
	if err == nil || !strings.Contains(err.Error(), "exit code = 1, want 0") {
		t.Fatalf("error = %v, want initial eligibility failure", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want no attempt after retry deadline", len(fake.runs))
	}
}

func TestNVCFCLIModelInvocationBoundsAttemptByRetryDeadline(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"

	var observedBudget time.Duration
	fake.runHook = func(ctx context.Context, _ int) (harness.Result, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return harness.Result{}, errors.New("attempt context has no deadline")
		}
		observedBudget = time.Until(deadline)
		<-ctx.Done()
		return harness.Result{ExitCode: -1}, ctx.Err()
	}

	parentCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	t.Cleanup(cancel)
	err := sc.iSuccessfullyInvokeModel(
		parentCtx,
		"model/name",
		"/v1/chat/completions",
		"0.05",
		&godog.DocString{Content: `{"messages":[]}`},
	)
	if err == nil {
		t.Fatal("invoke model succeeded, want deadline failure")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if observedBudget <= 0 || observedBudget > 100*time.Millisecond {
		t.Fatalf("attempt context budget = %s, want retry budget near 50ms", observedBudget)
	}
}

func TestNVCFCLIModelInvocationDoesNotRetryOtherErrors(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.result = harness.Result{ExitCode: 1, Stderr: "API error 401: unauthorized"}

	err := sc.iSuccessfullyInvokeModel(
		context.Background(),
		"model/name",
		"/v1/chat/completions",
		"1",
		&godog.DocString{Content: `{"messages":[]}`},
	)
	if err == nil || !strings.Contains(err.Error(), "exit code = 1, want 0") {
		t.Fatalf("error = %v, want exit-zero assertion failure", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(fake.runs))
	}
}

func TestNVCFCLISuccessStepRequiresExitZero(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.result = harness.Result{ExitCode: 22, Stderr: "CLI rejected the request"}
	fake.err = errors.New("exit status 22")
	options := docTable(t, [][]string{{"option", "value"}, {"--timeout", "invalid"}})

	err := sc.iSuccessfullyDeploySelectedFunction(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "exit code = 22, want 0") {
		t.Fatalf("error = %v, want exit-zero assertion failure", err)
	}
	if len(fake.runs) != 1 || sc.LastResult.ExitCode != 22 {
		t.Fatalf("runs = %+v, result = %+v", fake.runs, sc.LastResult)
	}
}

func TestNVCFCLIOptionsValidateOnlyTableShape(t *testing.T) {
	table := docTable(t, [][]string{{"flag", "setting"}, {"--timeout", "120"}})
	_, err := nvcfCLIOptions(table)
	if err == nil || !strings.Contains(err.Error(), "headers must be option and value") {
		t.Fatalf("error = %v, want structural header error", err)
	}
}

const selectedFunctionStatusJSON = `{"currentFunction":{"hasFunction":true,"functionId":"function-1","versionId":"version-1"}}`

func TestExportSelectedFunctionIdentityExportsBothNamedVariables(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Cleanup(func() { _ = sc.Suite.EnvLedger.RestoreAll() })
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.result = harness.Result{ExitCode: 0, Stdout: selectedFunctionStatusJSON}

	err := sc.iExportSelectedFunctionIdentity(context.Background(), "BDD_TEST_FUNCTION_ID", "BDD_TEST_VERSION_ID")
	if err != nil {
		t.Fatalf("export selected function identity: %v", err)
	}
	want := "nvcf-cli --config config.yaml status --json"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %+v, want a single %q", fake.runs, want)
	}
	if got := os.Getenv("BDD_TEST_FUNCTION_ID"); got != "function-1" {
		t.Fatalf("BDD_TEST_FUNCTION_ID = %q, want function-1", got)
	}
	if got := os.Getenv("BDD_TEST_VERSION_ID"); got != "version-1" {
		t.Fatalf("BDD_TEST_VERSION_ID = %q, want version-1", got)
	}
}

func TestExportSelectedFunctionIdentityRestoresTheEnvironment(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	t.Setenv("BDD_TEST_FUNCTION_ID", "preexisting")
	t.Cleanup(func() { _ = sc.Suite.EnvLedger.RestoreAll() })
	sc.NVCFCLIConfig = "config.yaml"
	fake.result = harness.Result{ExitCode: 0, Stdout: selectedFunctionStatusJSON}

	if err := sc.iExportSelectedFunctionIdentity(context.Background(), "BDD_TEST_FUNCTION_ID", "BDD_TEST_VERSION_ID"); err != nil {
		t.Fatalf("export selected function identity: %v", err)
	}
	if err := sc.Suite.EnvLedger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := os.Getenv("BDD_TEST_FUNCTION_ID"); got != "preexisting" {
		t.Fatalf("BDD_TEST_FUNCTION_ID = %q, want the pre-suite value", got)
	}
	if _, present := os.LookupEnv("BDD_TEST_VERSION_ID"); present {
		t.Fatal("BDD_TEST_VERSION_ID should be removed by teardown")
	}
}

func TestExportSelectedFunctionIdentityRejectsUnusableNames(t *testing.T) {
	tests := []struct {
		name             string
		function, verion string
	}{
		{name: "empty function name", function: "", verion: "BDD_TEST_VERSION_ID"},
		{name: "empty version name", function: "BDD_TEST_FUNCTION_ID", verion: "  "},
		{name: "identical names", function: "BDD_TEST_ID", verion: "BDD_TEST_ID"},
		{name: "function name contains equals", function: "BAD=NAME", verion: "BDD_TEST_VERSION_ID"},
		{name: "version name contains equals", function: "BDD_TEST_FUNCTION_ID", verion: "BAD=NAME"},
		{name: "function name contains null byte", function: "BAD\x00NAME", verion: "BDD_TEST_VERSION_ID"},
		{name: "version name contains null byte", function: "BDD_TEST_FUNCTION_ID", verion: "BAD\x00NAME"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sc, fake := newScenarioContext(t)
			t.Setenv("BDD_TEST_FUNCTION_ID", "sentinel-function")
			t.Setenv("BDD_TEST_VERSION_ID", "sentinel-version")

			if err := sc.iExportSelectedFunctionIdentity(context.Background(), test.function, test.verion); err == nil {
				t.Fatal("expected an error for unusable env var names")
			}
			if len(fake.runs) != 0 {
				t.Fatalf("runs = %d, want no command before validation passes", len(fake.runs))
			}
			if got := os.Getenv("BDD_TEST_FUNCTION_ID"); got != "sentinel-function" {
				t.Fatalf("BDD_TEST_FUNCTION_ID = %q, want sentinel-function", got)
			}
			if got := os.Getenv("BDD_TEST_VERSION_ID"); got != "sentinel-version" {
				t.Fatalf("BDD_TEST_VERSION_ID = %q, want sentinel-version", got)
			}
		})
	}
}

func TestExportSelectedFunctionIdentityFailsWhenNoFunctionSelected(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	t.Setenv("BDD_TEST_FUNCTION_ID", "sentinel-function")
	t.Setenv("BDD_TEST_VERSION_ID", "sentinel-version")
	sc.NVCFCLIConfig = "config.yaml"
	fake.result = harness.Result{ExitCode: 0, Stdout: `{"currentFunction":{"hasFunction":false}}`}

	err := sc.iExportSelectedFunctionIdentity(context.Background(), "BDD_TEST_FUNCTION_ID", "BDD_TEST_VERSION_ID")
	if err == nil || !strings.Contains(err.Error(), "no selected function") {
		t.Fatalf("error = %v, want a no-selected-function failure", err)
	}
	if got := os.Getenv("BDD_TEST_FUNCTION_ID"); got != "sentinel-function" {
		t.Fatalf("BDD_TEST_FUNCTION_ID = %q, want sentinel-function", got)
	}
	if got := os.Getenv("BDD_TEST_VERSION_ID"); got != "sentinel-version" {
		t.Fatalf("BDD_TEST_VERSION_ID = %q, want sentinel-version", got)
	}
}

func TestSelectedFunctionHasNoScheduledInstancesKeepsContextAndKubeconfigVisible(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	t.Setenv("BDD_TEST_KUBECONFIG", "/tmp/bdd kubeconfig.yaml")
	sc.NVCFCLIConfig = "config.yaml"
	fake.runResults = []harness.Result{
		{ExitCode: 0, Stdout: selectedFunctionStatusJSON},
		{ExitCode: 0, Stdout: `[{"functionId":"other","functionVersionId":"other-version","instanceCount":3}]`},
	}

	err := sc.selectedFunctionShouldHaveNoScheduledInstances(
		context.Background(), "k3d-ncp-local", "${BDD_TEST_KUBECONFIG}")
	if err != nil {
		t.Fatalf("no scheduled instances: %v", err)
	}
	want := "nvcf-cli --config config.yaml cluster agent list-functions" +
		" --compute-plane-context k3d-ncp-local --kubeconfig '/tmp/bdd kubeconfig.yaml' --json"
	if len(fake.runs) != 2 || fake.runs[1].command != want {
		t.Fatalf("runs = %+v, want second command %q", fake.runs, want)
	}
}

func TestSelectedFunctionHasNoScheduledInstancesFailsWhenAlreadyScheduled(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.runResults = []harness.Result{
		{ExitCode: 0, Stdout: selectedFunctionStatusJSON},
		{ExitCode: 0, Stdout: `[{"functionId":"function-1","functionVersionId":"version-1","instanceCount":1}]`},
	}

	err := sc.selectedFunctionShouldHaveNoScheduledInstances(
		context.Background(), "k3d-ncp-local", "/tmp/kubeconfig.yaml")
	if err == nil || !strings.Contains(err.Error(), "reports 1 scheduled instances") {
		t.Fatalf("error = %v, want the observed instance count", err)
	}
}

func TestSelectedFunctionReportsInstancesQueriesTheSelectedIdentity(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	t.Setenv("BDD_TEST_KUBECONFIG", "/tmp/kubeconfig.yaml")
	sc.NVCFCLIConfig = "config.yaml"
	fake.runResults = []harness.Result{
		{ExitCode: 0, Stdout: selectedFunctionStatusJSON},
		{ExitCode: 0, Stdout: `{"instanceCount":1,"instances":[{"id":"i-1","status":"RUNNING"}]}`},
	}

	err := sc.selectedFunctionShouldReportInstances(
		context.Background(), "1", "running", "k3d-ncp-local", "${BDD_TEST_KUBECONFIG}", "10m")
	if err != nil {
		t.Fatalf("report instances: %v", err)
	}
	want := "nvcf-cli --config config.yaml cluster agent get-function function-1 version-1" +
		" --compute-plane-context k3d-ncp-local --kubeconfig /tmp/kubeconfig.yaml --json"
	if len(fake.runs) != 2 || fake.runs[1].command != want {
		t.Fatalf("runs = %+v, want second command %q", fake.runs, want)
	}
}

func TestSelectedFunctionReportsInstancesPollsUntilReady(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.runResults = []harness.Result{
		{ExitCode: 0, Stdout: selectedFunctionStatusJSON},
		{ExitCode: 1, Stderr: "not scheduled yet"},
		{ExitCode: 0, Stdout: `{"instanceCount":1,"instances":[{"status":"pending"}]}`},
		{ExitCode: 0, Stdout: `{"instanceCount":1,"instances":[{"status":"running"}]}`},
	}
	previousInterval := selectedFunctionPollInterval
	selectedFunctionPollInterval = time.Nanosecond
	t.Cleanup(func() { selectedFunctionPollInterval = previousInterval })

	err := sc.selectedFunctionShouldReportInstances(
		context.Background(), "1", "running", "k3d-ncp-local", "/tmp/kubeconfig.yaml", "1m")
	if err != nil {
		t.Fatalf("report instances: %v", err)
	}
	if len(fake.runs) != 4 {
		t.Fatalf("runs = %d, want one status read and three polls", len(fake.runs))
	}
}

func TestSelectedFunctionReportsInstancesFailsAtTheVisibleTimeout(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NVCF_CLI", "nvcf-cli")
	sc.NVCFCLIConfig = "config.yaml"
	fake.runResults = []harness.Result{
		{ExitCode: 0, Stdout: selectedFunctionStatusJSON},
		{ExitCode: 0, Stdout: `{"instanceCount":0,"instances":[]}`},
	}
	previousInterval := selectedFunctionPollInterval
	selectedFunctionPollInterval = time.Second
	t.Cleanup(func() { selectedFunctionPollInterval = previousInterval })

	err := sc.selectedFunctionShouldReportInstances(
		context.Background(), "1", "running", "k3d-ncp-local", "/tmp/kubeconfig.yaml", "50ms")
	if err == nil {
		t.Fatal("expected a timeout failure")
	}
	for _, fragment := range []string{
		`function-1 version version-1`,
		`1 instances with status "running"`,
		`within 50ms`,
		`instance count = 0, want 1`,
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error = %v, want it to name %q", err, fragment)
		}
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs = %d, want no poll after the deadline", len(fake.runs))
	}
}

func TestSelectedFunctionReportsInstancesValidatesGherkinInputs(t *testing.T) {
	tests := []struct {
		name                     string
		count, status, timeoutIn string
	}{
		{name: "non-numeric count", count: "one", status: "running", timeoutIn: "10m"},
		{name: "negative count", count: "-1", status: "running", timeoutIn: "10m"},
		{name: "empty status", count: "1", status: "  ", timeoutIn: "10m"},
		{name: "non-duration timeout", count: "1", status: "running", timeoutIn: "600"},
		{name: "zero timeout", count: "1", status: "running", timeoutIn: "0s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sc, fake := newScenarioContext(t)
			t.Setenv("NVCF_CLI", "nvcf-cli")
			sc.NVCFCLIConfig = "config.yaml"

			err := sc.selectedFunctionShouldReportInstances(
				context.Background(), test.count, test.status, "k3d-ncp-local", "/tmp/kubeconfig.yaml", test.timeoutIn)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if len(fake.runs) != 0 {
				t.Fatalf("runs = %d, want no command before validation passes", len(fake.runs))
			}
		})
	}
}
