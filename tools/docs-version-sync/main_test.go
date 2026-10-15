// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArtifactListAllowsDuplicateArtifactNames(t *testing.T) {
	artifacts, err := ParseArtifactList(strings.NewReader(`nvcf-openbao:2.2.2-nv-1
nvcf-openbao:2.5.1-nv-1.2.1
`), map[string]DenylistEntry{})
	if err != nil {
		t.Fatalf("ParseArtifactList failed: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("got %d artifacts, want 2", len(artifacts))
	}
	if artifacts[0].ID != "nvcf-openbao-1" || artifacts[1].ID != "nvcf-openbao-2" {
		t.Fatalf("duplicate IDs = %q, %q, want ordinal IDs", artifacts[0].ID, artifacts[1].ID)
	}
}

func TestParseArtifactListAcceptsShortArtifactRefs(t *testing.T) {
	artifacts, err := ParseArtifactList(strings.NewReader(`strap:2.242.2
helm-nvcf-api:1.16.1
`), map[string]DenylistEntry{})
	if err != nil {
		t.Fatalf("ParseArtifactList failed: %v", err)
	}
	if artifacts[0].Name != "strap" || artifacts[0].Version != "2.242.2" || artifacts[0].Type != ArtifactTypeImage {
		t.Fatalf("first artifact = %#v, want strap image", artifacts[0])
	}
	if artifacts[1].Name != "helm-nvcf-api" || artifacts[1].Version != "1.16.1" || artifacts[1].Type != ArtifactTypeChart {
		t.Fatalf("second artifact = %#v, want helm-nvcf-api chart", artifacts[1])
	}
}

func TestParseArtifactListFiltersDenylistedArtifacts(t *testing.T) {
	artifacts, err := ParseArtifactList(strings.NewReader(`nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-base:0.1.4
nvcr.io/0833294136851237/nvcf-ncp-staging/samba:4.19
nvcr.io/0833294136851237/nvcf-ncp-staging/strap:2.234.0
`), map[string]DenylistEntry{
		"nvcf-base": {Name: "nvcf-base", Reason: "not part of docs catalog"},
		"samba":     {Name: "samba", Reason: "pulled by nvcf-base"},
	})
	if err != nil {
		t.Fatalf("ParseArtifactList failed: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(artifacts))
	}
	if artifacts[0].Name != "strap" {
		t.Fatalf("remaining artifact = %q, want strap", artifacts[0].Name)
	}
}

func TestRenderManifestDeploymentResources(t *testing.T) {
	catalog := testCatalog()
	got, err := Render("manifest-artifact-registry-paths", catalog)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	wantLines := []string{
		"| Artifact | Version | Required | Description | Distribution | Source code |",
		"| `llm-api-gateway` | `0.3.0` | Optional |",
		"| `llm-request-router` | `0.2.0` | Optional |",
		"| `nvcf-self-managed-stack` | `0.5.0` |",
		"| `nvcf-compute-plane-stack` | `0.5.0` |",
		"| `nvcf-cli` | `0.0.30` |",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered table missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "nvcf-base") {
		t.Fatalf("rendered table includes denylisted artifact:\n%s", got)
	}
}

func TestRenderManifestHandlesNewNVCAAndNVCTImageAndHelmArtifacts(t *testing.T) {
	catalog := testCatalog()
	catalog.Artifacts = append(catalog.Artifacts,
		Artifact{Name: "nvca", Type: ArtifactTypeImage, Registry: "staging", Version: "3.0.0-rc.13"},
		Artifact{Name: "nvca-operator", Type: ArtifactTypeImage, Registry: "staging", Version: "3.0.0-rc.13"},
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: "staging", Version: "1.11.1"},
		Artifact{Name: "nvct-service-oss", Type: ArtifactTypeImage, Registry: "staging", Version: "1.2.11"},
		Artifact{Name: "helm-nvcf-nvct-api", Type: ArtifactTypeChart, Registry: "staging", Version: "1.4.2"},
	)
	catalog.Manifest.Entries = append(catalog.Manifest.Entries,
		ManifestEntry{ArtifactID: "nvca", Plane: ManifestPlaneCompute, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "Registers GPU clusters."},
		ManifestEntry{ArtifactID: "nvca-operator", Plane: ManifestPlaneCompute, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "Reconciles compute resources."},
		ManifestEntry{ArtifactID: "helm-nvca-operator", Plane: ManifestPlaneCompute, Kind: ManifestKindChart, Requirement: ManifestRequired, Description: "Deploys the NVCA operator."},
		ManifestEntry{ArtifactID: "nvct-service-oss", Plane: ManifestPlaneControl, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "Provides tenant operations."},
		ManifestEntry{ArtifactID: "helm-nvcf-nvct-api", Plane: ManifestPlaneControl, Kind: ManifestKindChart, Requirement: ManifestRequired, Description: "Deploys the tenant API."},
	)

	got, err := Render("manifest-artifact-registry-paths", catalog)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	computeServices := sectionBetween(t, got, "### Compute plane services and images", "### EA-only CVE-impacted artifacts")
	for _, want := range []string{
		"| `nvca` | `3.0.0-rc.13` | Required |",
		"| `nvca-operator` | `3.0.0-rc.13` | Required |",
	} {
		if !strings.Contains(computeServices, want) {
			t.Fatalf("compute services section missing %q:\n%s", want, computeServices)
		}
	}
	computeCharts := sectionBetween(t, got, "### Compute plane Helm charts", "### Compute plane services and images")
	if !strings.Contains(computeCharts, "| `helm-nvca-operator` | `1.11.1` | Required |") {
		t.Fatalf("compute charts section missing NVCA chart:\n%s", computeCharts)
	}

	controlPlane := sectionBetween(t, got, "### Control plane Helm charts", "### Compute plane Helm charts")
	for _, want := range []string{
		"| `nvct-service-oss` | `1.2.11` | Required |",
		"| `helm-nvcf-nvct-api` | `1.4.2` | Required |",
	} {
		if !strings.Contains(controlPlane, want) {
			t.Fatalf("control plane section missing %q:\n%s", want, controlPlane)
		}
	}

	if strings.Contains(got, "helm-nvct-api") {
		t.Fatalf("rendered manifest contains obsolete helm-nvct-api chart name:\n%s", got)
	}
}

func TestRenderManifestUsesVerifiedPublicLocations(t *testing.T) {
	catalog := testCatalog()
	catalog.Registries["public-images"] = Registry{
		Host:      "nvcr.io",
		Namespace: "nvidia/nvcf",
	}
	catalog.Registries["public-helm"] = Registry{
		Host:            "https://helm.ngc.nvidia.com",
		Namespace:       "nvidia/nvcf",
		RepositoryAlias: "nvcf",
	}
	catalog.Artifacts = append(catalog.Artifacts,
		Artifact{Name: "nvcf-grpc-proxy", Type: ArtifactTypeImage, Registry: "staging", Version: "1.29.1"},
		Artifact{Name: "helm-nvcf-grpc-proxy", Type: ArtifactTypeChart, Registry: "staging", Version: "1.6.7"},
		Artifact{Name: "helm-nvcf-nats", Type: ArtifactTypeChart, Registry: "staging", Version: "0.6.1"},
	)
	catalog.Publications = []Publication{
		{Name: "nvcf-grpc-proxy", Version: "1.29.1", Registry: "public-images"},
		{Name: "helm-nvcf-grpc-proxy", Version: "1.6.7", Registry: "public-helm", ChartFormat: ChartFormatHTTP},
		{Name: "helm-nvcf-nats", Version: "0.7.1", Registry: "public-helm", ChartFormat: ChartFormatHTTP},
	}
	catalog.Manifest.Entries = append(catalog.Manifest.Entries,
		ManifestEntry{ArtifactID: "nvcf-grpc-proxy", Plane: ManifestPlaneControl, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "Proxies gRPC traffic."},
		ManifestEntry{ArtifactID: "helm-nvcf-grpc-proxy", Plane: ManifestPlaneControl, Kind: ManifestKindChart, Requirement: ManifestRequired, Description: "Deploys the gRPC proxy."},
		ManifestEntry{ArtifactID: "helm-nvcf-nats", Plane: ManifestPlaneControl, Kind: ManifestKindChart, Requirement: ManifestRequired, Description: "Deploys NATS."},
	)

	got, err := Render("manifest-artifact-registry-paths", catalog)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	for _, want := range []string{
		"`nvcr.io/nvidia/nvcf/nvcf-grpc-proxy:1.29.1`",
		"`https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-grpc-proxy:1.6.7`",
		"`nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-nats:0.6.1`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered table missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "nvcr.io/nvidia/nvcf/helm-nvcf-grpc-proxy") {
		t.Fatalf("public Helm chart rendered as an OCI artifact:\n%s", got)
	}
}

func TestCatalogRefreshPreservesVersionQualifiedPublications(t *testing.T) {
	base := testCatalog()
	base.Registries["public-images"] = Registry{Host: "nvcr.io", Namespace: "nvidia/nvcf"}
	base.Publications = []Publication{{
		Name:     "nvcf-grpc-proxy",
		Version:  "1.29.1",
		Registry: "public-images",
	}}

	updated := BuildCatalogFromArtifactsWithBase("0.6.0-rc.99", []Artifact{{
		Name:     "nvcf-grpc-proxy",
		Type:     ArtifactTypeImage,
		Registry: "staging",
		Version:  "1.30.0",
	}}, base)

	if len(updated.Publications) != 1 || updated.Publications[0].Version != "1.29.1" {
		t.Fatalf("publications = %#v, want preserved verified publication", updated.Publications)
	}
	artifact, ok := updated.findArtifact("nvcf-grpc-proxy")
	if !ok {
		t.Fatal("updated catalog is missing nvcf-grpc-proxy")
	}
	path, err := updated.artifactPath(artifact)
	if err != nil {
		t.Fatalf("artifactPath failed: %v", err)
	}
	if path != "nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-grpc-proxy:1.30.0" {
		t.Fatalf("path = %q, want refreshed version at its stack-provided location", path)
	}
}

func TestArtifactPathUsesPublishedVersionAlias(t *testing.T) {
	catalog := testCatalog()
	catalog.Registries["ea-images"] = Registry{Host: "nvcr.io", Namespace: "example/selfhosted-ga"}
	catalog.Publications = []Publication{{
		Name:             "llm-api-gateway",
		Version:          "0.3.0",
		PublishedVersion: "0.3.0-ea",
		Registry:         "ea-images",
	}}

	raw, err := MarshalCatalog(catalog)
	if err != nil {
		t.Fatalf("MarshalCatalog failed: %v", err)
	}

	path := filepath.Join(t.TempDir(), "catalog.yaml")
	writeFile(t, path, string(raw))
	loaded, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog failed: %v", err)
	}
	artifact, ok := loaded.findArtifact("llm-api-gateway")
	if !ok {
		t.Fatal("catalog is missing llm-api-gateway")
	}
	got, err := loaded.artifactPath(artifact)
	if err != nil {
		t.Fatalf("artifactPath failed: %v", err)
	}
	if want := "nvcr.io/example/selfhosted-ga/llm-api-gateway:0.3.0-ea"; got != want {
		t.Fatalf("artifactPath = %q, want %q", got, want)
	}
}

func TestCatalogRefreshAppliesVersionOverrides(t *testing.T) {
	base := testCatalog()
	base.VersionOverrides = []VersionOverride{{
		Name:    "nvcf_worker_utils",
		Version: "2.109.4",
		Source:  "helm-nvcf-api:1.22.5",
	}}
	updated := BuildCatalogFromArtifactsWithBase("0.6.0-rc.99", []Artifact{{
		Name:     "nvcf_worker_utils",
		Type:     ArtifactTypeImage,
		Registry: "staging",
		Version:  "2.101.0",
	}}, base)

	artifact, ok := updated.findArtifact("nvcf_worker_utils")
	if !ok {
		t.Fatal("updated catalog is missing nvcf_worker_utils")
	}
	if artifact.Version != "2.109.4" {
		t.Fatalf("version = %q, want chart-derived override 2.109.4", artifact.Version)
	}
	path, err := updated.artifactPath(artifact)
	if err != nil {
		t.Fatalf("artifactPath failed: %v", err)
	}
	if path != "nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf_worker_utils:2.109.4" {
		t.Fatalf("path = %q, want overridden version at its stack-provided location", path)
	}
}

func TestValidateCatalogRejectsPublishedVersionOverrideDrift(t *testing.T) {
	catalog := testCatalog()
	catalog.Registries["public-images"] = Registry{Host: "nvcr.io", Namespace: "nvidia/nvcf"}
	catalog.Publications = []Publication{{
		Name:     "nvcf_worker_utils",
		Version:  "2.110.0",
		Registry: "public-images",
	}}
	catalog.VersionOverrides = []VersionOverride{{
		Name:    "nvcf_worker_utils",
		Version: "2.109.4",
	}}

	err := ValidateCatalog(catalog)
	if err == nil || !strings.Contains(err.Error(), "version override nvcf_worker_utils:2.109.4 does not match publication version 2.110.0") {
		t.Fatalf("ValidateCatalog error = %v, want publication version drift", err)
	}
}

func TestValidateCatalogAllowsUnpublishedVersionOverride(t *testing.T) {
	catalog := testCatalog()
	catalog.VersionOverrides = []VersionOverride{{
		Name:    "pylon",
		Version: "0.3.0",
	}}

	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("ValidateCatalog failed for unpublished override: %v", err)
	}
}

func TestRenderImageMirroringResourceExamples(t *testing.T) {
	catalog := testCatalog()
	got, err := Render("image-mirroring-resource-examples", catalog)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if !strings.Contains(got, `export STACK_VERSION="0.5.0"`) {
		t.Fatalf("resource examples missing stack version:\n%s", got)
	}
	if !strings.Contains(got, `0833294136851237/nvcf-ncp-staging/nvcf-self-managed-stack:${STACK_VERSION}`) {
		t.Fatalf("resource examples missing versioned stack ref:\n%s", got)
	}
	if !strings.Contains(got, `export COMPUTE_STACK_VERSION="0.5.0"`) {
		t.Fatalf("resource examples missing compute stack version:\n%s", got)
	}
	if !strings.Contains(got, `0833294136851237/nvcf-ncp-staging/nvcf-compute-plane-stack:${COMPUTE_STACK_VERSION}`) {
		t.Fatalf("resource examples missing compute stack ref:\n%s", got)
	}
	for _, stale := range []string{"resource list", "Download latest", ":*"} {
		if strings.Contains(got, stale) {
			t.Fatalf("resource examples contain unqualified latest-version guidance %q:\n%s", stale, got)
		}
	}
}

func TestRenderImageMirroringSnippets(t *testing.T) {
	catalog := testCatalog()
	stack, err := Render("image-mirroring-stack-snippet", catalog)
	if err != nil {
		t.Fatalf("render stack snippet: %v", err)
	}
	if !strings.Contains(stack, `export VERSION="0.5.0"`) {
		t.Fatalf("stack snippet missing stack version:\n%s", stack)
	}
	if !strings.Contains(stack, `0833294136851237/nvcf-ncp-staging/nvcf-self-managed-stack:${VERSION}`) {
		t.Fatalf("stack snippet missing stack resource path:\n%s", stack)
	}
	computeStack, err := Render("image-mirroring-compute-stack-snippet", catalog)
	if err != nil {
		t.Fatalf("render compute stack snippet: %v", err)
	}
	if !strings.Contains(computeStack, `export COMPUTE_VERSION="0.5.0"`) {
		t.Fatalf("compute stack snippet missing version:\n%s", computeStack)
	}
	if !strings.Contains(computeStack, `0833294136851237/nvcf-ncp-staging/nvcf-compute-plane-stack:${COMPUTE_VERSION}`) {
		t.Fatalf("compute stack snippet missing resource path:\n%s", computeStack)
	}

	cli, err := Render("image-mirroring-cli-snippet", catalog)
	if err != nil {
		t.Fatalf("render cli snippet: %v", err)
	}
	if !strings.Contains(cli, `export VERSION="0.0.30"`) {
		t.Fatalf("cli snippet missing cli version:\n%s", cli)
	}
	if !strings.Contains(cli, `0833294136851237/nvcf-ncp-staging/nvcf-cli:${VERSION}`) {
		t.Fatalf("cli snippet missing cli resource path:\n%s", cli)
	}
}

func TestSyncInlineSelfManagedNVCAOperatorVersions(t *testing.T) {
	catalog := testCatalog()
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts,
		Artifact{Name: "nvca", Type: ArtifactTypeImage, Registry: "staging", Version: "3.0.0-rc.11"},
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: "staging", Version: "1.9.0"},
	)
	content := "| **Chart** | `helm-nvca-operator` |\n| --- | --- |\n| **Version** | `1.6.7` |\n\n" +
		"selfManaged:\n  nvcaVersion: \"3.0.0-rc.3\"  # NVCA agent version to deploy\n\n" +
		"helm upgrade --install nvca-operator \\\n" +
		"  oci://nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvca-operator \\\n" +
		"  --namespace nvca-operator --create-namespace \\\n" +
		"  --version 1.6.7\n"

	got, changed, err := SyncInlineVersions("docs/user/cluster-management/self-managed.md", content, catalog)
	if err != nil {
		t.Fatalf("SyncInlineVersions failed: %v", err)
	}
	if !changed {
		t.Fatal("SyncInlineVersions reported no change")
	}
	for _, want := range []string{"`1.9.0`", `nvcaVersion: "3.0.0-rc.11"`, "--version 1.9.0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated content missing %q:\n%s", want, got)
		}
	}
}

func TestSyncInlineSelfManagedNVCAOperatorPlainVersionTable(t *testing.T) {
	catalog := testCatalog()
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts,
		Artifact{Name: "nvca", Type: ArtifactTypeImage, Registry: "staging", Version: "3.0.0-rc.11"},
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: "staging", Version: "1.9.0"},
	)
	content := "| Chart | `helm-nvca-operator` |\n| --- | --- |\n| Version | `1.6.7` |\n\n" +
		"The compute-plane Helmfile installs the operator.\n"

	got, changed, err := SyncInlineVersions("docs/user/cluster-management/self-managed.md", content, catalog)
	if err != nil {
		t.Fatalf("SyncInlineVersions failed: %v", err)
	}
	if !changed {
		t.Fatal("SyncInlineVersions reported no change")
	}
	if !strings.Contains(got, "`1.9.0`") {
		t.Fatalf("updated content missing chart version:\n%s", got)
	}
}

func TestSyncInlineImageMirroringNVCAOperatorChartVersions(t *testing.T) {
	catalog := testCatalog()
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts,
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: "staging", Version: "1.9.0"},
	)
	content := "helm pull oci://nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvca-operator --version 1.4.7\n" +
		"# This creates: helm-nvca-operator-1.4.7.tgz\n" +
		"helm push helm-nvca-operator-1.4.7.tgz oci://example.test/repo\n" +
		"helm pull oci://nvcr.io/0833294136851237/nvcf-ncp-staging/nvca-operator --version 1.2.9\n" +
		"# This creates: nvca-operator-1.2.9.tgz\n" +
		"helm push nvca-operator-1.2.9.tgz oci://example.test/repo\n"

	got, changed, err := SyncInlineVersions("docs/user/image-mirroring.md", content, catalog)
	if err != nil {
		t.Fatalf("SyncInlineVersions failed: %v", err)
	}
	if !changed {
		t.Fatal("SyncInlineVersions reported no change")
	}
	for _, stale := range []string{"1.4.7", "1.2.9", "nvcf-ncp-staging/nvca-operator"} {
		if strings.Contains(got, stale) {
			t.Fatalf("updated content still contains %q:\n%s", stale, got)
		}
	}
	for _, want := range []string{
		"nvcf-ncp-staging/helm-nvca-operator --version 1.9.0",
		"# This creates: helm-nvca-operator-1.9.0.tgz",
		"helm push helm-nvca-operator-1.9.0.tgz",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated content missing %q:\n%s", want, got)
		}
	}
}

func TestSyncInlineImageMirroringUsesTraditionalPublicHelmChart(t *testing.T) {
	catalog := testCatalog()
	catalog.Registries["public-helm"] = Registry{
		Host:            "https://helm.ngc.nvidia.com",
		Namespace:       "nvidia/nvcf",
		RepositoryAlias: "nvcf",
	}
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts,
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: "staging", Version: "1.12.7"},
	)
	catalog.Publications = []Publication{{
		Name:        "helm-nvca-operator",
		Version:     "1.12.7",
		Registry:    "public-helm",
		ChartFormat: ChartFormatHTTP,
	}}
	content := "helm pull oci://nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvca-operator --version 1.12.6\n" +
		"# This creates: helm-nvca-operator-1.12.6.tgz\n" +
		"helm push helm-nvca-operator-1.12.6.tgz oci://example.test/repo\n"

	got, changed, err := SyncInlineVersions("docs/user/image-mirroring.md", content, catalog)
	if err != nil {
		t.Fatalf("SyncInlineVersions failed: %v", err)
	}
	if !changed {
		t.Fatal("SyncInlineVersions reported no change")
	}
	for _, want := range []string{
		"helm pull nvcf/helm-nvca-operator --version 1.12.7",
		"# This creates: helm-nvca-operator-1.12.7.tgz",
		"helm push helm-nvca-operator-1.12.7.tgz",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "oci://nvcr.io/nvidia/nvcf") {
		t.Fatalf("updated content treats the public chart as OCI:\n%s", got)
	}
}

func TestSyncDocsCheckModeDetectsDiff(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "docs/user/manifest.md"), `before
{/* docs-version-sync:BEGIN manifest-artifact-registry-paths */}
stale
{/* docs-version-sync:END manifest-artifact-registry-paths */}
after
`)

	catalog := testCatalog()
	catalog.Outputs = []OutputFile{{
		Path: "docs/user/manifest.md",
		Blocks: []OutputBlock{{
			Marker:   "manifest-artifact-registry-paths",
			Renderer: "manifest-artifact-registry-paths",
		}},
	}}

	err := SyncDocs(tmp, catalog, true)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("SyncDocs error = %v, want ErrCheckFailed", err)
	}

	got, err := os.ReadFile(filepath.Join(tmp, "docs/user/manifest.md"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(got), "nvcf-self-managed-stack:0.5.0") {
		t.Fatalf("check mode modified file:\n%s", got)
	}
}

func TestReplaceMarkedBlockMigratesLegacyHTMLMarkers(t *testing.T) {
	got, changed, err := ReplaceMarkedBlock(`before
<!-- docs-version-sync:BEGIN sample -->
stale
<!-- docs-version-sync:END sample -->
after
`, "sample", "fresh")
	if err != nil {
		t.Fatalf("ReplaceMarkedBlock failed: %v", err)
	}
	if !changed {
		t.Fatal("ReplaceMarkedBlock reported no change")
	}
	if strings.Contains(got, "<!--") {
		t.Fatalf("legacy marker was not migrated:\n%s", got)
	}
	for _, want := range []string{
		"{/* docs-version-sync:BEGIN sample */}",
		"fresh",
		"{/* docs-version-sync:END sample */}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated content missing %q:\n%s", want, got)
		}
	}
}

func TestSyncDocsRejectsMissingMarker(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "docs/user/manifest.md"), "no generated marker\n")

	catalog := testCatalog()
	catalog.Outputs = []OutputFile{{
		Path: "docs/user/manifest.md",
		Blocks: []OutputBlock{{
			Marker:   "manifest-artifact-registry-paths",
			Renderer: "manifest-artifact-registry-paths",
		}},
	}}

	err := SyncDocs(tmp, catalog, true)
	if err == nil {
		t.Fatal("SyncDocs succeeded, want missing marker error")
	}
	if !strings.Contains(err.Error(), `missing begin marker`) {
		t.Fatalf("error = %q, want missing marker", err)
	}
}

func TestValidateCatalogRejectsV05OutputPath(t *testing.T) {
	catalog := testCatalog()
	catalog.Outputs = []OutputFile{{
		Path: "docs/v0.5/manifest.md",
		Blocks: []OutputBlock{{
			Marker:   "manifest-artifact-registry-paths",
			Renderer: "manifest-artifact-registry-paths",
		}},
	}}

	err := ValidateCatalog(catalog)
	if err == nil {
		t.Fatal("ValidateCatalog succeeded, want path rejection")
	}
	if !strings.Contains(err.Error(), "docs/v0.5/manifest.md") {
		t.Fatalf("error = %q, want rejected v0.5 path", err)
	}
}

func TestValidateCatalogRejectsOutputOutsideDocsUser(t *testing.T) {
	catalog := testCatalog()
	catalog.Outputs = []OutputFile{{
		Path: "README.md",
		Blocks: []OutputBlock{{
			Marker:   "manifest-artifact-registry-paths",
			Renderer: "manifest-artifact-registry-paths",
		}},
	}}

	err := ValidateCatalog(catalog)
	if err == nil {
		t.Fatal("ValidateCatalog succeeded, want path rejection")
	}
	if !strings.Contains(err.Error(), "outside docs/user") {
		t.Fatalf("error = %q, want outside docs/user", err)
	}
}

func TestValidateTargetRejectsNonMainTargets(t *testing.T) {
	if err := ValidateTarget("v0.5"); err == nil {
		t.Fatal("ValidateTarget accepted v0.5, want rejection")
	}
	if err := ValidateTarget("main"); err != nil {
		t.Fatalf("ValidateTarget rejected main: %v", err)
	}
}

func TestUpdateCatalogFetchesArtifactsFromGitLab(t *testing.T) {
	var sawToken bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == "gitlab-token" {
			sawToken = true
		}
		wantPath := "/api/v4/projects/182049/packages/generic/ncp-deploy/0.9.0/artifacts-0.9.0.txt"
		if r.URL.Path != wantPath {
			http.Error(w, fmt.Sprintf("unexpected path %s", r.URL.Path), http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `nvcf-base:0.1.4
strap:2.234.0
helm-nvcf-api:1.13.0
`)
	}))
	defer server.Close()

	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, ".netrc"), "machine 127.0.0.1 login user password gitlab-token\n")
	t.Setenv("DOC_VERSION_SYNC_GITLAB_BASE_URL", server.URL)
	t.Setenv("NETRC", filepath.Join(tmp, ".netrc"))
	t.Setenv("DOC_VERSION_SYNC_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GITLAB_ACCESS_TOKEN", "")
	t.Setenv("OAUTH_TOKEN", "")
	t.Setenv("CI_JOB_TOKEN", "")

	catalog, err := updateCatalogFromGitLab("0.9.0", nil)
	if err != nil {
		t.Fatalf("updateCatalogFromGitLab failed: %v", err)
	}
	if !sawToken {
		t.Fatal("GitLab request did not use token from .netrc")
	}
	if catalog.Stack.Version != "0.9.0" {
		t.Fatalf("stack version = %q, want 0.9.0", catalog.Stack.Version)
	}
	if catalog.Stack.PackageName != defaultPackageName || catalog.Stack.Name != defaultStackResourceName {
		t.Fatalf("stack metadata = %#v, want package %s resource %s", catalog.Stack, defaultPackageName, defaultStackResourceName)
	}
	if names := strings.Join(artifactNames(catalog.Artifacts), ","); names != "helm-nvcf-api,strap" {
		t.Fatalf("artifact names = %q, want helm-nvcf-api,strap", names)
	}
	cli, ok := catalog.findArtifact("nvcf-cli")
	if !ok {
		t.Fatal("catalog missing supplemental nvcf-cli")
	}
	if cli.Registry != defaultCLIRegistry || cli.Version != defaultCLIVersion {
		t.Fatalf("nvcf-cli = %#v, want %s %s", cli, defaultCLIRegistry, defaultCLIVersion)
	}
}

func TestUpdateCatalogPreservesCustomDenylist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/182049/packages/generic/ncp-deploy/0.9.0/artifacts-0.9.0.txt":
			fmt.Fprint(w, `legacy-service:1.0.0
strap:2.234.0
`)
		case "/api/v4/projects/268903/packages":
			fmt.Fprint(w, `[{"name":"nvcf-compute-plane-stack","version":"1.0.0"}]`)
		default:
			http.Error(w, fmt.Sprintf("unexpected path %s", r.URL.Path), http.StatusNotFound)
			return
		}
	}))
	defer server.Close()

	t.Setenv("DOC_VERSION_SYNC_GITLAB_BASE_URL", server.URL)
	t.Setenv("DOC_VERSION_SYNC_GITLAB_TOKEN", "env-token")

	base := testCatalog()
	base.Denylist = []DenylistEntry{{Name: "legacy-service", Reason: "not published in docs"}}

	catalog, err := updateCatalogFromGitLab("0.9.0", base)
	if err != nil {
		t.Fatalf("updateCatalogFromGitLab failed: %v", err)
	}
	if _, denied := catalog.DenylistMap()["legacy-service"]; !denied {
		t.Fatalf("updated catalog denylist = %#v, want legacy-service", catalog.Denylist)
	}
	if _, ok := catalog.findArtifact("legacy-service"); ok {
		t.Fatalf("updated catalog contains denylisted artifact: %#v", catalog)
	}
	if _, ok := catalog.findArtifact("strap"); !ok {
		t.Fatal("updated catalog is missing allowed artifact strap")
	}
}

func TestUpdateCatalogSyncsComputePlaneStackPackage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/182049/packages/generic/ncp-deploy/0.9.0/artifacts-0.9.0.txt":
			fmt.Fprint(w, `strap:2.234.0`)
		case "/api/v4/projects/268903/packages":
			fmt.Fprint(w, `[{"name":"nvcf-compute-plane-stack","version":"1.0.0"}]`)
		default:
			http.Error(w, fmt.Sprintf("unexpected path %s", r.URL.Path), http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Setenv("DOC_VERSION_SYNC_GITLAB_BASE_URL", server.URL)
	t.Setenv("DOC_VERSION_SYNC_GITLAB_TOKEN", "env-token")

	catalog, err := updateCatalogFromGitLab("0.9.0", testCatalog())
	if err != nil {
		t.Fatalf("updateCatalogFromGitLab failed: %v", err)
	}
	compute, ok := catalog.findArtifact(computeStackResourceName)
	if !ok {
		t.Fatal("updated catalog is missing compute-plane stack artifact")
	}
	if compute.Version != "1.0.0" {
		t.Fatalf("compute stack version = %q, want 1.0.0", compute.Version)
	}
}

func TestUpdateCatalogDiscoversLatestStackPackage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/182049/packages":
			fmt.Fprint(w, `[{"name":"ncp-deploy","version":"0.9.1"}]`)
		case "/api/v4/projects/182049/packages/generic/ncp-deploy/0.9.1/artifacts-0.9.1.txt":
			fmt.Fprint(w, `strap:2.234.1`)
		default:
			http.Error(w, fmt.Sprintf("unexpected path %s", r.URL.Path), http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Setenv("DOC_VERSION_SYNC_GITLAB_BASE_URL", server.URL)
	t.Setenv("DOC_VERSION_SYNC_GITLAB_TOKEN", "env-token")

	catalog, err := updateCatalogFromGitLab("", nil)
	if err != nil {
		t.Fatalf("updateCatalogFromGitLab failed: %v", err)
	}
	if catalog.Stack.Version != "0.9.1" {
		t.Fatalf("stack version = %q, want discovered 0.9.1", catalog.Stack.Version)
	}
}

func TestLatestStackVersionPaginatesPackages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/182049/packages" {
			http.Error(w, fmt.Sprintf("unexpected path %s", r.URL.Path), http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			nextURL := serverURL(r) + r.URL.Path + "?page=2"
			w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", nextURL))
			fmt.Fprint(w, `[{"name":"unrelated","version":"1.0.0"}]`)
		case "2":
			fmt.Fprint(w, `[{"name":"ncp-deploy","version":"0.9.2"}]`)
		default:
			http.Error(w, fmt.Sprintf("unexpected page %s", r.URL.Query().Get("page")), http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := &GitLabClient{BaseURL: server.URL, HTTPClient: server.Client()}
	version, err := client.LatestStackVersion(defaultStackProjectID, defaultPackageName)
	if err != nil {
		t.Fatalf("LatestStackVersion failed: %v", err)
	}
	if version != "0.9.2" {
		t.Fatalf("version = %q, want 0.9.2", version)
	}
}

func TestLatestGenericPackageVersionDoesNotUseReleaseFallback(t *testing.T) {
	releaseRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/268903/packages":
			fmt.Fprint(w, `[]`)
		case "/api/v4/projects/268903/releases":
			releaseRequested = true
			fmt.Fprint(w, `[{"tag_name":"0.6.0-rc.84"}]`)
		default:
			http.Error(w, fmt.Sprintf("unexpected path %s", r.URL.Path), http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := &GitLabClient{BaseURL: server.URL, HTTPClient: server.Client()}
	_, err := client.LatestGenericPackageVersion(defaultComputeProjectID, computeStackResourceName)
	if err == nil {
		t.Fatal("LatestGenericPackageVersion succeeded without a package")
	}
	if releaseRequested {
		t.Fatal("LatestGenericPackageVersion requested releases")
	}
	if !strings.Contains(err.Error(), "no generic package version found") {
		t.Fatalf("error = %q, want missing generic package", err)
	}
}

func TestComputeStackProjectIDUsesCIProjectID(t *testing.T) {
	t.Setenv("DOC_VERSION_SYNC_COMPUTE_GITLAB_PROJECT_ID", "")
	t.Setenv("CI_PROJECT_ID", "12345")

	projectID, err := computeStackProjectID()
	if err != nil {
		t.Fatalf("computeStackProjectID failed: %v", err)
	}
	if projectID != 12345 {
		t.Fatalf("projectID = %d, want 12345", projectID)
	}
}

func TestComputeStackProjectIDUsesExplicitOverride(t *testing.T) {
	t.Setenv("DOC_VERSION_SYNC_COMPUTE_GITLAB_PROJECT_ID", "67890")
	t.Setenv("CI_PROJECT_ID", "12345")

	projectID, err := computeStackProjectID()
	if err != nil {
		t.Fatalf("computeStackProjectID failed: %v", err)
	}
	if projectID != 67890 {
		t.Fatalf("projectID = %d, want 67890", projectID)
	}
}

func testCatalog() *Catalog {
	return &Catalog{
		Version: 1,
		Target:  "main",
		Registries: map[string]Registry{
			"staging": {
				Host:      "nvcr.io",
				Namespace: "0833294136851237/nvcf-ncp-staging",
			},
		},
		Stack: StackMetadata{
			Name:     "nvcf-self-managed-stack",
			Version:  "0.5.0",
			Registry: "staging",
		},
		Denylist: []DenylistEntry{
			{Name: "nvcf-base", Reason: "managed separately"},
			{Name: "samba", Reason: "internal base dependency"},
		},
		Artifacts: []Artifact{
			{Name: "nvcf-base", Type: ArtifactTypeResource, Registry: "staging", Version: "0.1.4"},
		},
		SupplementalArtifacts: []Artifact{
			{Name: "nvcf-compute-plane-stack", Type: ArtifactTypeResource, Registry: "staging", Version: "0.5.0"},
			{Name: "nvcf-cli", Type: ArtifactTypeResource, Registry: "staging", Version: "0.0.30"},
			{Name: "llm-api-gateway", Type: ArtifactTypeImage, Registry: "staging", Version: "0.3.0"},
			{Name: "llm-request-router", Type: ArtifactTypeImage, Registry: "staging", RepositoryName: "stargate", Version: "0.2.0"},
		},
		Manifest: ManifestMetadata{Entries: []ManifestEntry{
			{ArtifactID: "nvcf-self-managed-stack", Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Control-plane deployment bundle."},
			{ArtifactID: "nvcf-compute-plane-stack", Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Compute-plane deployment bundle."},
			{ArtifactID: "nvcf-cli", Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "NVCF command-line interface."},
			{ArtifactID: "llm-api-gateway", Plane: ManifestPlaneControl, Kind: ManifestKindServiceImage, Requirement: ManifestOptional, Description: "Routes LLM API requests."},
			{ArtifactID: "llm-request-router", Plane: ManifestPlaneControl, Kind: ManifestKindServiceImage, Requirement: ManifestOptional, Description: "Routes LLM worker requests."},
		}},
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func sectionBetween(t *testing.T, content, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(content, startMarker)
	if start == -1 {
		t.Fatalf("content missing start marker %q:\n%s", startMarker, content)
	}
	end := strings.Index(content[start:], endMarker)
	if end == -1 {
		t.Fatalf("content missing end marker %q after %q:\n%s", endMarker, startMarker, content[start:])
	}
	return content[start : start+end]
}

func optionalSectionBetween(content, startMarker, endMarker string) (string, bool) {
	start := strings.Index(content, startMarker)
	if start == -1 {
		return "", false
	}
	end := strings.Index(content[start:], endMarker)
	if end == -1 {
		return content[start:], true
	}
	return content[start : start+end], true
}
