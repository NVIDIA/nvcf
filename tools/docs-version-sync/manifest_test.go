// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func loadMainCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := LoadCatalog(filepath.Join("..", "..", "docs", "version-catalog", "main.yaml"))
	if err != nil {
		t.Fatalf("LoadCatalog failed: %v", err)
	}
	return catalog
}

func TestResolveManifestEntriesRejectsUnclassifiedArtifact(t *testing.T) {
	catalog := loadMainCatalog(t)
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts,
		Artifact{
			Name:     "new-unclassified-service",
			Type:     ArtifactTypeImage,
			Registry: "staging",
			Version:  "1.0.0",
		},
		Artifact{
			Name:     "another-unclassified-service",
			Type:     ArtifactTypeImage,
			Registry: "staging",
			Version:  "1.0.0",
		},
	)

	_, err := resolveManifestEntries(catalog)
	want := "unclassified artifacts: another-unclassified-service, new-unclassified-service"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("resolveManifestEntries error = %v, want %q", err, want)
	}
}

func TestResolveManifestEntriesKeepsCassandraOnlyInEASection(t *testing.T) {
	entries, err := resolveManifestEntries(loadMainCatalog(t))
	if err != nil {
		t.Fatalf("resolveManifestEntries failed: %v", err)
	}

	var eaIDs []string
	for _, entry := range entries {
		if entry.Kind == ManifestKindEACVE {
			eaIDs = append(eaIDs, entry.ID)
		}
		if entry.Kind == ManifestKindServiceImage && (entry.ID == "bitnami-cassandra" || entry.ID == "nvcf-cassandra-migrations") {
			t.Fatalf("EA artifact %s also appears as a service image", entry.ID)
		}
	}
	sort.Strings(eaIDs)
	if got, want := strings.Join(eaIDs, ","), "bitnami-cassandra,nvcf-cassandra-migrations"; got != want {
		t.Fatalf("EA entries = %q, want %q", got, want)
	}
}

func TestRenderManifestTable(t *testing.T) {
	got, err := Render("manifest-artifact-registry-paths", loadMainCatalog(t))
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	wantInOrder := []string{
		"### Control plane Helm charts",
		"### Control plane services and images",
		"### Compute plane Helm charts",
		"### Compute plane services and images",
		"### EA-only CVE-impacted artifacts",
		"### Tools and deployment resources",
	}
	last := -1
	for _, want := range wantInOrder {
		index := strings.Index(got, want)
		if index < 0 {
			t.Fatalf("rendered manifest missing %q:\n%s", want, got)
		}
		if index <= last {
			t.Fatalf("rendered manifest section %q is out of order", want)
		}
		last = index
	}

	for _, want := range []string{
		"| Artifact | Version | Required | Description | Distribution | Source code |",
		"These Early Access artifacts have known CVE impact.",
		"[GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats)",
		"[Upstream](https://github.com/nats-io/k8s)",
		"`bitnami-cassandra`",
		"`nvcf-cassandra-migrations`",
		"`nvcf-self-managed-stack`",
		"`nvcf-compute-plane-stack`",
		"`nvcf-cli`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered manifest missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "load_tester_supreme") {
		t.Fatalf("rendered manifest contains load_tester_supreme:\n%s", got)
	}
	if strings.Contains(got, "Option A") || strings.Contains(got, "Option B") {
		t.Fatalf("rendered manifest contains layout comparison headings:\n%s", got)
	}
}

func TestManifestTableContainsAllArtifacts(t *testing.T) {
	entries, err := resolveManifestEntries(loadMainCatalog(t))
	if err != nil {
		t.Fatalf("resolveManifestEntries failed: %v", err)
	}
	tables := renderManifestTables(entries)

	for _, entry := range entries {
		tableNeedle := "| `" + entry.Name + "` | `" + entry.Version + "` |"
		if !strings.Contains(tables, tableNeedle) {
			t.Errorf("table layout missing %s", entry.ID)
		}
	}
}
