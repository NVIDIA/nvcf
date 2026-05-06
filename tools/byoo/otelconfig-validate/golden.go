/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// skipValidation is the placeholder used for values that should not be compared.
const skipValidation = "<SKIP_VALIDATION>"

// ignoreValueOfKeys lists metric label keys whose values are replaced with
// skipValidation before comparison because they vary per deployment.
var ignoreValueOfKeys = []string{
	"cloud_provider",
	"cloud_region",
	"DCGM_FI_DRIVER_VERSION",
	"device",
	"exporter",
	"function_id",
	"function_version_id",
	"host_id",
	"image",
	"instance_id",
	"modelName",
	"nca_id",
	"pci_bus_id",
	"task_id",
	"transport",
	"zone_name",
}

// ignoreMetricsPatterns lists regex patterns for metric names that should be
// removed entirely before comparison.
var ignoreMetricsPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^http_.*`),
	regexp.MustCompile(`^kube_pod_container_status_waiting_reason$`),
	regexp.MustCompile(`^kube_pod_container_status_terminated_reason$`),
}

// ignoreMetricIfLabel describes a (metric pattern, label, value) triple. If a
// metric name matches the pattern and the label equals value (nil means the label
// is absent or empty), the metric is removed.
type ignoreMetricIfLabel struct {
	pattern *regexp.Regexp
	label   string
	value   string // empty string means "label absent or empty"
}

var ignoreMetricsIfLabelRules = []ignoreMetricIfLabel{
	{regexp.MustCompile(`^DCGM_FI_DEV_GPU_UTIL$`), "container", ""},
}

// redactKeyRule describes a (label key, regex pattern, replacement) triple used
// to normalise dynamic values (e.g. pod names) before comparison.
type redactKeyRule struct {
	key         string
	pattern     *regexp.Regexp
	replacement string
}

var redactKeyRules = []redactKeyRule{
	{"pod", regexp.MustCompile(`^0-sr-.*`), "0-sr-" + skipValidation},
	{"pod", regexp.MustCompile(`^byoo-test-.*`), "byoo-test-" + skipValidation},
	{"created_by_name", regexp.MustCompile(`^byoo-test-.*`), "byoo-test-" + skipValidation},
	{"replicaset", regexp.MustCompile(`^byoo-test-.*`), "byoo-test-" + skipValidation},
	{"pod", regexp.MustCompile(`^task-helmchart-byoo-.*`), "task-helmchart-byoo-" + skipValidation},
}

// goldenData is a minimal representation of the Prometheus response "data"
// section used for golden comparison. We use interface{} maps so we can
// marshal/unmarshal arbitrary JSON without losing structure.
type goldenData struct {
	Result []goldenMetric `json:"result"`
}

type goldenMetric struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

// diff compares golden and actual metric data, applying normalisation rules to
// actual before comparison. It prints a colorised line-by-line diff and returns
// true if differences were found.
func diff(goldenBytes, actualBytes []byte) (bool, error) {
	var golden, actual goldenData
	if err := json.Unmarshal(goldenBytes, &golden); err != nil {
		return false, fmt.Errorf("parsing golden JSON: %w", err)
	}
	if err := json.Unmarshal(actualBytes, &actual); err != nil {
		return false, fmt.Errorf("parsing actual JSON: %w", err)
	}

	// Normalise actual data: clear values, redact dynamic keys, remove ignored metrics.
	normaliseActual(&actual)

	log.Printf("Number of golden metrics: %d", len(golden.Result))
	log.Printf("Number of actual metrics: %d", len(actual.Result))

	goldenJSON, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshalling golden JSON: %w", err)
	}
	actualJSON, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshalling actual JSON: %w", err)
	}

	goldenLines := strings.Split(string(goldenJSON), "\n")
	actualLines := strings.Split(string(actualJSON), "\n")

	diffLines := simpleDiff(goldenLines, actualLines)
	printDiffColorized(diffLines)

	return len(diffLines) > 0, nil
}

// normaliseActual applies all the ignore/redact rules to the actual data so it
// can be compared against the golden snapshot.
func normaliseActual(actual *goldenData) {
	// Iterate backwards so deletions don't shift indices.
	for i := len(actual.Result) - 1; i >= 0; i-- {
		m := &actual.Result[i]
		metricName := m.Metric["__name__"]

		// Clear values to avoid false positives.
		m.Values = nil

		// Remove metrics matching ignore patterns.
		removed := false
		for _, p := range ignoreMetricsPatterns {
			if p.MatchString(metricName) {
				actual.Result = append(actual.Result[:i], actual.Result[i+1:]...)
				removed = true
				break
			}
		}
		if removed {
			continue
		}

		// Remove metrics matching ignore-if-label rules.
		for _, rule := range ignoreMetricsIfLabelRules {
			if rule.pattern.MatchString(metricName) {
				labelVal, exists := m.Metric[rule.label]
				if (!exists || labelVal == "") && rule.value == "" {
					actual.Result = append(actual.Result[:i], actual.Result[i+1:]...)
					removed = true
					break
				}
			}
		}
		if removed {
			continue
		}

		// Replace dynamic label values with skipValidation.
		for _, key := range ignoreValueOfKeys {
			if _, ok := m.Metric[key]; ok {
				m.Metric[key] = skipValidation
			}
		}

		// Redact dynamic names (pods, replicasets, etc.).
		for _, rule := range redactKeyRules {
			if val, ok := m.Metric[rule.key]; ok {
				if rule.pattern.MatchString(val) {
					m.Metric[rule.key] = rule.replacement
				}
			}
		}
	}
}

// simpleDiff produces a minimal line-by-line diff between two slices of strings.
// It walks both slices and emits "- " / "+ " prefixed lines for differences and
// "  " prefixed lines for context. This is not a full unified-diff algorithm but
// is sufficient for readable JSON comparison output.
func simpleDiff(a, b []string) []string {
	// Build a map of lines in b for quick lookup.
	// Use a longest-common-subsequence approach for better diffs.
	aLen, bLen := len(a), len(b)

	// For very large inputs, fall back to a simpler comparison.
	if aLen+bLen > 20000 {
		return simpleFallbackDiff(a, b)
	}

	// Myers-like approach using the standard LCS dp table.
	// Build LCS length table.
	lcs := make([][]int, aLen+1)
	for i := range lcs {
		lcs[i] = make([]int, bLen+1)
	}
	for i := aLen - 1; i >= 0; i-- {
		for j := bLen - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var result []string
	i, j := 0, 0
	for i < aLen && j < bLen {
		if a[i] == b[j] {
			// Context line — skip for brevity unless near a change.
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			result = append(result, "- "+a[i])
			i++
		} else {
			result = append(result, "+ "+b[j])
			j++
		}
	}
	for ; i < aLen; i++ {
		result = append(result, "- "+a[i])
	}
	for ; j < bLen; j++ {
		result = append(result, "+ "+b[j])
	}

	return result
}

// simpleFallbackDiff handles very large inputs by doing a straight line-by-line
// comparison without LCS.
func simpleFallbackDiff(a, b []string) []string {
	var result []string
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	for i := 0; i < maxLen; i++ {
		aLine, bLine := "", ""
		if i < len(a) {
			aLine = a[i]
		}
		if i < len(b) {
			bLine = b[i]
		}
		if aLine != bLine {
			if i < len(a) {
				result = append(result, "- "+aLine)
			}
			if i < len(b) {
				result = append(result, "+ "+bLine)
			}
		}
	}
	return result
}
