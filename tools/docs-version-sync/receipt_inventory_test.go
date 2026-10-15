// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReceiptInventoryIncludesBundledChartAndDefaultHookImage(t *testing.T) {
	root := docsVersionSyncRepoRoot(t)
	config, err := loadResolvedInventoryConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var receipt *resolvedInventoryState
	for _, state := range config.States {
		if filepath.Base(state.Path) == "04-upgrade-receipt.yaml.gotmpl" {
			receipt = &state
		}
	}
	if receipt == nil {
		t.Fatal("self-managed inventory omits the receipt stage")
	}
	if os.Getenv("NVCF_RECEIPT_INVENTORY_E2E") != "1" {
		t.Skip("set NVCF_RECEIPT_INVENTORY_E2E=1 to render the real receipt stage with Helm and Helmfile")
	}
	for _, tool := range []string{"helm", "helmfile"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal(err)
		}
	}
	copiedRoot := t.TempDir()
	if err := copyResolvedInventoryInputs(root, copiedRoot, []resolvedInventoryState{*receipt}); err != nil {
		t.Fatal(err)
	}
	source := stackSourceRelease{Version: "1.2.3", Tag: stackTagPrefix + "1.2.3", Commit: strings.Repeat("a", 40)}
	env := setResolvedInventoryEnvironment(os.Environ(), map[string]string{"HELMFILE_ENV": "base"})
	releases, manifests, _, err := collectResolvedInventoryState(root, copiedRoot,
		filepath.Join(copiedRoot, receipt.Path), t.TempDir(), source, *receipt, env,
		resolvedInventoryHelmSource{}, config.PublishedChartRepository, nil, false, "", execResolvedInventoryCommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := generateResolvedStackInventory(source, []resolvedInventoryPlaneInput{{
		Name: receipt.Plane, ReleaseList: raw, ManifestByRelease: manifests,
	}})
	if err != nil {
		t.Fatal(err)
	}
	valuesRaw, err := os.ReadFile(filepath.Join(root, "deploy/stacks/self-managed/charts/nvcf-upgrade-receipt/values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Image map[string]string `yaml:"image"`
	}
	if err := yaml.Unmarshal(valuesRaw, &values); err != nil {
		t.Fatal(err)
	}
	wantImage := values.Image["registry"] + "/" + values.Image["repository"] + ":" + values.Image["tag"]
	if len(inventory.Releases) != 1 || inventory.Releases[0].Name != "upgrade-receipt" || !inventory.Releases[0].Required {
		t.Fatalf("receipt releases = %+v", inventory.Releases)
	}
	if len(inventory.Artifacts) != 2 {
		t.Fatalf("receipt artifacts = %+v, want chart and helper", inventory.Artifacts)
	}
	for _, artifact := range inventory.Artifacts {
		if len(artifact.Sources) != 1 || artifact.Sources[0].Requirement != ManifestRequired {
			t.Fatalf("receipt source = %+v, want required", artifact.Sources)
		}
		switch artifact.Type {
		case "container-image":
			if artifact.Reference != wantImage {
				t.Fatalf("receipt helper = %s, want %s", artifact.Reference, wantImage)
			}
		case "helm-chart":
			if !strings.Contains(artifact.Reference, source.Commit+"/deploy/stacks/self-managed/charts/nvcf-upgrade-receipt@") {
				t.Fatalf("bundled chart is not pinned to stack commit: %s", artifact.Reference)
			}
		default:
			t.Fatalf("unexpected artifact: %+v", artifact)
		}
	}
}

func TestInventoryPreservesMultipleHelperVersionsInOneStack(t *testing.T) {
	inventories := map[string]resolvedStackInventory{
		selfManagedStackKey:   testSeparatedInventory(t, stackInventorySpecs[0], "control-plane", "api"),
		computePlaneStackKey:  testSeparatedInventory(t, stackInventorySpecs[1], "compute-plane", "nvca"),
		observabilityStackKey: testSeparatedInventory(t, stackInventorySpecs[2], "observability", "collector"),
	}
	self := inventories[selfManagedStackKey]
	for _, version := range []string{"1.33.1", "1.37.0"} {
		self.Artifacts = append(self.Artifacts, resolvedInventoryArtifact{
			Type: "container-image", Name: "k8s", Repository: "docker.io/alpine/k8s",
			Version: version, Reference: "docker.io/alpine/k8s:" + version,
			Sources: []resolvedArtifactSource{{Plane: "control-plane", Release: "api", Requirement: ManifestRequired}},
		})
	}
	sort.Slice(self.Artifacts, func(i, j int) bool {
		return compareResolvedInventoryArtifacts(self.Artifacts[i], self.Artifacts[j]) < 0
	})
	inventories[selfManagedStackKey] = self
	combined, err := mergeResolvedStackInventories(inventories)
	if err != nil {
		t.Fatal(err)
	}
	base := &Catalog{Artifacts: []Artifact{{Name: "k8s", Type: ArtifactTypeImage, UpstreamRepository: "docker.io/alpine/k8s", Version: "1.37.0"}}}
	artifacts, err := catalogArtifactsFromResolvedStackInventory(combined, base)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, artifact := range artifacts {
		if artifact.Name == "k8s" {
			keys[artifact.Version] = artifact.catalogKey()
			if artifact.UpstreamRepository != "docker.io/alpine/k8s" || artifact.Requirement != ManifestRequired {
				t.Fatalf("helper facts changed: %+v", artifact)
			}
		}
	}
	if len(keys) != 2 || keys["1.37.0"] != "k8s" || keys["1.33.1"] != "k8s-image-1.33.1" {
		t.Fatalf("helper catalog identities = %v", keys)
	}
	var helpers []Artifact
	for _, artifact := range artifacts {
		if artifact.Name == "k8s" {
			helpers = append(helpers, artifact)
		}
	}
	catalog := newCatalogFromArtifacts("1.2.3", helpers)
	catalog.Manifest.Entries = []ManifestEntry{{ArtifactID: catalog.Stack.Name, Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Stack bundle."}}
	for _, helper := range helpers {
		catalog.Manifest.Entries = append(catalog.Manifest.Entries, ManifestEntry{
			ArtifactID: helper.catalogKey(), Plane: ManifestPlaneControl, Kind: ManifestKindServiceImage,
			Requirement: ManifestRequired, Description: "Kubernetes deployment helper.",
		})
	}
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	rendered, err := renderManifestArtifactRegistryPaths(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for version := range keys {
		if !strings.Contains(rendered, "docker.io/alpine/k8s:"+version) {
			t.Fatalf("manifest omitted helper %s:\n%s", version, rendered)
		}
	}
	base.VersionOverrides = []VersionOverride{{Name: "k8s", Type: ArtifactTypeImage, Version: "1.37.0"}}
	if _, err := catalogArtifactsFromResolvedStackInventory(combined, base); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous version override error = %v", err)
	}
	base.VersionOverrides = nil
	refreshed, err := catalogArtifactsFromResolvedStackInventory(combined, &Catalog{Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range refreshed {
		if artifact.Name == "k8s" && artifact.catalogKey() != keys[artifact.Version] {
			t.Fatalf("refresh changed helper identity: %+v", artifact)
		}
	}

	for _, mutation := range []string{"repository", "digest", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			bad := self
			bad.Artifacts = append([]resolvedInventoryArtifact(nil), self.Artifacts...)
			for i := range bad.Artifacts {
				artifact := &bad.Artifacts[i]
				if artifact.Name != "k8s" || artifact.Version != "1.33.1" {
					continue
				}
				switch mutation {
				case "repository":
					artifact.Repository = "docker.io/other/k8s"
					artifact.Reference = artifact.Repository + ":" + artifact.Version
				case "digest":
					artifact.Version = "1.37.0"
					artifact.Digest = "sha256:" + strings.Repeat("b", 64)
					artifact.Reference = artifact.Repository + ":" + artifact.Version + "@" + artifact.Digest
				case "duplicate":
					bad.Artifacts = append(bad.Artifacts, *artifact)
				}
				break
			}
			sort.Slice(bad.Artifacts, func(i, j int) bool { return compareResolvedInventoryArtifacts(bad.Artifacts[i], bad.Artifacts[j]) < 0 })
			inventories[selfManagedStackKey] = bad
			if _, err := mergeResolvedStackInventories(inventories); err == nil {
				t.Fatalf("accepted conflicting helper %s", mutation)
			}
		})
	}
}
