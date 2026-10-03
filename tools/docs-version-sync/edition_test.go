// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestEditionRejectsPrereleasePeerWithOpenCompatibility(t *testing.T) {
	for _, name := range []string{releaseSetStackComputePlane, releaseSetStackObservability} {
		t.Run(name, func(t *testing.T) {
			catalog := editionTestCatalog()
			stack, err := catalog.ReleaseSet.Stacks.byName(name)
			if err != nil {
				t.Fatal(err)
			}
			stack.Version += "-rc.1"
			stack.SourceTag += "-rc.1"
			catalog.Compatibility[0].CompatibleWith[name] = "1.0.0+"

			err = ValidateCatalog(catalog)
			want := "requires an exact stable " + name + " stack version"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
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

func TestEditionGeneratedStackLinksPreserveSelection(t *testing.T) {
	for _, renderer := range []string{"manifest-artifact-registry-paths", "compatibility-matrix"} {
		t.Run(renderer, func(t *testing.T) {
			catalog := loadMainCatalog(t)
			catalog.DocsEdition = &DocsEdition{Version: "1.0.0", Status: ReleaseSetDevelopment, Change: "initial"}
			got, err := Render(renderer, catalog)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"../self-managed/installation.md", "../compute-plane/cluster-management/index.md", "../observability/observability.md"} {
				if !strings.Contains(got, "]("+path+")") {
					t.Errorf("edition output is missing relative stack link %s", path)
				}
			}
			if strings.Contains(got, "](/nvcf/") {
				t.Error("edition output links to the default edition")
			}
			catalog.DocsEdition = nil
			got, err = Render(renderer, catalog)
			if err != nil {
				t.Fatal(err)
			}
			for _, slug := range []string{"self-managed", "compute-plane", "observability"} {
				if !strings.Contains(got, "](/nvcf/"+slug+"/)") {
					t.Errorf("legacy output changed its %s product link", slug)
				}
			}
		})
	}
}

func TestEditionBranchRedirectsUseLocalDefault(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fern", "docs.yml")
	writeFile(t, path, `versions:
  - display-name: Development
    slug: dev
    path: navigation.yml
redirects:
  - source: /nvcf/dev/manifest
    destination: /nvcf/dev/overview/manifest
  - source: /nvcf/self-managed/dev/:slug*
    destination: /nvcf/dev/self-managed/:slug*
  - source: /nvcf/v0.5/:slug*
    destination: /nvcf/self-managed/v0.5/:slug*
  - source: /external
    destination: https://example.com/nvcf/dev/overview
`)
	if err := writeBranchConfiguration(root, editionPreparation{Version: "1.0.1", Navigation: "navigation.yml"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Redirects []struct{ Source, Destination string } `yaml:"redirects"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"/nvcf/dev/manifest":            "/nvcf/overview/manifest",
		"/nvcf/self-managed/dev/:slug*": "/nvcf/self-managed/:slug*",
		"/nvcf/v0.5/:slug*":             "/nvcf/self-managed/v0.5/:slug*",
		"/external":                     "https://example.com/nvcf/dev/overview",
	}
	if len(config.Redirects) != len(want) {
		t.Fatalf("redirect count = %d, want %d", len(config.Redirects), len(want))
	}
	for _, redirect := range config.Redirects {
		if redirect.Destination != want[redirect.Source] {
			t.Errorf("%s destination = %s, want %s", redirect.Source, redirect.Destination, want[redirect.Source])
		}
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

func TestEditionSummaryUsesSelectedCombination(t *testing.T) {
	catalog := editionTestCatalog()
	summary, err := Render("edition-overview", catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Docs edition `1.0.0`", "Development candidate", "Each stack keeps its own artifact version"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary lacks %q: %s", want, summary)
		}
	}
	catalog.DocsEdition.Status = ReleaseSetQualified
	catalog.DocsEdition.Qualification = "https://github.com/NVIDIA/nvcf/pull/123"
	for _, name := range releaseSetStackNames {
		summary, err := Render("edition-"+name, catalog)
		if err != nil {
			t.Fatal(err)
		}
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		if !strings.Contains(summary, "Qualified combination") || !strings.Contains(summary, "`"+stack.Version+"`") {
			t.Fatalf("incorrect qualified stack summary: %s", summary)
		}
	}
	catalog.DocsEdition = nil
	summary, err = Render("edition-overview", catalog)
	if err != nil || summary != "" {
		t.Fatalf("legacy output changed: %q, %v", summary, err)
	}
}

func TestSyncDefaultOutputsLeavesLandingPagesAuthored(t *testing.T) {
	root := t.TempDir()
	catalog := editionTestCatalog()
	catalog.Outputs = defaultOutputs()
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts,
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.9.0"},
		Artifact{Name: "nvcf-image-credential-helper", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "1.0.0"},
	)
	catalog.Manifest.Entries = append(catalog.Manifest.Entries,
		ManifestEntry{ArtifactID: "helm-nvca-operator", Plane: ManifestPlaneCompute, Kind: ManifestKindChart, Requirement: ManifestRequired, Description: "NVCA operator chart."},
		ManifestEntry{ArtifactID: "nvcf-image-credential-helper", Plane: ManifestPlaneCompute, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "Image credential helper."},
	)
	inlineExamples := map[string]string{
		"docs/overview/image-mirroring.md":                      "helm pull nvcf/helm-nvca-operator --version 0.1.0\n",
		"docs/compute-plane/cluster-management/self-managed.md": "| Chart | `helm-nvca-operator` |\n| --- | --- |\n| Version | `0.1.0` |\n",
		"docs/compute-plane/cluster-management/reference.md":    "imageCredHelper:\n  imageRepository: \"\"\n  imageTag: 0.1.0\n",
	}
	for _, output := range catalog.Outputs {
		content := "# Guide\n\n" + inlineExamples[output.Path]
		for _, block := range output.Blocks {
			content += "\n{/*docs-version-sync:BEGIN " + block.Marker + "*/}\nstale\n{/*docs-version-sync:END " + block.Marker + "*/}\n"
		}
		writeFile(t, filepath.Join(root, output.Path), content)
	}
	landingPages := map[string]string{
		"docs/overview/index.md":                         "# NVIDIA Cloud Functions\n\nDeployment and operation guides.\n",
		"docs/self-managed/installation.md":              "# Deployment\n\nInstall the control plane.\n",
		"docs/compute-plane/cluster-management/index.md": "# GPU Cluster Setup\n\nConnect a GPU cluster.\n",
		"docs/observability/observability.md":            "# Observability Configuration\n\nConfigure metrics and logs.\n",
	}
	for path, content := range landingPages {
		writeFile(t, filepath.Join(root, path), content)
	}
	if err := SyncDocs(root, catalog, false); err != nil {
		t.Fatal(err)
	}
	if err := SyncDocs(root, catalog, true); err != nil {
		t.Fatalf("regenerated docs are inconsistent: %v", err)
	}
	for path, content := range landingPages {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != content {
			t.Fatalf("%s changed during regeneration: %q, %v", path, got, err)
		}
	}
	matrix, err := os.ReadFile(filepath.Join(root, "docs/overview/compatibility-matrix.md"))
	if err != nil || !strings.Contains(string(matrix), "`1.2.3`") {
		t.Fatalf("compatibility matrix was not regenerated: %q, %v", matrix, err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs/edition-manifest.json")); err != nil {
		t.Fatalf("edition manifest was not generated: %v", err)
	}
}
