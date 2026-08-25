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

package live

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Plan separates one optional provider feature from independent smoke
// features. The runner executes the phases explicitly and never relies on
// feature file ordering inside Godog.
type Plan struct {
	ProviderFeature string
	SmokeFeatures   []string
}

// LoadPlan reads feature selection from the operator's environment. Target
// files cannot select executable specifications.
func LoadPlan(repoRoot string) (Plan, error) {
	provider, err := resolveFeature(repoRoot, strings.TrimSpace(os.Getenv("BDD_PROVIDER_FEATURE")))
	if err != nil {
		return Plan{}, fmt.Errorf("BDD_PROVIDER_FEATURE: %w", err)
	}
	rawSmokes := strings.TrimSpace(os.Getenv("BDD_SMOKE_FEATURES"))
	if rawSmokes == "" {
		return Plan{}, errors.New("BDD_SMOKE_FEATURES must name at least one feature")
	}
	seen := make(map[string]struct{})
	if provider != "" {
		seen[provider] = struct{}{}
	}
	var smokes []string
	for _, raw := range strings.Split(rawSmokes, ",") {
		path, err := resolveFeature(repoRoot, strings.TrimSpace(raw))
		if err != nil {
			return Plan{}, fmt.Errorf("BDD_SMOKE_FEATURES: %w", err)
		}
		if path == "" {
			return Plan{}, errors.New("BDD_SMOKE_FEATURES contains an empty entry")
		}
		if _, exists := seen[path]; exists {
			return Plan{}, fmt.Errorf("feature selected more than once: %s", raw)
		}
		seen[path] = struct{}{}
		smokes = append(smokes, path)
	}
	return Plan{ProviderFeature: provider, SmokeFeatures: smokes}, nil
}

func resolveFeature(repoRoot, name string) (string, error) {
	if name == "" {
		return "", nil
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
