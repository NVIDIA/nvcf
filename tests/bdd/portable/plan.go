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

package portable

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plan is a committed, ordered composition that remains separate from target
// coordinates and invocation-time consent.
type Plan struct {
	Version int     `yaml:"version"`
	Name    string  `yaml:"name"`
	Phases  []Phase `yaml:"phases"`
}

// Phase names one independently executed feature and its optional tag filter.
// MutatesTopology makes provisioning intent reviewable and requires consent.
type Phase struct {
	Name            string   `yaml:"name"`
	Feature         string   `yaml:"feature"`
	Tags            string   `yaml:"tags,omitempty"`
	MutatesTopology bool     `yaml:"mutatesTopology"`
	Consent         *Consent `yaml:"consent,omitempty"`
}

// Consent ties a value supplied by the invoking operator to a non-secret
// target coordinate before any phase runs.
type Consent struct {
	Environment             string `yaml:"environment"`
	EqualsTargetEnvironment string `yaml:"equalsTargetEnvironment"`
}

// LoadPlan strictly decodes a versioned plan and resolves every feature below
// tests/bdd/features. The plan is selection, not authorization.
func LoadPlan(repoRoot, path string) (Plan, error) {
	resolvedPlanPath, err := resolvePlan(repoRoot, path)
	if err != nil {
		return Plan{}, err
	}
	raw, err := os.ReadFile(resolvedPlanPath)
	if err != nil {
		return Plan{}, fmt.Errorf("read portable plan: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode portable plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Plan{}, errors.New("decode portable plan: multiple YAML documents are not supported")
		}
		return Plan{}, fmt.Errorf("decode portable plan: %w", err)
	}
	if plan.Version != 1 {
		return Plan{}, fmt.Errorf("portable plan version must be 1, got %d", plan.Version)
	}
	plan.Name = strings.TrimSpace(plan.Name)
	if plan.Name == "" {
		return Plan{}, errors.New("portable plan field name must not be empty")
	}
	if len(plan.Phases) == 0 {
		return Plan{}, errors.New("portable plan must contain at least one phase")
	}
	seenNames := make(map[string]struct{}, len(plan.Phases))
	for index := range plan.Phases {
		phase := &plan.Phases[index]
		phase.Name = strings.TrimSpace(phase.Name)
		if phase.Name == "" {
			return Plan{}, fmt.Errorf("portable plan phase %d name must not be empty", index+1)
		}
		if _, exists := seenNames[phase.Name]; exists {
			return Plan{}, fmt.Errorf("portable plan phase name selected more than once: %s", phase.Name)
		}
		seenNames[phase.Name] = struct{}{}
		resolved, err := resolveFeature(repoRoot, strings.TrimSpace(phase.Feature))
		if err != nil {
			return Plan{}, fmt.Errorf("portable plan phase %s: %w", phase.Name, err)
		}
		phase.Feature = resolved
		if phase.MutatesTopology && phase.Consent == nil {
			return Plan{}, fmt.Errorf("portable plan phase %s mutates topology but has no consent", phase.Name)
		}
		if phase.Consent != nil {
			if err := validateEnvironmentName(phase.Consent.Environment); err != nil {
				return Plan{}, fmt.Errorf("portable plan phase %s consent: %w", phase.Name, err)
			}
			if err := validateEnvironmentName(phase.Consent.EqualsTargetEnvironment); err != nil {
				return Plan{}, fmt.Errorf("portable plan phase %s consent target: %w", phase.Name, err)
			}
		}
	}
	return plan, nil
}

// ValidateConsent verifies every phase's invocation-time consent against its
// selected target before the suite builds the CLI or executes a feature.
func (p Plan) ValidateConsent(target Target) error {
	for _, phase := range p.Phases {
		if phase.Consent == nil {
			continue
		}
		if _, targetSupplied := target.Env[phase.Consent.Environment]; targetSupplied {
			return fmt.Errorf(
				"portable plan phase %s consent %s must come from the invocation environment, not the target",
				phase.Name,
				phase.Consent.Environment,
			)
		}
		expected := strings.TrimSpace(target.Env[phase.Consent.EqualsTargetEnvironment])
		if expected == "" {
			return fmt.Errorf(
				"portable plan phase %s consent target env %s must not be empty",
				phase.Name,
				phase.Consent.EqualsTargetEnvironment,
			)
		}
		actual := strings.TrimSpace(os.Getenv(phase.Consent.Environment))
		if actual == "" {
			return fmt.Errorf(
				"portable plan phase %s requires invocation-time consent in %s",
				phase.Name,
				phase.Consent.Environment,
			)
		}
		if actual != expected {
			return fmt.Errorf(
				"portable plan phase %s consent %s does not match target env %s",
				phase.Name,
				phase.Consent.Environment,
				phase.Consent.EqualsTargetEnvironment,
			)
		}
	}
	return nil
}

func resolvePlan(repoRoot, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("portable plan path must not be empty")
	}
	plansRoot := filepath.Join(repoRoot, "tests", "bdd", "plans")
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(plansRoot, path)
	if err != nil {
		return "", fmt.Errorf("resolve portable plan %q: %w", name, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("portable plan %q is outside tests/bdd/plans", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("portable plan %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("portable plan %q is not a regular file", name)
	}
	return path, nil
}

func resolveFeature(repoRoot, name string) (string, error) {
	if name == "" {
		return "", errors.New("feature must not be empty")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("feature %q must be relative to tests/bdd/features", name)
	}
	featuresRoot := filepath.Join(repoRoot, "tests", "bdd", "features")
	path := filepath.Clean(filepath.Join(featuresRoot, name))
	relative, err := filepath.Rel(featuresRoot, path)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", name, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("feature %q is outside tests/bdd/features", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("feature %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("feature %q is not a regular file", name)
	}
	return path, nil
}
