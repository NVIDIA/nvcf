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

package dsl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const waitPrometheusTotalsScript = "tests/bdd/scripts/wait-prometheus-totals.sh"

var (
	prometheusLabelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	prometheusLabelValueRE = regexp.MustCompile(`^[^",\\\s]+$`)
)

// PrometheusTotalExpectation describes the expected sum of every sample of
// one metric whose labels include each name=value pair in Labels. Labels is a
// comma-separated list; an empty list matches every sample of the metric.
type PrometheusTotalExpectation struct {
	Metric     string
	Labels     string
	Comparison string
	Total      int
}

// PrometheusTotalsCommand builds an observation that sums matching samples
// across every running pod selected by an explicit label selector, namespace,
// metrics port, and Kubernetes context. The command polls until every
// expectation holds or the timeout elapses.
func PrometheusTotalsCommand(port, selector, namespace, kubeContext, timeout string, expectations []PrometheusTotalExpectation) (string, error) {
	values := []*string{&port, &selector, &namespace, &kubeContext, &timeout}
	names := []string{"port", "pod selector", "namespace", "kube context", "timeout"}
	for index := range values {
		*values[index] = strings.TrimSpace(Interpolate(*values[index]))
		if *values[index] == "" {
			return "", fmt.Errorf("%s is empty", names[index])
		}
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("port %q must be an integer from 1 to 65535", port)
	}
	if len(expectations) == 0 {
		return "", fmt.Errorf("prometheus total expectations are empty")
	}

	args := []string{"bash", waitPrometheusTotalsScript, port, selector, namespace, kubeContext, timeout}
	for index, expectation := range expectations {
		metric := strings.TrimSpace(Interpolate(expectation.Metric))
		comparison := strings.TrimSpace(Interpolate(expectation.Comparison))
		if !prometheusMetricNameRE.MatchString(metric) {
			return "", fmt.Errorf("total row %d has invalid metric name %q", index+1, metric)
		}
		labels, err := normalizePrometheusLabels(Interpolate(expectation.Labels))
		if err != nil {
			return "", fmt.Errorf("total row %d: %w", index+1, err)
		}
		if comparison != "exactly" && comparison != "at least" {
			return "", fmt.Errorf("total row %d comparison must be exactly or at least", index+1)
		}
		if expectation.Total < 0 {
			return "", fmt.Errorf("total row %d total must be non-negative", index+1)
		}
		args = append(args, metric, labels, comparison, strconv.Itoa(expectation.Total))
	}

	return BuildCommand(args...), nil
}

// normalizePrometheusLabels validates a comma-separated name=value list and
// returns it without surrounding whitespace. Values are compared literally
// against the exposition text, so quotes, commas, backslashes, and whitespace
// are rejected rather than escaped.
func normalizePrometheusLabels(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	pairs := strings.Split(raw, ",")
	seen := make(map[string]bool, len(pairs))
	for index, pair := range pairs {
		name, value, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found {
			return "", fmt.Errorf("label %q must be name=value", strings.TrimSpace(pair))
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !prometheusLabelNameRE.MatchString(name) {
			return "", fmt.Errorf("invalid label name %q", name)
		}
		if !prometheusLabelValueRE.MatchString(value) {
			return "", fmt.Errorf("label %s value %q must be non-empty without quotes, commas, backslashes, or whitespace", name, value)
		}
		if seen[name] {
			return "", fmt.Errorf("label %s is repeated", name)
		}
		seen[name] = true
		pairs[index] = name + "=" + value
	}
	return strings.Join(pairs, ","), nil
}
