// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// legacySnapshotOptions selects immutable inputs and a separate export directory.
type legacySnapshotOptions struct {
	inventoryPath string
	catalogPath   string
	outputPath    string
	source        string
	version       string
	draft         bool
	check         bool
}

// buildLegacyCatalog retains only inventory artifacts and matching publication metadata.
// Current supplemental artifacts and version overrides must never enter an older release.
func buildLegacyCatalog(inventory resolvedStackInventory, snapshot stackSourceSnapshot, base *Catalog) (*Catalog, error) {
	if err := validateResolvedStackInventory(inventory); err != nil {
		return nil, err
	}
	if snapshot.Release != inventory.Source {
		return nil, fmt.Errorf("legacy source snapshot does not match inventory")
	}
	artifacts, err := catalogArtifactsFromResolvedStackInventory(inventory, base)
	if err != nil {
		return nil, err
	}
	catalog := newCatalogFromArtifacts(inventory.Source.Version, artifacts)
	for key, registry := range base.Registries {
		catalog.Registries[key] = registry
	}
	catalog.Publications = append([]Publication(nil), base.Publications...)
	catalog.Denylist = append([]DenylistEntry(nil), base.Denylist...)
	catalog.Stack.SourceTag = inventory.Source.Tag
	catalog.Stack.SourceCommit = inventory.Source.Commit
	catalog.Stack.PinSources = []string{"deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl"}
	pins, err := effectiveStackPinsForSources(catalog.Stack.PinSources)
	if err != nil {
		return nil, err
	}
	catalog.Stack.PinSourceDigest, err = pinSourceDigest(snapshot.Files, pins)
	if err != nil {
		return nil, err
	}
	versions, err := extractEffectiveStackPins(snapshot.Files, pins)
	if err != nil {
		return nil, err
	}
	for _, pin := range pins {
		artifact, exists := catalog.findArtifactByNameAndType(pin.artifact, pin.artifactType)
		if !exists || artifact.Version != versions[pin.artifact] {
			return nil, fmt.Errorf("legacy inventory must contain source-pinned %s %s", pin.artifact, versions[pin.artifact])
		}
	}
	catalog.Outputs = nil
	catalog.Manifest.Entries = nil
	for _, entry := range base.Manifest.Entries {
		if entry.ArtifactID == "" {
			continue
		}
		if _, exists := catalog.findArtifact(entry.ArtifactID); exists {
			catalog.Manifest.Entries = append(catalog.Manifest.Entries, entry)
		}
	}
	retainCurrentPublications(catalog)
	catalog.reconcilePublicationPending()
	catalog.pruneUnusedRegistries()
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	if _, err := resolveManifestEntries(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func exportLegacySnapshot(repoRoot string, options legacySnapshotOptions) error {
	if !validStableStackVersion(options.version) {
		return fmt.Errorf("legacy documentation version must use %s", documentationVersionFormat)
	}
	if !strings.HasPrefix(options.source, "v") || !validStableStackVersion(strings.TrimPrefix(options.source, "v")) {
		return fmt.Errorf("legacy source must name a frozen tree such as v0.6.1")
	}
	raw, err := os.ReadFile(options.inventoryPath)
	if err != nil {
		return err
	}
	var inventory resolvedStackInventory
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return fmt.Errorf("decode legacy inventory: %w", err)
	}
	if err := validateResolvedStackInventory(inventory); err != nil {
		return err
	}
	if inventory.Source.Version != options.version && !(options.draft && strings.HasPrefix(inventory.Source.Version, options.version+"-rc.")) {
		return fmt.Errorf("inventory version %s does not match %s; candidates require --legacy-draft", inventory.Source.Version, options.version)
	}
	if !strings.HasPrefix(inventory.Source.Tag, "deploy/stacks/self-managed/v") {
		return fmt.Errorf("legacy inventory must belong to the self-managed stack")
	}
	base, err := LoadCatalog(options.catalogPath)
	if err != nil {
		return err
	}
	snapshot, err := loadStackSourceSnapshot(repoRoot, inventory.Source, []string{"deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl"})
	if err != nil {
		return err
	}
	catalog, err := buildLegacyCatalog(inventory, snapshot, base)
	if err != nil {
		return err
	}
	if options.check {
		equal, err := catalogsEqual(base, catalog)
		if err != nil {
			return err
		}
		if !equal {
			return fmt.Errorf("%w: legacy catalog differs from the selected inventory", ErrCheckFailed)
		}
	}
	pages, err := renderLegacyPages(repoRoot, options, catalog)
	if err != nil {
		return err
	}
	if options.check {
		return nil
	}
	if _, err := os.Lstat(options.outputPath); !os.IsNotExist(err) {
		return fmt.Errorf("legacy output directory must not exist: %s", options.outputPath)
	}
	if err := os.MkdirAll(options.outputPath, 0o755); err != nil {
		return err
	}
	if err := WriteCatalog(filepath.Join(options.outputPath, "catalog.yaml"), catalog); err != nil {
		return err
	}
	for name, content := range pages {
		if err := os.WriteFile(filepath.Join(options.outputPath, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func renderLegacyPages(repoRoot string, options legacySnapshotOptions, catalog *Catalog) (map[string]string, error) {
	pages := map[string]string{}
	for _, output := range []OutputFile{
		{Path: "manifest.md", Blocks: []OutputBlock{{Marker: "manifest-artifact-registry-paths", Renderer: "manifest-artifact-registry-paths"}}},
		{Path: "image-mirroring.md", Blocks: []OutputBlock{
			{Marker: "image-mirroring-resource-examples", Renderer: "image-mirroring-resource-examples"},
			{Marker: "image-mirroring-stack-snippet", Renderer: "image-mirroring-stack-snippet"},
		}},
	} {
		source := filepath.Join(repoRoot, "docs", options.source, output.Path)
		if options.check {
			source = filepath.Join(options.outputPath, output.Path)
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			return nil, err
		}
		content := string(raw)
		for _, block := range output.Blocks {
			rendered, err := Render(block.Renderer, catalog)
			if err != nil {
				return nil, err
			}
			updated, changed, err := ReplaceMarkedBlock(content, block.Marker, rendered)
			if err != nil {
				return nil, err
			}
			if options.check && changed {
				return nil, fmt.Errorf("%w: %s block %s", ErrCheckFailed, output.Path, block.Marker)
			}
			content = updated
		}
		if options.draft && !options.check && output.Path == "manifest.md" {
			title, body, _ := strings.Cut(content, "\n")
			content = title + "\n\n<Warning>\nDraft for " + options.version + ". Inventory source: `" + catalog.Stack.SourceTag + "`.\nStable release publication and live upgrade qualification are pending.\nDo not use this draft as a qualified upgrade procedure.\n</Warning>\n" + body
		}
		pages[output.Path] = content
	}
	return pages, nil
}
