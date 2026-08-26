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

package smoke

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plan is a committed, ordered smoke composition. A phase selects one feature
// or a collection that expands into independently restored feature runs.
type Plan struct {
	Version int     `yaml:"version"`
	Name    string  `yaml:"name"`
	Phases  []Phase `yaml:"phases"`
}

// Phase selects exactly one feature or one feature glob. LoadPlan expands
// feature globs into one Phase per file before returning.
type Phase struct {
	Name            string   `yaml:"name"`
	Feature         string   `yaml:"feature,omitempty"`
	Features        string   `yaml:"features,omitempty"`
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

// LoadSelection loads a committed plan or creates an ad hoc selection from
// repeatable safe feature paths. Plans and direct features are mutually
// exclusive. Plan phases own their tag filters; direct runs share one filter.
func LoadSelection(repoRoot, planPath string, featurePaths []string, tags string) (Plan, error) {
	planPath = strings.TrimSpace(planPath)
	if planPath != "" {
		if len(featurePaths) != 0 {
			return Plan{}, errors.New("select a smoke plan or direct features, not both")
		}
		if strings.TrimSpace(tags) != "" {
			return Plan{}, errors.New("direct smoke tags cannot be combined with a plan")
		}
		return LoadPlan(repoRoot, planPath)
	}
	return SelectFeatures(repoRoot, featurePaths, tags)
}

// SelectFeatures creates an ad hoc plan for safe top-level smoke features.
// Operational features in subdirectories require a committed consent plan.
func SelectFeatures(repoRoot string, names []string, tags string) (Plan, error) {
	if len(names) == 0 {
		return Plan{}, errors.New("select a smoke plan or at least one direct feature")
	}
	plan := Plan{Version: 1, Name: "direct"}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		resolved, err := resolveFeature(repoRoot, strings.TrimSpace(name))
		if err != nil {
			return Plan{}, fmt.Errorf("direct smoke feature: %w", err)
		}
		relative, err := filepath.Rel(smokeFeaturesRoot(repoRoot), resolved)
		if err != nil {
			return Plan{}, fmt.Errorf("direct smoke feature %q: %w", name, err)
		}
		if filepath.Dir(relative) != "." {
			return Plan{}, fmt.Errorf(
				"direct smoke feature %q is operational; select it through a committed plan",
				name,
			)
		}
		if _, exists := seen[resolved]; exists {
			return Plan{}, fmt.Errorf("direct smoke feature selected more than once: %s", name)
		}
		seen[resolved] = struct{}{}
		plan.Phases = append(plan.Phases, Phase{
			Name:    strings.TrimSuffix(filepath.Base(resolved), filepath.Ext(resolved)),
			Feature: resolved,
			Tags:    strings.TrimSpace(tags),
		})
	}
	return plan, nil
}

// LoadPlan strictly decodes a committed plan and expands every feature
// collection in stable path order. The plan is selection, not authorization.
func LoadPlan(repoRoot, path string) (Plan, error) {
	resolvedPlanPath, err := resolvePlan(repoRoot, path)
	if err != nil {
		return Plan{}, err
	}
	raw, err := os.ReadFile(resolvedPlanPath)
	if err != nil {
		return Plan{}, fmt.Errorf("read smoke plan: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode smoke plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Plan{}, errors.New("decode smoke plan: multiple YAML documents are not supported")
		}
		return Plan{}, fmt.Errorf("decode smoke plan: %w", err)
	}
	if plan.Version != 1 {
		return Plan{}, fmt.Errorf("smoke plan version must be 1, got %d", plan.Version)
	}
	plan.Name = strings.TrimSpace(plan.Name)
	if plan.Name == "" {
		return Plan{}, errors.New("smoke plan field name must not be empty")
	}
	if len(plan.Phases) == 0 {
		return Plan{}, errors.New("smoke plan must contain at least one phase")
	}

	specs := plan.Phases
	plan.Phases = nil
	seenNames := make(map[string]struct{})
	seenFeatures := make(map[string]struct{})
	for index := range specs {
		spec := specs[index]
		spec.Name = strings.TrimSpace(spec.Name)
		if spec.Name == "" {
			return Plan{}, fmt.Errorf("smoke plan phase %d name must not be empty", index+1)
		}
		if err := validatePhasePolicy(spec); err != nil {
			return Plan{}, fmt.Errorf("smoke plan phase %s: %w", spec.Name, err)
		}
		features, err := resolvePhaseFeatures(repoRoot, spec)
		if err != nil {
			return Plan{}, fmt.Errorf("smoke plan phase %s: %w", spec.Name, err)
		}
		for _, feature := range features {
			phase := spec
			phase.Feature = feature
			phase.Features = ""
			if len(features) > 1 {
				relative, err := filepath.Rel(smokeFeaturesRoot(repoRoot), feature)
				if err != nil {
					return Plan{}, fmt.Errorf("smoke plan phase %s: %w", spec.Name, err)
				}
				suffix := strings.TrimSuffix(filepath.ToSlash(relative), filepath.Ext(relative))
				phase.Name += "-" + strings.ReplaceAll(suffix, "/", "-")
			}
			if _, exists := seenNames[phase.Name]; exists {
				return Plan{}, fmt.Errorf("smoke plan phase name selected more than once: %s", phase.Name)
			}
			if _, exists := seenFeatures[feature]; exists {
				return Plan{}, fmt.Errorf("smoke feature selected more than once: %s", feature)
			}
			seenNames[phase.Name] = struct{}{}
			seenFeatures[feature] = struct{}{}
			plan.Phases = append(plan.Phases, phase)
		}
	}
	return plan, nil
}

func validatePhasePolicy(phase Phase) error {
	hasFeature := strings.TrimSpace(phase.Feature) != ""
	hasFeatures := strings.TrimSpace(phase.Features) != ""
	if hasFeature == hasFeatures {
		return errors.New("set exactly one of feature or features")
	}
	if phase.MutatesTopology && phase.Consent == nil {
		return errors.New("mutates topology but has no consent")
	}
	if phase.Consent == nil {
		return nil
	}
	if err := validateEnvironmentName(phase.Consent.Environment); err != nil {
		return fmt.Errorf("consent: %w", err)
	}
	if err := validateEnvironmentName(phase.Consent.EqualsTargetEnvironment); err != nil {
		return fmt.Errorf("consent target: %w", err)
	}
	return nil
}

func resolvePhaseFeatures(repoRoot string, phase Phase) ([]string, error) {
	if strings.TrimSpace(phase.Feature) != "" {
		resolved, err := resolveFeature(repoRoot, strings.TrimSpace(phase.Feature))
		if err != nil {
			return nil, err
		}
		return []string{resolved}, nil
	}
	return resolveFeatureGlob(repoRoot, strings.TrimSpace(phase.Features))
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
				"smoke plan phase %s consent %s must come from the invocation environment, not the target",
				phase.Name,
				phase.Consent.Environment,
			)
		}
		expected := strings.TrimSpace(target.Env[phase.Consent.EqualsTargetEnvironment])
		if expected == "" {
			return fmt.Errorf(
				"smoke plan phase %s consent target env %s must not be empty",
				phase.Name,
				phase.Consent.EqualsTargetEnvironment,
			)
		}
		actual := strings.TrimSpace(os.Getenv(phase.Consent.Environment))
		if actual == "" {
			return fmt.Errorf(
				"smoke plan phase %s requires invocation-time consent in %s",
				phase.Name,
				phase.Consent.Environment,
			)
		}
		if actual != expected {
			return fmt.Errorf(
				"smoke plan phase %s consent %s does not match target env %s",
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
		return "", errors.New("smoke plan path must not be empty")
	}
	plansRoot := filepath.Join(repoRoot, "tests", "bdd", "smoke", "plans")
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(plansRoot, path)
	if err != nil {
		return "", fmt.Errorf("resolve smoke plan %q: %w", name, err)
	}
	if outside(relative) {
		return "", fmt.Errorf("smoke plan %q is outside tests/bdd/smoke/plans", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("smoke plan %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("smoke plan %q is not a regular file", name)
	}
	return path, nil
}

func resolveFeature(repoRoot, name string) (string, error) {
	if name == "" {
		return "", errors.New("feature must not be empty")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("feature %q must be relative to tests/bdd/smoke/features", name)
	}
	root := smokeFeaturesRoot(repoRoot)
	path := filepath.Clean(filepath.Join(root, name))
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", name, err)
	}
	if outside(relative) {
		return "", fmt.Errorf("feature %q is outside tests/bdd/smoke/features", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("feature %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || filepath.Ext(path) != ".feature" {
		return "", fmt.Errorf("feature %q is not a regular .feature file", name)
	}
	return path, nil
}

func resolveFeatureGlob(repoRoot, pattern string) ([]string, error) {
	if pattern == "" {
		return nil, errors.New("features glob must not be empty")
	}
	if filepath.IsAbs(pattern) {
		return nil, fmt.Errorf("features glob %q must be relative to tests/bdd/smoke/features", pattern)
	}
	if _, err := filepath.Match(pattern, "placeholder"); err != nil {
		return nil, fmt.Errorf("invalid features glob %q: %w", pattern, err)
	}
	root := smokeFeaturesRoot(repoRoot)
	cleanPattern := filepath.Clean(pattern)
	relative, err := filepath.Rel(root, filepath.Join(root, cleanPattern))
	if err != nil {
		return nil, fmt.Errorf("resolve features glob %q: %w", pattern, err)
	}
	if outside(relative) {
		return nil, fmt.Errorf("features glob %q is outside tests/bdd/smoke/features", pattern)
	}
	matches, err := filepath.Glob(filepath.Join(root, cleanPattern))
	if err != nil {
		return nil, fmt.Errorf("resolve features glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("features glob %q matched no files", pattern)
	}
	resolved := make([]string, 0, len(matches))
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			return nil, fmt.Errorf("features glob %q: %w", pattern, err)
		}
		if info.Mode().IsRegular() && filepath.Ext(match) == ".feature" {
			resolved = append(resolved, match)
		}
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("features glob %q matched no regular .feature files", pattern)
	}
	sort.Strings(resolved)
	return resolved, nil
}

func smokeFeaturesRoot(repoRoot string) string {
	return filepath.Join(repoRoot, "tests", "bdd", "smoke", "features")
}

func outside(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
