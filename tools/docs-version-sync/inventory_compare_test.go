// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCompareInventorySetDirectoriesExplainsCustomerArtifactChanges(t *testing.T) {
	fromDirectory := t.TempDir()
	toDirectory := t.TempDir()
	writeComparisonInventory(t, fromDirectory, "old.json", comparisonInventory("1.0.0", []resolvedInventoryArtifact{
		comparisonArtifact("removed", "1.0.0", false),
		comparisonArtifact("changed", "1.0.0", true),
	}))
	writeComparisonInventory(t, toDirectory, "new.json", comparisonInventory("2.0.0", []resolvedInventoryArtifact{
		comparisonArtifact("added", "1.0.0", false),
		comparisonArtifact("changed", "2.0.0", false),
	}))

	report, err := compareInventorySetDirectories(fromDirectory, toDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"## Added artifacts\n\n- `added`",
		"## Removed artifacts\n\n- `removed`",
		"`changed` (container-image): reference",
		"requirement `required` -> `optional`",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("comparison report missing %q:\n%s", expected, report)
		}
	}
}

func TestCompareInventorySetDirectoriesDetectsDependencyDisappearance(t *testing.T) {
	fromDirectory := t.TempDir()
	toDirectory := t.TempDir()
	writeComparisonInventory(t, fromDirectory, "old.json", comparisonInventory("1.0.0", []resolvedInventoryArtifact{
		comparisonArtifact("dependency", "1.0.0", true),
	}))
	writeComparisonInventory(t, toDirectory, "new.json", comparisonInventory("2.0.0", []resolvedInventoryArtifact{
		comparisonArtifact("replacement", "1.0.0", true),
	}))

	report, err := compareInventorySetDirectories(fromDirectory, toDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "## Removed artifacts\n\n- `dependency`") {
		t.Fatalf("removed dependency was not reported:\n%s", report)
	}
}

func comparisonInventory(version string, artifacts []resolvedInventoryArtifact) resolvedStackInventory {
	requiredRelease := resolvedInventoryRelease{
		Plane: "control-plane", Name: "api", Required: true,
		Chart: "https://helm.example.test/api", Version: "1.0.0",
	}
	optionalRelease := resolvedInventoryRelease{
		Plane: "control-plane", Name: "addon", Required: false,
		Chart: "https://helm.example.test/addon", Version: "1.0.0",
	}
	requiredChart := resolvedInventoryArtifact{
		Type: "helm-chart", Name: "api", Repository: "https://helm.example.test", Version: "1.0.0",
		Reference: "https://helm.example.test/api@1.0.0",
		Sources:   []resolvedArtifactSource{{Plane: "control-plane", Release: "api"}},
	}
	optionalChart := resolvedInventoryArtifact{
		Type: "helm-chart", Name: "addon", Repository: "https://helm.example.test", Version: "1.0.0",
		Reference: "https://helm.example.test/addon@1.0.0",
		Sources:   []resolvedArtifactSource{{Plane: "control-plane", Release: "addon"}},
	}
	artifacts = append(artifacts, requiredChart, optionalChart)
	sortResolvedInventoryArtifacts(artifacts)
	return resolvedStackInventory{
		SchemaVersion: resolvedStackInventorySchemaVersion,
		Source: stackSourceRelease{
			Version: version, Tag: stackTagPrefix + version, Commit: strings.Repeat("a", 40),
		},
		Releases:  []resolvedInventoryRelease{optionalRelease, requiredRelease},
		Artifacts: artifacts,
	}
}

func comparisonArtifact(name, version string, required bool) resolvedInventoryArtifact {
	release := "addon"
	if required {
		release = "api"
	}
	return resolvedInventoryArtifact{
		Type: "container-image", Name: name, Repository: "registry.example.test/" + name, Version: version,
		Reference: "registry.example.test/" + name + ":" + version,
		Sources:   []resolvedArtifactSource{{Plane: "control-plane", Release: release}},
	}
}

func sortResolvedInventoryArtifacts(artifacts []resolvedInventoryArtifact) {
	sort.Slice(artifacts, func(i, j int) bool {
		return compareResolvedInventoryArtifacts(artifacts[i], artifacts[j]) < 0
	})
}

func writeComparisonInventory(t *testing.T, directory, name string, inventory resolvedStackInventory) {
	t.Helper()
	raw, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
