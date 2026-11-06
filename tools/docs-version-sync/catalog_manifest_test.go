// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManifestCatalogUsesBoundInventoriesWithoutPublicationClaims(t *testing.T) {
	repo, path, directory, manifest := catalogManifestFixture(t)
	base := testCatalog()
	base.Compatibility = []CompatibilityEntry{{Stack: "control-plane", Version: "0.5.0", CompatibleWith: map[string]string{"compute-plane": "0.5.0+", "observability": "0.5.0+"}}}
	before := append([]CompatibilityEntry(nil), base.Compatibility...)
	// Any accidental release discovery must fail, including "latest" lookups.
	t.Setenv("DOC_VERSION_SYNC_GITHUB_API_URL", "http://127.0.0.1:1")
	catalog, err := updateCatalogFromManifest(repo, path, directory, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range releaseSetStackNames {
		got, _ := catalog.ReleaseSet.Stacks.byName(name)
		want := manifest.Stacks[name]
		if got.Version != want.Version || got.SourceTag != want.SourceTag || got.SourceCommit != want.SourceCommit || got.Status != ReleaseSetDevelopment {
			t.Fatalf("%s selection = %+v, want %+v in development", name, got, want)
		}
	}
	if !reflect.DeepEqual(catalog.Compatibility, manifest.catalog().Compatibility) || !reflect.DeepEqual(base.Compatibility, before) {
		t.Fatal("manifest peer pins were widened or the input catalog was changed")
	}
	pylon, ok := catalog.findArtifact("pylon")
	if !ok || !catalog.publicationIsPending(pylon) || len(catalog.Publications) != 0 {
		t.Fatal("local inventories must not establish public registry publication")
	}
	cli, ok := catalog.findArtifact("nvcf-cli")
	if !ok || cli.Version != "0.0.30" {
		t.Fatal("independent CLI selection was lost")
	}
	base.DocsEdition = &DocsEdition{Status: ReleaseSetQualified}
	if _, err := updateCatalogFromManifest(repo, path, directory, base); err == nil {
		t.Fatal("qualified edition was accepted for refresh")
	}
}

func TestManifestCatalogRejectsChangedInventoryAndSourceTag(t *testing.T) {
	for _, change := range []string{"inventory", "source-tag"} {
		t.Run(change, func(t *testing.T) {
			repo, path, directory, manifest := catalogManifestFixture(t)
			if change == "inventory" {
				asset := filepath.Join(directory, manifest.Stacks["control-plane"].InventoryAsset)
				file, err := os.OpenFile(asset, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString("\n")
				if closeErr := file.Close(); err != nil || closeErr != nil {
					t.Fatalf("modify inventory: %v, %v", err, closeErr)
				}
			} else {
				stack := manifest.Stacks["compute-plane"]
				writeFile(t, filepath.Join(repo, "new-source"), "changed source")
				editionGit(t, repo, "add", ".")
				editionGit(t, repo, "commit", "-m", "later source")
				editionGit(t, repo, "tag", "-f", stack.SourceTag)
			}
			_, err := updateCatalogFromManifest(repo, path, directory, testCatalog())
			if err == nil || (!strings.Contains(err.Error(), "checksum mismatch") && !strings.Contains(err.Error(), "source tag does not resolve")) {
				t.Fatalf("changed immutable input accepted: %v", err)
			}
		})
	}
}

func TestManifestCatalogFlagsRequireExplicitBoundInputs(t *testing.T) {
	for _, args := range [][]string{
		{"--release-manifest", "manifest.json", "--inventories", "assets"},
		{"--update-catalog", "--release-manifest", "manifest.json"},
		{"--update-catalog", "--inventories", "assets"},
		{"--update-catalog", "--release-manifest", "manifest.json", "--inventories", "assets", "--stack-version", "latest"},
		{"--update-catalog", "--release-manifest", "manifest.json", "--inventories", "assets", "--generate-stack-inventory", "out.json"},
	} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "--release-manifest") {
			t.Fatalf("invalid flags %v: %v", args, err)
		}
	}
}

func catalogManifestFixture(t *testing.T) (string, string, string, *umbrellaManifest) {
	t.Helper()
	repo := initTestGitRepo(t)
	self := commitTestStackSource(t, repo, "1.2.3", testCatalogPinSources())
	manifest := &umbrellaManifest{SchemaVersion: 2, Version: "1.0.4", Change: "initial", Status: ReleaseSetDevelopment, SourceCommit: self.Commit,
		Toolchain: umbrellaToolchain{Helm: "3.21.4", Helmfile: "1.1.9"}, Stacks: map[string]umbrellaStack{}}
	directory := t.TempDir()
	full := testCatalogResolvedInventory(t, self)
	for index, plane := range releaseSetStackNames {
		key, _ := stackKeyForPlane(plane)
		spec, _ := stackInventorySpecByKey(key)
		version := []string{"1.2.3", "2.3.4", "3.4.5"}[index]
		source := stackSourceRelease{Version: version, Tag: spec.TagPrefix + version, Commit: self.Commit}
		if source.Tag != self.Tag {
			editionGit(t, repo, "tag", source.Tag)
		}
		inventory := filterResolvedInventoryByPlane(t, full, source, plane)
		data, err := marshalResolvedStackInventory(inventory)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(directory, spec.AssetName), string(data))
		manifest.Stacks[plane] = umbrellaStack{Version: version, SourceTag: source.Tag, SourceCommit: source.Commit, InventoryAsset: spec.AssetName, InventorySHA256: umbrellaDigest(data)}
	}
	for name, stack := range manifest.Stacks {
		stack.CompatibleWith = map[string]string{}
		for peer, selected := range manifest.Stacks {
			if name != peer {
				stack.CompatibleWith[peer] = selected.Version
			}
		}
		manifest.Stacks[name] = stack
	}
	data, err := marshalUmbrellaManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "manifest.json")
	writeFile(t, path, string(data))
	return repo, path, directory, manifest
}
