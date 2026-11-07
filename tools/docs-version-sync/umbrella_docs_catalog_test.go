// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// schema2DocsFixture separates provenance, product metadata, and docs revisions.
type schema2DocsFixture struct {
	root      string
	directory string
	manifest  *umbrellaManifest
	data      []byte
	release   editionRelease
	inputs    umbrellaDocsInputs
	selected  *Catalog
}

func newSchema2DocsFixture(t *testing.T) schema2DocsFixture {
	t.Helper()
	root, path, directory, manifest := catalogManifestFixture(t)
	base, err := updateCatalogFromManifest(root, path, directory, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	classified := map[string]bool{}
	for _, entry := range base.Manifest.Entries {
		classified[entry.ArtifactID] = true
	}
	for _, artifact := range base.Artifacts {
		if classified[artifact.catalogKey()] {
			continue
		}
		kind := ManifestKindServiceImage
		if artifact.Type == ArtifactTypeChart {
			kind = ManifestKindChart
		}
		plane := ManifestPlaneControl
		if len(artifact.Stacks) > 0 {
			switch artifact.Stacks[0] {
			case computePlaneStackKey:
				plane = ManifestPlaneCompute
			case observabilityStackKey:
				plane = ManifestPlaneObservability
			}
		}
		base.Manifest.Entries = append(base.Manifest.Entries, ManifestEntry{ArtifactID: artifact.catalogKey(), Plane: plane, Kind: kind, Description: "Selected fixture artifact."})
	}
	base.Outputs = []OutputFile{{Path: "docs/overview/generated.md", Blocks: []OutputBlock{{Marker: "manifest", Renderer: "manifest-artifact-registry-paths"}, {Marker: "compatibility", Renderer: "compatibility-matrix"}}}}
	base.DocsEdition = &DocsEdition{Version: "1.0.0", Status: ReleaseSetQualified, Change: "initial", Qualification: "https://github.com/dsx-ai-factory/nvcf/issues/2483"}
	writeFile(t, filepath.Join(root, "docs/page.md"), "# Legacy docs\n")
	writeFile(t, filepath.Join(root, "docs/overview/generated.md"), "{/* docs-version-sync:BEGIN manifest */}\nstale\n{/* docs-version-sync:END manifest */}\n{/* docs-version-sync:BEGIN compatibility */}\nstale\n{/* docs-version-sync:END compatibility */}\n")
	writeFile(t, filepath.Join(root, "fern/docs.yml"), "versions:\n  - display-name: '1.0.0'\n    path: navigation.yml\n")
	writeFile(t, filepath.Join(root, "fern/navigation.yml"), "navigation:\n  - page: Fixture\n    path: ../docs/page.md\n")
	writeFile(t, filepath.Join(root, "fern/fern.config.json"), "{\"version\":\"5.144.1\"}\n")
	writeFile(t, filepath.Join(root, "tools/ci/run-fern"), "#!/bin/sh\nset -eu\ntest \"$1\" = check\ntest -f docs/page.md\ntest -f docs/edition-manifest.json\n")
	if err := os.Chmod(filepath.Join(root, "tools/ci/run-fern"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteCatalog(filepath.Join(root, "docs/version-catalog/main.yaml"), base); err != nil {
		t.Fatal(err)
	}
	if err := SyncDocs(root, base, false); err != nil {
		t.Fatal(err)
	}
	editionGit(t, root, "add", ".")
	editionGit(t, root, "commit", "-m", "legacy edition")
	legacy := editionGit(t, root, "rev-parse", "HEAD")
	editionGit(t, root, "branch", "docs/releases/1.0.0")
	editionGit(t, root, "remote", "add", "origin", root)
	if err := registerEdition(root, editionRelease{Version: "1.0.0", Ref: "docs/releases/1.0.0", Commit: legacy}, false); err != nil {
		t.Fatal(err)
	}
	selected, err := LoadCatalog(filepath.Join(root, "docs/version-catalog/main.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The product tag contains Development metadata from a different selection.
	// Its source_commit predates any catalog, as in the real schema-2 release.
	base.DocsEdition = nil
	base.Compatibility = nil
	for i := range base.Artifacts {
		base.Artifacts[i].Version = "99.0.0"
	}
	pylon, ok := selected.findArtifact("pylon")
	if !ok {
		t.Fatal("missing fixture pylon")
	}
	base.VersionOverrides = []VersionOverride{{Name: "pylon", Type: pylon.Type, Version: "99.0.0"}, {Name: "nvcf-cli", Type: ArtifactTypeResource, Version: "0.0.30", Source: "independent shared CLI"}}
	base.Publications = []Publication{
		{Name: "pylon", Type: pylon.Type, Version: "99.0.0", Registry: pylon.Registry},
		{Name: "pylon", Type: pylon.Type, Version: pylon.Version, Registry: pylon.Registry},
		{Name: "nvcf-cli", Type: ArtifactTypeResource, Version: "0.0.30", Registry: defaultStackRegistry},
	}
	base.SupplementalArtifacts = append(base.SupplementalArtifacts,
		Artifact{Name: "unselected-runtime", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "99.0.0", Stacks: []string{computePlaneStackKey}},
		Artifact{Name: "unclassified-addon", Type: ArtifactTypeResource, Registry: defaultStackRegistry, Version: "99.0.0"},
		Artifact{Name: "disguised-runtime", Type: ArtifactTypeResource, Registry: defaultStackRegistry, Version: "99.0.0", Stacks: []string{computePlaneStackKey}},
	)
	base.Manifest.Entries = append(base.Manifest.Entries,
		ManifestEntry{ArtifactID: "unselected-runtime", Plane: ManifestPlaneCompute, Kind: ManifestKindServiceImage, Description: "Unselected runtime."},
		ManifestEntry{ArtifactID: "disguised-runtime", Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Still stack-owned."},
	)
	base.reconcilePublicationPending()
	if err := WriteCatalog(filepath.Join(root, "docs/version-catalog/main.yaml"), base); err != nil {
		t.Fatal(err)
	}
	manifest.Status = ReleaseSetQualified
	manifest.Qualification = "https://github.com/dsx-ai-factory/nvcf/issues/2483"
	data, err := marshalUmbrellaManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, umbrellaManifestPath(manifest.Version)), string(data))
	editionGit(t, root, "add", ".")
	editionGit(t, root, "commit", "-m", "qualified schema 2 product")
	product := editionGit(t, root, "rev-parse", "HEAD")
	editionGit(t, root, "tag", umbrellaTag(manifest.Version))
	release := editionRelease{Version: manifest.Version, Ref: "docs/versions/" + manifest.Version, Commit: product, Mode: "umbrella", Umbrella: &umbrellaDocsBinding{ProductCommit: product, ManifestSHA256: umbrellaDigest(data), Revisions: []string{product}}}
	inputs, err := umbrellaDocsPreparationInputs(root, release, manifest, directory, false)
	if err != nil {
		t.Fatal(err)
	}
	return schema2DocsFixture{root, directory, manifest, data, release, inputs, selected}
}

func TestSchema2UmbrellaDocsCatalogExactSelection(t *testing.T) {
	f := newSchema2DocsFixture(t)
	// The working catalog and network discovery cannot supply the pinned catalog.
	writeFile(t, filepath.Join(f.root, "docs/version-catalog/main.yaml"), "invalid Development catalog\n")
	t.Setenv("DOC_VERSION_SYNC_GITHUB_API_URL", "http://127.0.0.1:1")
	catalog, err := umbrellaDocsCatalog(f.root, f.manifest, f.data, f.inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range f.selected.Artifacts {
		got, ok := catalog.findArtifact(artifact.catalogKey())
		if !ok || got.Version != artifact.Version || got.Digest != artifact.Digest || !reflect.DeepEqual(got.Stacks, artifact.Stacks) {
			t.Fatalf("lost selected inventory artifact %s: %+v", artifact.catalogKey(), got)
		}
	}
	if !reflect.DeepEqual(catalog.ReleaseSet, f.selected.ReleaseSet) || !reflect.DeepEqual(catalog.Compatibility, f.manifest.catalog().Compatibility) || !reflect.DeepEqual(catalog.Outputs, f.selected.Outputs) {
		t.Fatal("lost source identity, exact compatibility, or generated outputs")
	}
	for _, name := range []string{"unselected-runtime", "unclassified-addon", "disguised-runtime"} {
		if _, ok := catalog.findArtifact(name); ok {
			t.Fatalf("unselected Development artifact survived: %s", name)
		}
	}
	if len(catalog.VersionOverrides) != 1 || catalog.VersionOverrides[0].Name != "nvcf-cli" || catalog.VersionOverrides[0].Source != "independent shared CLI" {
		t.Fatal("runtime override survived or independent provenance disappeared")
	}
	if len(catalog.Publications) != 2 {
		t.Fatalf("lost matching publication metadata: %+v", catalog.Publications)
	}
	for _, publication := range catalog.Publications {
		if publication.Version == "99.0.0" {
			t.Fatal("unselected publication survived")
		}
	}
	if err := validateUmbrellaDocsCatalog(f.root, f.manifest, f.data, catalog, f.inputs); err != nil {
		t.Fatal(err)
	}
	catalog.Artifacts[0].Version = "88.0.0"
	if err := validateUmbrellaDocsCatalog(f.root, f.manifest, f.data, catalog, f.inputs); err == nil || !strings.Contains(err.Error(), "immutable product catalog") {
		t.Fatalf("accepted changed artifact: %v", err)
	}
}

func TestSchema2UmbrellaDocsRejectsChangedInventory(t *testing.T) {
	for _, change := range []string{"missing", "hash", "source", "release-plane", "artifact-plane", "tag"} {
		t.Run(change, func(t *testing.T) {
			f := newSchema2DocsFixture(t)
			stack := f.manifest.Stacks["control-plane"]
			data := f.inputs.Inventories[stack.InventoryAsset]
			switch change {
			case "missing":
				delete(f.inputs.Inventories, stack.InventoryAsset)
			case "hash":
				f.inputs.Inventories[stack.InventoryAsset] = append(data, '\n')
			case "tag":
				editionGit(t, f.root, "tag", "-f", stack.SourceTag)
			default:
				inventory, err := parseResolvedStackInventory(data)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "source":
					inventory.Source.Commit = f.release.Umbrella.ProductCommit
				case "release-plane":
					inventory.Releases[0].Plane = "compute-plane"
				case "artifact-plane":
					inventory.Artifacts[0].Sources[0].Plane = "compute-plane"
				}
				data, err = json.Marshal(inventory)
				if err != nil {
					t.Fatal(err)
				}
				f.inputs.Inventories[stack.InventoryAsset] = data
				stack.InventorySHA256 = umbrellaDigest(data)
				f.manifest.Stacks["control-plane"] = stack
			}
			if _, err := umbrellaDocsCatalog(f.root, f.manifest, f.data, f.inputs); err == nil {
				t.Fatal("accepted changed " + change)
			}
		})
	}
}

func TestSchema2UmbrellaDocsCorrectionRollbackAndStaging(t *testing.T) {
	f := newSchema2DocsFixture(t)
	source := filepath.Join(t.TempDir(), "prepared")
	if err := prepareUmbrellaDocsWithInventories(f.root, source, f.release, false, f.directory); err != nil {
		t.Fatal(err)
	}
	for name, want := range f.inputs.Inventories {
		got, err := os.ReadFile(filepath.Join(source, umbrellaDocsInventoryDirectory, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("original inventory bytes changed: %s: %v", name, err)
		}
	}
	if err := stageEditionPreview(source); err != nil {
		t.Fatalf("prepared branch preview: %v", err)
	}
	editionGit(t, source, "config", "user.name", "Fixture")
	editionGit(t, source, "config", "user.email", "fixture@example.com")
	commitDocs := func(content string) string {
		t.Helper()
		writeFile(t, filepath.Join(source, "docs/page.md"), content)
		editionGit(t, source, "add", ".")
		editionGit(t, source, "commit", "-m", "docs correction")
		editionGit(t, source, "push", "origin", "HEAD:refs/heads/"+f.release.Ref)
		return editionGit(t, source, "rev-parse", "HEAD")
	}
	revise := func(commit string) error {
		release := f.release
		release.Commit = commit
		binding := *f.release.Umbrella
		binding.Revisions = []string{commit}
		release.Umbrella = &binding
		return reviseUmbrellaDocs(f.root, release, false)
	}
	first := commitDocs("# Initial schema 2 docs\n")
	if err := revise(first); err != nil {
		t.Fatal(err)
	}
	second := commitDocs("# Corrected schema 2 docs\n")
	if err := revise(second); err != nil {
		t.Fatal(err)
	}
	commitDocs("# Unapproved content\n")
	writeFile(t, filepath.Join(f.root, "fern/docs.yml"), "versions:\n  - display-name: '1.0.0'\n    slug: '1.0.0'\n    ref: docs/releases/1.0.0\n  - display-name: '1.0.4'\n    slug: '1.0.4'\n    ref: docs/versions/1.0.4\n  - display-name: Development\n    slug: dev\n    path: navigation.yml\n")
	for _, approved := range []struct{ commit, text string }{{second, "# Corrected schema 2 docs\n"}, {first, "# Initial schema 2 docs\n"}} {
		if err := revise(approved.commit); err != nil {
			t.Fatal(err)
		}
		if err := validateEditionRegistry(f.root, true); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "canonical")
		if err := stageCanonicalDocs(f.root, out, false); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(out, "fern/versions/1.0.4/docs/page.md"))
		if err != nil || string(got) != approved.text {
			t.Fatalf("staging followed branch HEAD instead of approved SHA: %v %s", err, got)
		}
		if _, err := os.Stat(filepath.Join(out, "fern/versions/1.0.0/docs/page.md")); err != nil {
			t.Fatal("mixed staging lost frozen legacy edition")
		}
		if err := stageEditionPreview(out); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := readEditionRegistry(f.root)
	if err != nil || registry.Default != "1.0.0" || len(registry.Editions) != 2 || len(registry.Editions[1].Umbrella.Revisions) != 2 {
		t.Fatalf("correction or rollback changed registry identity: %+v, %v", registry, err)
	}
	stack := f.manifest.Stacks["control-plane"]
	writeFile(t, filepath.Join(source, umbrellaDocsInventoryDirectory, stack.InventoryAsset), "{}\n")
	if err := revise(commitDocs("# Corrupted embedded inventory\n")); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("accepted changed embedded inventory: %v", err)
	}
	changed := f.release
	changed.Umbrella = &umbrellaDocsBinding{ProductCommit: first, ManifestSHA256: f.release.Umbrella.ManifestSHA256, Revisions: []string{first}}
	changed.Commit = first
	if err := reviseUmbrellaDocs(f.root, changed, false); err == nil || !strings.Contains(err.Error(), "cannot change a registered product identity") {
		t.Fatalf("accepted changed product binding: %v", err)
	}
}
