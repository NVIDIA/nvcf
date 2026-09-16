// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
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

func updateCatalogFromGitHubInventories(repoRoot string, sourceRefs map[string]string, qualificationVersion string, base *Catalog) (*Catalog, error) {
	client := newGitHubClientFromEnvironment()
	inventories := make(map[string]resolvedStackInventory, len(stackInventorySpecs))
	for _, spec := range stackInventorySpecs {
		release, err := client.resolveStackSourceReleaseForSpec(spec, sourceRefs[spec.Key])
		if err != nil {
			return nil, fmt.Errorf("resolve %s stack release: %w", spec.Key, err)
		}
		inventory, err := client.resolvedStackInventoryForSpec(spec, release)
		if err != nil {
			return nil, fmt.Errorf("read %s stack inventory: %w", spec.Key, err)
		}
		if err := validateInventoryPlaneOwnership(spec, inventory); err != nil {
			return nil, err
		}
		for _, warning := range inventory.Warnings {
			fmt.Fprintf(os.Stderr, "WARNING: %s stack inventory: %s\n", spec.Key, warning)
		}
		inventories[spec.Key] = inventory
	}

	combined, err := mergeResolvedStackInventories(inventories)
	if err != nil {
		return nil, err
	}
	selfManaged := inventories[selfManagedStackKey]
	sourcePaths := effectiveStackPinSourcePaths(effectiveStackPins)
	snapshot, err := loadStackSourceSnapshot(repoRoot, selfManaged.Source, sourcePaths)
	if err != nil {
		return nil, err
	}
	catalog, err := buildCatalogFromResolvedStackInventory(combined, snapshot, base)
	if err != nil {
		return nil, err
	}
	status := ReleaseSetDevelopment
	documentationVersion := "dev"
	if qualificationVersion != "" {
		status = ReleaseSetQualified
		documentationVersion = strings.TrimPrefix(qualificationVersion, "v")
	}
	releaseSet, err := releaseSetFromInventories(inventories, documentationVersion, status)
	if err != nil {
		return nil, err
	}
	if qualificationVersion == "" && base != nil && base.ReleaseSet.Status == ReleaseSetQualified && releaseSet.sameStackReleases(base.ReleaseSet) {
		releaseSet.DocumentationVersion = base.ReleaseSet.DocumentationVersion
		releaseSet.Status = base.ReleaseSet.Status
	}
	catalog.ReleaseSet = releaseSet
	for _, spec := range stackInventorySpecs[1:] {
		version := inventories[spec.Key].Source.Version
		if !setArtifactVersionByNameAndType(catalog, spec.ResourceName, ArtifactTypeResource, version) {
			catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts, Artifact{
				Name:     spec.ResourceName,
				Type:     ArtifactTypeResource,
				Registry: defaultStackRegistry,
				Version:  version,
				Stacks:   []string{spec.Key},
			})
		} else {
			setArtifactStacksByNameAndType(catalog, spec.ResourceName, ArtifactTypeResource, []string{spec.Key})
		}
	}
	retainCurrentPublications(catalog)
	catalog.markAllUnpublishedAsPending()
	catalog.reconcilePublicationPending()
	catalog.pruneUnusedRegistries()
	if qualificationVersion != "" && len(catalog.PublicationPending) > 0 {
		return nil, fmt.Errorf("qualified release set has unpublished artifacts: %s", strings.Join(catalog.PublicationPending, ", "))
	}
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func setArtifactStacksByNameAndType(catalog *Catalog, name string, artifactType ArtifactType, stacks []string) {
	for index := range catalog.Artifacts {
		if catalog.Artifacts[index].Name == name && catalog.Artifacts[index].Type == artifactType {
			catalog.Artifacts[index].Stacks = append([]string(nil), stacks...)
		}
	}
	for index := range catalog.SupplementalArtifacts {
		if catalog.SupplementalArtifacts[index].Name == name && catalog.SupplementalArtifacts[index].Type == artifactType {
			catalog.SupplementalArtifacts[index].Stacks = append([]string(nil), stacks...)
		}
	}
}

func validateInventoryPlaneOwnership(spec stackInventorySpec, inventory resolvedStackInventory) error {
	wantPlane := map[string]string{
		selfManagedStackKey:   "control-plane",
		computePlaneStackKey:  "compute-plane",
		observabilityStackKey: "observability",
	}[spec.Key]
	for _, release := range inventory.Releases {
		if release.Plane != wantPlane {
			return fmt.Errorf("%s stack inventory contains %s release %s; want only %s releases", spec.Key, release.Plane, release.Name, wantPlane)
		}
	}
	return nil
}

func mergeResolvedStackInventories(inventories map[string]resolvedStackInventory) (resolvedStackInventory, error) {
	selfManaged, ok := inventories[selfManagedStackKey]
	if !ok {
		return resolvedStackInventory{}, fmt.Errorf("self-managed stack inventory is required")
	}
	combined := resolvedStackInventory{
		SchemaVersion: resolvedStackInventorySchemaVersion,
		Source:        selfManaged.Source,
	}
	releases := make(map[string]struct{})
	artifacts := make(map[string]*resolvedInventoryArtifact)
	artifactIdentities := make(map[string]string)
	artifactOwners := make(map[string]string)
	for _, spec := range stackInventorySpecs {
		inventory, exists := inventories[spec.Key]
		if !exists {
			return resolvedStackInventory{}, fmt.Errorf("%s stack inventory is required", spec.Key)
		}
		if err := validateResolvedStackInventory(inventory); err != nil {
			return resolvedStackInventory{}, fmt.Errorf("validate %s stack inventory: %w", spec.Key, err)
		}
		for _, release := range inventory.Releases {
			key := resolvedReleaseKey(release.Plane, release.Name)
			if _, duplicate := releases[key]; duplicate {
				return resolvedStackInventory{}, fmt.Errorf("duplicate release %s across stack inventories", key)
			}
			releases[key] = struct{}{}
			combined.Releases = append(combined.Releases, release)
		}
		for _, artifact := range inventory.Artifacts {
			identity := artifact.Type + "\x00" + artifact.Name
			if reference, exists := artifactIdentities[identity]; exists && reference != artifact.Reference {
				return resolvedStackInventory{}, fmt.Errorf("stack inventory version conflict for %s %s: %s uses %s and %s uses %s", artifact.Type, artifact.Name, artifactOwners[identity], reference, spec.Key, artifact.Reference)
			}
			artifactIdentities[identity] = artifact.Reference
			if _, exists := artifactOwners[identity]; !exists {
				artifactOwners[identity] = spec.Key
			}
			key := artifact.Type + "\x00" + artifact.Reference
			existing, exists := artifacts[key]
			if !exists {
				copy := artifact
				copy.Sources = append([]resolvedArtifactSource(nil), artifact.Sources...)
				artifacts[key] = &copy
				continue
			}
			for _, source := range artifact.Sources {
				mergeResolvedArtifactSource(existing, source)
			}
		}
	}
	for _, artifact := range artifacts {
		sort.Slice(artifact.Sources, func(i, j int) bool {
			return compareResolvedArtifactSources(artifact.Sources[i], artifact.Sources[j]) < 0
		})
		combined.Artifacts = append(combined.Artifacts, *artifact)
	}
	sort.Slice(combined.Releases, func(i, j int) bool {
		return compareResolvedInventoryReleases(combined.Releases[i], combined.Releases[j]) < 0
	})
	sort.Slice(combined.Artifacts, func(i, j int) bool {
		return compareResolvedInventoryArtifacts(combined.Artifacts[i], combined.Artifacts[j]) < 0
	})
	if err := validateResolvedStackInventory(combined); err != nil {
		return resolvedStackInventory{}, fmt.Errorf("validate combined stack inventory: %w", err)
	}
	return combined, nil
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
	catalog := refreshCatalogFromArtifacts(inventory.Source.Version, artifacts, base)
	retainIndependentManifestArtifacts(catalog, inventory, base)
	if err := materializeMissingEffectiveStackPins(catalog, base, snapshot.Files); err != nil {
		return nil, err
	}
	if base != nil {
		catalog.Stack.Name = base.Stack.Name
		catalog.Stack.Registry = base.Stack.Registry
		catalog.Registries[base.Stack.Registry] = base.Registries[base.Stack.Registry]
	}
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

	retainCurrentPublications(catalog)
	catalog.markAllUnpublishedAsPending()
	catalog.reconcilePublicationPending()
	catalog.pruneUnusedRegistries()
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
	requirements := make(map[string]bool, len(inventory.Releases))
	for _, release := range inventory.Releases {
		requirements[resolvedReleaseKey(release.Plane, release.Name)] = release.Required
	}
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
		stacks := make(map[string]struct{})
		requirement := ManifestOptional
		for _, source := range resolved.Sources {
			stack, err := stackKeyForPlane(source.Plane)
			if err != nil {
				return nil, fmt.Errorf("artifact %s: %w", resolved.Reference, err)
			}
			stacks[stack] = struct{}{}
			if resolvedArtifactSourceIsRequired(source, requirements) {
				requirement = ManifestRequired
			}
		}
		owningStacks := make([]string, 0, len(stacks))
		for stack := range stacks {
			owningStacks = append(owningStacks, stack)
		}
		sort.Strings(owningStacks)
		artifacts = append(artifacts, Artifact{
			Name:               resolved.Name,
			Type:               artifactType,
			Registry:           publicRegistryForArtifactType(artifactType),
			UpstreamRepository: publicUpstreamRepository(resolved.Repository, artifactType),
			Version:            resolved.Version,
			Digest:             resolved.Digest,
			Stacks:             owningStacks,
			Requirement:        requirement,
		})
	}
	preserveCatalogArtifactIdentity(artifacts, base)
	assignCatalogArtifactIDs(artifacts)
	return artifacts, nil
}

func resolvedArtifactSourceIsRequired(source resolvedArtifactSource, releaseRequirements map[string]bool) bool {
	switch source.Requirement {
	case ManifestRequired:
		return true
	case ManifestOptional:
		return false
	default:
		return releaseRequirements[resolvedReleaseKey(source.Plane, source.Release)]
	}
}

func publicRegistryForArtifactType(artifactType ArtifactType) string {
	switch artifactType {
	case ArtifactTypeImage:
		return defaultImageRegistry
	case ArtifactTypeChart:
		return defaultChartRegistry
	default:
		return defaultStackRegistry
	}
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

// retainIndependentManifestArtifacts keeps public add-ons and artifacts from
// deployment planes that are not owned by this inventory. Legacy aggregate
// inventories still replace artifacts from every plane they contain.
func retainIndependentManifestArtifacts(catalog *Catalog, inventory resolvedStackInventory, base *Catalog) {
	denylist := catalog.DenylistMap()
	referenced := make(map[string]ManifestPlane, len(catalog.Manifest.Entries))
	for _, entry := range catalog.Manifest.Entries {
		if entry.ArtifactID != "" {
			referenced[entry.ArtifactID] = entry.Plane
		}
	}
	ownedPlanes := resolvedInventoryManifestPlanes(inventory)
	independentArtifacts := make(map[string]struct{})
	if base != nil {
		for _, artifact := range base.SupplementalArtifacts {
			independentArtifacts[artifact.catalogKey()] = struct{}{}
		}
	}
	retained := catalog.SupplementalArtifacts[:0]
	for _, artifact := range catalog.SupplementalArtifacts {
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		if artifact.Type != ArtifactTypeResource {
			plane, manifestArtifact := referenced[artifact.catalogKey()]
			if !manifestArtifact {
				continue
			}
			if _, owned := ownedPlanes[plane]; owned {
				if _, independent := independentArtifacts[artifact.catalogKey()]; !independent {
					continue
				}
			}
		}
		artifact.Registry = publicRegistryForArtifactType(artifact.Type)
		retained = append(retained, artifact)
	}
	catalog.SupplementalArtifacts = retained
}

func resolvedInventoryManifestPlanes(inventory resolvedStackInventory) map[ManifestPlane]struct{} {
	planes := make(map[ManifestPlane]struct{})
	for _, release := range inventory.Releases {
		switch release.Plane {
		case "control-plane":
			planes[ManifestPlaneControl] = struct{}{}
		case "compute-plane":
			planes[ManifestPlaneCompute] = struct{}{}
		case "observability":
			planes[ManifestPlaneObservability] = struct{}{}
		}
	}
	return planes
}

// retainCurrentPublications keeps exact public availability records for the
// versions represented by the refreshed catalog.
func retainCurrentPublications(catalog *Catalog) {
	current := make(map[string]bool, len(catalog.Artifacts)+len(catalog.SupplementalArtifacts)+1)
	for _, artifact := range append(append([]Artifact{catalog.stackArtifact()}, catalog.Artifacts...), catalog.SupplementalArtifacts...) {
		current[publicationIdentityKey(artifact.Name, artifact.Type, artifact.Version)] = artifact.UpstreamRepository == ""
	}

	publications := catalog.Publications[:0]
	for _, publication := range catalog.Publications {
		if retain, ok := current[publicationIdentityKey(publication.Name, publication.Type, publication.Version)]; !ok || !retain {
			continue
		}
		registry, ok := catalog.Registries[publication.Registry]
		if !ok || !isPublicCatalogRegistry(registry) {
			continue
		}
		publications = append(publications, publication)
	}
	catalog.Publications = publications
}

func isPublicCatalogRegistry(registry Registry) bool {
	host := strings.ToLower(strings.TrimSuffix(registry.Host, "/"))
	namespace := strings.Trim(registry.Namespace, "/")
	switch host {
	case "nvcr.io", "https://helm.ngc.nvidia.com":
		return namespace == "nvidia" || strings.HasPrefix(namespace, "nvidia/")
	case "https://open-telemetry.github.io", "https://prometheus-community.github.io", "https://victoriametrics.github.io":
		return true
	case "docker.io", "ghcr.io", "quay.io", "registry.k8s.io":
		return true
	default:
		return false
	}
}

func publicUpstreamRepository(repository string, artifactType ArtifactType) string {
	repository = canonicalPublicRepository(repository, artifactType)
	if _, ok := publicUpstreamRegistry(repository, artifactType); !ok || isNVCFPublicationRepository(repository) {
		return ""
	}
	return repository
}

func canonicalPublicRepository(repository string, artifactType ArtifactType) string {
	if artifactType != ArtifactTypeImage || strings.Contains(repository, "://") {
		return repository
	}
	host, _, found := strings.Cut(repository, "/")
	if found && !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "docker.io/" + repository
	}
	return repository
}

func publicUpstreamRegistry(repository string, artifactType ArtifactType) (Registry, bool) {
	if repository == "" || repository != strings.TrimSpace(repository) || strings.HasSuffix(repository, "/") ||
		strings.ContainsAny(repository, "@?# \t\r\n") {
		return Registry{}, false
	}
	value := repository
	hostPrefix := ""
	switch artifactType {
	case ArtifactTypeImage:
		if strings.Contains(value, "://") {
			return Registry{}, false
		}
	case ArtifactTypeChart:
		switch {
		case strings.HasPrefix(value, "oci://"):
			value = strings.TrimPrefix(value, "oci://")
		case strings.HasPrefix(value, "https://"):
			value = strings.TrimPrefix(value, "https://")
			hostPrefix = "https://"
		default:
			return Registry{}, false
		}
	default:
		return Registry{}, false
	}
	host, namespace, found := strings.Cut(value, "/")
	if !found || host == "" || namespace == "" || strings.Contains(namespace, "//") ||
		(artifactType == ArtifactTypeImage && strings.Contains(namespace, ":")) {
		return Registry{}, false
	}
	registry := Registry{Host: hostPrefix + host, Namespace: namespace}
	return registry, isPublicCatalogRegistry(registry)
}

func isNVCFPublicationRepository(repository string) bool {
	value := strings.TrimSuffix(repository, "/")
	value = strings.TrimPrefix(value, "oci://")
	return value == "nvcr.io/nvidia/nvcf" || strings.HasPrefix(value, "nvcr.io/nvidia/nvcf/") ||
		value == "https://helm.ngc.nvidia.com/nvidia/nvcf" || strings.HasPrefix(value, "https://helm.ngc.nvidia.com/nvidia/nvcf/")
}

func upstreamArtifactPath(artifact Artifact) string {
	repository := strings.TrimSuffix(artifact.UpstreamRepository, "/")
	reference := repository
	if artifact.Type == ArtifactTypeChart {
		name := artifact.RepositoryName
		if name == "" {
			name = artifact.Name
		}
		reference += "/" + name
	}
	reference += ":" + artifact.Version
	if artifact.Digest != "" {
		reference += "@" + artifact.Digest
	}
	return reference
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

// materializeMissingEffectiveStackPins adds source-only pins that do not
// appear as image fields in rendered Kubernetes manifests.
func materializeMissingEffectiveStackPins(catalog, base *Catalog, sources map[string][]byte) error {
	versions, err := extractEffectiveStackPins(sources, effectiveStackPins)
	if err != nil {
		return err
	}
	for _, pin := range effectiveStackPins {
		if primaryArtifactExists(catalog, pin.artifact, pin.artifactType) {
			continue
		}
		artifact := Artifact{
			Name:     pin.artifact,
			Type:     pin.artifactType,
			Registry: publicRegistryForArtifactType(pin.artifactType),
			Version:  versions[pin.artifact],
		}
		if base != nil {
			if baseArtifact, found := base.findArtifactByNameAndType(pin.artifact, pin.artifactType); found {
				artifact.ID = baseArtifact.ID
				artifact.RepositoryName = baseArtifact.RepositoryName
			}
		}
		removeSupplementalArtifact(catalog, pin.artifact, pin.artifactType)
		catalog.Artifacts = append(catalog.Artifacts, artifact)
	}
	assignCatalogArtifactIDs(catalog.Artifacts)
	return nil
}

func primaryArtifactExists(catalog *Catalog, name string, artifactType ArtifactType) bool {
	for _, artifact := range catalog.Artifacts {
		if artifact.Name == name && artifact.Type == artifactType {
			return true
		}
	}
	return false
}

func removeSupplementalArtifact(catalog *Catalog, name string, artifactType ArtifactType) {
	retained := catalog.SupplementalArtifacts[:0]
	for _, artifact := range catalog.SupplementalArtifacts {
		if artifact.Name == name && artifact.Type == artifactType {
			continue
		}
		retained = append(retained, artifact)
	}
	catalog.SupplementalArtifacts = retained
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
