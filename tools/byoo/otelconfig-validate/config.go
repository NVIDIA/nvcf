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
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// WrapperType represents the type of NVCF wrapper (function or task).
type WrapperType string

const (
	WrapperTypeFunction WrapperType = "function"
	WrapperTypeTask     WrapperType = "task"
)

// WorkloadType represents the workload deployment type (container or helm).
type WorkloadType string

const (
	WorkloadTypeContainer WorkloadType = "container"
	WorkloadTypeHelm      WorkloadType = "helm"
)

// CloudProvider represents the cloud provider category.
type CloudProvider string

const (
	CloudProviderGFN    CloudProvider = "gfn"
	CloudProviderNonGFN CloudProvider = "non-gfn"
)

// MetricsJob represents a known Prometheus scrape job name.
type MetricsJob string

const (
	MetricsJobNvidiaDCGM    MetricsJob = "nvidia-dcgm-exporter"
	MetricsJobKubeState     MetricsJob = "kube-state-metrics"
	MetricsJobCadvisor      MetricsJob = "kubernetes-cadvisor"
	MetricsJobOtelCollector MetricsJob = "opentelemetry-collector"
	MetricsJobByooTest      MetricsJob = "byoo-test"
	MetricsJobByooTaskTest  MetricsJob = "byoo-task-test"
	MetricsJobOther         MetricsJob = "other"
)

// MetricsJobFromString converts a string to a MetricsJob, returning MetricsJobOther
// for unrecognised values.
func MetricsJobFromString(s string) MetricsJob {
	switch MetricsJob(s) {
	case MetricsJobNvidiaDCGM, MetricsJobKubeState, MetricsJobCadvisor,
		MetricsJobOtelCollector, MetricsJobByooTest, MetricsJobByooTaskTest:
		return MetricsJob(s)
	default:
		return MetricsJobOther
	}
}

// MetricsValidationResult describes the outcome of validating a single job.
type MetricsValidationResult string

const (
	ResultValid             MetricsValidationResult = "Valid"
	ResultValidWithWarnings MetricsValidationResult = "Valid with warnings"
	ResultInvalid           MetricsValidationResult = "Invalid"
	ResultSkipped           MetricsValidationResult = "Skipped"
)

// MetricsJobConfig holds the allow-list rules for a single job.
type MetricsJobConfig struct {
	Name             string   `yaml:"name"`
	MetricsAllowList []string `yaml:"metrics_allow_list"`
	AttrAllowList    []string `yaml:"attr_allow_list"`
}

// Config is the top-level validator configuration loaded from YAML.
type Config struct {
	MetadataAttributes map[string]map[string][]string `yaml:"metadata_attributes"`
	ValidatingRules    map[string][]MetricsJobConfig  `yaml:"validating_rules"`
}

// LoadConfig reads and parses a validator-config YAML file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return &cfg, nil
}

// GetMetadataAttributes returns the expected metadata attributes for the given
// cloud provider and wrapper type combination.
func (c *Config) GetMetadataAttributes(cp CloudProvider, wt WrapperType) []string {
	providerAttrs, ok := c.MetadataAttributes[string(cp)]
	if !ok {
		return nil
	}
	return providerAttrs[string(wt)]
}

// GetAllowList returns the MetricsJobConfig for the given workload type and job.
func (c *Config) GetAllowList(workloadType WorkloadType, job MetricsJob) (*MetricsJobConfig, error) {
	rules, ok := c.ValidatingRules[string(workloadType)]
	if !ok {
		return nil, fmt.Errorf("no validating rules for workload type %s", workloadType)
	}
	for i := range rules {
		if rules[i].Name == string(job) {
			return &rules[i], nil
		}
	}
	return nil, fmt.Errorf("no configuration found for job %s", job)
}
