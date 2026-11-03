// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func umbrellaTestManifest() *umbrellaManifest {
	catalog := editionTestCatalog()
	manifest := &umbrellaManifest{SchemaVersion: 1, Version: "1.0.3", Change: "initial",
		Status: ReleaseSetDevelopment, SourceCommit: strings.Repeat("d", 40),
		Toolchain: umbrellaToolchain{Helm: "3.19.1", Helmfile: "1.1.9"}, Stacks: map[string]umbrellaStack{}}
	for _, name := range releaseSetStackNames {
		metadata, _ := catalog.ReleaseSet.Stacks.byName(name)
		stack := umbrellaStack{Version: metadata.Version, SourceTag: metadata.SourceTag, SourceCommit: metadata.SourceCommit,
			InventoryAsset: metadata.InventoryAsset, InventorySHA256: umbrellaDigest([]byte(name))}
		for _, entry := range catalog.Compatibility {
			if entry.Stack == name {
				stack.CompatibleWith = entry.CompatibleWith
			}
		}
		manifest.Stacks[name] = stack
	}
	return manifest
}

func TestUmbrellaManifestValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*umbrellaManifest)
		want   string
	}{
		{"independent versions", func(*umbrellaManifest) {}, ""},
		{"unknown schema", func(m *umbrellaManifest) { m.SchemaVersion = 3 }, "schema"},
		{"missing stack", func(m *umbrellaManifest) { delete(m.Stacks, releaseSetStackComputePlane) }, "exactly"},
		{"missing provenance", func(m *umbrellaManifest) { m.SourceCommit = "main" }, "reviewed commit"},
		{"unsupported Helm", func(m *umbrellaManifest) { m.Toolchain.Helm = "4.0.0" }, "Helm 3"},
		{"unsupported stage order", func(m *umbrellaManifest) { m.Toolchain.Helmfile = "1.2.0" }, "sequential"},
		{"unqualified approval", func(m *umbrellaManifest) { m.Qualification = "https://github.com/NVIDIA/nvcf/issues/1" }, "must not claim"},
		{"missing approval", func(m *umbrellaManifest) { m.Status = ReleaseSetQualified }, "requires a public"},
		{"qualified", func(m *umbrellaManifest) {
			m.Status = ReleaseSetQualified
			m.Qualification = "https://github.com/NVIDIA/nvcf/issues/1"
		}, ""},
		{"unknown stack", func(m *umbrellaManifest) {
			m.Stacks["self-managed"] = m.Stacks[releaseSetStackControlPlane]
			delete(m.Stacks, releaseSetStackControlPlane)
		}, "control-plane"},
		{"wrong source tag", func(m *umbrellaManifest) {
			s := m.Stacks[releaseSetStackControlPlane]
			s.SourceTag = "main"
			m.Stacks[releaseSetStackControlPlane] = s
		}, "source tag"},
		{"inventory path traversal", func(m *umbrellaManifest) {
			s := m.Stacks[releaseSetStackControlPlane]
			s.InventoryAsset = "../inventory.json"
			m.Stacks[releaseSetStackControlPlane] = s
		}, "canonical inventory"},
		{"no checksum", func(m *umbrellaManifest) {
			s := m.Stacks[releaseSetStackControlPlane]
			s.InventorySHA256 = ""
			m.Stacks[releaseSetStackControlPlane] = s
		}, "SHA-256"},
		{"incompatible combination", func(m *umbrellaManifest) {
			m.Stacks[releaseSetStackControlPlane].CompatibleWith[releaseSetStackComputePlane] = "99.0.0+"
		}, "requires compute-plane"},
		{"unknown peer", func(m *umbrellaManifest) {
			m.Stacks[releaseSetStackControlPlane].CompatibleWith["other"] = "1.0.0"
		}, "exactly two"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := umbrellaTestManifest()
			test.mutate(manifest)
			err := validateUmbrellaManifest(manifest)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestUmbrellaManifestParsing(t *testing.T) {
	data, err := marshalUmbrellaManifest(umbrellaTestManifest())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseUmbrellaManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := marshalUmbrellaManifest(parsed)
	if err != nil || !bytes.Equal(data, roundTrip) {
		t.Fatalf("unstable serialization: %v", err)
	}
	for _, invalid := range [][]byte{
		append(append([]byte{}, data...), []byte("{}")...),
		bytes.Replace(data, []byte(`"schema_version": 1`), []byte(`"schema_version": 1, "typo": true`), 1),
	} {
		if _, err := parseUmbrellaManifest(invalid); err == nil {
			t.Fatal("accepted unknown fields or trailing document")
		}
	}
}

func TestUmbrellaTransition(t *testing.T) {
	previous := umbrellaTestManifest()
	previous.Status = ReleaseSetQualified
	previous.Qualification = "https://github.com/NVIDIA/nvcf/pull/1"
	current := umbrellaTestManifest()
	current.PreviousVersion = previous.Version
	current.Version = "1.0.4"
	current.Change = "patch"
	if err := validateUmbrellaTransition(previous, current); err != nil {
		t.Fatal(err)
	}
	if err := validateUmbrellaTransition(nil, current); err == nil {
		t.Fatal("missing predecessor accepted")
	}
	stack := current.Stacks[releaseSetStackComputePlane]
	stack.SourceCommit = strings.Repeat("e", 40)
	current.Stacks[releaseSetStackComputePlane] = stack
	if err := validateUmbrellaTransition(previous, current); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("same component version changed identity: %v", err)
	}
	stack.Version = "3.0.0"
	current.Stacks[releaseSetStackComputePlane] = stack
	if err := validateUmbrellaTransition(previous, current); err == nil || !strings.Contains(err.Error(), "major upgrade") {
		t.Fatalf("component major accepted in patch: %v", err)
	}
}

// umbrellaFixture builds independent component tags and a later reviewed catalog commit.
type umbrellaFixture struct {
	root         string
	inventories  string
	source       string
	catalog      *Catalog
	manifestPath string
}

func newUmbrellaFixture(t *testing.T) umbrellaFixture {
	return newUmbrellaFixtureWithSources(t, nil)
}

func newUmbrellaFixtureWithSources(t *testing.T, sources map[string]map[string]string) umbrellaFixture {
	t.Helper()
	fixture := umbrellaFixture{root: initTestGitRepo(t), inventories: t.TempDir(), catalog: editionTestCatalog(), manifestPath: filepath.Join(t.TempDir(), "manifest.json")}
	writeFile(t, filepath.Join(fixture.root, "LICENSE"), "Fixture license\n")
	writeFile(t, filepath.Join(fixture.root, "NOTICE"), "Fixture notices\n")
	for _, name := range releaseSetStackNames {
		metadata, _ := fixture.catalog.ReleaseSet.Stacks.byName(name)
		for path, data := range sources[name] {
			writeFile(t, filepath.Join(fixture.root, path), data)
		}
		writeFile(t, filepath.Join(fixture.root, name+".txt"), name+" source\n")
		editionGit(t, fixture.root, "add", ".")
		editionGit(t, fixture.root, "commit", "-m", name+" source")
		metadata.SourceCommit = editionGit(t, fixture.root, "rev-parse", "HEAD")
		editionGit(t, fixture.root, "tag", metadata.SourceTag)
		var inputs []resolvedInventoryPlaneInput
		for _, input := range resolvedInventoryTestInputs(t) {
			if input.Name == name {
				inputs = append(inputs, input)
			}
		}
		inventory, err := generateResolvedStackInventory(stackSourceRelease{Version: metadata.Version, Tag: metadata.SourceTag, Commit: metadata.SourceCommit}, inputs)
		if err != nil {
			t.Fatal(err)
		}
		data, err := marshalResolvedStackInventory(inventory)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(fixture.inventories, metadata.InventoryAsset), string(data))
	}
	if err := WriteCatalog(filepath.Join(fixture.root, "docs/version-catalog/main.yaml"), fixture.catalog); err != nil {
		t.Fatal(err)
	}
	editionGit(t, fixture.root, "add", ".")
	editionGit(t, fixture.root, "commit", "-m", "reviewed catalog")
	fixture.source = editionGit(t, fixture.root, "rev-parse", "HEAD")
	return fixture
}

func (fixture umbrellaFixture) prepareArgs() []string {
	return []string{"umbrella", "prepare", "--repo", fixture.root, "--source", fixture.source,
		"--version", "1.0.3", "--change", "initial", "--helm-version", "3.19.1", "--helmfile-version", "1.1.9",
		"--inventories", fixture.inventories, "--out", fixture.manifestPath}
}

func (fixture umbrellaFixture) checkArgs() []string {
	return []string{"umbrella", "check", "--repo", fixture.root, "--manifest", fixture.manifestPath, "--inventories", fixture.inventories}
}

func TestUmbrellaCommandE2E(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docs-version-sync")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build command: %v\n%s", err, output)
	}
	invoke := func(want string, args ...string) {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = t.TempDir()
		output, err := command.CombinedOutput()
		if want == "" && err != nil {
			t.Fatalf("command failed: %v\n%s", err, output)
		}
		if want != "" && (err == nil || !strings.Contains(string(output), want)) {
			t.Fatalf("want failure containing %q, got %v\n%s", want, err, output)
		}
	}
	fixture := newUmbrellaFixture(t)
	before := editionGit(t, fixture.root, "status", "--porcelain")
	tags := editionGit(t, fixture.root, "tag", "--list")
	invoke("", fixture.prepareArgs()...)
	invoke("", fixture.checkArgs()...)
	invoke("", fixture.prepareArgs()...)
	manifest, data, err := loadUmbrellaManifest(fixture.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range releaseSetStackNames {
		want, _ := fixture.catalog.ReleaseSet.Stacks.byName(name)
		got := manifest.Stacks[name]
		if got.Version != want.Version || got.SourceCommit != want.SourceCommit {
			t.Fatalf("%s changed identity: %+v", name, got)
		}
		inventoryData, err := os.ReadFile(filepath.Join(fixture.inventories, want.InventoryAsset))
		if err != nil || umbrellaDigest(inventoryData) != got.InventorySHA256 {
			t.Fatalf("%s checksum mismatch: %v", name, err)
		}
	}
	if editionGit(t, fixture.root, "status", "--porcelain") != before || editionGit(t, fixture.root, "tag", "--list") != tags {
		t.Fatal("candidate command changed the source checkout or release tags")
	}
	manifest.Toolchain.Helm = "3.20.0"
	changed, err := marshalUmbrellaManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, fixture.manifestPath, string(changed))
	invoke("refusing to overwrite", fixture.prepareArgs()...)
	writeFile(t, fixture.manifestPath, string(data))
	stack := manifest.Stacks[releaseSetStackControlPlane]
	inventoryPath := filepath.Join(fixture.inventories, stack.InventoryAsset)
	inventoryData, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, inventoryPath, string(inventoryData)+"\n")
	invoke("checksum mismatch", fixture.checkArgs()...)
	writeFile(t, inventoryPath, string(inventoryData))
	editionGit(t, fixture.root, "tag", "-f", stack.SourceTag, fixture.source)
	invoke("source tag does not resolve", fixture.checkArgs()...)
	editionGit(t, fixture.root, "tag", "-f", stack.SourceTag, stack.SourceCommit)

	// Exercise the existing commands through the same binary after the new flow.
	legacyRoot, _, legacyCommit := editionGitFixture(t)
	invoke("", "edition", "register", "--repo", legacyRoot, "--version", "1.0.0", "--commit", legacyCommit, "--local-refs")
	invoke("", "edition", "check", "--repo", legacyRoot, "--local-refs")
	editionGit(t, legacyRoot, "branch", "-f", "docs/releases/1.0.0", "HEAD~1")
	invoke("does not match", "edition", "check", "--repo", legacyRoot, "--local-refs")

	// A released product manifest cannot be changed even to a valid new tool version.
	writeFile(t, filepath.Join(fixture.root, umbrellaManifestPath(manifest.Version)), string(data))
	editionGit(t, fixture.root, "add", ".")
	editionGit(t, fixture.root, "commit", "-m", "product manifest")
	editionGit(t, fixture.root, "tag", umbrellaTag(manifest.Version))
	invoke("", fixture.checkArgs()...)
	writeFile(t, fixture.manifestPath, string(changed))
	invoke("refusing to change published", fixture.checkArgs()...)
}

func TestUmbrellaPreparationRejectsInventorySource(t *testing.T) {
	fixture := newUmbrellaFixture(t)
	metadata := fixture.catalog.ReleaseSet.Stacks.ComputePlane
	path := filepath.Join(fixture.inventories, metadata.InventoryAsset)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var inventory resolvedStackInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	inventory.Source.Commit = fixture.source
	data, err = marshalResolvedStackInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	if err := run(fixture.prepareArgs()); err == nil || !strings.Contains(err.Error(), "inventory source differs") {
		t.Fatalf("wrong source accepted: %v", err)
	}
	if _, err := os.Stat(fixture.manifestPath); !os.IsNotExist(err) {
		t.Fatalf("failed preparation created output: %v", err)
	}
}

func TestUmbrellaCommandsRejectImplicitInputs(t *testing.T) {
	for _, args := range [][]string{
		{}, {"unknown"}, {"prepare"}, {"check"},
		{"check", "--out", "unexpected"},
		{"prepare", "--manifest", "unexpected"},
		{"prepare", "unexpected"},
	} {
		if err := runUmbrella(args); err == nil {
			t.Fatalf("accepted incomplete command: %v", args)
		}
	}
}

func TestUmbrellaTagDoesNotTriggerComponentPublisher(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".github/workflows/release-tags.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			Push struct {
				Tags []string `yaml:"tags"`
			} `yaml:"push"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.On.Push.Tags) == 0 {
		t.Fatal("component publisher tag triggers were not found")
	}
	tag := umbrellaTag(umbrellaTestManifest().Version)
	for _, pattern := range workflow.On.Push.Tags {
		matches, err := path.Match(pattern, tag)
		if err != nil || matches {
			t.Fatalf("umbrella tag %s matches component trigger %s: %v", tag, pattern, err)
		}
	}
}
