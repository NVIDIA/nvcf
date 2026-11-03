// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProductCommit and ManifestSHA256 are immutable. Commit selects a reviewed docs revision.
type umbrellaDocsBinding struct {
	ProductCommit  string   `yaml:"product_commit"`
	ManifestSHA256 string   `yaml:"manifest_sha256"`
	Revisions      []string `yaml:"revisions"`
}

func validateDocsRegistryIdentity(release editionRelease) error {
	if !validStableStackVersion(release.Version) || !fullLowercaseCommitSHARe.MatchString(release.Commit) {
		return fmt.Errorf("invalid edition registry identity: %+v", release)
	}
	if release.Mode == "" && release.Umbrella == nil && release.Ref == "docs/releases/"+release.Version {
		return nil
	}
	if release.Mode != "umbrella" || release.Ref != "docs/versions/"+release.Version || release.Umbrella == nil {
		return fmt.Errorf("invalid docs registry mode or ref for %s", release.Version)
	}
	binding := release.Umbrella
	if !fullLowercaseCommitSHARe.MatchString(binding.ProductCommit) || !lowercaseSHA256DigestRe.MatchString(binding.ManifestSHA256) || !slices.Contains(binding.Revisions, release.Commit) {
		return fmt.Errorf("umbrella docs require product identity and an approved revision")
	}
	seen := map[string]bool{}
	for _, commit := range binding.Revisions {
		if !fullLowercaseCommitSHARe.MatchString(commit) || seen[commit] {
			return fmt.Errorf("invalid or duplicate approved docs revision")
		}
		seen[commit] = true
	}
	return nil
}

func umbrellaDocsProduct(root string, release editionRelease, remote bool) (*umbrellaManifest, []byte, error) {
	if err := validateDocsRegistryIdentity(release); err != nil {
		return nil, nil, err
	}
	if release.Umbrella == nil {
		return nil, nil, fmt.Errorf("umbrella docs require a product binding")
	}
	if remote {
		if _, err := gitOutput(root, "fetch", "--no-tags", "origin", "refs/tags/"+umbrellaTag(release.Version)); err != nil {
			return nil, nil, err
		}
		resolved, err := gitOutput(root, "rev-parse", "FETCH_HEAD^{commit}")
		if err != nil || trimOutput(resolved) != release.Umbrella.ProductCommit {
			return nil, nil, fmt.Errorf("umbrella product tag moved")
		}
	}
	tag, err := gitOutput(root, "rev-parse", "refs/tags/"+umbrellaTag(release.Version)+"^{commit}")
	if err != nil || trimOutput(tag) != release.Umbrella.ProductCommit {
		return nil, nil, fmt.Errorf("umbrella product tag must match registered product commit")
	}
	data, err := gitOutput(root, "show", release.Umbrella.ProductCommit+":"+umbrellaManifestPath(release.Version))
	if err != nil {
		return nil, nil, err
	}
	manifest, err := parseUmbrellaManifest(data)
	if err != nil {
		return nil, nil, err
	}
	if manifest.Version != release.Version || manifest.Status != ReleaseSetQualified || umbrellaDigest(data) != release.Umbrella.ManifestSHA256 {
		return nil, nil, fmt.Errorf("umbrella product identity or qualification mismatch")
	}
	return manifest, data, nil
}

func validateUmbrellaDocsCatalogIdentity(manifest *umbrellaManifest, catalog *Catalog) error {
	if !reflect.DeepEqual(catalog.DocsEdition, manifest.catalog().DocsEdition) {
		return fmt.Errorf("docs revision changed umbrella version or qualification")
	}
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		locked := manifest.Stacks[name]
		if stack.Version != locked.Version || stack.SourceTag != locked.SourceTag || stack.SourceCommit != locked.SourceCommit || stack.InventoryAsset != locked.InventoryAsset {
			return fmt.Errorf("docs revision changed %s product inputs", name)
		}
	}
	return nil
}

func umbrellaDocsCatalog(root string, manifest *umbrellaManifest, data []byte) (*Catalog, error) {
	if manifest.SchemaVersion == 2 {
		return nil, fmt.Errorf("maintained docs preparation and activation for release-local schema 2 manifests are not yet supported; retain the existing docs edition until selected-inventory catalog support is available (https://github.com/NVIDIA/nvcf/issues/2798)")
	}
	catalog, err := catalogAtCommit(root, manifest.SourceCommit)
	if err != nil {
		return nil, err
	}
	catalog.DocsEdition = manifest.catalog().DocsEdition
	catalog.UmbrellaManifestSHA256 = umbrellaDigest(data)
	if err := validateUmbrellaDocsCatalogIdentity(manifest, catalog); err != nil {
		return nil, err
	}
	return catalog, ValidateCatalog(catalog)
}

func validateUmbrellaDocsCatalog(root string, manifest *umbrellaManifest, data []byte, catalog *Catalog) error {
	expected, err := umbrellaDocsCatalog(root, manifest, data)
	if err != nil {
		return err
	}
	if err := validateUmbrellaDocsCatalogIdentity(manifest, catalog); err != nil {
		return err
	}
	if err := validateUmbrellaDocsOutputs(expected.Outputs, catalog.Outputs); err != nil {
		return err
	}
	// Presentation may evolve; release inputs and published artifact locations cannot.
	actual := *catalog
	actual.Outputs = nil
	expected.Outputs = nil
	actual.Manifest.Entries = append([]ManifestEntry(nil), catalog.Manifest.Entries...)
	for _, c := range []*Catalog{expected, &actual} {
		for i := range c.Manifest.Entries {
			c.Manifest.Entries[i].Description = ""
			c.Manifest.Entries[i].GitHubURL = ""
			c.Manifest.Entries[i].UpstreamURL = ""
		}
	}
	equal, err := catalogsEqual(expected, &actual)
	if err != nil {
		return err
	}
	if !equal {
		return fmt.Errorf("docs revision changed immutable product catalog inputs")
	}
	return nil
}

func validateUmbrellaDocsOutputs(expected, actual []OutputFile) error {
	paths := make(map[string]bool)
	blocks := make(map[OutputBlock]int)
	for _, output := range actual {
		if paths[output.Path] {
			return fmt.Errorf("docs revision repeats generated output %s", output.Path)
		}
		paths[output.Path] = true
		for _, block := range output.Blocks {
			blocks[block]++
		}
	}
	for _, output := range expected {
		// Inline renderers select by path, so those paths must remain registered.
		if (len(output.Blocks) == 0 || inlineVersionSyncer(output.Path) != nil) && !paths[output.Path] {
			return fmt.Errorf("docs revision removed required output %s", output.Path)
		}
		for _, block := range output.Blocks {
			if blocks[block] == 0 {
				return fmt.Errorf("docs revision removed required output block %s (%s)", block.Marker, block.Renderer)
			}
			blocks[block]--
		}
	}
	return nil
}

func verifyUmbrellaDocsRef(root string, release editionRelease, remote bool) (*Catalog, error) {
	manifest, data, err := umbrellaDocsProduct(root, release, remote)
	if err != nil {
		return nil, err
	}
	branch := "refs/heads/" + release.Ref
	if remote {
		if _, err := gitOutput(root, "fetch", "--no-tags", "origin", branch); err != nil {
			return nil, err
		}
		branch = "FETCH_HEAD"
	}
	if _, err := gitOutput(root, "merge-base", "--is-ancestor", release.Commit, branch); err != nil {
		return nil, fmt.Errorf("approved docs commit is missing from maintained branch %s: %w", release.Ref, err)
	}
	catalog, err := catalogAtCommit(root, release.Commit)
	if err != nil {
		return nil, err
	}
	if err := validateUmbrellaDocsCatalog(root, manifest, data, catalog); err != nil {
		return nil, err
	}
	embedded, err := gitOutput(root, "show", release.Commit+":docs/umbrella-manifest.json")
	if err != nil || !bytes.Equal(data, embedded) {
		return nil, fmt.Errorf("docs source changed its bound product manifest")
	}
	if err := validateEditionSources(root, catalog); err != nil {
		return nil, err
	}
	config, err := gitOutput(root, "show", release.Commit+":fern/docs.yml")
	if err != nil {
		return nil, err
	}
	if err := validateBranchNavigation(root, release.Commit, config); err != nil {
		return nil, err
	}
	if err := validateEditionContent(root, release, catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func reviseUmbrellaDocs(root string, release editionRelease, remote bool) error {
	if err := validateDocsRegistryIdentity(release); err != nil {
		return err
	}
	if release.Umbrella == nil {
		return fmt.Errorf("docs revision requires an umbrella product binding")
	}
	registry, err := readEditionRegistry(root)
	if err != nil {
		return err
	}
	index := -1
	for i, existing := range registry.Editions {
		if existing.Version != release.Version {
			continue
		}
		index = i
		if existing.Mode != "umbrella" || existing.Ref != release.Ref || existing.Umbrella.ProductCommit != release.Umbrella.ProductCommit || existing.Umbrella.ManifestSHA256 != release.Umbrella.ManifestSHA256 {
			return fmt.Errorf("cannot change a registered product identity or legacy edition")
		}
		release.Umbrella.Revisions = append([]string(nil), existing.Umbrella.Revisions...)
	}
	if !slices.Contains(release.Umbrella.Revisions, release.Commit) {
		release.Umbrella.Revisions = append(release.Umbrella.Revisions, release.Commit)
	}
	if _, err := verifyUmbrellaDocsRef(root, release, remote); err != nil {
		return err
	}
	if index < 0 {
		registry.Editions = append(registry.Editions, release)
		if registry.Default == "" {
			registry.Default = release.Version
		}
	} else {
		registry.Editions[index] = release
	}
	data, err := yaml.Marshal(registry)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "fern/editions.yml"), data, 0o644)
}

func runUmbrellaDocs(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	root := flags.String("repo", ".", "canonical repository")
	version := flags.String("version", "", "umbrella product version")
	product := flags.String("product-commit", "", "full immutable product tag commit")
	digest := flags.String("manifest-sha256", "", "trusted product manifest digest")
	commit := flags.String("commit", "", "full reviewed docs source commit")
	out := flags.String("out", "", "new docs preparation directory outside the repository")
	local := flags.Bool("local-refs", false, "local fixture refs only; publication checks origin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected docs arguments")
	}
	release := editionRelease{Version: *version, Ref: "docs/versions/" + *version, Commit: *commit, Mode: "umbrella", Umbrella: &umbrellaDocsBinding{ProductCommit: *product, ManifestSHA256: *digest, Revisions: []string{*commit}}}
	switch command {
	case "docs-revise":
		if *out != "" {
			return fmt.Errorf("docs-revise does not take --out")
		}
		return reviseUmbrellaDocs(*root, release, !*local)
	case "docs-prepare":
		return prepareUmbrellaDocs(*root, *out, release, !*local)
	default:
		return fmt.Errorf("unknown umbrella docs command %s", command)
	}
}

func prepareUmbrellaDocs(root, out string, release editionRelease, remote bool) error {
	if out == "" {
		return fmt.Errorf("docs preparation requires --out")
	}
	manifest, data, err := umbrellaDocsProduct(root, release, remote)
	if err != nil {
		return err
	}
	catalog, err := umbrellaDocsCatalog(root, manifest, data)
	if err != nil {
		return err
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
		return fmt.Errorf("docs preparation must use a new directory outside the source repository")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		return fmt.Errorf("docs preparation output must not exist")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(out), ".umbrella-docs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	staged := filepath.Join(temporary, "source")
	if err := stageEditionSource(root, staged, release.Commit); err != nil {
		return err
	}
	if err := WriteCatalog(filepath.Join(staged, "docs/version-catalog/main.yaml"), catalog); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staged, "docs/umbrella-manifest.json"), data, 0o644); err != nil {
		return err
	}
	if err := SyncDocs(staged, catalog, false); err != nil {
		return err
	}
	if err := writeBranchConfiguration(staged, editionPreparation{Version: release.Version, Navigation: "navigation.yml"}); err != nil {
		return err
	}
	if err := stageEditionLinks(staged, "navigation.yml"); err != nil {
		return err
	}
	return os.Rename(staged, out)
}
