// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type docsBuildReceipt struct {
	SchemaVersion    int              `json:"schema_version"`
	CanonicalCommit  string           `json:"canonical_commit"`
	RegistrySHA256   string           `json:"registry_sha256"`
	FernConfigSHA256 string           `json:"fern_config_sha256"`
	FernToolchain    json.RawMessage  `json:"fern_toolchain"`
	Editions         []editionRelease `json:"editions"`
}

func copyDocsTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), link))
			within, err := filepath.Rel(filepath.Dir(source), resolved)
			if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
				return fmt.Errorf("docs symlink escapes staged source: %s", path)
			}
			if filepath.IsAbs(link) {
				return fmt.Errorf("absolute docs symlink: %s", path)
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported docs source file %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func verifyPublishedDocsProduct(root string, release editionRelease, publisher umbrellaPublisher) error {
	manifest, data, err := umbrellaDocsProduct(root, release, false)
	if err != nil {
		return err
	}
	var remote umbrellaRelease
	if err := publisher.json("GET", "/releases/tags/"+umbrellaTag(release.Version), nil, &remote); err != nil {
		return err
	}
	if remote.Draft || remote.Prerelease || remote.Tag != umbrellaTag(release.Version) {
		return fmt.Errorf("docs product release must be publicly stable")
	}
	if err := publisher.verifyTag(remote.Tag, release.Umbrella.ProductCommit, false); err != nil {
		return err
	}
	assets, err := publisher.assets(remote.ID)
	if err != nil {
		return err
	}
	required := []string{"manifest.json", "release-assets.json", "inventory.json", umbrellaBundleName(release.Version)}
	for _, stack := range manifest.Stacks {
		required = append(required, stack.InventoryAsset)
	}
	temporary, err := os.MkdirTemp("", "nvcf-docs-product-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	for _, name := range required {
		asset, ok := assets[name]
		if !ok || asset.State != "uploaded" {
			return fmt.Errorf("docs product release is missing %s", name)
		}
		content, err := publisher.request("GET", fmt.Sprintf("%s/repos/%s/releases/assets/%d", publisher.api, publisher.repository, asset.ID), "download", nil)
		if err != nil {
			return err
		}
		if name == "manifest.json" && !bytes.Equal(content, data) {
			return fmt.Errorf("published product manifest differs from docs binding")
		}
		if err := os.WriteFile(filepath.Join(temporary, name), content, 0o644); err != nil {
			return err
		}
	}
	_, _, err = readUmbrellaReleaseAssets(temporary)
	return err
}

func runDocsMode(args []string) error {
	flags := flag.NewFlagSet("docs-mode", flag.ContinueOnError)
	root := flags.String("repo", ".", "canonical source repository")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected docs-mode arguments")
	}
	registry, err := readEditionRegistry(*root)
	if err != nil {
		return err
	}
	for _, release := range registry.Editions {
		if release.Mode == "umbrella" {
			fmt.Println("umbrella")
			return nil
		}
	}
	fmt.Println("legacy")
	return nil
}

func runDocsStage(args []string) error {
	flags := flag.NewFlagSet("docs-stage", flag.ContinueOnError)
	root := flags.String("repo", ".", "canonical source repository")
	out := flags.String("out", "", "new isolated staged build directory")
	local := flags.Bool("local-refs", false, "fixture refs only; skips public asset verification")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *out == "" {
		return fmt.Errorf("docs-stage requires --out")
	}
	return stageCanonicalDocs(*root, *out, !*local)
}

func stageCanonicalDocs(root, out string, remote bool) error {
	registry, err := readEditionRegistry(root)
	if err != nil {
		return err
	}
	if err := validateEditionRegistry(root, remote); err != nil {
		return err
	}
	mixed := false
	for _, release := range registry.Editions {
		if release.Mode == "umbrella" {
			mixed = true
			if remote {
				p := umbrellaPublisher{client: &http.Client{Timeout: 2 * time.Minute}, api: "https://api.github.com", repository: "NVIDIA/nvcf"}
				if err := verifyPublishedDocsProduct(root, release, p); err != nil {
					return err
				}
			}
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(absolute, out)
	if err != nil || relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..") {
		return fmt.Errorf("docs-stage output must be outside the source repository")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		return fmt.Errorf("docs-stage output must not exist")
	}
	canonical, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(out), ".docs-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	staged := filepath.Join(temporary, "source")
	if _, err := gitOutput(root, "clone", "--quiet", "--shared", "--no-checkout", root, staged); err != nil {
		return err
	}
	origin, err := gitOutput(root, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if _, err := gitOutput(staged, "remote", "set-url", "origin", trimOutput(origin)); err != nil {
		return err
	}
	for _, directory := range []string{"docs", "fern"} {
		if err := copyDocsTree(filepath.Join(root, directory), filepath.Join(staged, directory)); err != nil {
			return err
		}
	}
	configData, err := os.ReadFile(filepath.Join(staged, "fern/docs.yml"))
	if err != nil {
		return err
	}
	if mixed {
		if err := materializeDocsVersions(root, staged, registry, configData); err != nil {
			return err
		}
	}
	registryData, err := os.ReadFile(filepath.Join(root, "fern/editions.yml"))
	if err != nil {
		return err
	}
	pin, err := os.ReadFile(filepath.Join(root, "fern/fern.config.json"))
	if err != nil {
		return err
	}
	if !json.Valid(pin) {
		return fmt.Errorf("invalid Fern toolchain pin")
	}
	receipt := docsBuildReceipt{SchemaVersion: 1, CanonicalCommit: trimOutput(canonical), RegistrySHA256: umbrellaDigest(registryData), FernConfigSHA256: umbrellaDigest(configData), FernToolchain: pin, Editions: registry.Editions}
	data, err := umbrellaJSON(receipt)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staged, "docs-build-receipt.json"), data, 0o644); err != nil {
		return err
	}
	return os.Rename(staged, out)
}

func materializeDocsVersions(root, staged string, registry *editionRegistry, configData []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(configData, &document); err != nil {
		return err
	}
	if len(document.Content) != 1 {
		return fmt.Errorf("invalid canonical docs configuration")
	}
	mapping := document.Content[0]
	var versions *yaml.Node
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "versions" {
			versions = mapping.Content[i+1]
		}
	}
	if versions == nil || versions.Kind != yaml.SequenceNode {
		return fmt.Errorf("canonical docs require version navigation")
	}
	releases := map[string]editionRelease{}
	for _, release := range registry.Editions {
		releases[release.Ref] = release
	}
	for _, version := range versions.Content {
		ref := ""
		slug := ""
		for i := 0; i < len(version.Content); i += 2 {
			if version.Content[i].Value == "ref" {
				ref = version.Content[i+1].Value
			}
			if version.Content[i].Value == "slug" {
				slug = version.Content[i+1].Value
			}
		}
		if ref == "" {
			continue
		}
		release, ok := releases[ref]
		if !ok {
			return fmt.Errorf("unapproved docs source %s", ref)
		}
		if slug != release.Version {
			return fmt.Errorf("registered docs slug must remain %s", release.Version)
		}
		source, err := os.MkdirTemp("", "nvcf-docs-version-")
		if err != nil {
			return err
		}
		err = materializeDocsVersion(root, staged, source, release, version)
		os.RemoveAll(source)
		if err != nil {
			return err
		}
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(staged, "fern/docs.yml"), encoded.Bytes(), 0o644)
}

func materializeDocsVersion(root, staged, temporary string, release editionRelease, version *yaml.Node) error {
	source := filepath.Join(temporary, "source")
	if err := stageEditionSource(root, source, release.Commit); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(source, "fern/docs.yml"))
	if err != nil {
		return err
	}
	var config struct {
		Versions []struct {
			Path string `yaml:"path"`
		} `yaml:"versions"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}
	if len(config.Versions) == 0 || !safeFernPath(config.Versions[0].Path) {
		return fmt.Errorf("approved docs version lacks local navigation")
	}
	prefix := filepath.Join("versions", release.Version)
	for _, directory := range []string{"docs", "fern"} {
		if err := copyDocsTree(filepath.Join(source, directory), filepath.Join(staged, "fern", prefix, directory)); err != nil {
			return err
		}
	}
	for i := 0; i < len(version.Content); i += 2 {
		if version.Content[i].Value == "ref" {
			version.Content[i].Value = "path"
			version.Content[i+1].Value = filepath.ToSlash(filepath.Join(prefix, "fern", config.Versions[0].Path))
		}
	}
	return nil
}
