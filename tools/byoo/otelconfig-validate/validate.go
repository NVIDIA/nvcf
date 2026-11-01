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
	"os"
	"path/filepath"
)

// MetricsValidator orchestrates the end-to-end metrics validation.
type MetricsValidator struct {
	config     *Config
	prometheus *PrometheusClient
}

// NewMetricsValidator creates a validator with the given config file and a
// Prometheus client initialised from environment variables.
func NewMetricsValidator(configPath string) (*MetricsValidator, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	prom, err := NewPrometheusClient()
	if err != nil {
		return nil, fmt.Errorf("creating prometheus client: %w", err)
	}

	return &MetricsValidator{
		config:     cfg,
		prometheus: prom,
	}, nil
}

// Validate runs the full validation pipeline and returns per-job results.
func (v *MetricsValidator) Validate(
	wrapperType WrapperType,
	workloadType WorkloadType,
	cloudProvider CloudProvider,
	id, start, end, extraFilters string,
	golden bool,
) (map[string]MetricsValidationResult, error) {
	result, err := v.prometheus.QueryMetrics(wrapperType, id, start, end, extraFilters)
	if err != nil {
		return nil, fmt.Errorf("querying metrics: %w", err)
	}

	if len(result.Data.Result) == 0 {
		log.Printf("No metrics found for the %s id '%s' from %s to %s.", wrapperType, id, start, end)
		return map[string]MetricsValidationResult{
			"metrics_validation_result": ResultInvalid,
		}, nil
	}

	// Golden comparison mode.
	if golden {
		log.Println("Validating against golden metrics...")
		// Directory containing the golden metric fixtures. Override via
		// GOLDEN_DIR so the same binary works in-repo, in CI, and inside
		// the validator container image.
		goldenDir := os.Getenv("GOLDEN_DIR")
		if goldenDir == "" {
			goldenDir = "../../src/libraries/go/lib/validator/otelconfig/golden"
		}
		// cloudProvider, wrapperType, and workloadType are validated against a
		// fixed set of enum values in main.go before reaching this point, so
		// they cannot introduce path traversal sequences. goldenDir is
		// operator-controlled via the GOLDEN_DIR environment variable.
		goldenFile := fmt.Sprintf("metrics_%s_%s_%s.json",
			string(cloudProvider), string(wrapperType), string(workloadType))
		goldenPath := filepath.Clean(filepath.Join(goldenDir, goldenFile))

		goldenBytes, err := os.ReadFile(goldenPath) // #nosec G304 -- path components are validated enums; goldenDir is operator-controlled
		if err != nil {
			return nil, fmt.Errorf("reading golden file %s: %w", goldenPath, err)
		}

		// Marshal the data section of the result for comparison.
		actualBytes, err := json.Marshal(result.Data)
		if err != nil {
			return nil, fmt.Errorf("marshalling actual data: %w", err)
		}

		hasDiff, err := diff(goldenBytes, actualBytes)
		if err != nil {
			return nil, fmt.Errorf("diffing golden metrics: %w", err)
		}
		if hasDiff {
			log.Println("Differences found between the metrics and golden metric.")
			return map[string]MetricsValidationResult{
				"golden_metrics_compare_result": ResultInvalid,
			}, nil
		}
		return map[string]MetricsValidationResult{
			"golden_metrics_compare_result": ResultValid,
		}, nil
	}

	// Group metrics by job label.
	metricsByJob := make(map[string][]MetricResult)
	for _, entry := range result.Data.Result {
		job := entry.Metric["job"]
		if job == "" {
			continue
		}
		metricsByJob[job] = append(metricsByJob[job], entry)
	}

	metadataAttrs := v.config.GetMetadataAttributes(cloudProvider, wrapperType)

	jobNames := make([]string, 0, len(metricsByJob))
	for k := range metricsByJob {
		jobNames = append(jobNames, k)
	}
	log.Printf("Found %d jobs in the metrics data: %v", len(metricsByJob), jobNames)

	// Required jobs: platform + custom.
	requiredJobs := []MetricsJob{
		MetricsJobKubeState,
		MetricsJobCadvisor,
		MetricsJobNvidiaDCGM,
		MetricsJobOtelCollector,
	}
	if wrapperType == WrapperTypeFunction {
		requiredJobs = append(requiredJobs, MetricsJobByooTest)
	} else {
		requiredJobs = append(requiredJobs, MetricsJobByooTaskTest)
	}

	// Check for missing/extra jobs.
	requiredSet := make(map[string]bool, len(requiredJobs))
	for _, j := range requiredJobs {
		requiredSet[string(j)] = true
	}

	var missingJobs []string
	for _, j := range requiredJobs {
		if _, ok := metricsByJob[string(j)]; !ok {
			missingJobs = append(missingJobs, string(j))
		}
	}

	var extraJobs []string
	for job := range metricsByJob {
		if !requiredSet[job] {
			extraJobs = append(extraJobs, job)
		}
	}

	if len(missingJobs) > 0 {
		log.Printf("Missing required jobs: %v", missingJobs)
	} else {
		log.Println("All required jobs are present")
	}

	if len(extraJobs) > 0 {
		log.Printf("Found unexpected jobs: %v", extraJobs)
	} else {
		log.Println("No unexpected jobs found")
	}
	log.Println()

	// Validate each job.
	results := make(map[string]MetricsValidationResult)
	for job, metrics := range metricsByJob {
		log.Printf("=== Start validating metrics for job '%s' ===", job)

		metricsComponent := MetricsJobFromString(job)
		if metricsComponent == MetricsJobOther {
			results[job] = ResultSkipped
			log.Printf("Skip the validating for job %s", job)
		} else {
			rules, err := v.config.GetAllowList(workloadType, metricsComponent)
			if err != nil {
				return nil, fmt.Errorf("getting allow list for job %s: %w", job, err)
			}
			results[job] = validateMetrics(metrics, rules, metadataAttrs)
		}

		log.Println("=== Finished metrics validation for job ===")
		log.Println()
	}

	return results, nil
}

// validateMetrics checks a set of metrics for a single job against the allow-list
// rules and required metadata attributes.
func validateMetrics(metrics []MetricResult, rules *MetricsJobConfig, metadataAttrs []string) MetricsValidationResult {
	hasErr := false
	hasWarn := false

	// Build the full attribute allow list (rules + metadata) without mutating rules.
	fullAttrAllowList := make(map[string]bool, len(rules.AttrAllowList)+len(metadataAttrs))
	for _, a := range rules.AttrAllowList {
		fullAttrAllowList[a] = true
	}
	for _, a := range metadataAttrs {
		fullAttrAllowList[a] = true
	}

	// Check metric count.
	if len(metrics) == 0 {
		hasErr = true
		log.Println("No metrics found for this job.")
	} else if len(metrics) == 1 {
		hasErr = true
		log.Println("Only one metric (up) found for this job.")
	} else {
		log.Printf("Found %d metrics for this job.", len(metrics))
	}

	// Check "up" metric value.
	for _, m := range metrics {
		if m.Metric["__name__"] == "up" && len(m.Values) > 0 {
			lastVal := m.Values[len(m.Values)-1]
			if len(lastVal) >= 2 {
				if valStr, ok := lastVal[1].(string); ok && valStr == "0" {
					hasErr = true
					log.Println("Value for metric 'up' is 0, indicating the target is down")
				}
			}
		}
	}

	// Build sets for metric-name validation.
	actualMetrics := make(map[string]bool)
	for _, m := range metrics {
		if name, ok := m.Metric["__name__"]; ok {
			actualMetrics[name] = true
		}
	}

	allowedMetrics := make(map[string]bool, len(rules.MetricsAllowList))
	for _, m := range rules.MetricsAllowList {
		allowedMetrics[m] = true
	}

	// Extra metrics (not in allow list).
	var invalidMetrics []string
	for name := range actualMetrics {
		if !allowedMetrics[name] {
			invalidMetrics = append(invalidMetrics, name)
		}
	}
	if len(invalidMetrics) > 0 {
		hasErr = true
		log.Printf("Metrics not in allow list: %v", invalidMetrics)
	}

	// Missing metrics (in allow list but not found).
	var missingMetrics []string
	for name := range allowedMetrics {
		if !actualMetrics[name] {
			missingMetrics = append(missingMetrics, name)
		}
	}
	if len(missingMetrics) > 0 {
		hasWarn = true
		log.Printf("Metrics not found: %v", missingMetrics)
	}

	// Check attributes per metric.
	if checkMetricAttributes(metrics, fullAttrAllowList, metadataAttrs) {
		hasErr = true
	}

	if hasErr {
		return ResultInvalid
	}
	if hasWarn {
		return ResultValidWithWarnings
	}
	return ResultValid
}

// checkMetricAttributes validates attributes for each metric against the allow list
// and checks for required metadata attributes. Returns true if any errors were found.
func checkMetricAttributes(metrics []MetricResult, fullAttrAllowList map[string]bool, metadataAttrs []string) bool {
	metadataSet := make(map[string]bool, len(metadataAttrs))
	for _, a := range metadataAttrs {
		metadataSet[a] = true
	}

	hasErr := false
	for _, m := range metrics {
		metricName := m.Metric["__name__"]

		var invalidAttrs []string
		for attr := range m.Metric {
			if attr != "__name__" && !fullAttrAllowList[attr] {
				invalidAttrs = append(invalidAttrs, attr)
			}
		}
		if len(invalidAttrs) > 0 {
			hasErr = true
			log.Printf("Attributes not in allow list: %v, metric: %s", invalidAttrs, metricName)
		}

		var missingMeta []string
		for attr := range metadataSet {
			if _, ok := m.Metric[attr]; !ok {
				missingMeta = append(missingMeta, attr)
			}
		}
		if len(missingMeta) > 0 {
			hasErr = true
			log.Printf("Missing necessary metadata attributes: %v, metric: %s", missingMeta, metricName)
		}
	}
	return hasErr
}
