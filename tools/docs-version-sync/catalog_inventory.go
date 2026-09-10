// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
)

func updateCatalogFromGitHub(repoRoot, sourceRef string, base *Catalog) (*Catalog, error) {
	client := newGitHubClientFromEnvironment()
	release, err := client.resolveStackSourceRelease(sourceRef)
	if err != nil {
		return nil, err
	}
	inventory, err := client.resolvedStackInventory(release)
	if err != nil {
		return nil, err
	}
	sourcePaths := effectiveStackPinSourcePaths(effectiveStackPins)
	snapshot, err := loadStackSourceSnapshot(repoRoot, release, sourcePaths)
	if err != nil {
		return nil, err
	}
	return buildCatalogFromResolvedStackInventory(inventory, snapshot, base)
}

func buildCatalogFromResolvedStackInventory(inventory resolvedStackInventory, snapshot stackSourceSnapshot, base *Catalog) (*Catalog, error) {
	if err := validateResolvedStackInventory(inventory); err != nil {
		return nil, err
	}
	if snapshot.Release != inventory.Source {
		return nil, fmt.Errorf("stack source snapshot is %+v, want inventory source %+v", snapshot.Release, inventory.Source)
	}
	artifacts, err := catalogArtifactsFromResolvedStackInventory(inventory, base)
	if err != nil {
		return nil, err
	}
	catalog := BuildCatalogFromArtifactsWithBase(inventory.Source.Version, artifacts, base)
	retainIndependentResourceArtifacts(catalog)
	if base != nil {
		catalog.Stack.Name = base.Stack.Name
		catalog.Stack.Registry = base.Stack.Registry
		catalog.Registries[base.Stack.Registry] = base.Registries[base.Stack.Registry]
	}
	catalog.Stack.SourceVersion = inventory.Source.Version
	catalog.Stack.SourceTag = inventory.Source.Tag
	catalog.Stack.SourceCommit = inventory.Source.Commit
	catalog.Stack.PinSources = effectiveStackPinSourcePaths(effectiveStackPins)
	catalog.Stack.PinSourceDigest, err = pinSourceDigest(snapshot.Files, effectiveStackPins)
	if err != nil {
		return nil, err
	}
	if err := validateCatalogEffectiveStackPins(catalog, snapshot.Files); err != nil {
		return nil, err
	}

	markPublicationPendingUnlessExact(catalog, catalog.stackArtifact())
	if setArtifactVersion(catalog, computeStackResourceName, inventory.Source.Version) {
		compute, _ := catalog.findArtifact(computeStackResourceName)
		markPublicationPendingUnlessExact(catalog, compute)
	}
	catalog.reconcilePublicationPending()
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func catalogArtifactsFromResolvedStackInventory(inventory resolvedStackInventory, base *Catalog) ([]Artifact, error) {
	denylistSource := base
	if denylistSource == nil {
		denylistSource = &Catalog{}
	}
	denylist := denylistSource.DenylistMap()
	artifacts := make([]Artifact, 0, len(inventory.Artifacts))
	for _, resolved := range inventory.Artifacts {
		if _, denied := denylist[resolved.Name]; denied {
			continue
		}
		artifactType, err := catalogArtifactType(resolved.Type)
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", resolved.Reference, err)
		}
		if resolved.Version == "" {
			return nil, fmt.Errorf("artifact %s has no tag version representable in the documentation catalog", resolved.Reference)
		}
		artifacts = append(artifacts, Artifact{
			Name:     resolved.Name,
			Type:     artifactType,
			Registry: defaultStackRegistry,
			Version:  resolved.Version,
			Digest:   resolved.Digest,
		})
	}
	preserveCatalogArtifactIdentity(artifacts, base)
	assignCatalogArtifactIDs(artifacts)
	return artifacts, nil
}

func catalogArtifactType(resolvedType string) (ArtifactType, error) {
	switch resolvedType {
	case "container-image":
		return ArtifactTypeImage, nil
	case "helm-chart":
		return ArtifactTypeChart, nil
	default:
		return "", fmt.Errorf("unsupported resolved inventory type %q", resolvedType)
	}
}

func preserveCatalogArtifactIdentity(artifacts []Artifact, base *Catalog) {
	if base == nil {
		return
	}
	candidates := make([]Artifact, 0, len(base.Artifacts)+len(base.SupplementalArtifacts))
	candidates = append(candidates, base.Artifacts...)
	candidates = append(candidates, base.SupplementalArtifacts...)
	for i := range artifacts {
		var matches []Artifact
		for _, candidate := range candidates {
			if candidate.Name == artifacts[i].Name && candidate.Type == artifacts[i].Type {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			continue
		}
		artifacts[i].ID = matches[0].ID
	}
}

func retainIndependentResourceArtifacts(catalog *Catalog) {
	denylist := catalog.DenylistMap()
	resources := catalog.SupplementalArtifacts[:0]
	for _, artifact := range catalog.SupplementalArtifacts {
		if artifact.Type != ArtifactTypeResource {
			continue
		}
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		resources = append(resources, artifact)
	}
	catalog.SupplementalArtifacts = resources
}

func assignCatalogArtifactIDs(artifacts []Artifact) {
	counts := make(map[string]int, len(artifacts))
	used := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		counts[artifact.Name]++
		if artifact.ID != "" {
			used[artifact.ID] = struct{}{}
		}
	}
	for _, artifact := range artifacts {
		if counts[artifact.Name] == 1 && artifact.ID == "" {
			used[artifact.Name] = struct{}{}
		}
	}
	for i := range artifacts {
		if counts[artifacts[i].Name] == 1 || artifacts[i].ID != "" {
			continue
		}
		baseID := artifacts[i].Name + "-" + string(artifacts[i].Type)
		candidate := baseID
		for ordinal := 2; ; ordinal++ {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s-%d", baseID, ordinal)
		}
		artifacts[i].ID = candidate
		used[candidate] = struct{}{}
	}
}

func effectiveStackPinSourcePaths(pins []effectiveStackPin) []string {
	unique := make(map[string]struct{}, len(pins))
	for _, pin := range pins {
		unique[pin.path] = struct{}{}
	}
	paths := make([]string, 0, len(unique))
	for sourcePath := range unique {
		paths = append(paths, sourcePath)
	}
	sort.Strings(paths)
	return paths
}

func validateCatalogEffectiveStackPins(catalog *Catalog, sources map[string][]byte) error {
	versions, err := extractEffectiveStackPins(sources, effectiveStackPins)
	if err != nil {
		return err
	}
	for _, pin := range effectiveStackPins {
		version := versions[pin.artifact]
		artifact, ok := catalog.findArtifactByNameAndType(pin.artifact, pin.artifactType)
		if !ok {
			return fmt.Errorf("resolved inventory is missing stack-pinned %s artifact %s", pin.artifactType, pin.artifact)
		}
		if artifact.Version != version {
			return fmt.Errorf("resolved inventory %s version is %s, want tagged stack pin %s", pin.artifact, artifact.Version, version)
		}
	}
	return nil
}

func markPublicationPendingUnlessExact(catalog *Catalog, artifact Artifact) {
	if _, published := catalog.publicationFor(artifact); !published {
		catalog.PublicationPending = append(catalog.PublicationPending, artifact.catalogKey())
	}
}
