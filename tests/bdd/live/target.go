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

// Package live owns target selection and phased execution for portable live
// smoke features. It reuses the shared strict DSL without adding target modes
// to the local installation suite.
package live

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"nvcf-bdd/dsl"
)

// Target contains only execution coordinates and workload inputs. It does not
// select features, contain credentials, or validate product-level values.
type Target struct {
	Version  int            `yaml:"version"`
	Name     string         `yaml:"name"`
	NVCF     NVCFTarget     `yaml:"nvcf"`
	Compute  ComputeTarget  `yaml:"compute"`
	Workload WorkloadTarget `yaml:"workload"`
}

// NVCFTarget identifies the CLI config and its exact mutable state file.
type NVCFTarget struct {
	CLIConfig string `yaml:"cliConfig"`
	CLIState  string `yaml:"cliState"`
}

// ComputeTarget identifies one attached compute cluster without relying on
// the ambient kubeconfig or current context.
type ComputeTarget struct {
	Kubeconfig       string `yaml:"kubeconfig"`
	Context          string `yaml:"context"`
	Cluster          string `yaml:"cluster"`
	BackendNamespace string `yaml:"backendNamespace"`
	SystemNamespace  string `yaml:"systemNamespace"`
	Region           string `yaml:"region"`
}

// WorkloadTarget carries values that vary across local and remote capacity.
type WorkloadTarget struct {
	GPU           string `yaml:"gpu"`
	InstanceType  string `yaml:"instanceType"`
	FunctionImage string `yaml:"functionImage"`
}

// LoadTarget strictly decodes a versioned target file and interpolates its
// strings with the shared ${VAR} implementation. Validation is limited to the
// target contract needed by every smoke. Individual features declare any
// compute or workload fields they require through environment preconditions.
func LoadTarget(path string) (Target, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Target{}, fmt.Errorf("read live target: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var target Target
	if err := decoder.Decode(&target); err != nil {
		return Target{}, fmt.Errorf("decode live target: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Target{}, errors.New("decode live target: multiple YAML documents are not supported")
		}
		return Target{}, fmt.Errorf("decode live target: %w", err)
	}
	if err := target.interpolate(); err != nil {
		return Target{}, err
	}
	if target.Version != 1 {
		return Target{}, fmt.Errorf("live target version must be 1, got %d", target.Version)
	}
	for _, item := range []struct {
		field string
		value string
	}{
		{field: "name", value: target.Name},
		{field: "nvcf.cliConfig", value: target.NVCF.CLIConfig},
		{field: "nvcf.cliState", value: target.NVCF.CLIState},
	} {
		if item.value == "" {
			return Target{}, fmt.Errorf("live target field %s must not be empty", item.field)
		}
	}
	if !filepath.IsAbs(target.NVCF.CLIState) {
		return Target{}, errors.New("live target field nvcf.cliState must be an absolute path")
	}
	return target, nil
}

func (t *Target) interpolate() error {
	required := []struct {
		field string
		value *string
	}{
		{field: "name", value: &t.Name},
		{field: "nvcf.cliConfig", value: &t.NVCF.CLIConfig},
		{field: "nvcf.cliState", value: &t.NVCF.CLIState},
	}
	for _, item := range required {
		resolved, err := dsl.InterpolateRequired(*item.value)
		if err != nil {
			return fmt.Errorf("live target field %s: %w", item.field, err)
		}
		*item.value = resolved
	}
	optional := []*string{
		&t.Compute.Kubeconfig,
		&t.Compute.Context,
		&t.Compute.Cluster,
		&t.Compute.BackendNamespace,
		&t.Compute.SystemNamespace,
		&t.Compute.Region,
		&t.Workload.GPU,
		&t.Workload.InstanceType,
		&t.Workload.FunctionImage,
	}
	for _, value := range optional {
		*value = dsl.Interpolate(*value)
	}
	return nil
}

// Environment maps non-secret target fields to the stable names used by
// portable feature files. Empty optional values remain unset so the feature's
// existing environment precondition reports the missing input.
func (t Target) Environment(runID string) map[string]string {
	values := map[string]string{
		"BDD_TARGET_NAME":            t.Name,
		"BDD_NVCF_CLI_CONFIG":        t.NVCF.CLIConfig,
		"BDD_COMPUTE_KUBECONFIG":     t.Compute.Kubeconfig,
		"BDD_COMPUTE_CONTEXT":        t.Compute.Context,
		"BDD_COMPUTE_CLUSTER":        t.Compute.Cluster,
		"BDD_NVCA_BACKEND_NAMESPACE": t.Compute.BackendNamespace,
		"BDD_NVCA_SYSTEM_NAMESPACE":  t.Compute.SystemNamespace,
		"BDD_COMPUTE_REGION":         t.Compute.Region,
		"BDD_WORKLOAD_GPU":           t.Workload.GPU,
		"BDD_WORKLOAD_INSTANCE_TYPE": t.Workload.InstanceType,
		"BDD_FUNCTION_IMAGE":         t.Workload.FunctionImage,
		"BDD_RUN_ID":                 runID,
	}
	for name, value := range values {
		if value == "" {
			delete(values, name)
		}
	}
	return values
}
