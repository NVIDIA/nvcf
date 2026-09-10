// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestUpdateCatalogFromGitHubUsesSelectedReleaseInventoryWithoutGitLab(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", testCatalogPinSources())
	inventory := testCatalogResolvedInventory(t, release)
	raw, err := marshalResolvedStackInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}

	requested := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested[r.URL.Path] = true
		switch r.URL.Path {
		case "/repos/NVIDIA/nvcf/git/ref/tags/" + release.Tag:
			fmt.Fprintf(w, `{"ref":"refs/tags/%s","object":{"type":"commit","sha":"%s"}}`, release.Tag, release.Commit)
		case "/repos/NVIDIA/nvcf/releases/tags/" + release.Tag:
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q}]}`, release.Tag, resolvedStackInventoryAssetName, server.URL+"/inventory")
		case "/inventory":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("asset request Authorization = %q, want no credentials", got)
			}
			w.Write(raw)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Setenv("DOC_VERSION_SYNC_GITHUB_API_URL", server.URL)
	t.Setenv("DOC_VERSION_SYNC_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("DOC_VERSION_SYNC_GITLAB_BASE_URL", "://must-not-be-read")
	t.Setenv("DOC_VERSION_SYNC_GITLAB_TOKEN", "must-not-be-read")

	catalog, err := updateCatalogFromGitHub(repo, release.Version, testCatalog())
	if err != nil {
		t.Fatalf("updateCatalogFromGitHub failed: %v", err)
	}
	for _, path := range []string{
		"/repos/NVIDIA/nvcf/git/ref/tags/" + release.Tag,
		"/repos/NVIDIA/nvcf/releases/tags/" + release.Tag,
		"/inventory",
	} {
		if !requested[path] {
			t.Errorf("GitHub path %s was not requested", path)
		}
	}
	if catalog.Stack.SourceVersion != release.Version || catalog.Stack.SourceTag != release.Tag || catalog.Stack.SourceCommit != release.Commit {
		t.Fatalf("stack source metadata = %#v, want %+v", catalog.Stack, release)
	}
	if catalog.Stack.GitLabProjectID != 0 || catalog.Stack.PackageName != "" || catalog.Stack.ArtifactsFile != "" {
		t.Fatalf("updated stack metadata retains GitLab package fields: %#v", catalog.Stack)
	}
	compute, ok := catalog.findArtifact(computeStackResourceName)
	if !ok || compute.Version != release.Version {
		t.Fatalf("compute stack = %#v, want release version %s", compute, release.Version)
	}
}

func TestBuildCatalogFromResolvedInventoryKeepsPublicationAvailabilityIndependent(t *testing.T) {
	source := stackSourceRelease{Version: "1.2.3", Tag: stackTagPrefix + "1.2.3", Commit: strings.Repeat("a", 40)}
	inventory := testCatalogResolvedInventory(t, source)
	snapshot := stackSourceSnapshot{Release: source, Files: testCatalogPinSourceBytes()}
	base := testCatalog()
	base.Registries["public-helm"] = Registry{Host: "https://helm.ngc.nvidia.com", Namespace: "nvidia/nvcf", RepositoryAlias: "nvcf"}
	base.Registries["public-resources"] = Registry{Host: "nvcr.io", Namespace: "nvidia/nvcf"}
	base.Stack.Registry = "public-resources"
	base.Denylist = append(base.Denylist, DenylistEntry{Name: "source-chart", Reason: "source-only test chart"})
	base.Artifacts = append(base.Artifacts, Artifact{Name: "stale-service", Type: ArtifactTypeImage, Registry: "staging", Version: "0.9.0"})
	base.SupplementalArtifacts = append(base.SupplementalArtifacts, Artifact{Name: "independent-resource", Type: ArtifactTypeResource, Registry: "public-resources", Version: "4.5.6"})
	base.Publications = []Publication{
		{Name: "helm-nvcf-llm-request-router", Version: "1.2.3", Registry: "public-helm", ChartFormat: ChartFormatHTTP},
		{Name: "nvca", Version: "6.7.8", Registry: "public-resources"},
		{Name: computeStackResourceName, Version: source.Version, Registry: "public-resources"},
	}

	catalog, err := buildCatalogFromResolvedStackInventory(inventory, snapshot, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.findArtifact("source-chart"); found {
		t.Fatal("custom-denylisted source-chart is present")
	}
	if _, found := catalog.findArtifact("stale-service"); found {
		t.Fatal("artifact absent from the resolved inventory was retained")
	}
	if _, found := catalog.findArtifact("independent-resource"); !found {
		t.Fatal("independent resource artifact was not preserved")
	}
	router, ok := catalog.findArtifact("helm-nvcf-llm-request-router")
	if !ok {
		t.Fatal("catalog is missing request router chart")
	}
	distribution, err := catalog.artifactDistribution(router)
	if err != nil {
		t.Fatal(err)
	}
	if distribution != "https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-request-router:1.2.3" {
		t.Fatalf("published router distribution = %q", distribution)
	}
	pylon, ok := catalog.findArtifact("pylon")
	if !ok {
		t.Fatal("catalog is missing pylon")
	}
	distribution, err = catalog.artifactDistribution(pylon)
	if err != nil {
		t.Fatal(err)
	}
	if distribution != "Publication pending" {
		t.Fatalf("unpublished pylon distribution = %q, want Publication pending", distribution)
	}
	nvca, _ := catalog.findArtifact("nvca")
	distribution, err = catalog.artifactDistribution(nvca)
	if err != nil {
		t.Fatal(err)
	}
	if distribution != "nvcr.io/nvidia/nvcf/nvca:6.7.8@sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("digest-pinned NVCA distribution = %q", distribution)
	}
	compute, _ := catalog.findArtifact(computeStackResourceName)
	if catalog.publicationIsPending(compute) {
		t.Fatal("compute stack has an exact publication but is marked pending")
	}
	if catalog.Stack.PinSourceDigest == "" || strings.Join(catalog.Stack.PinSources, ",") != strings.Join(effectiveStackPinSourcePaths(effectiveStackPins), ",") {
		t.Fatalf("stack pin metadata = %#v", catalog.Stack)
	}
}

func TestBuildCatalogFromResolvedInventoryRejectsPinDrift(t *testing.T) {
	source := stackSourceRelease{Version: "1.2.3", Tag: stackTagPrefix + "1.2.3", Commit: strings.Repeat("a", 40)}
	inventory := testCatalogResolvedInventory(t, source)
	for i := range inventory.Artifacts {
		if inventory.Artifacts[i].Name == "pylon" {
			inventory.Artifacts[i].Version = "9.9.9"
			inventory.Artifacts[i].Reference = "nvcr.io/nvidia/nvcf/pylon:9.9.9"
		}
	}
	sort.Slice(inventory.Artifacts, func(i, j int) bool {
		return compareResolvedInventoryArtifacts(inventory.Artifacts[i], inventory.Artifacts[j]) < 0
	})

	_, err := buildCatalogFromResolvedStackInventory(inventory, stackSourceSnapshot{Release: source, Files: testCatalogPinSourceBytes()}, testCatalog())
	if err == nil || !strings.Contains(err.Error(), "pylon version is 9.9.9, want tagged stack pin 3.4.5") {
		t.Fatalf("pin drift error = %v", err)
	}
}

func TestCatalogArtifactsFromResolvedInventoryPreservesTypedIDs(t *testing.T) {
	source := stackSourceRelease{Version: "1.2.3", Tag: stackTagPrefix + "1.2.3", Commit: strings.Repeat("a", 40)}
	inventory := testCatalogResolvedInventory(t, source)
	inventory.Artifacts = append(inventory.Artifacts, resolvedInventoryArtifact{
		Type:       "container-image",
		Name:       "helm-nvcf-llm-request-router",
		Repository: "nvcr.io/nvidia/nvcf/helm-nvcf-llm-request-router",
		Version:    "1.2.3",
		Reference:  "nvcr.io/nvidia/nvcf/helm-nvcf-llm-request-router:1.2.3",
		Sources:    []resolvedArtifactSource{{Plane: "control-plane", Release: "llm-request-router"}},
	})
	base := testCatalog()
	base.Artifacts = []Artifact{
		{ID: "request-router-chart", Name: "helm-nvcf-llm-request-router", Type: ArtifactTypeChart, Registry: "staging", Version: "1.0.0"},
		{ID: "request-router-image", Name: "helm-nvcf-llm-request-router", Type: ArtifactTypeImage, Registry: "staging", Version: "1.0.0"},
	}

	artifacts, err := catalogArtifactsFromResolvedStackInventory(inventory, base)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[ArtifactType]string{}
	for _, artifact := range artifacts {
		if artifact.Name == "helm-nvcf-llm-request-router" {
			ids[artifact.Type] = artifact.ID
		}
	}
	if ids[ArtifactTypeChart] != "request-router-chart" || ids[ArtifactTypeImage] != "request-router-image" {
		t.Fatalf("preserved IDs = %#v", ids)
	}
}

func testCatalogResolvedInventory(t *testing.T, source stackSourceRelease) resolvedStackInventory {
	t.Helper()
	releases := []resolvedInventoryRelease{
		{Plane: "compute-plane", Name: "nvca-operator", Namespace: "nvca-operator", Required: true, Chart: "https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvca-operator", Version: "4.5.6"},
		{Plane: "control-plane", Name: "ingress", Namespace: "gateway-system", Required: true, Chart: "https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-gateway-routes", Version: "2.3.4"},
		{Plane: "control-plane", Name: "llm-request-router", Namespace: "nvcf", Required: false, Chart: "https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-request-router", Version: "1.2.3"},
		{Plane: "observability", Name: "collector", Namespace: "monitoring", Required: false, Chart: "https://github.com/NVIDIA/nvcf/tree/" + source.Commit + "/deploy/stacks/observability/charts/source-chart", Version: "0.1.0"},
	}
	artifacts := []resolvedInventoryArtifact{
		{Type: "container-image", Name: "nvca", Repository: "nvcr.io/nvidia/nvcf/nvca", Version: "6.7.8", Digest: "sha256:" + strings.Repeat("b", 64), Reference: "nvcr.io/nvidia/nvcf/nvca:6.7.8@sha256:" + strings.Repeat("b", 64), Sources: []resolvedArtifactSource{{Plane: "compute-plane", Release: "nvca-operator"}}},
		{Type: "container-image", Name: "nvca-operator", Repository: "nvcr.io/nvidia/nvcf/nvca-operator", Version: "5.6.7", Reference: "nvcr.io/nvidia/nvcf/nvca-operator:5.6.7", Sources: []resolvedArtifactSource{{Plane: "compute-plane", Release: "nvca-operator"}}},
		{Type: "container-image", Name: "pylon", Repository: "nvcr.io/nvidia/nvcf/pylon", Version: "3.4.5", Reference: "nvcr.io/nvidia/nvcf/pylon:3.4.5", Sources: []resolvedArtifactSource{{Plane: "control-plane", Release: "llm-request-router"}}},
		{Type: "helm-chart", Name: "helm-nvca-operator", Repository: "https://helm.ngc.nvidia.com/nvidia/nvcf", Version: "4.5.6", Reference: "https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvca-operator@4.5.6", Sources: []resolvedArtifactSource{{Plane: "compute-plane", Release: "nvca-operator"}}},
		{Type: "helm-chart", Name: "helm-nvcf-llm-request-router", Repository: "https://helm.ngc.nvidia.com/nvidia/nvcf", Version: "1.2.3", Reference: "https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-request-router@1.2.3", Sources: []resolvedArtifactSource{{Plane: "control-plane", Release: "llm-request-router"}}},
		{Type: "helm-chart", Name: "nvcf-gateway-routes", Repository: "https://helm.ngc.nvidia.com/nvidia/nvcf", Version: "2.3.4", Reference: "https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-gateway-routes@2.3.4", Sources: []resolvedArtifactSource{{Plane: "control-plane", Release: "ingress"}}},
		{Type: "helm-chart", Name: "source-chart", Repository: "https://github.com/NVIDIA/nvcf/tree/" + source.Commit + "/deploy/stacks/observability/charts", Version: "0.1.0", Reference: "https://github.com/NVIDIA/nvcf/tree/" + source.Commit + "/deploy/stacks/observability/charts/source-chart@0.1.0", Sources: []resolvedArtifactSource{{Plane: "observability", Release: "collector"}}},
	}
	sort.Slice(releases, func(i, j int) bool { return compareResolvedInventoryReleases(releases[i], releases[j]) < 0 })
	sort.Slice(artifacts, func(i, j int) bool { return compareResolvedInventoryArtifacts(artifacts[i], artifacts[j]) < 0 })
	inventory := resolvedStackInventory{SchemaVersion: resolvedStackInventorySchemaVersion, Source: source, Releases: releases, Artifacts: artifacts}
	if err := validateResolvedStackInventory(inventory); err != nil {
		t.Fatalf("test inventory is invalid: %v", err)
	}
	return inventory
}

func testCatalogPinSources() map[string]string {
	return map[string]string{
		"deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl":       "  - name: llm-request-router\n    version: 1.2.3\n  - name: ingress\n    version: 2.3.4\n",
		"deploy/stacks/self-managed/global.yaml.gotmpl":                   "image: nvcr.io/nvidia/nvcf/pylon:3.4.5\n",
		"deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl": "  - name: nvca-operator\n    version: 4.5.6\n",
		"deploy/stacks/nvcf-compute-plane/environments/base.yaml":         "  nvcaOperator:\n    imageTag: \"5.6.7\"\n    selfManaged:\n      nvcaVersion: \"6.7.8\"\n",
	}
}

func testCatalogPinSourceBytes() map[string][]byte {
	stringsByPath := testCatalogPinSources()
	bytesByPath := make(map[string][]byte, len(stringsByPath))
	for path, body := range stringsByPath {
		bytesByPath[path] = []byte(body)
	}
	return bytesByPath
}
