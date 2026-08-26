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
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

func registerNVCFCLISteps(ctx StepRegistrar, sc *ScenarioContext) {
	ctx.Step(`^I use NVCF CLI config "([^"]*)"$`, sc.iUseNVCFCLIConfig)
	ctx.Step(`^I successfully create function "([^"]*)" from image "([^"]*)" with CLI options:$`, sc.iSuccessfullyCreateFunction)
	ctx.Step(`^I successfully deploy the function selected by NVCF CLI with options:$`, sc.iSuccessfullyDeploySelectedFunction)
	ctx.Step(`^I successfully generate a function API key with CLI options:$`, sc.iSuccessfullyGenerateFunctionAPIKey)
	ctx.Step(`^I successfully invoke the function selected by NVCF CLI over HTTP with timeout "([^"]*)" seconds and poll duration "([^"]*)" seconds:$`, sc.iSuccessfullyInvokeFunctionHTTP)
	ctx.Step(`^I successfully invoke the function selected by NVCF CLI over plaintext gRPC service "([^"]*)" method "([^"]*)" with timeout "([^"]*)" seconds and poll duration "([^"]*)" seconds:$`, sc.iSuccessfullyInvokeFunctionGRPC)
	ctx.Step(`^I successfully invoke model "([^"]*)" at "([^"]*)" with timeout "([^"]*)" seconds:$`, sc.iSuccessfullyInvokeModel)
	ctx.Step(`^I successfully undeploy the function selected by NVCF CLI$`, sc.iSuccessfullyUndeploySelectedFunction)
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
	return sc.runNVCFCLIWithOptions(ctx, []string{"api-key", "generate", "--for", "function"}, table)
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
	return sc.runNVCFCLI(ctx,
		"function", "invoke",
		"--inference-url", inferenceURL,
		"--model-name", model,
		"--request-body", doc.Content,
		"--timeout", timeout,
	)
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
	commandArgs := make([]string, 0, len(args)+3)
	commandArgs = append(commandArgs,
		dsl.Interpolate("${NVCF_CLI}"),
		"--config",
		sc.NVCFCLIConfig,
	)
	for _, arg := range args {
		commandArgs = append(commandArgs, dsl.Interpolate(arg))
	}
	return sc.runResolvedSuccessfully(ctx, dsl.BuildCommand(commandArgs...))
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
