// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DocsEdition versions one documented deployment contract and qualified stack combination.
// Qualification records human review; tooling never infers approval from released artifacts.
type DocsEdition struct {
	Version         string           `yaml:"version" json:"version"`
	Status          ReleaseSetStatus `yaml:"status" json:"status"`
	PreviousVersion string           `yaml:"previous_version,omitempty" json:"previous_version,omitempty"`
	Change          string           `yaml:"change" json:"change"`
	Qualification   string           `yaml:"qualification,omitempty" json:"qualification,omitempty"`
}

// editionManifest is the public machine-readable view of an edition's exact stack inputs.
type editionManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	Edition       DocsEdition             `json:"docs_edition"`
	Stacks        map[string]editionStack `json:"stacks"`
}

// editionStack identifies the artifact version and its source inventory without a docs version alias.
type editionStack struct {
	Version        string `json:"version"`
	SourceTag      string `json:"source_tag"`
	SourceCommit   string `json:"source_commit"`
	InventoryAsset string `json:"inventory_asset"`
	InventoryURL   string `json:"inventory_url"`
}

var qualificationPathRE = regexp.MustCompile(`^/NVIDIA/nvcf/(pull|issues)/[1-9][0-9]*$`)

func validateDocsEdition(catalog *Catalog) error {
	edition := catalog.DocsEdition
	if edition == nil {
		return nil
	}
	if err := validateEditionBump(edition.PreviousVersion, edition.Version, edition.Change); err != nil {
		return err
	}
	switch edition.Status {
	case ReleaseSetDevelopment:
		if edition.Qualification != "" {
			return fmt.Errorf("development docs edition must not claim qualification")
		}
	case ReleaseSetQualified:
		evidence, err := url.Parse(edition.Qualification)
		if err != nil || evidence.Scheme != "https" || evidence.Host != "github.com" || evidence.User != nil || !qualificationPathRE.MatchString(evidence.Path) {
			return fmt.Errorf("qualified docs edition requires a public NVIDIA/nvcf pull request or issue URL recording approval of the complete combination")
		}
	default:
		return fmt.Errorf("docs edition status must be development or qualified")
	}
	if catalog.ReleaseSet == (ReleaseSetMetadata{}) {
		return fmt.Errorf("docs edition requires all three release_set stacks")
	}
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		if !validStableStackVersion(stack.Version) {
			return fmt.Errorf("docs edition %s requires an exact stable %s stack version", edition.Version, name)
		}
	}
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		found := false
		for _, entry := range catalog.Compatibility {
			if entry.Stack != name || entry.Version != stack.Version {
				continue
			}
			found = true
			for _, other := range releaseSetStackNames {
				if other == name {
					continue
				}
				peer, _ := catalog.ReleaseSet.Stacks.byName(other)
				requirement := entry.CompatibleWith[other]
				minimum, open := strings.CutSuffix(requirement, "+")
				if !validStableStackVersion(minimum) || (open && compareVersions(peer.Version, minimum) < 0) || (!open && peer.Version != minimum) {
					return fmt.Errorf("docs edition %s: %s %s requires %s %s, selected %s", edition.Version, name, stack.Version, other, requirement, peer.Version)
				}
			}
		}
		if !found {
			return fmt.Errorf("docs edition %s has no compatibility declaration for %s %s", edition.Version, name, stack.Version)
		}
	}
	return nil
}

func validateEditionBump(previous, version, change string) error {
	if !validStableStackVersion(version) {
		return fmt.Errorf("docs edition version must use stable X.Y.Z SemVer")
	}
	if previous == "" {
		if change != "initial" {
			return fmt.Errorf("first docs edition must declare change: initial")
		}
		return nil
	}
	if !validStableStackVersion(previous) {
		return fmt.Errorf("previous docs edition must use stable X.Y.Z SemVer")
	}
	parts := strings.Split(previous, ".")
	index := -1
	switch change {
	case "major":
		index = 0
	case "minor":
		index = 1
	case "patch":
		index = 2
	}
	if index < 0 {
		return fmt.Errorf("docs edition change must be major, minor, or patch after the first edition")
	}
	number, _ := new(big.Int).SetString(parts[index], 10)
	parts[index] = number.Add(number, big.NewInt(1)).String()
	for i := index + 1; i < 3; i++ {
		parts[i] = "0"
	}
	expected := strings.Join(parts, ".")
	if version != expected {
		return fmt.Errorf("%s change after docs edition %s requires %s, got %s", change, previous, expected, version)
	}
	return nil
}

func validateEditionTransition(previous, current *Catalog) error {
	if previous == nil {
		return validateEditionBump("", current.DocsEdition.Version, current.DocsEdition.Change)
	}
	if previous.DocsEdition == nil {
		return fmt.Errorf("previous catalog has no docs edition")
	}
	if current.DocsEdition.PreviousVersion != previous.DocsEdition.Version {
		return fmt.Errorf("previous edition does not match the registered predecessor")
	}
	if err := validateEditionBump(previous.DocsEdition.Version, current.DocsEdition.Version, current.DocsEdition.Change); err != nil {
		return err
	}
	for _, name := range releaseSetStackNames {
		oldStack, _ := previous.ReleaseSet.Stacks.byName(name)
		newStack, _ := current.ReleaseSet.Stacks.byName(name)
		oldParts := strings.Split(oldStack.Version, ".")
		newParts := strings.Split(newStack.Version, ".")
		if compareVersions(newStack.Version, oldStack.Version) < 0 {
			return fmt.Errorf("edition regresses %s from %s to %s", name, oldStack.Version, newStack.Version)
		}
		if newParts[0] != oldParts[0] && current.DocsEdition.Change != "major" {
			return fmt.Errorf("%s major upgrade requires a docs edition major", name)
		}
		if newParts[0] == oldParts[0] && newParts[1] != oldParts[1] && current.DocsEdition.Change == "patch" {
			return fmt.Errorf("%s feature upgrade requires a docs edition minor or major", name)
		}
	}
	return nil
}

func manifestForEdition(catalog *Catalog) ([]byte, error) {
	if catalog.DocsEdition == nil {
		return nil, fmt.Errorf("catalog has no docs edition")
	}
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	manifest := editionManifest{SchemaVersion: 1, Edition: *catalog.DocsEdition, Stacks: map[string]editionStack{}}
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		manifest.Stacks[name] = editionStack{Version: stack.Version, SourceTag: stack.SourceTag, SourceCommit: stack.SourceCommit, InventoryAsset: stack.InventoryAsset, InventoryURL: "https://github.com/NVIDIA/nvcf/releases/download/" + stack.SourceTag + "/" + stack.InventoryAsset}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	return append(data, '\n'), err
}

func syncEditionManifest(repoRoot string, catalog *Catalog, check bool) error {
	if catalog.DocsEdition == nil {
		return nil
	}
	data, err := manifestForEdition(catalog)
	if err != nil {
		return err
	}
	path := filepath.Join(repoRoot, "docs", "edition-manifest.json")
	existing, readErr := os.ReadFile(path)
	if readErr == nil && string(existing) == string(data) {
		return nil
	}
	if check {
		return fmt.Errorf("%w: docs/edition-manifest.json does not match the edition catalog", ErrCheckFailed)
	}
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	return os.WriteFile(path, data, 0o644)
}

func validateEditionSources(repoRoot string, catalog *Catalog) error {
	if err := validateStackSourceSnapshot(repoRoot, catalog); err != nil {
		return err
	}
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		actual, err := gitOutput(repoRoot, "rev-parse", "--verify", "refs/tags/"+stack.SourceTag+"^{commit}")
		if err != nil {
			return fmt.Errorf("resolve %s inventory source: %w", name, err)
		}
		if trimOutput(actual) != stack.SourceCommit {
			return fmt.Errorf("%s source tag resolves to %s, expected %s", name, trimOutput(actual), stack.SourceCommit)
		}
	}
	return nil
}
