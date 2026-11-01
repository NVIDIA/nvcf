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

package backendconfig

import (
	"fmt"
	"strings"
	"text/template"
)

// templateFuncMap provides the Go template FuncMap that replaces all Jinja2
// variable substitution from the Python generator.
var templateFuncMap = template.FuncMap{
	"indent":            indentFunc,
	"metricAllowList":   metricAllowListFunc,
	"attrAllowList":     attrAllowListFunc,
	"dropLabelPatterns": dropLabelPatternsFunc,
	"attributes":        attributesFunc,
	"metricsTransform":  metricsTransformFunc,
}

// TemplateFuncMap returns the template function map for use when parsing templates.
func TemplateFuncMap() template.FuncMap {
	return templateFuncMap
}

// indentFunc indents every non-empty line of s by n spaces.
func indentFunc(n int, s string) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// findJob looks up the Job in sourceConfig matching functionType and jobName.
func findJob(functionType, jobName string) (*Job, error) {
	for i := range sourceConfig.Metrics {
		if sourceConfig.Metrics[i].FunctionType == functionType {
			for j := range sourceConfig.Metrics[i].Jobs {
				if sourceConfig.Metrics[i].Jobs[j].Name == jobName {
					return &sourceConfig.Metrics[i].Jobs[j], nil
				}
			}
		}
	}
	return nil, fmt.Errorf("job %q not found in function_type %q", jobName, functionType)
}

// metricAllowListFunc returns a pipe-delimited list of metric names for the
// given functionType and jobName.
func metricAllowListFunc(functionType, jobName string) (string, error) {
	job, err := findJob(functionType, jobName)
	if err != nil {
		return "", err
	}
	var names []string
	for _, cat := range job.MetricAllowList {
		for _, item := range cat.List {
			names = append(names, item.Name)
		}
	}
	return strings.Join(names, "|"), nil
}

// attrAllowListFunc returns a pipe-delimited list of attribute names for the
// given functionType and jobName.
func attrAllowListFunc(functionType, jobName string) (string, error) {
	job, err := findJob(functionType, jobName)
	if err != nil {
		return "", err
	}
	var names []string
	for _, cat := range job.AttrAllowList {
		for _, item := range cat.List {
			names = append(names, item.Name)
		}
	}
	return strings.Join(names, "|"), nil
}

// dropLabelPatternsFunc generates YAML blocks for metrics_drop_label_patterns.
//
// The backend parameter should be "vm" or "k8s". The function maps source-config
// backend values ("gfn" -> "vm", "non-gfn" -> "k8s", "" -> both) and filters
// categories accordingly.
func dropLabelPatternsFunc(functionType, backend, jobName string) (string, error) {
	job, err := findJob(functionType, jobName)
	if err != nil {
		return "", err
	}

	var lines []string
	for _, cat := range job.MetricsDropLabelPatterns {
		// Map source-config backend values to template backend values.
		catBackend := cat.Backend
		var include bool
		switch catBackend {
		case "gfn":
			include = backend == "vm"
		case "non-gfn":
			include = backend == "k8s"
		case "":
			// Empty backend means the pattern applies to both backends.
			include = true
		default:
			include = false
		}
		if !include {
			continue
		}

		var names []string
		for _, item := range cat.List {
			names = append(names, item.Name)
		}
		if len(names) == 0 {
			continue
		}

		joined := strings.Join(names, "|")
		lines = append(lines,
			fmt.Sprintf("- source_labels: [%s]", cat.Catagory), //nolint:misspell // intentional
			fmt.Sprintf("  regex: \"(%s)\"", joined),
			"  action: drop",
		)
	}

	return strings.Join(lines, "\n"), nil
}

// configValueEntry holds a resolved attribute's value and the set of workloads
// it applies to, mirroring the Python generator's _config_value_map.
type configValueEntry struct {
	name      string
	value     string
	workloads map[string]bool // "function", "task"
}

// buildConfigValueMap constructs the ordered list of attributes for a backend
// from sourceConfig.Global, preserving insertion order.
func buildConfigValueMap(backend string) []configValueEntry {
	seen := map[string]int{} // name -> index in result
	var result []configValueEntry

	for _, entry := range sourceConfig.Global {
		if entry.Backend != backend {
			continue
		}
		for _, attr := range entry.Metrics.RequiredAttributes {
			if idx, ok := seen[attr.Name]; ok {
				// Same name, same value: add the workload.
				if result[idx].value == attr.Value {
					result[idx].workloads[entry.Workload] = true
				}
			} else {
				seen[attr.Name] = len(result)
				result = append(result, configValueEntry{
					name:      attr.Name,
					value:     attr.Value,
					workloads: map[string]bool{entry.Workload: true},
				})
			}
		}
	}
	return result
}

// checkGuard determines the Go-template guard for an attribute based on its
// name and workload set, matching the Python _check_additional_condition logic.
func checkGuard(attrName string, workloads map[string]bool) string {
	if attrName == "cloud_region" || attrName == "zone_name" {
		return "ZoneName"
	}
	hasFunction := workloads["function"]
	hasTask := workloads["task"]
	if hasFunction && !hasTask {
		return "FunctionID"
	}
	if hasTask && !hasFunction {
		return "TaskID"
	}
	return "" // both workloads -> no guard
}

// resolveValue replaces Go template expressions in attribute values with
// actual values from the TemplateConfig.
func resolveValue(value string, cfg TemplateConfig) string {
	value = strings.ReplaceAll(value, "{{ .ZoneName }}", cfg.ZoneName)
	value = strings.ReplaceAll(value, "{{ .FunctionID }}", cfg.FunctionID)
	value = strings.ReplaceAll(value, "{{ .FunctionVersionID }}", cfg.FunctionVersionID)
	value = strings.ReplaceAll(value, "{{ .TaskID }}", cfg.TaskID)
	value = strings.ReplaceAll(value, "{{ .InstanceID }}", cfg.InstanceID)
	value = strings.ReplaceAll(value, "{{ .Namespace }}", cfg.Namespace)
	return value
}

// shouldInclude checks whether an attribute should be included based on its
// guard and the current TemplateConfig values.
func shouldInclude(guard string, cfg TemplateConfig) bool {
	switch guard {
	case "ZoneName":
		return cfg.ZoneName != ""
	case "FunctionID":
		return cfg.FunctionID != ""
	case "TaskID":
		return cfg.TaskID != ""
	default:
		return true
	}
}

// attributesFunc generates the attributes/add-metadata actions YAML block.
// It resolves conditionals at render time using the TemplateConfig values
// instead of emitting Go template {{- if ... }} blocks.
func attributesFunc(backend string, cfg TemplateConfig) string {
	entries := buildConfigValueMap(backend)
	var lines []string

	for _, entry := range entries {
		guard := checkGuard(entry.name, entry.workloads)
		if !shouldInclude(guard, cfg) {
			continue
		}
		value := resolveValue(entry.value, cfg)
		lines = append(lines,
			"- action: insert",
			"  key: "+entry.name,
			"  value: \""+value+"\"",
		)
	}

	return strings.Join(lines, "\n")
}

// metricsTransformFunc generates the metricstransform operations YAML block.
// Same logic as attributesFunc but with add_label actions and 8-space indent.
func metricsTransformFunc(backend string, cfg TemplateConfig) string {
	entries := buildConfigValueMap(backend)
	var lines []string

	for _, entry := range entries {
		guard := checkGuard(entry.name, entry.workloads)
		if !shouldInclude(guard, cfg) {
			continue
		}
		value := resolveValue(entry.value, cfg)
		lines = append(lines,
			"- action: add_label",
			"  new_label: "+entry.name,
			"  new_value: \""+value+"\"",
		)
	}

	return strings.Join(lines, "\n")
}
