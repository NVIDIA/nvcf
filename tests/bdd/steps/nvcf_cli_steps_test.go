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
