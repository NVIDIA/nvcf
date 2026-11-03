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

func registerMetricsSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^Prometheus metrics on port "([^"]*)" of pods selected by "([^"]*)" in namespace "([^"]*)" using context "([^"]*)" should total within "([^"]*)":$`, sc.prometheusMetricsShouldTotal)
}

func (sc *ScenarioContext) prometheusMetricsShouldTotal(
	ctx context.Context,
	port,
	selector,
	namespace,
	kubeContext,
	timeout string,
	table *godog.Table,
) error {
	expectations, err := tableToPrometheusTotalExpectations(table)
	if err != nil {
		return err
	}
	command, err := dsl.PrometheusTotalsCommand(port, selector, namespace, kubeContext, timeout, expectations)
	if err != nil {
		return err
	}
	if err := sc.runResolvedSuccessfully(ctx, command); err != nil {
		return fmt.Errorf("pods selected by %q in namespace %q did not report the expected Prometheus totals: %w", dsl.Interpolate(selector), dsl.Interpolate(namespace), err)
	}
	return nil
}

func tableToPrometheusTotalExpectations(table *godog.Table) ([]dsl.PrometheusTotalExpectation, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have metric, labels, comparison, and total headers and at least one data row")
	}
	headers := table.Rows[0].Cells
	want := []string{"metric", "labels", "comparison", "total"}
	if len(headers) != len(want) {
		return nil, fmt.Errorf("table headers must be metric, labels, comparison, and total")
	}
	for index, header := range headers {
		if strings.TrimSpace(header.Value) != want[index] {
			return nil, fmt.Errorf("table headers must be metric, labels, comparison, and total")
		}
	}

	expectations := make([]dsl.PrometheusTotalExpectation, 0, len(table.Rows)-1)
	for index, row := range table.Rows[1:] {
		if len(row.Cells) != len(headers) {
			return nil, fmt.Errorf("row %d has %d cells, expected %d", index+1, len(row.Cells), len(headers))
		}
		total, err := strconv.Atoi(strings.TrimSpace(dsl.Interpolate(row.Cells[3].Value)))
		if err != nil || total < 0 {
			return nil, fmt.Errorf("row %d total must be a non-negative integer", index+1)
		}
		expectations = append(expectations, dsl.PrometheusTotalExpectation{
			Metric:     row.Cells[0].Value,
			Labels:     row.Cells[1].Value,
			Comparison: row.Cells[2].Value,
			Total:      total,
		})
	}
	return expectations, nil
}
