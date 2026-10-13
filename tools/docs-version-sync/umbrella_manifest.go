// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// umbrellaManifest locks the product inputs independently of documentation revisions.
type umbrellaManifest struct {
	SchemaVersion   int                      `json:"schema_version"`
	Version         string                   `json:"version"`
	PreviousVersion string                   `json:"previous_version,omitempty"`
	Change          string                   `json:"change"`
	Status          ReleaseSetStatus         `json:"status"`
	Qualification   string                   `json:"qualification,omitempty"`
	SourceCommit    string                   `json:"source_commit"`
	Toolchain       umbrellaToolchain        `json:"toolchain"`
	Stacks          map[string]umbrellaStack `json:"stacks"`
}

// umbrellaToolchain records the supported render tools for the selected combination.
type umbrellaToolchain struct {
	Helm     string `json:"helm"`
	Helmfile string `json:"helmfile"`
}

// umbrellaStack identifies one immutable component and its reviewed peer requirements.
type umbrellaStack struct {
	Version         string            `json:"version"`
	SourceTag       string            `json:"source_tag"`
	SourceCommit    string            `json:"source_commit"`
	InventoryAsset  string            `json:"inventory_asset"`
	InventorySHA256 string            `json:"inventory_sha256"`
	CompatibleWith  map[string]string `json:"compatible_with"`
}

func umbrellaTag(version string) string {
	// Existing component release workflows match *-v* and **/v*.
	return "nvcf/" + version
}

func umbrellaManifestPath(version string) string {
	return "deploy/releases/" + version + "/manifest.json"
}

func umbrellaDigest(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func parseUmbrellaManifest(data []byte) (*umbrellaManifest, error) {
	var manifest umbrellaManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode umbrella manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := validateUmbrellaManifest(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func loadUmbrellaManifest(path string) (*umbrellaManifest, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := parseUmbrellaManifest(data)
	return manifest, data, err
}

func marshalUmbrellaManifest(manifest *umbrellaManifest) ([]byte, error) {
	if err := validateUmbrellaManifest(manifest); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	return append(data, '\n'), err
}

func validateUmbrellaManifest(manifest *umbrellaManifest) error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("unsupported umbrella manifest schema %d", manifest.SchemaVersion)
	}
	if !fullLowercaseCommitSHARe.MatchString(manifest.SourceCommit) {
		return fmt.Errorf("umbrella source_commit must be a full reviewed commit SHA")
	}
	if !validStableStackVersion(manifest.Toolchain.Helm) || !strings.HasPrefix(manifest.Toolchain.Helm, "3.") {
		return fmt.Errorf("umbrella toolchain requires an exact Helm 3 version")
	}
	if !validStableStackVersion(manifest.Toolchain.Helmfile) || !strings.HasPrefix(manifest.Toolchain.Helmfile, "1.1.") {
		return fmt.Errorf("umbrella toolchain requires an exact Helmfile 1.1 version for sequential stages")
	}
	if len(manifest.Stacks) != len(releaseSetStackNames) {
		return fmt.Errorf("umbrella manifest requires exactly control-plane, compute-plane, and observability")
	}
	for _, name := range releaseSetStackNames {
		stack, ok := manifest.Stacks[name]
		if !ok || !validStableStackVersion(stack.Version) {
			return fmt.Errorf("umbrella manifest requires an exact stable %s version", name)
		}
		key, _ := stackKeyForPlane(name)
		spec, _ := stackInventorySpecByKey(key)
		if err := validateStackSourceReleaseForSpec(stack.source(), spec); err != nil {
			return fmt.Errorf("umbrella %s: %w", name, err)
		}
		if stack.InventoryAsset != spec.AssetName || !lowercaseSHA256DigestRe.MatchString(stack.InventorySHA256) {
			return fmt.Errorf("umbrella %s requires its canonical inventory asset and SHA-256 digest", name)
		}
		if len(stack.CompatibleWith) != len(releaseSetStackNames)-1 {
			return fmt.Errorf("umbrella %s requires exactly two peer compatibility declarations", name)
		}
		for _, peer := range releaseSetStackNames {
			if peer != name && stack.CompatibleWith[peer] == "" {
				return fmt.Errorf("umbrella %s requires compatibility with %s", name, peer)
			}
		}
	}
	// Use the established combination and SemVer rules without altering a docs catalog.
	if err := validateDocsEdition(manifest.catalog()); err != nil {
		return fmt.Errorf("umbrella release: %w", err)
	}
	return nil
}

func (stack umbrellaStack) source() stackSourceRelease {
	return stackSourceRelease{Version: stack.Version, Tag: stack.SourceTag, Commit: stack.SourceCommit}
}

func (manifest *umbrellaManifest) catalog() *Catalog {
	catalog := &Catalog{DocsEdition: &DocsEdition{
		Version: manifest.Version, PreviousVersion: manifest.PreviousVersion,
		Change: manifest.Change, Status: manifest.Status, Qualification: manifest.Qualification,
	}}
	for _, name := range releaseSetStackNames {
		stack := manifest.Stacks[name]
		metadata, _ := catalog.ReleaseSet.Stacks.byName(name)
		*metadata = StackReleaseMetadata{Version: stack.Version, SourceTag: stack.SourceTag,
			SourceCommit: stack.SourceCommit, InventoryAsset: stack.InventoryAsset}
		catalog.Compatibility = append(catalog.Compatibility, CompatibilityEntry{
			Stack: name, Version: stack.Version, CompatibleWith: stack.CompatibleWith,
		})
	}
	return catalog
}

func validateUmbrellaTransition(previous, current *umbrellaManifest) error {
	if previous == nil {
		if current.PreviousVersion != "" {
			return fmt.Errorf("umbrella %s requires its previous manifest %s", current.Version, current.PreviousVersion)
		}
		return validateEditionTransition(nil, current.catalog())
	}
	if previous.Status != ReleaseSetQualified {
		return fmt.Errorf("previous umbrella release must be qualified")
	}
	if err := validateEditionTransition(previous.catalog(), current.catalog()); err != nil {
		return fmt.Errorf("umbrella transition: %w", err)
	}
	for _, name := range releaseSetStackNames {
		oldStack, newStack := previous.Stacks[name], current.Stacks[name]
		if oldStack.Version == newStack.Version && (oldStack.source() != newStack.source() || oldStack.InventorySHA256 != newStack.InventorySHA256) {
			return fmt.Errorf("umbrella %s changes the immutable %s %s source or inventory", current.Version, name, oldStack.Version)
		}
	}
	return nil
}

func validateUmbrellaSources(root string, manifest *umbrellaManifest, inventoryDir string) (map[string]resolvedStackInventory, error) {
	catalog, err := catalogAtCommit(root, manifest.SourceCommit)
	if err != nil {
		return nil, fmt.Errorf("umbrella reviewed source: %w", err)
	}
	inventories := map[string]resolvedStackInventory{}
	for _, name := range releaseSetStackNames {
		stack := manifest.Stacks[name]
		reviewed, _ := catalog.ReleaseSet.Stacks.byName(name)
		if reviewed.Version != stack.Version || reviewed.SourceTag != stack.SourceTag || reviewed.SourceCommit != stack.SourceCommit || reviewed.InventoryAsset != stack.InventoryAsset {
			return nil, fmt.Errorf("umbrella %s differs from the reviewed catalog", name)
		}
		if err := validateUmbrellaCompatibilitySource(catalog, name, stack); err != nil {
			return nil, err
		}
		actual, err := gitOutput(root, "rev-parse", "--verify", "refs/tags/"+stack.SourceTag+"^{commit}")
		if err != nil {
			return nil, fmt.Errorf("umbrella %s source tag: %w", name, err)
		}
		if trimOutput(actual) != stack.SourceCommit {
			return nil, fmt.Errorf("umbrella %s source tag does not resolve to %s", name, stack.SourceCommit)
		}
		data, err := os.ReadFile(filepath.Join(inventoryDir, stack.InventoryAsset))
		if err != nil {
			return nil, err
		}
		if umbrellaDigest(data) != stack.InventorySHA256 {
			return nil, fmt.Errorf("umbrella %s inventory checksum mismatch", name)
		}
		inventory, err := parseResolvedStackInventory(data)
		if err != nil {
			return nil, fmt.Errorf("umbrella %s inventory: %w", name, err)
		}
		if inventory.Source != stack.source() {
			return nil, fmt.Errorf("umbrella %s inventory source differs from the selected release", name)
		}
		key, _ := stackKeyForPlane(name)
		spec, _ := stackInventorySpecByKey(key)
		if err := validateInventoryPlaneOwnership(spec, inventory); err != nil {
			return nil, err
		}
		for _, artifact := range inventory.Artifacts {
			for _, source := range artifact.Sources {
				if source.Plane != name {
					return nil, fmt.Errorf("umbrella %s inventory contains an artifact owned by %s", name, source.Plane)
				}
			}
		}
		inventories[key] = inventory
	}
	if _, err := mergeResolvedStackInventories(inventories); err != nil {
		return nil, err
	}
	return inventories, nil
}

func validateUmbrellaCompatibilitySource(catalog *Catalog, name string, stack umbrellaStack) error {
	for _, entry := range catalog.Compatibility {
		if entry.Stack != name || entry.Version != stack.Version {
			continue
		}
		if len(entry.CompatibleWith) != len(stack.CompatibleWith) {
			break
		}
		for peer, requirement := range stack.CompatibleWith {
			if entry.CompatibleWith[peer] != requirement {
				return fmt.Errorf("umbrella %s compatibility differs from the reviewed catalog", name)
			}
		}
		return nil
	}
	return fmt.Errorf("umbrella %s has no matching reviewed compatibility declaration", name)
}

func validatePublishedUmbrella(root string, manifest *umbrellaManifest, data []byte) error {
	ref := "refs/tags/" + umbrellaTag(manifest.Version)
	refs, err := gitOutput(root, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return err
	}
	if !strings.Contains("\n"+string(refs), "\n"+ref+"\n") {
		return nil
	}
	released, err := gitOutput(root, "show", ref+":"+umbrellaManifestPath(manifest.Version))
	if err != nil {
		return fmt.Errorf("published umbrella manifest is unavailable: %w", err)
	}
	if !bytes.Equal(data, released) {
		return fmt.Errorf("refusing to change published umbrella release %s", manifest.Version)
	}
	return nil
}

func writeImmutableUmbrellaFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("umbrella output must be a regular file: %s", path)
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("refusing to overwrite different umbrella output: %s", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
