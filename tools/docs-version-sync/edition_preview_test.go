// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func previewFixture(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fern", "docs.yml"), config)
	writeFile(t, filepath.Join(root, "fern", "navigation.yml"), `tabs:
  overview:
    slug: overview
navigation:
  - tab: overview
    layout:
      - page: Overview
        slug: overview
        path: ../docs/overview/index.md
`)
	writeFile(t, filepath.Join(root, "docs", "overview", "index.md"), "[Overview](/nvcf/overview/overview#setup)\n")
	writeFile(t, filepath.Join(root, "docs", "v0.6.1", "index.md"), "[Historical](/nvcf/overview/overview)\n")
	return root
}

func TestEditionPreviewPreservesCanonicalSettings(t *testing.T) {
	original := `# Shared site settings
instances:
  - url: example.docs.buildwithfern.com/nvcf
    custom-domain: docs.example.com/nvcf
    multi-source: true
css: [custom.css]
versions:
  - display-name: "1.0.2"
    slug: "1.0.2"
    ref: docs/releases/1.0.2 # Frozen branch
  - display-name: "1.0.1"
    slug: "1.0.1"
    ref: docs/releases/1.0.1
  - display-name: Development
    slug: dev
    path: navigation.yml
  - display-name: Archive
    slug: self-managed/v0.6.1
    path: products/self-managed/v0.6.1.yml
    hidden: true
redirects:
  - source: /nvcf/old
    destination: /nvcf/overview/overview
check:
  rules:
    broken-links: error
`
	root := previewFixture(t, original)
	if err := runEdition([]string{"preview", "--repo", root}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "fern", "docs.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := yaml.Unmarshal([]byte(original), &want); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	versions := want["versions"].([]any)
	want["versions"] = []any{versions[2], versions[0], versions[1], versions[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preview changed more than version order: got %v, want %v", got, want)
	}
	for _, comment := range []string{"# Shared site settings", "# Frozen branch"} {
		if !strings.Contains(string(data), comment) {
			t.Errorf("lost comment %q", comment)
		}
	}
	page, _ := os.ReadFile(filepath.Join(root, "docs", "overview", "index.md"))
	if string(page) != "[Overview](index.md#setup)\n" {
		t.Fatalf("current link was not normalized: %s", page)
	}
	frozen, _ := os.ReadFile(filepath.Join(root, "docs", "v0.6.1", "index.md"))
	if string(frozen) != "[Historical](/nvcf/overview/overview)\n" {
		t.Fatalf("archive changed: %s", frozen)
	}
	if err := stageEditionPreview(root); err != nil {
		t.Fatal(err)
	}
	retry, _ := os.ReadFile(filepath.Join(root, "fern", "docs.yml"))
	if string(retry) != string(data) {
		t.Fatal("preview configuration is not idempotent")
	}
}

func TestEditionPreviewSupportsPreparedReleaseBranch(t *testing.T) {
	root := previewFixture(t, "versions:\n  - display-name: 1.0.3\n    path: navigation.yml\n")
	if err := stageEditionPreview(root); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "fern", "docs.yml"))
	if strings.Contains(string(data), "Development") || !strings.Contains(string(data), "1.0.3") {
		t.Fatalf("release-branch default changed: %s", data)
	}
}

func TestEditionPreviewRejectsInvalidConfiguration(t *testing.T) {
	for name, config := range map[string]string{
		"not a mapping":      "[]\n",
		"missing versions":   "title: Example\n",
		"empty versions":     "versions: []\n",
		"invalid versions":   "versions: {}\n",
		"invalid entry":      "versions: [42]\n",
		"duplicate versions": "versions: []\nversions: []\n",
		"remote only":        "versions:\n  - ref: docs/releases/1.0.2\n",
		"remote development": "versions:\n  - slug: dev\n    ref: main\n    path: navigation.yml\n",
		"hidden development": "versions:\n  - slug: dev\n    hidden: true\n    path: navigation.yml\n",
		"duplicate dev":      "versions:\n  - slug: dev\n    path: navigation.yml\n  - display-name: Development\n    path: navigation.yml\n",
		"path escapes fern":  "versions:\n  - slug: dev\n    path: ../navigation.yml\n",
		"missing navigation": "versions:\n  - slug: dev\n    path: missing.yml\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := previewFixture(t, config)
			if err := stageEditionPreview(root); err == nil {
				t.Fatal("invalid preview configuration accepted")
			}
			data, _ := os.ReadFile(filepath.Join(root, "fern", "docs.yml"))
			if string(data) != config {
				t.Fatal("failed preview changed configuration")
			}
		})
	}
}
