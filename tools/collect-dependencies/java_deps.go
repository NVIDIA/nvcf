// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const javaDescriptorName = "bazel-java-ci.json"

type javaComponent struct {
	ID            string
	Path          string
	InventoryPath string
	Target        string
}

type javaRuntimeInventory struct {
	Dependencies []javaRuntimeDependency `json:"dependencies"`
}

type javaRuntimeDependency struct {
	Coordinate string   `json:"coordinate"`
	Licenses   []string `json:"licenses"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
}

type mergedJavaDependency struct {
	Coordinate string
	Licenses   map[string]struct{}
	Names      map[string]struct{}
	URLs       map[string]struct{}
}

func discoverJavaComponents(root string) ([]javaComponent, error) {
	srcRoot := filepath.Join(root, "src")
	if st, err := os.Stat(srcRoot); err != nil || !st.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return nil, fmt.Errorf("discover Java components under %s: %w", srcRoot, err)
	}

	var components []javaComponent
	ids := map[string]string{}
	err := walkImportRoot(srcRoot, func(path string, d fs.DirEntry) error {
		if d.IsDir() || d.Name() != javaDescriptorName {
			return nil
		}
		component, err := parseJavaComponentDescriptor(root, path)
		if err != nil {
			return err
		}
		if previous, ok := ids[component.ID]; ok {
			return fmt.Errorf(
				"duplicate Java component id %q in %s and %s",
				component.ID,
				previous,
				path,
			)
		}
		ids[component.ID] = path
		components = append(components, component)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(components, func(i, j int) bool {
		return components[i].Path < components[j].Path
	})
	if len(components) == 0 {
		return nil, fmt.Errorf(
			"no %s descriptors found under %s; Java dependency collection cannot continue",
			javaDescriptorName,
			srcRoot,
		)
	}
	return components, nil
}

func parseJavaComponentDescriptor(root, descriptorPath string) (javaComponent, error) {
	raw, err := os.ReadFile(descriptorPath)
	if err != nil {
		return javaComponent{}, fmt.Errorf("read Java descriptor %s: %w", descriptorPath, err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return javaComponent{}, fmt.Errorf("parse Java descriptor %s: %w", descriptorPath, err)
	}

	id, err := requiredDescriptorString(document, "id")
	if err != nil {
		return javaComponent{}, fmt.Errorf("%s: %w", descriptorPath, err)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(id) {
		return javaComponent{}, fmt.Errorf(
			"%s: id must contain lowercase letters, digits, or hyphens, got %q",
			descriptorPath,
			id,
		)
	}
	componentKind, err := requiredDescriptorString(document, "component_kind")
	if err != nil {
		return javaComponent{}, fmt.Errorf("%s: %w", descriptorPath, err)
	}
	if componentKind != "java-framework" && componentKind != "java-service" {
		return javaComponent{}, fmt.Errorf(
			"%s: component_kind must be java-framework or java-service, got %q",
			descriptorPath,
			componentKind,
		)
	}
	ciLane, err := requiredDescriptorString(document, "ci_lane")
	if err != nil {
		return javaComponent{}, fmt.Errorf("%s: %w", descriptorPath, err)
	}
	if ciLane != "build-container" && ciLane != "docker-host" {
		return javaComponent{}, fmt.Errorf(
			"%s: ci_lane must be build-container or docker-host, got %q",
			descriptorPath,
			ciLane,
		)
	}
	if rawTestsSkip, ok := document["tests_skip"]; !ok {
		return javaComponent{}, fmt.Errorf("%s: missing required field tests_skip", descriptorPath)
	} else {
		var testsSkip bool
		if err := json.Unmarshal(rawTestsSkip, &testsSkip); err != nil {
			return javaComponent{}, fmt.Errorf("%s: tests_skip must be boolean", descriptorPath)
		}
	}

	componentDir := filepath.Dir(descriptorPath)
	relativePath, err := filepath.Rel(root, componentDir)
	if err != nil {
		return javaComponent{}, fmt.Errorf("resolve Java component path for %s: %w", descriptorPath, err)
	}
	relativePath = filepath.ToSlash(relativePath)
	if relativePath == "." || strings.HasPrefix(relativePath, "../") {
		return javaComponent{}, fmt.Errorf(
			"Java descriptor %s is outside repository root %s",
			descriptorPath,
			root,
		)
	}
	return javaComponent{
		ID:            id,
		Path:          relativePath,
		InventoryPath: filepath.FromSlash(relativePath + "/runtime_inventory.json"),
		Target:        "//" + relativePath + ":runtime_inventory.json",
	}, nil
}

func requiredDescriptorString(document map[string]json.RawMessage, field string) (string, error) {
	raw, ok := document[field]
	if !ok {
		return "", fmt.Errorf("missing required field %s", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", field)
	}
	return strings.TrimSpace(value), nil
}

func collectJavaInventoryDependencies(root string) (map[string]mergedJavaDependency, int, error) {
	components, err := discoverJavaComponents(root)
	if err != nil {
		return nil, 0, err
	}
	bazelBin, err := materializeJavaInventories(root, components)
	if err != nil {
		return nil, 0, err
	}

	dependencies := map[string]mergedJavaDependency{}
	for _, component := range components {
		inventoryPath := filepath.Join(bazelBin, component.InventoryPath)
		inventory, err := readJavaRuntimeInventory(inventoryPath)
		if err != nil {
			return nil, 0, fmt.Errorf(
				"read Java runtime inventory for component %s from %s: %w",
				component.ID,
				inventoryPath,
				err,
			)
		}
		for _, dependency := range inventory.Dependencies {
			mergeJavaDependency(dependencies, dependency)
		}
	}
	return dependencies, len(components), nil
}

func materializeJavaInventories(root string, components []javaComponent) (string, error) {
	if override := strings.TrimSpace(os.Getenv("COLLECT_DEPS_JAVA_INVENTORY_ROOT")); override != "" {
		return override, nil
	}

	bazel := strings.TrimSpace(os.Getenv("COLLECT_DEPS_BAZEL"))
	if bazel == "" {
		bazel = "bazel"
	}
	startupArgs := javaBazelStartupArgs()
	targets := make([]string, 0, len(components))
	for _, component := range components {
		targets = append(targets, component.Target)
	}
	buildArgs := append(append([]string{}, startupArgs...), "build")
	buildArgs = append(buildArgs, targets...)
	fmt.Fprintf(
		os.Stderr,
		"collect-dependencies: building %d Java runtime inventories with Bazel\n",
		len(targets),
	)
	stdout, stderr, err := runCommand(root, nil, javaBazelTimeout, bazel, buildArgs...)
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return "", fmt.Errorf(
				"build Java runtime inventories: %q is not available; install Bazelisk as bazel",
				bazel,
			)
		}
		return "", fmt.Errorf(
			"build Java runtime inventories with %s: %w\n%s",
			bazel,
			err,
			commandFailureOutput(stdout, stderr),
		)
	}

	infoArgs := append(append([]string{}, startupArgs...), "info", "bazel-bin")
	stdout, stderr, err = runCommand(root, nil, javaBazelTimeout, bazel, infoArgs...)
	if err != nil {
		return "", fmt.Errorf(
			"locate bazel-bin with %s: %w\n%s",
			bazel,
			err,
			commandFailureOutput(stdout, stderr),
		)
	}
	bazelBin := strings.TrimSpace(stdout)
	if bazelBin == "" {
		return "", fmt.Errorf("%s info bazel-bin returned an empty path", bazel)
	}
	return bazelBin, nil
}

func javaBazelStartupArgs() []string {
	outputRoot := strings.TrimSpace(os.Getenv("BAZEL_OUTPUT_USER_ROOT"))
	if outputRoot == "" {
		return nil
	}
	return []string{"--output_user_root=" + outputRoot}
}

func commandFailureOutput(stdout, stderr string) string {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = strings.TrimSpace(stdout)
	}
	if len(message) > 4000 {
		message = message[len(message)-4000:]
	}
	return message
}

func readJavaRuntimeInventory(path string) (javaRuntimeInventory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return javaRuntimeInventory{}, err
	}
	var inventory javaRuntimeInventory
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return javaRuntimeInventory{}, fmt.Errorf("parse JSON: %w", err)
	}
	if inventory.Dependencies == nil {
		return javaRuntimeInventory{}, fmt.Errorf("missing dependencies array")
	}
	for i, dependency := range inventory.Dependencies {
		if strings.TrimSpace(dependency.Coordinate) == "" {
			return javaRuntimeInventory{}, fmt.Errorf("dependency %d has an empty coordinate", i)
		}
	}
	return inventory, nil
}

func mergeJavaDependency(
	dependencies map[string]mergedJavaDependency,
	dependency javaRuntimeDependency,
) {
	coordinate := strings.TrimSpace(dependency.Coordinate)
	merged, ok := dependencies[coordinate]
	if !ok {
		merged = mergedJavaDependency{
			Coordinate: coordinate,
			Licenses:   map[string]struct{}{},
			Names:      map[string]struct{}{},
			URLs:       map[string]struct{}{},
		}
	}
	for _, license := range dependency.Licenses {
		if license = strings.TrimSpace(license); license != "" {
			merged.Licenses[license] = struct{}{}
		}
	}
	if name := strings.TrimSpace(dependency.Name); name != "" {
		merged.Names[name] = struct{}{}
	}
	if rawURL := strings.TrimSpace(dependency.URL); rawURL != "" {
		merged.URLs[rawURL] = struct{}{}
	}
	dependencies[coordinate] = merged
}

func buildJavaInventoryRows(
	dependencies map[string]mergedJavaDependency,
) []dependencyRow {
	rows := make([]dependencyRow, 0, len(dependencies))
	for _, coordinate := range sortedMapKeys(dependencies) {
		dependency := dependencies[coordinate]
		licenses := sortedKeys(dependency.Licenses)
		license := "_(runtime inventory has no license metadata)_"
		if len(licenses) > 0 {
			license = strings.Join(licenses, " / ")
		}
		rows = append(rows, dependencyRow{
			Language: "Java",
			SortKey:  strings.ToLower(coordinate),
			Spec:     javaDependencySpec(dependency),
			License:  license,
		})
	}
	return rows
}

func javaDependencySpec(dependency mergedJavaDependency) string {
	spec := "`" + dependency.Coordinate + "`"
	names := sortedKeys(dependency.Names)
	urls := sortedKeys(dependency.URLs)
	details := []string{}
	if len(names) > 0 {
		details = append(details, strings.Join(names, " / "))
	}
	if len(urls) > 0 {
		details = append(details, strings.Join(urls, " / "))
	}
	if len(details) > 0 {
		spec += " (" + strings.Join(details, "; ") + ")"
	}
	return spec
}
