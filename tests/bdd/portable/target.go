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

// Package portable owns target selection and phased execution for attach-mode
// smoke features. It reuses the shared strict DSL without adding target modes
// to the local installation suite.
package portable

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"nvcf-bdd/dsl"
)

const cliConfigEnvironment = "BDD_NVCF_CLI_CONFIG"

var targetReservedEnvironment = map[string]struct{}{
	"BDD_ALLOW_CLUSTER_MAINTENANCE": {},
	"BDD_ALLOW_TOPOLOGY_MUTATION":   {},
	"BDD_CLEANUP_MODE":              {},
	"BDD_RUN_ID":                    {},
	"BDD_TARGET_NAME":               {},
	"NVCF_CLI":                      {},
	"REPO_ROOT":                     {},
}

// Target contains non-secret execution coordinates and workload inputs. Env
// is intentionally open-ended so adding a feature input does not require a Go
// schema change. The strict top-level shape keeps execution control separate.
type Target struct {
	Version int               `yaml:"version"`
	Name    string            `yaml:"name"`
	Env     map[string]string `yaml:"env"`
}

// LoadTarget strictly decodes structural target metadata and interpolates all
// environment values with the shared ${VAR} implementation. Individual
// features remain responsible for declaring which values they require.
func LoadTarget(path string) (Target, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Target{}, fmt.Errorf("read portable target: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var target Target
	if err := decoder.Decode(&target); err != nil {
		return Target{}, fmt.Errorf("decode portable target: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Target{}, errors.New("decode portable target: multiple YAML documents are not supported")
		}
		return Target{}, fmt.Errorf("decode portable target: %w", err)
	}
	if target.Version != 1 {
		return Target{}, fmt.Errorf("portable target version must be 1, got %d", target.Version)
	}
	name, err := dsl.InterpolateRequired(target.Name)
	if err != nil {
		return Target{}, fmt.Errorf("portable target field name: %w", err)
	}
	target.Name = strings.TrimSpace(name)
	if target.Name == "" {
		return Target{}, errors.New("portable target field name must not be empty")
	}
	if target.Env == nil {
		target.Env = make(map[string]string)
	}
	for name, value := range target.Env {
		if err := validateEnvironmentName(name); err != nil {
			return Target{}, fmt.Errorf("portable target env: %w", err)
		}
		if _, reserved := targetReservedEnvironment[name]; reserved {
			return Target{}, fmt.Errorf("portable target env %s is runner-owned or invocation-time consent", name)
		}
		target.Env[name] = dsl.Interpolate(value)
	}
	if strings.TrimSpace(target.Env[cliConfigEnvironment]) == "" {
		return Target{}, fmt.Errorf("portable target env %s must not be empty", cliConfigEnvironment)
	}
	return target, nil
}

func validateEnvironmentName(name string) error {
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return fmt.Errorf("invalid environment variable name %q", name)
	}
	return nil
}

// Environment returns a copy of the target values plus runner-owned identity
// fields. Empty feature inputs remain visible so Gherkin preconditions report
// them instead of the target loader duplicating feature validation.
func (t Target) Environment(runID string) map[string]string {
	values := make(map[string]string, len(t.Env)+2)
	for name, value := range t.Env {
		values[name] = value
	}
	values["BDD_TARGET_NAME"] = t.Name
	values["BDD_RUN_ID"] = runID
	return values
}
