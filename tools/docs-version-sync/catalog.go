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
	defaultPackageName       = "ncp-deploy"
	defaultStackResourceName = "nvcf-self-managed-stack"
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

type Catalog struct {
	Version               int                 `yaml:"version"`
	Target                string              `yaml:"target"`
	Registries            map[string]Registry `yaml:"registries"`
	Stack                 StackMetadata       `yaml:"stack"`
	Denylist              []DenylistEntry     `yaml:"denylist,omitempty"`
	Artifacts             []Artifact          `yaml:"artifacts"`
	SupplementalArtifacts []Artifact          `yaml:"supplemental_artifacts"`
	Outputs               []OutputFile        `yaml:"outputs"`
}

type Registry struct {
	Host      string `yaml:"host"`
	Namespace string `yaml:"namespace"`
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
	registry, ok := catalog.Registries[artifact.Registry]
	if !ok {
		return "", fmt.Errorf("artifact %s registry %q is not defined", artifact.Name, artifact.Registry)
	}
	name := artifact.RepositoryName
	if name == "" {
		name = artifact.Name
	}
	return registry.fullPath(name, artifact.Version), nil
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
	catalog.Denylist = append(catalog.Denylist, base.Denylist...)

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
	catalog.pruneUnusedRegistries()
	return catalog
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
					Marker:   "image-mirroring-cli-snippet",
					Renderer: "image-mirroring-cli-snippet",
				},
			},
		},
		{Path: "docs/user/standalone-infrastructure.md"},
		{Path: "docs/user/standalone-core-services.md"},
		{Path: "docs/user/standalone-gateway.md"},
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
