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
	_ "embed"

	"gopkg.in/yaml.v3"
)

//go:embed source-config.yaml
var sourceConfigData []byte

var sourceConfig *SourceConfig

func init() {
	var sc SourceConfig
	if err := yaml.Unmarshal(sourceConfigData, &sc); err != nil {
		panic("failed to parse source-config.yaml: " + err.Error())
	}
	sourceConfig = &sc
}

// SourceConfig is the top-level structure of source-config.yaml.
type SourceConfig struct {
	Global  []GlobalEntry   `yaml:"global"`
	Metrics []MetricSection `yaml:"metrics"`
}

// GlobalEntry represents a backend/workload combination with its required attributes.
type GlobalEntry struct {
	Backend  string        `yaml:"backend"`
	Workload string        `yaml:"workload"`
	Metrics  GlobalMetrics `yaml:"metrics"`
}

// GlobalMetrics holds the required_attributes list within a global entry.
type GlobalMetrics struct {
	RequiredAttributes []RequiredAttribute `yaml:"required_attributes"`
}

// RequiredAttribute is a name/value pair for a metric attribute.
type RequiredAttribute struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// MetricSection groups jobs by function_type (e.g. "helm", "container").
type MetricSection struct {
	FunctionType string `yaml:"function_type"`
	Jobs         []Job  `yaml:"jobs"`
}

// Job represents a Prometheus scrape job with its allow lists and drop patterns.
type Job struct {
	Name                     string              `yaml:"name"`
	MetricAllowList          []Category          `yaml:"metric_allow_list"`
	AttrAllowList            []Category          `yaml:"attr_allow_list"`
	MetricsDropLabelPatterns []DropLabelCategory `yaml:"metrics_drop_label_patterns"`
}

// Category groups metric or attribute items under a category name.
type Category struct {
	Catagory  string       `yaml:"catagory"` //nolint:misspell // intentional typo matching source-config.yaml
	Docstring string       `yaml:"docstring"`
	List      []MetricItem `yaml:"list"`
}

// MetricItem is a named item with an optional comment.
type MetricItem struct {
	Name    string `yaml:"name"`
	Comment string `yaml:"comment"`
}

// DropLabelCategory defines patterns for dropping metrics by label value,
// optionally scoped to a specific backend.
type DropLabelCategory struct {
	Catagory string       `yaml:"catagory"` //nolint:misspell // source label name, intentional typo matching YAML
	Backend  string       `yaml:"backend"`  // "gfn", "non-gfn", or "" (both)
	List     []MetricItem `yaml:"list"`
}
