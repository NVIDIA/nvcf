// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const umbrellaDocsInventoryDirectory = "docs/umbrella-inventories"

// umbrellaDocsInputs binds schema-2 catalog metadata and original inventories.
type umbrellaDocsInputs struct {
	ProductCommit string
	Inventories   map[string][]byte
}

func umbrellaDocsPreparationInputs(root string, release editionRelease, manifest *umbrellaManifest, inventoryDir string, remote bool) (umbrellaDocsInputs, error) {
	inputs := umbrellaDocsInputs{ProductCommit: release.Umbrella.ProductCommit}
	if manifest.SchemaVersion != 2 {
		if inventoryDir != "" {
			return inputs, fmt.Errorf("--inventories is only supported for schema 2 docs preparation")
		}
		return inputs, nil
	}
	inputs.Inventories = map[string][]byte{}
	if inventoryDir == "" {
		if !remote {
			return inputs, fmt.Errorf("schema 2 local docs preparation requires --inventories")
		}
		publisher := umbrellaPublisher{client: &http.Client{Timeout: 2 * time.Minute}, api: defaultGitHubAPIURL, repository: defaultGitHubOwner + "/" + defaultGitHubRepo}
		assets, err := readPublishedDocsProduct(root, release, publisher)
		if err != nil {
			return inputs, err
		}
		for _, stack := range manifest.Stacks {
			inputs.Inventories[stack.InventoryAsset] = assets[stack.InventoryAsset]
		}
		return inputs, nil
	}
	for _, stack := range manifest.Stacks {
		data, err := os.ReadFile(filepath.Join(inventoryDir, stack.InventoryAsset))
		if err != nil {
			return inputs, err
		}
		inputs.Inventories[stack.InventoryAsset] = data
	}
	return inputs, nil
}

func umbrellaDocsRevisionInputs(root string, release editionRelease, manifest *umbrellaManifest) (umbrellaDocsInputs, error) {
	inputs := umbrellaDocsInputs{ProductCommit: release.Umbrella.ProductCommit}
	if manifest.SchemaVersion != 2 {
		return inputs, nil
	}
	inputs.Inventories = map[string][]byte{}
	for _, stack := range manifest.Stacks {
		data, err := gitOutput(root, "show", release.Commit+":"+umbrellaDocsInventoryDirectory+"/"+stack.InventoryAsset)
		if err != nil {
			return inputs, fmt.Errorf("docs revision requires original inventory %s: %w", stack.InventoryAsset, err)
		}
		inputs.Inventories[stack.InventoryAsset] = data
	}
	return inputs, nil
}

func schema2UmbrellaDocsCatalog(root string, manifest *umbrellaManifest, inputs umbrellaDocsInputs) (*Catalog, error) {
	if !fullLowercaseCommitSHARe.MatchString(inputs.ProductCommit) {
		return nil, fmt.Errorf("schema 2 docs require the immutable product tag commit")
	}
	inventories, err := validateUmbrellaSourcesWithReader(root, manifest, func(name string) ([]byte, error) {
		data, ok := inputs.Inventories[name]
		if !ok {
			return nil, fmt.Errorf("schema 2 docs require original inventory %s", name)
		}
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	// Schema 2 source_commit supplies provenance and license files, not the docs
	// catalog. Metadata is frozen with the published product, not current main.
	base, err := catalogAtCommit(root, inputs.ProductCommit)
	if err != nil {
		return nil, err
	}
	selectedUmbrellaDocsMetadata(base, inventories)
	base.Compatibility = manifest.catalog().Compatibility
	base.ReleaseSet = ReleaseSetMetadata{}
	base.DocsEdition = nil
	catalog, err := buildCatalogFromStackInventories(root, inventories, base)
	if err != nil {
		return nil, err
	}
	catalog.Outputs = base.Outputs
	catalog.Compatibility = manifest.catalog().Compatibility
	return catalog, nil
}

// selectedUmbrellaDocsMetadata retains classifications for selected artifacts
// and explicitly independent shared resources. It cannot select runtime pins.
func selectedUmbrellaDocsMetadata(base *Catalog, inventories map[string]resolvedStackInventory) {
	selected := map[string]bool{}
	for _, inventory := range inventories {
		for _, artifact := range inventory.Artifacts {
			kind, _ := catalogArtifactType(artifact.Type)
			selected[artifactNameAndTypeIdentityKey(artifact.Name, kind)] = true
		}
	}
	for _, pin := range effectiveStackPins {
		selected[artifactNameAndTypeIdentityKey(pin.artifact, pin.artifactType)] = true
	}
	for _, spec := range stackInventorySpecs {
		selected[artifactNameAndTypeIdentityKey(spec.ResourceName, ArtifactTypeResource)] = true
	}
	shared := map[string]bool{}
	for _, entry := range base.Manifest.Entries {
		if entry.Plane == ManifestPlaneShared && entry.Kind == ManifestKindResource {
			shared[entry.ArtifactID] = true
		}
	}
	independent := map[string]bool{}
	for _, artifact := range base.SupplementalArtifacts {
		key := artifactNameAndTypeKey(artifact)
		if artifact.Type == ArtifactTypeResource && len(artifact.Stacks) == 0 && shared[artifact.catalogKey()] && !selected[key] {
			independent[key] = true
		}
	}
	classified := map[string]bool{base.Stack.Name: true}
	filter := func(artifacts []Artifact) []Artifact {
		retained := make([]Artifact, 0, len(artifacts))
		for _, artifact := range artifacts {
			key := artifactNameAndTypeKey(artifact)
			if selected[key] || independent[key] {
				retained = append(retained, artifact)
				classified[artifact.catalogKey()] = true
			}
		}
		return retained
	}
	base.Artifacts = filter(base.Artifacts)
	base.SupplementalArtifacts = filter(base.SupplementalArtifacts)
	entries := make([]ManifestEntry, 0, len(base.Manifest.Entries))
	for _, entry := range base.Manifest.Entries {
		if entry.ArtifactID == "" || classified[entry.ArtifactID] {
			entries = append(entries, entry)
		}
	}
	base.Manifest.Entries = entries
	overrides := make([]VersionOverride, 0, len(base.VersionOverrides))
	for _, override := range base.VersionOverrides {
		if independent[artifactNameAndTypeIdentityKey(override.Name, override.Type)] {
			overrides = append(overrides, override)
		}
	}
	base.VersionOverrides = overrides
}
