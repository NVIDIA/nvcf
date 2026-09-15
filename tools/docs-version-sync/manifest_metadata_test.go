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

func TestValidateCatalogAcceptsManifestMetadata(t *testing.T) {
	catalog := testCatalog()
	catalog.Manifest = ManifestMetadata{Entries: []ManifestEntry{
		{
			ArtifactID:  "llm-api-gateway",
			Plane:       ManifestPlaneControl,
			Kind:        ManifestKindServiceImage,
			Requirement: ManifestRequired,
			Description: "Routes OpenAI-compatible LLM requests.",
			GitHubURL:   "https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/llm-api-gateway",
		},
		{
			Name:         "gpu-operator",
			Version:      "supported",
			Distribution: "https://helm.ngc.nvidia.com/nvidia",
			Plane:        ManifestPlaneCompute,
			Kind:         ManifestKindChart,
			Requirement:  ManifestRequired,
			Description:  "Manages NVIDIA GPU software on Kubernetes nodes.",
			UpstreamURL:  "https://github.com/NVIDIA/gpu-operator",
		},
	}}

	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("ValidateCatalog failed: %v", err)
	}
}

func TestValidateCatalogRejectsInvalidManifestMetadata(t *testing.T) {
	validCatalogEntry := ManifestEntry{
		ArtifactID:  "llm-api-gateway",
		Plane:       ManifestPlaneControl,
		Kind:        ManifestKindServiceImage,
		Requirement: ManifestRequired,
		Description: "Routes OpenAI-compatible LLM requests.",
		GitHubURL:   "https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/llm-api-gateway",
	}
	validStaticEntry := ManifestEntry{
		Name:         "gpu-operator",
		Version:      "supported",
		Distribution: "https://helm.ngc.nvidia.com/nvidia",
		Plane:        ManifestPlaneCompute,
		Kind:         ManifestKindChart,
		Requirement:  ManifestRequired,
		Description:  "Manages NVIDIA GPU software on Kubernetes nodes.",
		UpstreamURL:  "https://github.com/NVIDIA/gpu-operator",
	}
	validResourceEntry := ManifestEntry{
		ArtifactID:  "nvcf-cli",
		Plane:       ManifestPlaneShared,
		Kind:        ManifestKindResource,
		Description: "Manages functions, deployments, and clusters from the command line.",
	}

	tests := []struct {
		name    string
		entries []ManifestEntry
		want    string
	}{
		{
			name: "unknown plane",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validCatalogEntry
				entry.Plane = "edge"
				return entry
			}()},
			want: "unsupported plane",
		},
		{
			name: "static entry error uses name",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validStaticEntry
				entry.Plane = "edge"
				return entry
			}()},
			want: `manifest entry "gpu-operator" has unsupported plane "edge"`,
		},
		{
			name: "unknown kind",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validCatalogEntry
				entry.Kind = "binary"
				return entry
			}()},
			want: "unsupported kind",
		},
		{
			name: "unknown requirement",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validCatalogEntry
				entry.Requirement = "recommended"
				return entry
			}()},
			want: "unsupported requirement",
		},
		{
			name: "empty description",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validCatalogEntry
				entry.Description = ""
				return entry
			}()},
			want: "empty description",
		},
		{
			name: "non github source",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validCatalogEntry
				entry.GitHubURL = "https://gitlab.example.com/nvcf"
				return entry
			}()},
			want: "github_url must use https://github.com/",
		},
		{
			name: "catalog entry with static fields",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validCatalogEntry
				entry.Version = "1.0.0"
				return entry
			}()},
			want: "catalog-backed entry cannot set static fields",
		},
		{
			name: "static entry without version",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validStaticEntry
				entry.Version = ""
				return entry
			}()},
			want: "static entry requires name, version, and distribution",
		},
		{
			name: "duplicate id",
			entries: []ManifestEntry{
				validCatalogEntry,
				validCatalogEntry,
			},
			want: "duplicate manifest entry",
		},
		{
			name: "resource uses shared plane",
			entries: []ManifestEntry{func() ManifestEntry {
				entry := validResourceEntry
				entry.Plane = ManifestPlaneCompute
				return entry
			}()},
			want: `manifest resource entry "nvcf-cli" must use plane "shared"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := testCatalog()
			catalog.Manifest = ManifestMetadata{Entries: tt.entries}
			err := ValidateCatalog(catalog)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCatalog error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCatalogRefreshPreservesManifestMetadata(t *testing.T) {
	base := testCatalog()
	base.Manifest = ManifestMetadata{Entries: []ManifestEntry{{
		ArtifactID:  "llm-api-gateway",
		Plane:       ManifestPlaneControl,
		Kind:        ManifestKindServiceImage,
		Requirement: ManifestRequired,
		Description: "Routes OpenAI-compatible LLM requests.",
	}}}

	updated := BuildCatalogFromArtifactsWithBase("0.6.0-rc.99", base.Artifacts, base)
	if len(updated.Manifest.Entries) != 1 || updated.Manifest.Entries[0].ArtifactID != "llm-api-gateway" {
		t.Fatalf("manifest metadata = %#v, want preserved entry", updated.Manifest)
	}
}

func TestManifestMetadataClassifiesAllArtifacts(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "docs", "version-catalog", "main.yaml"))
	if err != nil {
		t.Fatalf("LoadCatalog failed: %v", err)
	}

	want := map[string]struct{}{catalog.Stack.Name: {}}
	denylist := catalog.DenylistMap()
	for _, artifact := range append(append([]Artifact{}, catalog.Artifacts...), catalog.SupplementalArtifacts...) {
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		want[artifact.catalogKey()] = struct{}{}
	}

	got := map[string]struct{}{}
	var eaCVE []string
	for _, entry := range catalog.Manifest.Entries {
		if entry.ArtifactID == "" {
			continue
		}
		got[entry.ArtifactID] = struct{}{}
		if entry.Kind == ManifestKindEACVE {
			eaCVE = append(eaCVE, entry.ArtifactID)
		}
		if entry.ArtifactID == "load_tester_supreme" {
			t.Fatal("manifest metadata must not classify load_tester_supreme")
		}
	}

	var missing, extra []string
	for key := range want {
		if _, ok := got[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("manifest classification mismatch: missing=%v extra=%v", missing, extra)
	}

	sort.Strings(eaCVE)
	wantEACVE := []string{"bitnami-cassandra", "nvcf-cassandra-migrations"}
	if strings.Join(eaCVE, ",") != strings.Join(wantEACVE, ",") {
		t.Fatalf("EA-CVE entries = %v, want %v", eaCVE, wantEACVE)
	}
}
