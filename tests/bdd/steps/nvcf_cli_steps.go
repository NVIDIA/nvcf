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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
	"nvcf-bdd/harness"
)

var modelInvocationRetryInterval = time.Second

var selectedFunctionPollInterval = 5 * time.Second

type cliAuthState struct {
	APIKey string `json:"apiKey"`
}

func registerNVCFCLISteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^I use NVCF CLI config "([^"]*)"$`, sc.iUseNVCFCLIConfig)
	ctx.Step(`^I successfully create function "([^"]*)" from image "([^"]*)" with CLI options:$`, sc.iSuccessfullyCreateFunction)
	ctx.Step(`^I successfully deploy the function selected by NVCF CLI with options:$`, sc.iSuccessfullyDeploySelectedFunction)
	ctx.Step(`^I successfully generate a function API key with CLI options:$`, sc.iSuccessfullyGenerateFunctionAPIKey)
	ctx.Step(`^I successfully invoke the function selected by NVCF CLI over HTTP with timeout "([^"]*)" seconds and poll duration "([^"]*)" seconds:$`, sc.iSuccessfullyInvokeFunctionHTTP)
	ctx.Step(`^I successfully invoke the function selected by NVCF CLI over plaintext gRPC service "([^"]*)" method "([^"]*)" with timeout "([^"]*)" seconds and poll duration "([^"]*)" seconds:$`, sc.iSuccessfullyInvokeFunctionGRPC)
	ctx.Step(`^I successfully invoke model "([^"]*)" at "([^"]*)" with timeout "([^"]*)" seconds:$`, sc.iSuccessfullyInvokeModel)
	ctx.Step(`^I successfully invoke the function selected by NVCF CLI through Vanity Gateway host "([^"]*)" path "([^"]*)" with timeout "([^"]*)" seconds:$`, sc.iSuccessfullyInvokeFunctionThroughVanityGateway)
	ctx.Step(`^I successfully undeploy the function selected by NVCF CLI$`, sc.iSuccessfullyUndeploySelectedFunction)
	ctx.Step(`^I export the function selected by NVCF CLI to environment variables "([^"]*)" and "([^"]*)"$`, sc.iExportSelectedFunctionIdentity)
	ctx.Step(`^the function selected by NVCF CLI should have no scheduled compute-plane instances using context "([^"]*)" and kubeconfig "([^"]*)"$`, sc.selectedFunctionShouldHaveNoScheduledInstances)
	ctx.Step(`^the function selected by NVCF CLI should report "([^"]*)" compute-plane instances with status "([^"]*)" using context "([^"]*)" and kubeconfig "([^"]*)" within "([^"]*)"$`, sc.selectedFunctionShouldReportInstances)
}

func (sc *ScenarioContext) iUseNVCFCLIConfig(config string) error {
	sc.NVCFCLIConfig = dsl.Interpolate(config)
	return nil
}

func (sc *ScenarioContext) iSuccessfullyCreateFunction(
	ctx context.Context,
	name,
	image string,
	table *godog.Table,
) error {
	return sc.runNVCFCLIWithOptions(ctx, []string{
		"function", "create", "--name", name, "--image", image,
	}, table)
}

func (sc *ScenarioContext) iSuccessfullyDeploySelectedFunction(ctx context.Context, table *godog.Table) error {
	return sc.runNVCFCLIWithOptions(ctx, []string{"function", "deploy", "create"}, table)
}

func (sc *ScenarioContext) iSuccessfullyGenerateFunctionAPIKey(ctx context.Context, table *godog.Table) error {
	options, err := nvcfCLIOptions(table)
	if err != nil {
		return err
	}
	return sc.runNVCFCLISuppressingStdout(
		ctx,
		append([]string{"api-key", "generate", "--for", "function"}, options...)...,
	)
}

func (sc *ScenarioContext) iSuccessfullyInvokeFunctionHTTP(
	ctx context.Context,
	timeout,
	pollDuration string,
	doc *godog.DocString,
) error {
	return sc.runNVCFCLI(ctx,
		"function", "invoke",
		"--request-body", doc.Content,
		"--timeout", timeout,
		"--poll-duration", pollDuration,
	)
}

func (sc *ScenarioContext) iSuccessfullyInvokeFunctionGRPC(
	ctx context.Context,
	service,
	method,
	timeout,
	pollDuration string,
	doc *godog.DocString,
) error {
	return sc.runNVCFCLI(ctx,
		"function", "invoke", "--grpc", "--grpc-plaintext",
		"--grpc-service", service,
		"--grpc-method", method,
		"--request-body", doc.Content,
		"--timeout", timeout,
		"--poll-duration", pollDuration,
	)
}

func (sc *ScenarioContext) iSuccessfullyInvokeModel(
	ctx context.Context,
	model,
	inferenceURL,
	timeout string,
	doc *godog.DocString,
) error {
	args := []string{
		"function", "invoke",
		"--inference-url", inferenceURL,
		"--model-name", model,
		"--request-body", doc.Content,
		"--timeout", timeout,
	}
	retryFor, retryTimeoutErr := time.ParseDuration(timeout + "s")
	deadline := time.Now().Add(retryFor)
	retryCtx := ctx
	if retryTimeoutErr == nil && retryFor > 0 {
		var cancel context.CancelFunc
		retryCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	for {
		err := sc.runNVCFCLI(retryCtx, args...)
		if err == nil {
			return nil
		}
		if retryTimeoutErr != nil || retryFor <= 0 ||
			!strings.Contains(combinedOutput(sc.LastResult), "no_eligible_candidates") {
			return err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		waitFor := min(modelInvocationRetryInterval, remaining)
		timer := time.NewTimer(waitFor)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		case <-timer.C:
		}
		if time.Until(deadline) <= 0 {
			return err
		}
		if retryCtx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

func (sc *ScenarioContext) iSuccessfullyInvokeFunctionThroughVanityGateway(
	ctx context.Context,
	host,
	inferenceURL,
	timeout string,
	doc *godog.DocString,
) error {
	apiKey, err := currentFunctionAPIKey(sc.NVCFCLIConfig)
	if err != nil {
		return err
	}
	if strings.ContainsAny(apiKey, "\r\n\x00") {
		return fmt.Errorf("saved function API key contains an invalid control character")
	}

	// nvcf-cli prefixes INVOKE_HOST with the selected function ID for normal
	// function invocations. Vanity Gateway routes use the exact configured host,
	// so send this smoke request directly through the local Envoy listener. The
	// shell reads the key from stdin to keep it out of argv and command logs.
	// TODO(https://github.com/NVIDIA/nvcf/issues/1399): replace this curl path
	// with first-class nvcf-cli Vanity Gateway invocation support.
	// Envoy can briefly return an HTTP error while a just-rolled gateway backend
	// propagates; retries remain bounded by the scenario timeout.
	script := `IFS= read -r api_key || [ -n "$api_key" ]; exec curl --silent --show-error --fail-with-body --header "Authorization: Bearer ${api_key}" "$@"`
	command := dsl.BuildCommand(
		"/bin/sh", "-c", script, "vanity-gateway-request",
		"--request", "POST",
		"--header", "Host: "+dsl.Interpolate(host),
		"--header", "Content-Type: application/json",
		"--data", dsl.Interpolate(doc.Content),
		"--retry", "24",
		"--retry-all-errors",
		"--retry-delay", "5",
		"--retry-max-time", dsl.Interpolate(timeout),
		"--max-time", dsl.Interpolate(timeout),
		"http://127.0.0.1:8080"+dsl.Interpolate(inferenceURL),
	)
	if err := sc.runResolvedAndRecordWith(
		ctx,
		command,
		func(runCtx context.Context, resolved string) (harness.Result, error) {
			return sc.Suite.Runner.RunWithSensitiveStdin(runCtx, resolved, apiKey)
		},
	); err != nil {
		return err
	}
	return sc.commandExitCodeShouldBe(0)
}

func currentFunctionAPIKey(configName string) (string, error) {
	statePath, err := nvcfCLIStatePath(configName)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(statePath)
	if err != nil {
		return "", fmt.Errorf("read NVCF CLI state: %w", err)
	}
	var state cliAuthState
	if err := json.Unmarshal(body, &state); err != nil {
		return "", fmt.Errorf("parse NVCF CLI state: %w", err)
	}
	state.APIKey = strings.TrimSpace(state.APIKey)
	if state.APIKey == "" {
		return "", fmt.Errorf("NVCF CLI state does not contain a function API key")
	}
	return state.APIKey, nil
}

func nvcfCLIStatePath(configName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	contextName := filepath.Base(strings.TrimSpace(configName))
	if extension := filepath.Ext(contextName); extension != "" {
		contextName = strings.TrimSuffix(contextName, extension)
	}
	if contextName == "" || contextName == "default" || contextName == ".nvcf-cli" {
		return filepath.Join(home, ".nvcf-cli.state"), nil
	}
	return filepath.Join(home, ".nvcf-cli."+contextName+".state"), nil
}

func (sc *ScenarioContext) iSuccessfullyUndeploySelectedFunction(ctx context.Context) error {
	return sc.runNVCFCLI(ctx, "function", "delete", "--deployment-only")
}

func (sc *ScenarioContext) runNVCFCLIWithOptions(
	ctx context.Context,
	fixed []string,
	table *godog.Table,
) error {
	options, err := nvcfCLIOptions(table)
	if err != nil {
		return err
	}
	return sc.runNVCFCLI(ctx, append(fixed, options...)...)
}

func (sc *ScenarioContext) runNVCFCLI(ctx context.Context, args ...string) error {
	return sc.runResolvedSuccessfully(ctx, dsl.BuildCommand(sc.nvcfCLICommandArgs(args...)...))
}

func (sc *ScenarioContext) runNVCFCLISuppressingStdout(ctx context.Context, args ...string) error {
	commandArgs := append(
		[]string{"/bin/sh", "-c", `exec "$@" >/dev/null`, "nvcf-cli"},
		sc.nvcfCLICommandArgs(args...)...,
	)
	return sc.runResolvedSuccessfully(ctx, dsl.BuildCommand(commandArgs...))
}

func (sc *ScenarioContext) nvcfCLICommandArgs(args ...string) []string {
	commandArgs := make([]string, 0, len(args)+3)
	commandArgs = append(commandArgs,
		dsl.Interpolate("${NVCF_CLI}"),
		"--config",
		sc.NVCFCLIConfig,
	)
	for _, arg := range args {
		commandArgs = append(commandArgs, dsl.Interpolate(arg))
	}
	return commandArgs
}

func nvcfCLIOptions(table *godog.Table) ([]string, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have option and value headers and at least one data row")
	}
	header := table.Rows[0]
	if len(header.Cells) != 2 ||
		strings.TrimSpace(header.Cells[0].Value) != "option" ||
		strings.TrimSpace(header.Cells[1].Value) != "value" {
		return nil, fmt.Errorf("table headers must be option and value")
	}
	options := make([]string, 0, (len(table.Rows)-1)*2)
	for index, row := range table.Rows[1:] {
		if len(row.Cells) != 2 {
			return nil, fmt.Errorf("row %d has %d cells, expected exactly 2", index+1, len(row.Cells))
		}
		options = append(options, row.Cells[0].Value, row.Cells[1].Value)
	}
	return options, nil
}

// selectedFunctionIdentity reads the function and version IDs the CLI reports
// as selected. Parsing lives in dsl so the handler stays a command adapter.
func (sc *ScenarioContext) selectedFunctionIdentity(ctx context.Context) (string, string, error) {
	if err := sc.runNVCFCLI(ctx, "status", "--json"); err != nil {
		return "", "", err
	}
	return dsl.SelectedFunctionIdentity(sc.LastResult.Stdout)
}

// validateEnvVarName checks that an environment variable name is non-empty and
// does not contain '=' or NUL characters that would be rejected by os.Setenv.
func validateEnvVarName(name string) error {
	if name == "" {
		return fmt.Errorf("name must be non-empty")
	}
	if strings.ContainsAny(name, "=\x00") {
		return fmt.Errorf("name %q contains invalid characters '=' or NUL", name)
	}
	return nil
}

// iExportSelectedFunctionIdentity exports the selected function and version IDs
// under the two named environment variables. Both names stay visible in
// Gherkin. The EnvLedger snapshots each name before the write so suite teardown
// restores the operator's original environment. Both variable names are validated
// before executing the CLI command.
func (sc *ScenarioContext) iExportSelectedFunctionIdentity(ctx context.Context, functionVariable, versionVariable string) error {
	functionVariable = strings.TrimSpace(functionVariable)
	versionVariable = strings.TrimSpace(versionVariable)
	if err := validateEnvVarName(functionVariable); err != nil {
		return fmt.Errorf("export selected function: function env var %w", err)
	}
	if err := validateEnvVarName(versionVariable); err != nil {
		return fmt.Errorf("export selected function: version env var %w", err)
	}
	if functionVariable == versionVariable {
		return fmt.Errorf("export selected function: env var names must differ, both are %q", functionVariable)
	}
	functionID, versionID, err := sc.selectedFunctionIdentity(ctx)
	if err != nil {
		return err
	}
	for _, export := range []struct{ name, value string }{
		{name: functionVariable, value: functionID},
		{name: versionVariable, value: versionID},
	} {
		if err := sc.Suite.EnvLedger.Snapshot(export.name); err != nil {
			return fmt.Errorf("export to %s: snapshot: %w", export.name, err)
		}
		if err := os.Setenv(export.name, export.value); err != nil {
			return fmt.Errorf("export to %s: setenv: %w", export.name, err)
		}
	}
	return nil
}

// selectedFunctionShouldHaveNoScheduledInstances proves the selected function
// is idle before demand exists. The compute-plane context and kubeconfig stay
// visible in Gherkin.
func (sc *ScenarioContext) selectedFunctionShouldHaveNoScheduledInstances(ctx context.Context, kubeContext, kubeconfig string) error {
	functionID, versionID, err := sc.selectedFunctionIdentity(ctx)
	if err != nil {
		return err
	}
	if err := sc.runNVCFCLI(ctx,
		"cluster", "agent", "list-functions",
		"--compute-plane-context", kubeContext,
		"--kubeconfig", kubeconfig,
		"--json",
	); err != nil {
		return err
	}
	if err := dsl.ScheduledFunctionInstancesAbsent(sc.LastResult.Stdout, functionID, versionID); err != nil {
		return fmt.Errorf("selected function is not idle on the compute plane: %w", err)
	}
	return nil
}

// selectedFunctionShouldReportInstances polls the compute-plane CLI until the
// selected function reports the expected instance count and status. Each
// attempt is a separate runner invocation so every poll reaches the per-command
// logs. The count, status, context, kubeconfig, and timeout stay visible in
// Gherkin.
func (sc *ScenarioContext) selectedFunctionShouldReportInstances(ctx context.Context, count, status, kubeContext, kubeconfig, timeout string) error {
	expectedCount, err := strconv.Atoi(strings.TrimSpace(dsl.Interpolate(count)))
	if err != nil || expectedCount < 0 {
		return fmt.Errorf("instance count %q must be a non-negative integer", count)
	}
	expectedStatus := strings.TrimSpace(dsl.Interpolate(status))
	if expectedStatus == "" {
		return fmt.Errorf("instance status must be non-empty")
	}
	budget, err := time.ParseDuration(strings.TrimSpace(dsl.Interpolate(timeout)))
	if err != nil || budget <= 0 {
		return fmt.Errorf("timeout %q must be a positive Go duration such as 10m", timeout)
	}

	functionID, versionID, err := sc.selectedFunctionIdentity(ctx)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(budget)
	pollCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var lastErr error
	for {
		lastErr = sc.observeSelectedFunctionInstances(pollCtx, functionID, versionID, kubeContext, kubeconfig, expectedCount, expectedStatus)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Until(deadline) <= 0 {
			break
		}
		timer := time.NewTimer(min(selectedFunctionPollInterval, time.Until(deadline)))
		select {
		case <-pollCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Until(deadline) <= 0 {
			break
		}
	}
	return fmt.Errorf("function %s version %s did not report %d instances with status %q within %s: %w",
		functionID, versionID, expectedCount, expectedStatus, budget, lastErr)
}

// observeSelectedFunctionInstances runs one get-function attempt. A runner
// failure, a non-zero exit, and an unmet expectation are all retryable, so the
// caller keeps polling until its deadline.
func (sc *ScenarioContext) observeSelectedFunctionInstances(
	ctx context.Context,
	functionID, versionID, kubeContext, kubeconfig string,
	expectedCount int,
	expectedStatus string,
) error {
	command := dsl.BuildCommand(sc.nvcfCLICommandArgs(
		"cluster", "agent", "get-function", functionID, versionID,
		"--compute-plane-context", kubeContext,
		"--kubeconfig", kubeconfig,
		"--json",
	)...)
	if err := sc.runAndRecord(ctx, command); err != nil {
		return err
	}
	if sc.LastResult.ExitCode != 0 {
		return fmt.Errorf("cluster agent get-function exited %d (see %s for stdout/stderr)",
			sc.LastResult.ExitCode, sc.Suite.Config.CommandLogDir)
	}
	return dsl.FunctionInstancesReady(sc.LastResult.Stdout, expectedCount, expectedStatus)
}
