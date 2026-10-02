// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editionTestCatalog() *Catalog {
	catalog := testCatalogWithReleaseSet()
	catalog.DocsEdition = &DocsEdition{Version: "1.0.0", Status: ReleaseSetDevelopment, Change: "initial"}
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		peers := map[string]string{}
		for _, other := range releaseSetStackNames {
			if other != name {
				peer, _ := catalog.ReleaseSet.Stacks.byName(other)
				peers[other] = peer.Version
			}
		}
		catalog.Compatibility = append(catalog.Compatibility, CompatibilityEntry{Stack: name, Version: stack.Version, CompatibleWith: peers})
	}
	return catalog
}

func TestEditionSemVer(t *testing.T) {
	for _, test := range []struct {
		previous, version, change string
		valid                     bool
	}{
		{"", "1.0.1", "initial", true}, {"1.0.1", "1.0.2", "patch", true}, {"1.0.2", "2.0.0", "major", true}, {"2.0.0", "3.0.0", "major", true},
		{"2.0.0", "2.0.0", "major", false}, {"2.0.0", "2.1.0", "minor", true}, {"2.1.9", "2.1.10", "patch", true},
		{"2.0.0", "3.1.0", "major", false}, {"2.0.0", "2.0.0-rc.1", "patch", false}, {"", "01.0.0", "initial", false},
		{"1.0.999999999999999999999999", "1.0.1000000000000000000000000", "patch", true},
	} {
		t.Run(test.previous+"/"+test.version, func(t *testing.T) {
			err := validateEditionBump(test.previous, test.version, test.change)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestEditionQualificationAndCombination(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{"development", func(*Catalog) {}, ""},
		{"qualification", func(c *Catalog) {
			c.DocsEdition.Status = ReleaseSetQualified
			c.DocsEdition.Qualification = "https://github.com/NVIDIA/nvcf/pull/123"
		}, ""},
		{"missing approval", func(c *Catalog) { c.DocsEdition.Status = ReleaseSetQualified }, "requires a public"},
		{"private evidence", func(c *Catalog) {
			c.DocsEdition.Status = ReleaseSetQualified
			c.DocsEdition.Qualification = "https://example.com/approval"
		}, "requires a public"},
		{"development approval", func(c *Catalog) { c.DocsEdition.Qualification = "https://github.com/NVIDIA/nvcf/pull/123" }, "must not claim"},
		{"missing stack", func(c *Catalog) { c.ReleaseSet.Stacks.ComputePlane = StackReleaseMetadata{} }, "version"},
		{"missing matrix", func(c *Catalog) { c.Compatibility = nil }, "no compatibility"},
		{"incompatible", func(c *Catalog) { c.Compatibility[0].CompatibleWith[releaseSetStackComputePlane] = "99.0.0+" }, "requires compute-plane"},
		{"exact mismatch", func(c *Catalog) { c.Compatibility[0].CompatibleWith[releaseSetStackComputePlane] = "1.0.0" }, "requires compute-plane"},
		{"docs distinct", func(c *Catalog) {
			c.ReleaseSet.Stacks.ControlPlane.DocumentationVersion = "99.0.0"
			c.ReleaseSet.Stacks.ControlPlane.Status = ReleaseSetQualified
		}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := editionTestCatalog()
			test.mutate(catalog)
			err := ValidateCatalog(catalog)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
	legacy := editionTestCatalog()
	legacy.DocsEdition = nil
	legacy.ReleaseSet.Stacks.ControlPlane.Status = ReleaseSetQualified
	legacy.ReleaseSet.Stacks.ControlPlane.DocumentationVersion = "99.0.0"
	if err := ValidateCatalog(legacy); err == nil {
		t.Fatal("legacy documentation version validation was weakened")
	}
}

func TestEditionTransition(t *testing.T) {
	previous := editionTestCatalog()
	previous.DocsEdition.Version = "2.0.0"
	current := editionTestCatalog()
	current.DocsEdition.PreviousVersion = "2.0.0"
	current.DocsEdition.Version = "2.0.1"
	current.DocsEdition.Change = "patch"
	if err := validateEditionTransition(previous, current); err != nil {
		t.Fatalf("unchanged-stack docs patch: %v", err)
	}
	current.ReleaseSet.Stacks.ComputePlane.Version = "3.0.0"
	if err := validateEditionTransition(previous, current); err == nil || !strings.Contains(err.Error(), "major upgrade") {
		t.Fatalf("compute major accepted as patch: %v", err)
	}
	current.DocsEdition.Version = "3.0.0"
	current.DocsEdition.Change = "major"
	if err := validateEditionTransition(previous, current); err != nil {
		t.Fatal(err)
	}
	current.ReleaseSet.Stacks.Observability.Version = "1.0.0"
	if err := validateEditionTransition(previous, current); err == nil || !strings.Contains(err.Error(), "regresses") {
		t.Fatalf("downgrade accepted: %v", err)
	}
}

func TestEditionManifestDrift(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := editionTestCatalog()
	if err := syncEditionManifest(root, catalog, false); err != nil {
		t.Fatal(err)
	}
	if err := syncEditionManifest(root, catalog, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docs", "edition-manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "nvcf-compute-plane-stack-inventory.json") || strings.Contains(string(data), "documentation_version") {
		t.Fatalf("unexpected manifest: %s", data)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncEditionManifest(root, catalog, true); err == nil {
		t.Fatal("stale manifest accepted")
	}
}

func TestEditionCommandRejectsImplicitInputs(t *testing.T) {
	for _, args := range [][]string{
		{"prepare", "--repo", t.TempDir()},
		{"prepare", "--repo", t.TempDir(), "--source", strings.Repeat("a", 40), "--version", "1.0.0", "--change", "initial", "--self-managed", "latest"},
		{"check", "--repo", t.TempDir(), "--qualification", "https://github.com/NVIDIA/nvcf/pull/123"},
		{"register", "--repo", t.TempDir(), "--version", "1.0.0", "--commit", "main"},
	} {
		if err := runEdition(args); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
}

func TestEditionQualifiedCatalogCannotBeRefreshed(t *testing.T) {
	catalog := editionTestCatalog()
	catalog.DocsEdition.Status = ReleaseSetQualified
	catalog.DocsEdition.Qualification = "https://github.com/NVIDIA/nvcf/pull/123"
	if _, err := updateCatalogFromGitHubInventories(t.TempDir(), nil, catalog); err == nil || !strings.Contains(err.Error(), "cannot refresh") {
		t.Fatalf("qualified edition refresh was allowed: %v", err)
	}
}
