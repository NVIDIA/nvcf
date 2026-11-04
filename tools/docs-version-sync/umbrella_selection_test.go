// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (fixture umbrellaFixture) releaseLocalArgs() []string {
	args := fixture.prepareArgs()
	for _, name := range releaseSetStackNames {
		metadata, _ := fixture.catalog.ReleaseSet.Stacks.byName(name)
		key, _ := stackKeyForPlane(name)
		args = append(args, "--"+key+"-version", metadata.Version)
	}
	return args
}

// Advance Development independently of the previously tagged stack sources.
func divergeUmbrellaDevelopment(t *testing.T, fixture *umbrellaFixture) {
	t.Helper()
	catalog := editionTestCatalog()
	for _, name := range releaseSetStackNames {
		metadata, _ := catalog.ReleaseSet.Stacks.byName(name)
		key, _ := stackKeyForPlane(name)
		spec, _ := stackInventorySpecByKey(key)
		metadata.Version, metadata.SourceTag, metadata.SourceCommit = "9.0.0", spec.TagPrefix+"9.0.0", fixture.source
		writeFile(t, filepath.Join(fixture.root, umbrellaStackDirectory(name), "charts/fixture/templates/config.yaml"), "unselected Development source\n")
	}
	for index := range catalog.Compatibility {
		entry := &catalog.Compatibility[index]
		entry.Version = "9.0.0"
		for peer := range entry.CompatibleWith {
			entry.CompatibleWith[peer] = "9.0.0"
		}
	}
	if err := WriteCatalog(filepath.Join(fixture.root, "docs/version-catalog/main.yaml"), catalog); err != nil {
		t.Fatal(err)
	}
	editionGit(t, fixture.root, "add", ".")
	editionGit(t, fixture.root, "commit", "-m", "advance Development independently")
	fixture.source = editionGit(t, fixture.root, "rev-parse", "HEAD")
}

func TestUmbrellaReleaseLocalSelectionAndBundle(t *testing.T) {
	fixture := umbrellaBundleFixture(t)
	divergeUmbrellaDevelopment(t, &fixture)
	before := editionGit(t, fixture.root, "status", "--porcelain")
	tags := editionGit(t, fixture.root, "tag", "--list")
	for _, args := range [][]string{fixture.releaseLocalArgs(), fixture.checkArgs(), fixture.releaseLocalArgs(), {"umbrella", "manifest", "--manifest", fixture.manifestPath}} {
		if err := run(args); err != nil {
			t.Fatal(err)
		}
	}
	manifest, data, err := loadUmbrellaManifest(fixture.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 || manifest.Status != ReleaseSetDevelopment || manifest.SourceCommit != fixture.source {
		t.Fatalf("wrong release-local identity: %+v", manifest)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	if err := run([]string{"umbrella", "bundle", "--repo", fixture.root, "--manifest", fixture.manifestPath, "--inventories", fixture.inventories, "--out", out}); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(out, umbrellaBundleName(manifest.Version))
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"umbrella", "verify-bundle", "--manifest", fixture.manifestPath, "--bundle", archivePath, "--sha256", umbrellaDigest(archive)}); err != nil {
		t.Fatal(err)
	}
	files, err := readUmbrellaArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	commits := map[string]bool{}
	for _, name := range releaseSetStackNames {
		stack := manifest.Stacks[name]
		commits[stack.SourceCommit] = true
		want, _ := fixture.catalog.ReleaseSet.Stacks.byName(name)
		if stack.Version != want.Version || stack.SourceTag != want.SourceTag || stack.SourceCommit != want.SourceCommit {
			t.Fatalf("changed selected %s identity: %+v", name, stack)
		}
		for _, peer := range releaseSetStackNames {
			if peer != name && stack.CompatibleWith[peer] != manifest.Stacks[peer].Version {
				t.Fatalf("%s did not lock selected peer %s", name, peer)
			}
		}
		path := umbrellaStackDirectory(name) + "/charts/fixture/templates/config.yaml"
		selected, err := gitOutput(fixture.root, "show", stack.SourceCommit+":"+path)
		if err != nil || !bytes.Equal(files[path].data, selected) {
			t.Fatalf("bundle did not use independent %s source: %v", name, err)
		}
		if string(files[umbrellaStackDirectory(name)+"/VERSION"].data) != stack.Version+"\n" {
			t.Fatalf("wrong bundled %s version", name)
		}
	}
	if len(commits) != 3 {
		t.Fatal("fixture did not exercise three independent source commits")
	}
	if editionGit(t, fixture.root, "status", "--porcelain") != before || editionGit(t, fixture.root, "tag", "--list") != tags {
		t.Fatal("release-local preparation changed the checkout or tags")
	}
	// Schema 1 still binds the reviewed catalog even when all other inputs are valid.
	manifest.SchemaVersion = 1
	if _, err := validateUmbrellaSources(fixture.root, manifest, fixture.inventories); err == nil || !strings.Contains(err.Error(), "differs from the reviewed catalog") {
		t.Fatalf("legacy validation was weakened: %v", err)
	}
	manifest.SchemaVersion = 2
	if err := (umbrellaPublisher{}).publish(manifest, fixture.source, "candidate", nil, true); err == nil || !strings.Contains(err.Error(), "whole-combination qualification") {
		t.Fatalf("unqualified release-local promotion accepted: %v", err)
	}
	if _, err := umbrellaDocsCatalog(fixture.root, manifest, data); err == nil || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("release-local docs used Development pins: %v", err)
	}
	if err := validateUmbrellaDocsCatalog(fixture.root, manifest, data, fixture.catalog); err == nil || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("release-local docs activation accepted: %v", err)
	}
}

func TestUmbrellaReleaseLocalRejectsInvalidInputs(t *testing.T) {
	fixture := umbrellaBundleFixture(t)
	args := fixture.releaseLocalArgs()
	for _, version := range []string{"", "latest", "main", "1", "1.0", "v1.0.0", "01.0.0", "1.0.0-rc.1", "1.0.0+build", "1.0.0+", "../1.0.0"} {
		t.Run("version="+version, func(t *testing.T) {
			invalid := append([]string(nil), args...)
			invalid[len(invalid)-1] = version
			if err := run(invalid); err == nil || !strings.Contains(err.Error(), "exact stable") {
				t.Fatalf("invalid version accepted: %v", err)
			}
		})
	}
	if err := run(args[:len(args)-2]); err == nil || !strings.Contains(err.Error(), "together") {
		t.Fatalf("partial selection accepted: %v", err)
	}
	if err := run(append(args, "--observability-version", "1.0.0")); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate selection accepted: %v", err)
	}
	if _, err := os.Stat(fixture.manifestPath); !os.IsNotExist(err) {
		t.Fatalf("invalid preparation wrote a manifest: %v", err)
	}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	_, data, err := loadUmbrellaManifest(fixture.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*umbrellaManifest)
		want   string
	}{
		{"wrong tag", func(m *umbrellaManifest) {
			s := m.Stacks[releaseSetStackControlPlane]
			s.SourceTag = "main"
			m.Stacks[releaseSetStackControlPlane] = s
		}, "source tag"},
		{"wrong commit", func(m *umbrellaManifest) {
			s := m.Stacks[releaseSetStackControlPlane]
			s.SourceCommit = fixture.source
			m.Stacks[releaseSetStackControlPlane] = s
		}, "source tag does not resolve"},
		{"missing source", func(m *umbrellaManifest) { m.SourceCommit = strings.Repeat("f", 40) }, "available commit"},
		{"wrong checksum", func(m *umbrellaManifest) {
			s := m.Stacks[releaseSetStackControlPlane]
			s.InventorySHA256 = umbrellaDigest([]byte("changed"))
			m.Stacks[releaseSetStackControlPlane] = s
		}, "checksum mismatch"},
		{"wrong peer", func(m *umbrellaManifest) {
			m.Stacks[releaseSetStackControlPlane].CompatibleWith[releaseSetStackComputePlane] = "99.0.0"
		}, "exact selected"},
		{"broad peer", func(m *umbrellaManifest) {
			m.Stacks[releaseSetStackControlPlane].CompatibleWith[releaseSetStackComputePlane] += "+"
		}, "exact selected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, err := parseUmbrellaManifest(data)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(m)
			changed, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, fixture.manifestPath, string(changed))
			if err := run(fixture.checkArgs()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestUmbrellaReleaseLocalRejectsChangedInventory(t *testing.T) {
	for _, change := range []string{"source", "ownership"} {
		t.Run(change, func(t *testing.T) {
			fixture := newUmbrellaFixture(t)
			path := filepath.Join(fixture.inventories, fixture.catalog.ReleaseSet.Stacks.ControlPlane.InventoryAsset)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := parseResolvedStackInventory(data)
			if err != nil {
				t.Fatal(err)
			}
			want := "inventory source differs"
			if change == "source" {
				inventory.Source.Commit = fixture.source
			} else {
				want = "want only control-plane releases"
				for index := range inventory.Releases {
					inventory.Releases[index].Plane = releaseSetStackComputePlane
				}
				for index := range inventory.Artifacts {
					for j := range inventory.Artifacts[index].Sources {
						inventory.Artifacts[index].Sources[j].Plane = releaseSetStackComputePlane
					}
				}
			}
			changed, err := marshalResolvedStackInventory(inventory)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, string(changed))
			if err := run(fixture.releaseLocalArgs()); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("invalid inventory accepted: %v", err)
			}
		})
	}
}

func TestUmbrellaReleaseLocalImmutableTransition(t *testing.T) {
	for _, change := range []string{"source", "inventory"} {
		t.Run(change, func(t *testing.T) {
			previous, current := umbrellaTestManifest(), umbrellaTestManifest()
			previous.Status, previous.Qualification = ReleaseSetQualified, "https://github.com/NVIDIA/nvcf/issues/1"
			current.SchemaVersion, current.Version, current.PreviousVersion, current.Change = 2, "1.0.4", previous.Version, "patch"
			if err := validateUmbrellaTransition(previous, current); err != nil {
				t.Fatalf("legacy to release-local transition: %v", err)
			}
			stack := current.Stacks[releaseSetStackControlPlane]
			if change == "source" {
				stack.SourceCommit = strings.Repeat("e", 40)
			} else {
				stack.InventorySHA256 = umbrellaDigest([]byte("changed"))
			}
			current.Stacks[releaseSetStackControlPlane] = stack
			if err := validateUmbrellaTransition(previous, current); err == nil || !strings.Contains(err.Error(), "immutable") {
				t.Fatalf("same-version identity mutation accepted: %v", err)
			}
		})
	}
}
