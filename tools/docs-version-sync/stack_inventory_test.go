// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestStackInventorySpecsMatchReleasedStacks(t *testing.T) {
	for _, spec := range stackInventorySpecs {
		t.Run(spec.Key, func(t *testing.T) {
			release := stackSourceRelease{
				Version: "1.2.3",
				Tag:     spec.TagPrefix + "1.2.3",
				Commit:  strings.Repeat("a", 40),
			}
			if err := validateStackSourceReleaseForSpec(release, spec); err != nil {
				t.Fatal(err)
			}
			got, err := stackInventorySpecByTag(release.Tag)
			if err != nil {
				t.Fatal(err)
			}
			if got != spec {
				t.Fatalf("spec = %#v, want %#v", got, spec)
			}
		})
	}
}

func TestReleaseSetRecordsAllThreeImmutableSources(t *testing.T) {
	inventories := map[string]resolvedStackInventory{}
	for index, spec := range stackInventorySpecs {
		version := fmt.Sprintf("1.%d.0", index)
		inventories[spec.Key] = resolvedStackInventory{Source: stackSourceRelease{
			Version: version,
			Tag:     spec.TagPrefix + version,
			Commit:  strings.Repeat(string(rune('a'+index)), 40),
		}}
	}
	releaseSet, err := releaseSetFromInventories(inventories, "1.4.0", ReleaseSetQualified)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseSet(releaseSet); err != nil {
		t.Fatal(err)
	}
	if releaseSet.Stacks.ControlPlane.InventoryAsset != stackInventorySpecs[0].AssetName ||
		releaseSet.Stacks.ComputePlane.InventoryAsset != stackInventorySpecs[1].AssetName ||
		releaseSet.Stacks.Observability.InventoryAsset != stackInventorySpecs[2].AssetName {
		t.Fatalf("release set assets = %#v", releaseSet.Stacks)
	}
}

func TestReleaseSetStackComparisonIncludesImmutableSourceMetadata(t *testing.T) {
	releaseSet := ReleaseSetMetadata{Stacks: ReleaseSetStacks{
		ControlPlane: StackReleaseMetadata{
			Version: "1.2.3", SourceTag: stackInventorySpecs[0].TagPrefix + "1.2.3",
			SourceCommit: strings.Repeat("a", 40), InventoryAsset: stackInventorySpecs[0].AssetName,
		},
		ComputePlane: StackReleaseMetadata{
			Version: "2.3.4", SourceTag: stackInventorySpecs[1].TagPrefix + "2.3.4",
			SourceCommit: strings.Repeat("b", 40), InventoryAsset: stackInventorySpecs[1].AssetName,
		},
		Observability: StackReleaseMetadata{
			Version: "3.4.5", SourceTag: stackInventorySpecs[2].TagPrefix + "3.4.5",
			SourceCommit: strings.Repeat("c", 40), InventoryAsset: stackInventorySpecs[2].AssetName,
		},
	}}
	if !releaseSet.sameStackReleases(releaseSet) {
		t.Fatal("identical stack metadata did not match")
	}

	changed := releaseSet
	changed.Stacks.Observability.SourceCommit = strings.Repeat("d", 40)
	if releaseSet.sameStackReleases(changed) {
		t.Fatal("release sets with different immutable source commits matched")
	}
}

func TestMergeResolvedStackInventoriesCombinesPeerSources(t *testing.T) {
	inventories := map[string]resolvedStackInventory{
		selfManagedStackKey:   testSeparatedInventory(t, stackInventorySpecs[0], "control-plane", "api"),
		computePlaneStackKey:  testSeparatedInventory(t, stackInventorySpecs[1], "compute-plane", "nvca"),
		observabilityStackKey: testSeparatedInventory(t, stackInventorySpecs[2], "observability", "collector"),
	}
	combined, err := mergeResolvedStackInventories(inventories)
	if err != nil {
		t.Fatal(err)
	}
	if combined.Source != inventories[selfManagedStackKey].Source {
		t.Fatalf("combined source = %#v, want self-managed source %#v", combined.Source, inventories[selfManagedStackKey].Source)
	}
	if len(combined.Releases) != 3 {
		t.Fatalf("combined releases = %d, want 3", len(combined.Releases))
	}
	for _, artifact := range combined.Artifacts {
		if artifact.Name == "shared-runtime" && len(artifact.Sources) != 3 {
			t.Fatalf("shared runtime sources = %#v, want all three stacks", artifact.Sources)
		}
	}
}

func TestMergeResolvedStackInventoriesRejectsVersionConflict(t *testing.T) {
	inventories := map[string]resolvedStackInventory{
		selfManagedStackKey:   testSeparatedInventory(t, stackInventorySpecs[0], "control-plane", "api"),
		computePlaneStackKey:  testSeparatedInventory(t, stackInventorySpecs[1], "compute-plane", "nvca"),
		observabilityStackKey: testSeparatedInventory(t, stackInventorySpecs[2], "observability", "collector"),
	}
	compute := inventories[computePlaneStackKey]
	for index := range compute.Artifacts {
		if compute.Artifacts[index].Name == "shared-runtime" {
			compute.Artifacts[index].Version = "2.0.0"
			compute.Artifacts[index].Reference = "nvcr.io/nvidia/nvcf/shared-runtime:2.0.0"
		}
	}
	inventories[computePlaneStackKey] = compute
	_, err := mergeResolvedStackInventories(inventories)
	if err == nil || !strings.Contains(err.Error(), "compute-plane uses nvcr.io/nvidia/nvcf/shared-runtime:2.0.0") {
		t.Fatalf("merge error = %v, want cross-stack version conflict", err)
	}
}

func testSeparatedInventory(t *testing.T, spec stackInventorySpec, plane, releaseName string) resolvedStackInventory {
	t.Helper()
	source := stackSourceRelease{Version: "1.2.3", Tag: spec.TagPrefix + "1.2.3", Commit: strings.Repeat("a", 40)}
	release := resolvedInventoryRelease{
		Plane: plane, Name: releaseName, Namespace: "nvcf", Required: true,
		Chart:   "https://helm.ngc.nvidia.com/nvidia/nvcf/helm-" + releaseName,
		Version: "1.0.0",
	}
	sourceRef := resolvedArtifactSource{Plane: plane, Release: releaseName}
	inventory := resolvedStackInventory{
		SchemaVersion: resolvedStackInventorySchemaVersion,
		Source:        source,
		Releases:      []resolvedInventoryRelease{release},
		Artifacts: []resolvedInventoryArtifact{
			{
				Type: "helm-chart", Name: "helm-" + releaseName,
				Repository: "https://helm.ngc.nvidia.com/nvidia/nvcf", Version: "1.0.0",
				Reference: release.Chart + "@1.0.0", Sources: []resolvedArtifactSource{sourceRef},
			},
			{
				Type: "container-image", Name: "shared-runtime",
				Repository: "nvcr.io/nvidia/nvcf/shared-runtime", Version: "1.0.0",
				Reference: "nvcr.io/nvidia/nvcf/shared-runtime:1.0.0", Sources: []resolvedArtifactSource{sourceRef},
			},
		},
	}
	if compareResolvedInventoryArtifacts(inventory.Artifacts[0], inventory.Artifacts[1]) > 0 {
		inventory.Artifacts[0], inventory.Artifacts[1] = inventory.Artifacts[1], inventory.Artifacts[0]
	}
	if err := validateResolvedStackInventory(inventory); err != nil {
		t.Fatalf("test inventory is invalid: %v", err)
	}
	return inventory
}
