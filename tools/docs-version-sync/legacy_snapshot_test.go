// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyTestCatalog(t *testing.T, inventory resolvedStackInventory) *Catalog {
	t.Helper()
	base := testCatalog()
	for _, artifact := range inventory.Artifacts {
		kind := ManifestKindServiceImage
		if artifact.Type == "helm-chart" {
			kind = ManifestKindChart
		}
		base.Manifest.Entries = append(base.Manifest.Entries, ManifestEntry{
			ArtifactID: artifact.Name, Plane: ManifestPlaneControl, Kind: kind,
			Requirement: ManifestRequired, Description: "Historical dependency.",
		})
	}
	return base
}

func TestLegacyCatalogExcludesCurrentVersionsAndSupplementalArtifacts(t *testing.T) {
	release := resolvedInventoryTestSource()
	inventory := testCatalogResolvedInventory(t, release)
	base := legacyTestCatalog(t, inventory)
	base.VersionOverrides = []VersionOverride{{Name: "pylon", Type: ArtifactTypeImage, Version: "9.9.9"}}
	base.Publications = []Publication{
		{Name: "pylon", Type: ArtifactTypeImage, Version: "3.4.5", Registry: defaultImageRegistry},
		{Name: "nvca", Type: ArtifactTypeImage, Version: "9.9.9", Registry: defaultImageRegistry},
	}
	snapshot := stackSourceSnapshot{Release: release, Files: map[string][]byte{
		"deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl": testCatalogPinSourceBytes()["deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl"],
	}}
	before, err := MarshalCatalog(base)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildLegacyCatalog(inventory, snapshot, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Artifacts) != len(inventory.Artifacts) || len(catalog.SupplementalArtifacts) != 0 || len(catalog.Outputs) != 0 || len(catalog.VersionOverrides) != 0 || catalog.ReleaseSet != (ReleaseSetMetadata{}) {
		t.Fatalf("legacy catalog included current-release inputs: %+v", catalog)
	}
	pylon, _ := catalog.findArtifact("pylon")
	if pylon.Version != "3.4.5" || catalog.publicationIsPending(pylon) {
		t.Fatalf("exact historical publication was lost: %+v", pylon)
	}
	nvca, _ := catalog.findArtifact("nvca")
	if !catalog.publicationIsPending(nvca) {
		t.Fatal("publication of a different version was inherited")
	}
	after, err := MarshalCatalog(base)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("snapshot mutated its source catalog")
	}
}

func TestLegacyExportRequiresDraftAndChecksFrozenBlocks(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "0.6.2-rc.0", testCatalogPinSources())
	inventory := testCatalogResolvedInventory(t, release)
	raw, err := marshalResolvedStackInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	options := legacySnapshotOptions{
		inventoryPath: filepath.Join(repo, "inventory.json"), catalogPath: filepath.Join(repo, "catalog.yaml"),
		outputPath: filepath.Join(repo, "export"), source: "v0.6.1", version: "0.6.2",
	}
	if err := os.WriteFile(options.inventoryPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCatalog(options.catalogPath, legacyTestCatalog(t, inventory)); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repo, "docs", options.source)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, markers := range map[string][]string{
		"manifest.md":        {"manifest-artifact-registry-paths"},
		"image-mirroring.md": {"image-mirroring-resource-examples", "image-mirroring-stack-snippet"},
	} {
		content := "# Legacy\n"
		for _, marker := range markers {
			content += "\n{/* docs-version-sync:BEGIN " + marker + " */}\nold\n{/* docs-version-sync:END " + marker + " */}\n"
		}
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := exportLegacySnapshot(repo, options); err == nil || !strings.Contains(err.Error(), "--legacy-draft") {
		t.Fatalf("candidate export error = %v", err)
	}
	if _, err := os.Stat(options.outputPath); !os.IsNotExist(err) {
		t.Fatal("rejected candidate created output")
	}
	options.draft = true
	if err := exportLegacySnapshot(repo, options); err != nil {
		t.Fatal(err)
	}
	if err := exportLegacySnapshot(repo, options); err == nil {
		t.Fatal("existing snapshot was overwritten")
	}
	options.catalogPath = filepath.Join(options.outputPath, "catalog.yaml")
	options.check = true
	if err := exportLegacySnapshot(repo, options); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(options.outputPath, "manifest.md")
	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), release.Tag) || !strings.Contains(string(content), "qualification are pending") {
		t.Fatal("candidate provenance and qualification warning missing")
	}
	mirroring, err := os.ReadFile(filepath.Join(options.outputPath, "image-mirroring.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mirroring), "<Warning>") || strings.Contains(string(mirroring), "Publication pending") {
		t.Fatal("image mirroring repeated the manifest's publication warning")
	}
	if strings.Contains(string(mirroring), "ngc registry resource download-version") || !strings.Contains(string(mirroring), "artifact manifest") {
		t.Fatal("unpublished stack must use the manifest reference instead of a download command")
	}
	if err := os.WriteFile(manifest, []byte(strings.ReplaceAll(string(content), "`3.4.5`", "`9.9.9`")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exportLegacySnapshot(repo, options); err == nil {
		t.Fatal("changed frozen manifest was not detected")
	}
}

func TestLegacyExportRejectsSourcePinDrift(t *testing.T) {
	release := resolvedInventoryTestSource()
	inventory := testCatalogResolvedInventory(t, release)
	base := legacyTestCatalog(t, inventory)
	snapshot := stackSourceSnapshot{Release: release, Files: map[string][]byte{
		"deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl": []byte(strings.ReplaceAll(testCatalogPinSources()["deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl"], "1.2.3", "9.9.9")),
	}}
	if _, err := buildLegacyCatalog(inventory, snapshot, base); err == nil || !strings.Contains(err.Error(), "source-pinned") {
		t.Fatalf("source drift error = %v", err)
	}
}
