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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultStackProjectID    = 182049
	defaultComputeProjectID  = 268903
	defaultPackageName       = "ncp-deploy"
	defaultStackResourceName = "nvcf-self-managed-stack"
	computeStackResourceName = "nvcf-compute-plane-stack"
	defaultStackRegistry     = "staging"
	defaultCLIRegistry       = defaultStackRegistry
	defaultCLIVersion        = "0.0.30"
)

type ArtifactType string

const (
	ArtifactTypeImage    ArtifactType = "image"
	ArtifactTypeChart    ArtifactType = "chart"
	ArtifactTypeResource ArtifactType = "resource"
)

type ManifestPlane string
type ManifestKind string
type ManifestRequirement string

const (
	ManifestPlaneControl ManifestPlane = "control"
	ManifestPlaneCompute ManifestPlane = "compute"
	ManifestPlaneShared  ManifestPlane = "shared"

	ManifestKindChart        ManifestKind = "chart"
	ManifestKindServiceImage ManifestKind = "service-image"
	ManifestKindEACVE        ManifestKind = "ea-cve"
	ManifestKindResource     ManifestKind = "resource"

	ManifestRequired ManifestRequirement = "required"
	ManifestOptional ManifestRequirement = "optional"
)

type ManifestMetadata struct {
	Entries []ManifestEntry `yaml:"entries"`
}

type ManifestEntry struct {
	ArtifactID   string              `yaml:"artifact_id,omitempty"`
	Name         string              `yaml:"name,omitempty"`
	Version      string              `yaml:"version,omitempty"`
	Distribution string              `yaml:"distribution,omitempty"`
	Plane        ManifestPlane       `yaml:"plane"`
	Kind         ManifestKind        `yaml:"kind"`
	Requirement  ManifestRequirement `yaml:"requirement,omitempty"`
	Description  string              `yaml:"description"`
	GitHubURL    string              `yaml:"github_url,omitempty"`
	UpstreamURL  string              `yaml:"upstream_url,omitempty"`
}

type Catalog struct {
	Version               int                 `yaml:"version"`
	Target                string              `yaml:"target"`
	Registries            map[string]Registry `yaml:"registries"`
	Publications          []Publication       `yaml:"publications,omitempty"`
	VersionOverrides      []VersionOverride   `yaml:"version_overrides,omitempty"`
	Manifest              ManifestMetadata    `yaml:"manifest,omitempty"`
	Stack                 StackMetadata       `yaml:"stack"`
	Denylist              []DenylistEntry     `yaml:"denylist,omitempty"`
	Artifacts             []Artifact          `yaml:"artifacts"`
	SupplementalArtifacts []Artifact          `yaml:"supplemental_artifacts"`
	Outputs               []OutputFile        `yaml:"outputs"`
}

type Registry struct {
	Host            string `yaml:"host"`
	Namespace       string `yaml:"namespace"`
	RepositoryAlias string `yaml:"repository_alias,omitempty"`
}

type ChartFormat string

const (
	ChartFormatHTTP ChartFormat = "http"
)

type Publication struct {
	Name             string      `yaml:"name"`
	Version          string      `yaml:"version"`
	PublishedVersion string      `yaml:"published_version,omitempty"`
	Registry         string      `yaml:"registry"`
	ChartFormat      ChartFormat `yaml:"chart_format,omitempty"`
}

type VersionOverride struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	Source  string `yaml:"source,omitempty"`
}

type StackMetadata struct {
	Name            string `yaml:"name"`
	Version         string `yaml:"version"`
	Registry        string `yaml:"registry"`
	GitLabProjectID int    `yaml:"gitlab_project_id,omitempty"`
	PackageName     string `yaml:"package_name,omitempty"`
	ArtifactsFile   string `yaml:"artifacts_file,omitempty"`
}

type DenylistEntry struct {
	Name   string `yaml:"name"`
	Reason string `yaml:"reason"`
}

var defaultDenylist = []DenylistEntry{
	{Name: "nvcf-base", Reason: "retired repository"},
	{Name: "samba", Reason: "retired internal dependency"},
}

type Artifact struct {
	ID             string       `yaml:"id,omitempty"`
	Name           string       `yaml:"name"`
	Type           ArtifactType `yaml:"type"`
	Registry       string       `yaml:"registry"`
	RepositoryName string       `yaml:"repository_name,omitempty"`
	Version        string       `yaml:"version"`
	Source         string       `yaml:"source,omitempty"`

	registryHost      string
	registryNamespace string
}

type OutputFile struct {
	Path   string        `yaml:"path"`
	Blocks []OutputBlock `yaml:"blocks,omitempty"`
}

type OutputBlock struct {
	Marker   string `yaml:"marker"`
	Renderer string `yaml:"renderer"`
}

func LoadCatalog(path string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var catalog Catalog
	if err := dec.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := ValidateCatalog(&catalog); err != nil {
		return nil, err
	}
	return &catalog, nil
}

func WriteCatalog(path string, catalog *Catalog) error {
	if err := ValidateCatalog(catalog); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := MarshalCatalog(catalog)
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func MarshalCatalog(catalog *Catalog) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(catalog); err != nil {
		enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func ValidateTarget(target string) error {
	if target != "main" {
		return fmt.Errorf("unsupported target %q: only main is supported", target)
	}
	return nil
}

func ValidateCatalog(catalog *Catalog) error {
	if catalog == nil {
		return fmt.Errorf("catalog is nil")
	}
	if err := ValidateTarget(catalog.Target); err != nil {
		return err
	}
	if catalog.Version != 1 {
		return fmt.Errorf("unsupported catalog version %d", catalog.Version)
	}
	if len(catalog.Registries) == 0 {
		return fmt.Errorf("catalog has no registries")
	}
	for name, registry := range catalog.Registries {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("registry key cannot be empty")
		}
		if strings.TrimSpace(registry.Host) == "" {
			return fmt.Errorf("registry %q has empty host", name)
		}
		if strings.TrimSpace(registry.Namespace) == "" {
			return fmt.Errorf("registry %q has empty namespace", name)
		}
	}
	seenPublications := map[string]struct{}{}
	publicationVersions := map[string][]string{}
	for _, publication := range catalog.Publications {
		if strings.TrimSpace(publication.Name) == "" {
			return fmt.Errorf("publication name cannot be empty")
		}
		if strings.TrimSpace(publication.Version) == "" {
			return fmt.Errorf("publication %s has empty version", publication.Name)
		}
		if _, ok := catalog.Registries[publication.Registry]; !ok {
			return fmt.Errorf("publication %s:%s registry %q is not defined", publication.Name, publication.Version, publication.Registry)
		}
		if publication.ChartFormat != "" && publication.ChartFormat != ChartFormatHTTP {
			return fmt.Errorf("publication %s:%s has unsupported chart format %q", publication.Name, publication.Version, publication.ChartFormat)
		}
		key := publication.Name + ":" + publication.Version
		if _, exists := seenPublications[key]; exists {
			return fmt.Errorf("duplicate publication %s", key)
		}
		seenPublications[key] = struct{}{}
		publicationVersions[publication.Name] = append(publicationVersions[publication.Name], publication.Version)
	}
	seenVersionOverrides := map[string]struct{}{}
	for _, override := range catalog.VersionOverrides {
		if strings.TrimSpace(override.Name) == "" {
			return fmt.Errorf("version override name cannot be empty")
		}
		if strings.TrimSpace(override.Version) == "" {
			return fmt.Errorf("version override %s has empty version", override.Name)
		}
		if _, exists := seenVersionOverrides[override.Name]; exists {
			return fmt.Errorf("duplicate version override %s", override.Name)
		}
		if versions, published := publicationVersions[override.Name]; published {
			key := override.Name + ":" + override.Version
			if _, matches := seenPublications[key]; !matches {
				return fmt.Errorf("version override %s does not match publication version %s", key, strings.Join(versions, ", "))
			}
		}
		seenVersionOverrides[override.Name] = struct{}{}
	}
	if strings.TrimSpace(catalog.Stack.Name) == "" {
		return fmt.Errorf("stack name cannot be empty")
	}
	if strings.TrimSpace(catalog.Stack.Version) == "" {
		return fmt.Errorf("stack version cannot be empty")
	}
	if _, ok := catalog.Registries[catalog.Stack.Registry]; !ok {
		return fmt.Errorf("stack registry %q is not defined", catalog.Stack.Registry)
	}

	denylist := catalog.DenylistMap()
	seen := map[string]struct{}{}
	for _, artifact := range append(append([]Artifact{}, catalog.Artifacts...), catalog.SupplementalArtifacts...) {
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		if err := catalog.validateArtifact(artifact); err != nil {
			return err
		}
		key := artifact.catalogKey()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate artifact id %s", key)
		}
		seen[key] = struct{}{}
	}
	if _, exists := seen[catalog.Stack.Name]; exists {
		return fmt.Errorf("duplicate artifact id %s", catalog.Stack.Name)
	}
	if err := validateManifestMetadata(catalog.Manifest); err != nil {
		return err
	}

	for _, output := range catalog.Outputs {
		if err := validateOutputPath(output.Path); err != nil {
			return err
		}
		for _, block := range output.Blocks {
			if strings.TrimSpace(block.Marker) == "" {
				return fmt.Errorf("output %s has block with empty marker", output.Path)
			}
			if strings.TrimSpace(block.Renderer) == "" {
				return fmt.Errorf("output %s has block with empty renderer", output.Path)
			}
		}
	}
	return nil
}

func validateManifestMetadata(metadata ManifestMetadata) error {
	seen := map[string]struct{}{}
	// Changing the EA-CVE allowlist requires an explicit documentation decision.
	allowedEACVE := map[string]struct{}{
		"bitnami-cassandra":         {},
		"nvcf-cassandra-migrations": {},
	}
	for _, entry := range metadata.Entries {
		identifier := entry.ArtifactID
		if identifier == "" {
			identifier = entry.Name
		}
		switch entry.Plane {
		case ManifestPlaneControl, ManifestPlaneCompute, ManifestPlaneShared:
		default:
			return fmt.Errorf("manifest entry %q has unsupported plane %q", identifier, entry.Plane)
		}
		switch entry.Kind {
		case ManifestKindChart, ManifestKindServiceImage, ManifestKindEACVE, ManifestKindResource:
		default:
			return fmt.Errorf("manifest entry %q has unsupported kind %q", identifier, entry.Kind)
		}
		if entry.Kind == ManifestKindResource {
			if entry.Plane != ManifestPlaneShared {
				return fmt.Errorf("manifest resource entry %q must use plane %q", identifier, ManifestPlaneShared)
			}
			if entry.Requirement != "" {
				return fmt.Errorf("manifest resource entry %q cannot set requirement", identifier)
			}
		} else {
			switch entry.Requirement {
			case ManifestRequired, ManifestOptional:
			default:
				return fmt.Errorf("manifest entry %q has unsupported requirement %q", identifier, entry.Requirement)
			}
		}
		if strings.TrimSpace(entry.Description) == "" {
			return fmt.Errorf("manifest entry %q has empty description", identifier)
		}
		for field, value := range map[string]string{
			"github_url":   entry.GitHubURL,
			"upstream_url": entry.UpstreamURL,
		} {
			if value != "" && !strings.HasPrefix(value, "https://github.com/") {
				return fmt.Errorf("manifest entry %q %s must use https://github.com/", identifier, field)
			}
		}

		key := entry.ArtifactID
		if key != "" {
			if entry.Name != "" || entry.Version != "" || entry.Distribution != "" {
				return fmt.Errorf("manifest entry %q catalog-backed entry cannot set static fields", key)
			}
		} else {
			if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Version) == "" || strings.TrimSpace(entry.Distribution) == "" {
				return fmt.Errorf("manifest static entry requires name, version, and distribution")
			}
			key = "static:" + entry.Name
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate manifest entry %s", key)
		}
		seen[key] = struct{}{}

		if entry.Kind == ManifestKindEACVE {
			if _, allowed := allowedEACVE[entry.ArtifactID]; !allowed {
				return fmt.Errorf("manifest EA-CVE entry %q is not an approved Cassandra artifact", entry.ArtifactID)
			}
		}
	}
	return nil
}

func (catalog *Catalog) validateArtifact(artifact Artifact) error {
	if strings.TrimSpace(artifact.ID) != artifact.ID {
		return fmt.Errorf("artifact %s has an id with leading or trailing whitespace", artifact.Name)
	}
	if strings.TrimSpace(artifact.Name) == "" {
		return fmt.Errorf("artifact name cannot be empty")
	}
	switch artifact.Type {
	case ArtifactTypeImage, ArtifactTypeChart, ArtifactTypeResource:
	default:
		return fmt.Errorf("artifact %s has unsupported type %q", artifact.Name, artifact.Type)
	}
	if strings.TrimSpace(artifact.Version) == "" {
		return fmt.Errorf("artifact %s has empty version", artifact.Name)
	}
	if _, ok := catalog.Registries[artifact.Registry]; !ok {
		return fmt.Errorf("artifact %s registry %q is not defined", artifact.Name, artifact.Registry)
	}
	return nil
}

func (artifact Artifact) catalogKey() string {
	if artifact.ID != "" {
		return artifact.ID
	}
	return artifact.Name
}

func validateOutputPath(path string) error {
	clean := filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) || strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("output path %s must be repository relative", path)
	}
	if strings.HasPrefix(clean, "docs/v0.5/") {
		return fmt.Errorf("output path %s is not allowed: versioned docs are generated from main", path)
	}
	if !strings.HasPrefix(clean, "docs/user/") {
		return fmt.Errorf("output path %s is outside docs/user", path)
	}
	return nil
}

func (catalog *Catalog) DenylistMap() map[string]DenylistEntry {
	entries := append(append([]DenylistEntry{}, defaultDenylist...), catalog.Denylist...)
	denylist := make(map[string]DenylistEntry, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == "" {
			continue
		}
		denylist[entry.Name] = entry
	}
	return denylist
}

func (catalog *Catalog) artifactPath(artifact Artifact) (string, error) {
	registryName := artifact.Registry
	version := artifact.Version
	if publication, ok := catalog.publicationFor(artifact); ok {
		registryName = publication.Registry
		if publication.PublishedVersion != "" {
			version = publication.PublishedVersion
		}
	}
	registry, ok := catalog.Registries[registryName]
	if !ok {
		return "", fmt.Errorf("artifact %s registry %q is not defined", artifact.Name, registryName)
	}
	name := artifact.RepositoryName
	if name == "" {
		name = artifact.Name
	}
	return registry.fullPath(name, version), nil
}

func (catalog *Catalog) publicationFor(artifact Artifact) (Publication, bool) {
	for _, publication := range catalog.Publications {
		if publication.Name == artifact.Name && publication.Version == artifact.Version {
			return publication, true
		}
	}
	return Publication{}, false
}

func (catalog *Catalog) chartPullReference(artifact Artifact) (string, error) {
	publication, published := catalog.publicationFor(artifact)
	if published && publication.ChartFormat == ChartFormatHTTP {
		registry := catalog.Registries[publication.Registry]
		if registry.RepositoryAlias == "" {
			return "", fmt.Errorf("publication registry %q has no repository alias", publication.Registry)
		}
		name := artifact.RepositoryName
		if name == "" {
			name = artifact.Name
		}
		return registry.RepositoryAlias + "/" + name, nil
	}
	path, err := catalog.artifactPath(artifact)
	if err != nil {
		return "", err
	}
	return "oci://" + strings.TrimSuffix(path, ":"+artifact.Version), nil
}

func (catalog *Catalog) resourceRef(artifact Artifact) (string, error) {
	registry, ok := catalog.Registries[artifact.Registry]
	if !ok {
		return "", fmt.Errorf("artifact %s registry %q is not defined", artifact.Name, artifact.Registry)
	}
	name := artifact.RepositoryName
	if name == "" {
		name = artifact.Name
	}
	return registry.resourceRef(name, artifact.Version), nil
}

func (registry Registry) fullPath(name, version string) string {
	return fmt.Sprintf("%s/%s/%s:%s", registry.Host, registry.Namespace, name, version)
}

func (registry Registry) resourceRef(name, version string) string {
	return fmt.Sprintf("%s/%s:%s", registry.Namespace, name, version)
}

func BuildCatalogFromArtifacts(stackVersion string, artifacts []Artifact) *Catalog {
	catalog := &Catalog{
		Version: 1,
		Target:  "main",
		Registries: map[string]Registry{
			defaultStackRegistry: {
				Host:      "nvcr.io",
				Namespace: "0833294136851237/nvcf-ncp-staging",
			},
		},
		Stack: StackMetadata{
			Name:            defaultStackResourceName,
			Version:         stackVersion,
			Registry:        defaultStackRegistry,
			GitLabProjectID: defaultStackProjectID,
			PackageName:     defaultPackageName,
			ArtifactsFile:   fmt.Sprintf("artifacts-%s.txt", stackVersion),
		},
		SupplementalArtifacts: []Artifact{
			{Name: "nvcf-cli", Type: ArtifactTypeResource, Registry: defaultCLIRegistry, Version: defaultCLIVersion},
		},
		Outputs: defaultOutputs(),
	}

	for i := range artifacts {
		artifact := artifacts[i]
		if artifact.Registry == "" {
			artifact.Registry = catalog.registryKeyFor(artifact.registryHost, artifact.registryNamespace)
		}
		artifact.Source = ""
		catalog.Artifacts = append(catalog.Artifacts, artifact)
	}
	return catalog
}

func BuildCatalogFromArtifactsWithBase(stackVersion string, artifacts []Artifact, base *Catalog) *Catalog {
	catalog := BuildCatalogFromArtifacts(stackVersion, artifacts)
	if base == nil {
		return catalog
	}

	for key, registry := range base.Registries {
		if _, exists := catalog.Registries[key]; !exists {
			catalog.Registries[key] = registry
		}
	}
	catalog.Publications = append(catalog.Publications, base.Publications...)
	catalog.VersionOverrides = append(catalog.VersionOverrides, base.VersionOverrides...)
	catalog.Denylist = append(catalog.Denylist, base.Denylist...)
	catalog.Manifest = base.Manifest

	manifestNames := map[string]struct{}{}
	for _, artifact := range catalog.Artifacts {
		manifestNames[artifact.Name] = struct{}{}
	}

	seenSupplemental := map[string]struct{}{}
	for _, artifact := range catalog.SupplementalArtifacts {
		seenSupplemental[artifact.catalogKey()] = struct{}{}
	}
	denylist := catalog.DenylistMap()
	for _, artifact := range append(append([]Artifact{}, base.Artifacts...), base.SupplementalArtifacts...) {
		if artifact.Name == "" || artifact.Name == defaultStackResourceName || artifact.Name == "nvcf-cli" {
			continue
		}
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		if _, published := manifestNames[artifact.Name]; published {
			continue
		}
		key := artifact.catalogKey()
		if _, exists := seenSupplemental[key]; exists {
			continue
		}
		artifact.Source = ""
		catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts, artifact)
		seenSupplemental[key] = struct{}{}
	}
	catalog.applyVersionOverrides()
	catalog.pruneUnusedRegistries()
	return catalog
}

func (catalog *Catalog) applyVersionOverrides() {
	for _, override := range catalog.VersionOverrides {
		for i := range catalog.Artifacts {
			if catalog.Artifacts[i].Name == override.Name {
				catalog.Artifacts[i].Version = override.Version
			}
		}
		for i := range catalog.SupplementalArtifacts {
			if catalog.SupplementalArtifacts[i].Name == override.Name {
				catalog.SupplementalArtifacts[i].Version = override.Version
			}
		}
	}
}

func (catalog *Catalog) pruneUnusedRegistries() {
	used := map[string]struct{}{
		catalog.Stack.Registry: {},
	}
	for _, artifact := range append(append([]Artifact{}, catalog.Artifacts...), catalog.SupplementalArtifacts...) {
		if artifact.Registry != "" {
			used[artifact.Registry] = struct{}{}
		}
	}
	for _, publication := range catalog.Publications {
		if publication.Registry != "" {
			used[publication.Registry] = struct{}{}
		}
	}
	for key := range catalog.Registries {
		if _, ok := used[key]; !ok {
			delete(catalog.Registries, key)
		}
	}
}

func (catalog *Catalog) registryKeyFor(host, namespace string) string {
	if host == "" || namespace == "" {
		return defaultStackRegistry
	}
	for key, registry := range catalog.Registries {
		if registry.Host == host && registry.Namespace == namespace {
			return key
		}
	}

	base := sanitizeRegistryKey(namespace)
	key := base
	for i := 2; ; i++ {
		if _, exists := catalog.Registries[key]; !exists {
			catalog.Registries[key] = Registry{Host: host, Namespace: namespace}
			return key
		}
		key = fmt.Sprintf("%s-%d", base, i)
	}
}

var registryKeyRe = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeRegistryKey(namespace string) string {
	key := strings.ToLower(strings.ReplaceAll(namespace, "/", "-"))
	key = registryKeyRe.ReplaceAllString(key, "-")
	key = strings.Trim(key, "-")
	if key == "" {
		return "registry"
	}
	return key
}

func defaultOutputs() []OutputFile {
	return []OutputFile{
		{
			Path: "docs/user/manifest.md",
			Blocks: []OutputBlock{{
				Marker:   "manifest-artifact-registry-paths",
				Renderer: "manifest-artifact-registry-paths",
			}},
		},
		{
			Path: "docs/user/image-mirroring.md",
			Blocks: []OutputBlock{
				{
					Marker:   "image-mirroring-resource-examples",
					Renderer: "image-mirroring-resource-examples",
				},
				{
					Marker:   "image-mirroring-stack-snippet",
					Renderer: "image-mirroring-stack-snippet",
				},
				{
					Marker:   "image-mirroring-compute-stack-snippet",
					Renderer: "image-mirroring-compute-stack-snippet",
				},
				{
					Marker:   "image-mirroring-cli-snippet",
					Renderer: "image-mirroring-cli-snippet",
				},
			},
		},
		{Path: "docs/user/cluster-management/self-managed.md"},
		{Path: "docs/user/cluster-management/reference.md"},
	}
}

func artifactNames(artifacts []Artifact) []string {
	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		names = append(names, artifact.Name)
	}
	sort.Strings(names)
	return names
}
