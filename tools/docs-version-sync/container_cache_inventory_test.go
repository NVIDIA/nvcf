// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestContainerCacheInventoryIncludesOptionalHelpers verifies the inventory uses
// the chart's image defaults and includes its optional monitoring sidecar.
func TestContainerCacheInventoryIncludesOptionalHelpers(t *testing.T) {
	root := docsVersionSyncRepoRoot(t)
	config, err := loadResolvedInventoryConfig(root, filepath.Join(root, "deploy/stacks/nvcf-compute-plane/release-inventory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var state *resolvedInventoryState
	for _, candidate := range config.States {
		if strings.HasSuffix(candidate.Path, "/inventory/container-cache.yaml.gotmpl") {
			state = &candidate
			break
		}
	}
	if state == nil {
		t.Fatal("compute-plane inventory omits container-cache")
	}
	chart, ok := config.SourceCharts["nvcf-container-cache"]
	if !ok || chart.Path != "deploy/helm/container-cache/deploy" || chart.TagPrefix == "" {
		t.Fatalf("missing immutable container-cache chart source: %+v", chart)
	}
	if os.Getenv("NVCF_CACHE_INVENTORY_E2E") != "1" {
		t.Skip("set NVCF_CACHE_INVENTORY_E2E=1 to render with Helm and Helmfile")
	}
	copiedRoot := t.TempDir()
	if err := copyResolvedInventoryInputs(root, copiedRoot, []resolvedInventoryState{*state}); err != nil {
		t.Fatal(err)
	}
	chartPath := filepath.Join(copiedRoot, "deploy/stacks/nvcf-compute-plane/charts/nvcf-container-cache")
	if err := copyResolvedInventoryStack(filepath.Join(root, chart.Path), chartPath); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(copiedRoot, state.Path)
	if err := replaceResolvedInventoryStateChart(stateFile, "nvcf-container-cache", "nvcf-container-cache", chartPath, nil); err != nil {
		t.Fatal(err)
	}
	state.BaseOverrides = append(state.BaseOverrides, "global.helm.sources.registry=nvcr.io", "global.helm.sources.repository=nvidia/nvcf")
	source := stackSourceRelease{Version: "1.2.3", Tag: "deploy/stacks/nvcf-compute-plane/v1.2.3", Commit: strings.Repeat("a", 40)}
	releases, manifests, _, err := collectResolvedInventoryState(root, copiedRoot, stateFile, t.TempDir(), source, *state,
		os.Environ(), resolvedInventoryHelmSource{Registry: "nvcr.io", Repository: "nvidia/nvcf"}, config.PublishedChartRepository, nil, false, "", execResolvedInventoryCommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := generateResolvedStackInventory(source, []resolvedInventoryPlaneInput{{Name: state.Plane, ReleaseList: raw, ManifestByRelease: manifests}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Releases) != 1 || inventory.Releases[0].Required {
		t.Fatalf("expected one optional release: %+v", inventory.Releases)
	}
	valuesRaw, err := os.ReadFile(filepath.Join(root, chart.Path, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Images struct {
			Server       string `yaml:"server"`
			Certificates string `yaml:"certificates"`
			Exporter     string `yaml:"exporter"`
		} `yaml:"images"`
	}
	if err := yaml.Unmarshal(valuesRaw, &values); err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{values.Images.Server: false, values.Images.Certificates: false, values.Images.Exporter: false}
	chartSeen := false
	for _, artifact := range inventory.Artifacts {
		for _, source := range artifact.Sources {
			if source.Plane != "compute-plane" || source.Requirement != ManifestOptional {
				t.Fatalf("unexpected artifact ownership: %+v", artifact)
			}
		}
		if artifact.Type == "helm-chart" {
			chartSeen = artifact.Name == "nvcf-container-cache"
			continue
		}
		// Older source snapshots can contain additional helper images.
		if _, ok := wanted[artifact.Reference]; ok {
			wanted[artifact.Reference] = true
		}
	}
	if !chartSeen {
		t.Fatal("inventory omits the chart")
	}
	for ref, seen := range wanted {
		if !seen {
			t.Errorf("inventory omits %s", ref)
		}
	}
}
