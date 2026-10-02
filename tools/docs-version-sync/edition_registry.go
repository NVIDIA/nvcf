// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// editionRegistry pins the commits of branch-backed editions outside those commits.
type editionRegistry struct {
	SchemaVersion int              `yaml:"schema_version"`
	Default       string           `yaml:"default,omitempty"`
	Editions      []editionRelease `yaml:"editions"`
}

// editionRelease is the source identity of one published docs edition.
type editionRelease struct {
	Version string `yaml:"version"`
	Ref     string `yaml:"ref"`
	Commit  string `yaml:"commit"`
}

func readEditionRegistry(root string) (*editionRegistry, error) {
	data, err := os.ReadFile(filepath.Join(root, "fern", "editions.yml"))
	if os.IsNotExist(err) {
		return &editionRegistry{SchemaVersion: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var registry editionRegistry
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&registry); err != nil {
		return nil, err
	}
	if registry.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported edition registry schema %d", registry.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, release := range registry.Editions {
		if !validStableStackVersion(release.Version) || release.Ref != "docs/releases/"+release.Version || !fullLowercaseCommitSHARe.MatchString(release.Commit) {
			return nil, fmt.Errorf("invalid edition registry identity: %+v", release)
		}
		if seen[release.Version] {
			return nil, fmt.Errorf("duplicate docs edition %s", release.Version)
		}
		seen[release.Version] = true
	}
	if len(registry.Editions) > 0 && !seen[registry.Default] {
		return nil, fmt.Errorf("edition registry default %q is not registered", registry.Default)
	}
	return &registry, nil
}

func decodeEditionCatalog(data []byte) (*Catalog, error) {
	var catalog Catalog
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&catalog); err != nil {
		return nil, err
	}
	if err := ValidateCatalog(&catalog); err != nil {
		return nil, err
	}
	return &catalog, nil
}

func catalogAtCommit(root, commit string) (*Catalog, error) {
	data, err := gitOutput(root, "show", commit+":docs/version-catalog/main.yaml")
	if err != nil {
		return nil, err
	}
	return decodeEditionCatalog(data)
}

func verifyEditionRef(root string, release editionRelease, remote bool) (*Catalog, error) {
	if remote {
		data, err := gitOutput(root, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+release.Ref)
		if err != nil {
			return nil, fmt.Errorf("edition %s: missing or inaccessible branch %s: %w", release.Version, release.Ref, err)
		}
		fields := strings.Fields(string(data))
		if len(fields) != 2 || fields[0] != release.Commit || fields[1] != "refs/heads/"+release.Ref {
			return nil, fmt.Errorf("edition %s: branch %s moved; expected %s, received %q", release.Version, release.Ref, release.Commit, trimOutput(data))
		}
		if _, err := gitOutput(root, "fetch", "--no-tags", "origin", "refs/heads/"+release.Ref); err != nil {
			return nil, err
		}
		fetched, err := gitOutput(root, "rev-parse", "FETCH_HEAD^{commit}")
		if err != nil || trimOutput(fetched) != release.Commit {
			return nil, fmt.Errorf("edition %s branch moved while fetching", release.Version)
		}
	} else {
		resolved, err := gitOutput(root, "rev-parse", "--verify", "refs/heads/"+release.Ref+"^{commit}")
		if err != nil || trimOutput(resolved) != release.Commit {
			return nil, fmt.Errorf("edition %s: local branch %s does not match %s", release.Version, release.Ref, release.Commit)
		}
	}
	catalog, err := catalogAtCommit(root, release.Commit)
	if err != nil {
		return nil, err
	}
	if catalog.DocsEdition == nil || catalog.DocsEdition.Version != release.Version || catalog.DocsEdition.Status != ReleaseSetQualified {
		return nil, fmt.Errorf("edition %s: branch must contain its matching qualified catalog", release.Version)
	}
	if err := validateEditionSources(root, catalog); err != nil {
		return nil, fmt.Errorf("edition %s: %w", release.Version, err)
	}
	config, err := gitOutput(root, "show", release.Commit+":fern/docs.yml")
	if err != nil {
		return nil, err
	}
	if err := validateBranchNavigation(root, release.Commit, config); err != nil {
		return nil, fmt.Errorf("edition %s: %w", release.Version, err)
	}
	if err := validateEditionContent(root, release, catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validateEditionContent(root string, release editionRelease, catalog *Catalog) error {
	temporary, err := os.MkdirTemp("", "nvcf-edition-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	staged := filepath.Join(temporary, "source")
	if err := stageEditionSource(root, staged, release.Commit); err != nil {
		return err
	}
	if err := SyncDocs(staged, catalog, true); err != nil {
		return fmt.Errorf("edition %s: %w", release.Version, err)
	}
	command := exec.Command(filepath.Join(root, "tools", "ci", "run-fern"), "check")
	command.Dir = staged
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("edition %s content validation: %w\n%s", release.Version, err, output)
	}
	return nil
}

func validateBranchNavigation(root, commit string, data []byte) error {
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}
	if _, ok := config["products"]; ok {
		return fmt.Errorf("release branch must select edition navigation, not products")
	}
	versions, ok := config["versions"].([]any)
	if !ok || len(versions) != 1 {
		return fmt.Errorf("release branch must have exactly one local default version")
	}
	version, ok := versions[0].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid branch version configuration")
	}
	if _, ok := version["ref"]; ok {
		return fmt.Errorf("release branch default must use path, never ref")
	}
	nav, ok := version["path"].(string)
	if !ok || !safeFernPath(nav) {
		return fmt.Errorf("release branch default path must stay under fern/")
	}
	if _, err := gitOutput(root, "show", commit+":fern/"+filepath.ToSlash(filepath.Clean(nav))); err != nil {
		return fmt.Errorf("release navigation %s is unavailable: %w", nav, err)
	}
	return nil
}

func safeFernPath(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func validateEditionRegistry(root string, remote bool) error {
	registry, err := readEditionRegistry(root)
	if err != nil {
		return err
	}
	catalogs := map[string]*Catalog{}
	for _, release := range registry.Editions {
		catalog, err := verifyEditionRef(root, release, remote)
		if err != nil {
			return err
		}
		catalogs[release.Version] = catalog
	}
	for _, catalog := range catalogs {
		var previous *Catalog
		if version := catalog.DocsEdition.PreviousVersion; version != "" {
			previous = catalogs[version]
			if previous == nil {
				return fmt.Errorf("edition %s: predecessor %s is not registered", catalog.DocsEdition.Version, version)
			}
		}
		if err := validateEditionTransition(previous, catalog); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "fern", "docs.yml"))
	if err != nil {
		return err
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}
	versions, _ := config["versions"].([]any)
	registered := map[string]bool{}
	for _, release := range registry.Editions {
		registered[release.Ref] = true
	}
	for _, item := range versions {
		version, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid Fern version entry")
		}
		if ref, ok := version["ref"].(string); ok && !registered[ref] {
			return fmt.Errorf("Fern version ref %s has no pinned edition registry entry", ref)
		}
	}
	return nil
}

func registerEdition(root string, release editionRelease, remote bool) error {
	if !validStableStackVersion(release.Version) || release.Ref != "docs/releases/"+release.Version || !fullLowercaseCommitSHARe.MatchString(release.Commit) {
		return fmt.Errorf("registration requires an exact edition, docs/releases/X.Y.Z branch, and full commit SHA")
	}
	registry, err := readEditionRegistry(root)
	if err != nil {
		return err
	}
	for _, existing := range registry.Editions {
		if existing.Version != release.Version {
			continue
		}
		if existing != release {
			return fmt.Errorf("refusing to overwrite registered docs edition %s", release.Version)
		}
		_, err := verifyEditionRef(root, release, remote)
		return err
	}
	catalog, err := verifyEditionRef(root, release, remote)
	if err != nil {
		return err
	}
	var previous *Catalog
	if registry.Default != "" {
		for _, entry := range registry.Editions {
			if entry.Version == registry.Default {
				previous, err = catalogAtCommit(root, entry.Commit)
				if err != nil {
					return err
				}
			}
		}
	}
	if err := validateEditionTransition(previous, catalog); err != nil {
		return err
	}
	registry.Editions = append([]editionRelease{release}, registry.Editions...)
	registry.Default = release.Version
	data, err := yaml.Marshal(registry)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "fern", "editions.yml"), data, 0o644)
}
