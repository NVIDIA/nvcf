// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditionLinksPreserveVersionAndFrozenContent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fern", "navigation.yml"), `tabs:
  overview:
    slug: overview
  compute:
    slug: compute-plane
navigation:
  - tab: overview
    layout:
      - page: Overview
        slug: overview
        path: ../docs/overview/index.md
  - tab: compute
    layout:
      - page: GPU cluster
        slug: gpu-cluster-setup
        path: ../docs/compute-plane/cluster.md
`)
	writeFile(t, filepath.Join(root, "fern", "docs.yml"), "redirects:\n  - source: /nvcf/overview/cluster\n    destination: /nvcf/compute-plane/gpu-cluster-setup\n")
	writeFile(t, filepath.Join(root, "docs", "compute-plane", "cluster.md"), "# Cluster\n")
	original := `[Cluster](/nvcf/compute-plane/gpu-cluster-setup#registration)
<Card href="/nvcf/overview/cluster">Cluster</Card>
[Archive](/nvcf/self-managed/1.0.1/installation-overview)
[API](https://example.com/nvcf/overview/cluster)
`
	page := filepath.Join(root, "docs", "overview", "index.md")
	writeFile(t, page, original)
	archive := filepath.Join(root, "docs", "v0.6.1", "index.md")
	writeFile(t, archive, original)
	if err := stageEditionLinks(root, "navigation.yml"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(page)
	for _, want := range []string{"[Cluster](../compute-plane/cluster.md#registration)", `href="../compute-plane/cluster.md"`, "[Archive](/nvcf/self-managed/1.0.1/installation-overview)", "https://example.com/nvcf/overview/cluster"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q in %s", want, data)
		}
	}
	frozen, _ := os.ReadFile(archive)
	if string(frozen) != original {
		t.Fatal("frozen source changed")
	}
	if err := stageEditionLinks(root, "navigation.yml"); err != nil {
		t.Fatal(err)
	}
	retry, _ := os.ReadFile(page)
	if string(retry) != string(data) {
		t.Fatal("conversion is not idempotent")
	}
	writeFile(t, page, "[Missing](/nvcf/overview/missing)\n")
	if err := stageEditionLinks(root, "navigation.yml"); err == nil || !strings.Contains(err.Error(), "no edition page") {
		t.Fatalf("missing target accepted: %v", err)
	}
}

func TestEditionLinksRejectSymlinkNavigation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fern", "docs.yml"), "redirects: []\n")
	writeFile(t, filepath.Join(root, "fern", "navigation.yml"), `tabs:
  overview:
    slug: overview
navigation:
  - tab: overview
    layout:
      - page: Shared
        slug: shared
        path: ../docs/shared.md
`)
	writeFile(t, filepath.Join(root, "docs", "page.md"), "# Shared\n")
	if err := os.Symlink("page.md", filepath.Join(root, "docs", "shared.md")); err != nil {
		t.Fatal(err)
	}
	if err := stageEditionLinks(root, "navigation.yml"); err == nil || !strings.Contains(err.Error(), "real source path") {
		t.Fatalf("symlink navigation accepted: %v", err)
	}
}

func TestEditionArchivesKeepHistoricalRoutes(t *testing.T) {
	config := map[string]any{"products": []any{map[string]any{
		"display-name": "Self-Managed", "slug": "self-managed", "versions": []any{
			map[string]any{"display-name": "1.0.1", "slug": "1.0.1", "path": "products/self-managed/1.0.1.yml"},
			map[string]any{"display-name": "dev", "slug": "dev", "path": "products/self-managed/dev.yml"},
		},
	}}}
	archives := editionArchives(config)
	if len(archives) != 1 {
		t.Fatalf("expected one archive, got %v", archives)
	}
	archive := archives[0].(map[string]any)
	if archive["slug"] != "self-managed/1.0.1" || archive["hidden"] != true || archive["path"] != "products/self-managed/1.0.1.yml" {
		t.Fatalf("archive identity changed: %v", archive)
	}
}
