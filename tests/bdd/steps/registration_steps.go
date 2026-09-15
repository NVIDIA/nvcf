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
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

func registerRegistrationSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^I successfully observe WatchStargates at "([^"]*)" with TLS authority "([^"]*)" using CA secret "([^"]*)" in namespace "([^"]*)" and context "([^"]*)" for "([^"]*)" seconds$`, sc.iSuccessfullyObserveWatchStargates)
	ctx.Step(`^every Pylon for function "([^"]*)" using container "([^"]*)" and context "([^"]*)" should report metrics within "([^"]*)":$`, sc.everyPylonForFunctionShouldReportMetrics)
}

func (sc *ScenarioContext) iSuccessfullyObserveWatchStargates(
	ctx context.Context,
	endpoint,
	authority,
	caSecret,
	namespace,
	kubeContext,
	durationSeconds string,
) error {
	command, err := dsl.WatchStargatesCommand(endpoint, authority, caSecret, namespace, kubeContext, durationSeconds)
	if err != nil {
		return err
	}
	if err := sc.runResolvedSuccessfully(ctx, command); err != nil {
		return fmt.Errorf("WatchStargates at %q with TLS authority %q failed: %w", dsl.Interpolate(endpoint), dsl.Interpolate(authority), err)
	}
	return nil
}

func (sc *ScenarioContext) everyPylonForFunctionShouldReportMetrics(
	ctx context.Context,
	functionName,
	containerName,
	kubeContext,
	timeout string,
	table *godog.Table,
) error {
	expectations, err := tableToPylonMetricExpectations(table)
	if err != nil {
		return err
	}
	command, err := dsl.PylonMetricsCommand(functionName, containerName, kubeContext, timeout, expectations)
	if err != nil {
		return err
	}
	if err := sc.runResolvedSuccessfully(ctx, command); err != nil {
		return fmt.Errorf("pylon pods for function %q using container %q did not report expected metrics: %w", dsl.Interpolate(functionName), dsl.Interpolate(containerName), err)
	}
	return nil
}

func tableToPylonMetricExpectations(table *godog.Table) ([]dsl.PylonMetricExpectation, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have metric, comparison, and count headers and at least one data row")
	}
	headers := table.Rows[0].Cells
	if len(headers) != 3 ||
		strings.TrimSpace(headers[0].Value) != "metric" ||
		strings.TrimSpace(headers[1].Value) != "comparison" ||
		strings.TrimSpace(headers[2].Value) != "count" {
		return nil, fmt.Errorf("table headers must be metric, comparison, and count")
	}

	expectations := make([]dsl.PylonMetricExpectation, 0, len(table.Rows)-1)
	for index, row := range table.Rows[1:] {
		if len(row.Cells) != len(headers) {
			return nil, fmt.Errorf("row %d has %d cells, expected %d", index+1, len(row.Cells), len(headers))
		}
		countText := strings.TrimSpace(dsl.Interpolate(row.Cells[2].Value))
		count, err := strconv.Atoi(countText)
		if err != nil || count < 0 {
			return nil, fmt.Errorf("row %d count must be a non-negative integer", index+1)
		}
		expectations = append(expectations, dsl.PylonMetricExpectation{
			Metric:     row.Cells[0].Value,
			Comparison: row.Cells[1].Value,
			Count:      count,
		})
	}
	return expectations, nil
}
