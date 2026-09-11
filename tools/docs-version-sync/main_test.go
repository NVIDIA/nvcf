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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderManifestArtifactRegistryPaths(t *testing.T) {
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
		"| `nvcf-observability-stack` | `0.5.0` |",
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
		Artifact{Name: "nvca", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "3.0.0-rc.13"},
		Artifact{Name: "nvca-operator", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "3.0.0-rc.13"},
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.11.1"},
		Artifact{Name: "nvct-service-oss", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "1.2.11"},
		Artifact{Name: "helm-nvcf-nvct-api", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.4.2"},
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
		Artifact{Name: "nvcf-grpc-proxy", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "1.29.1"},
		Artifact{Name: "helm-nvcf-grpc-proxy", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.6.7"},
		Artifact{Name: "helm-nvcf-nats", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "0.6.1"},
	)
	catalog.Publications = []Publication{
		{Name: "nvcf-grpc-proxy", Type: ArtifactTypeImage, Version: "1.29.1", Registry: "public-images"},
		{Name: "helm-nvcf-grpc-proxy", Type: ArtifactTypeChart, Version: "1.6.7", Registry: "public-helm", ChartFormat: ChartFormatHTTP},
		{Name: "helm-nvcf-nats", Type: ArtifactTypeChart, Version: "0.7.1", Registry: "public-helm", ChartFormat: ChartFormatHTTP},
	}
	catalog.PublicationPending = []string{"helm-nvcf-nats"}
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
		"`Publication pending`",
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
		Type:     ArtifactTypeImage,
		Version:  "1.29.1",
		Registry: "public-images",
	}}

	updated := refreshCatalogFromArtifacts("0.6.0-rc.99", []Artifact{{
		Name:     "nvcf-grpc-proxy",
		Type:     ArtifactTypeImage,
		Registry: defaultImageRegistry,
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
	if path != "nvcr.io/nvidia/nvcf/nvcf-grpc-proxy:1.30.0" {
		t.Fatalf("path = %q, want refreshed version at its stack-provided location", path)
	}
}

func TestCatalogConstructionWithoutBaseRendersUnverifiedArtifactsAsPending(t *testing.T) {
	catalog := refreshCatalogFromArtifacts("0.9.1", []Artifact{
		{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.21.3"},
		{Name: "nvca", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "3.2.19"},
	}, nil)
	catalog.Manifest.Entries = []ManifestEntry{
		{ArtifactID: controlStackResourceName, Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Control-plane stack."},
		{ArtifactID: "helm-nvca-operator", Plane: ManifestPlaneCompute, Kind: ManifestKindChart, Requirement: ManifestRequired, Description: "NVCA chart."},
		{ArtifactID: "nvca", Plane: ManifestPlaneCompute, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "NVCA agent."},
	}

	got, err := Render("manifest-artifact-registry-paths", catalog)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	for _, name := range []string{controlStackResourceName, "helm-nvca-operator", "nvca"} {
		row := manifestRow(t, got, name)
		if !strings.Contains(row, "`Publication pending`") {
			t.Errorf("%s row does not mark the unverified artifact pending: %s", name, row)
		}
	}
}

func TestCatalogRefreshMarksChangedVersionsPendingAndKeepsPublishedVersionsPublic(t *testing.T) {
	base := testCatalog()
	base.Registries["public-images"] = Registry{Host: "nvcr.io", Namespace: "nvidia/nvcf"}
	base.Artifacts = append(base.Artifacts,
		Artifact{Name: "nvca", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "3.2.18"},
		Artifact{Name: "verified-service", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "2.0.0"},
	)
	base.Publications = []Publication{
		{Name: "nvca", Type: ArtifactTypeImage, Version: "3.2.18", Registry: "public-images"},
		{Name: "verified-service", Type: ArtifactTypeImage, Version: "2.0.0", Registry: "public-images"},
	}
	base.Manifest.Entries = append(base.Manifest.Entries,
		ManifestEntry{ArtifactID: "nvca", Plane: ManifestPlaneCompute, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "NVCA agent."},
		ManifestEntry{ArtifactID: "verified-service", Plane: ManifestPlaneControl, Kind: ManifestKindServiceImage, Requirement: ManifestRequired, Description: "Published service."},
	)

	catalog := refreshCatalogFromArtifacts("0.6.0", []Artifact{
		{Name: "nvca", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "3.2.19"},
		{Name: "verified-service", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "2.0.0"},
	}, base)
	got, err := Render("manifest-artifact-registry-paths", catalog)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	for _, name := range []string{controlStackResourceName, computeStackResourceName, observabilityStackResourceName, "nvcf-cli", "nvca"} {
		row := manifestRow(t, got, name)
		if !strings.Contains(row, "`Publication pending`") {
			t.Errorf("%s row does not mark the unverified artifact pending: %s", name, row)
		}
	}
	publicRow := manifestRow(t, got, "verified-service")
	if !strings.Contains(publicRow, "`nvcr.io/nvidia/nvcf/verified-service:2.0.0`") {
		t.Errorf("matching verified publication did not retain its public distribution: %s", publicRow)
	}
}

func TestArtifactPathUsesPublishedVersionAlias(t *testing.T) {
	catalog := testCatalog()
	catalog.Registries["public-images"] = Registry{Host: "nvcr.io", Namespace: "nvidia/nvcf"}
	catalog.Publications = []Publication{{
		Name:             "llm-api-gateway",
		Type:             ArtifactTypeImage,
		Version:          "0.3.0",
		PublishedVersion: "0.3.0-ea",
		Registry:         "public-images",
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
	if want := "nvcr.io/nvidia/nvcf/llm-api-gateway:0.3.0-ea"; got != want {
		t.Fatalf("artifactPath = %q, want %q", got, want)
	}
}

func TestArtifactPathKeepsDigestOnlyForUnchangedReference(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	artifact := Artifact{
		Name:     "example",
		Type:     ArtifactTypeImage,
		Registry: defaultImageRegistry,
		Version:  "1.2.3",
		Digest:   digest,
	}
	tests := []struct {
		name        string
		publication *Publication
		want        string
	}{
		{
			name: "source reference",
			want: "nvcr.io/nvidia/nvcf/example:1.2.3@" + digest,
		},
		{
			name: "unchanged publication",
			publication: &Publication{
				Name:     artifact.Name,
				Type:     artifact.Type,
				Version:  artifact.Version,
				Registry: artifact.Registry,
			},
			want: "nvcr.io/nvidia/nvcf/example:1.2.3@" + digest,
		},
		{
			name: "registry remap",
			publication: &Publication{
				Name:     artifact.Name,
				Type:     artifact.Type,
				Version:  artifact.Version,
				Registry: "public-mirror",
			},
			want: "registry.example.com/public/nvcf/example:1.2.3",
		},
		{
			name: "version remap",
			publication: &Publication{
				Name:             artifact.Name,
				Type:             artifact.Type,
				Version:          artifact.Version,
				PublishedVersion: "1.2.3-public",
				Registry:         artifact.Registry,
			},
			want: "nvcr.io/nvidia/nvcf/example:1.2.3-public",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := testCatalog()
			catalog.Registries["public-mirror"] = Registry{Host: "registry.example.com", Namespace: "public/nvcf"}
			if tt.publication != nil {
				catalog.Publications = []Publication{*tt.publication}
			}
			got, err := catalog.artifactPath(artifact)
			if err != nil {
				t.Fatalf("artifactPath failed: %v", err)
			}
			if got != tt.want {
				t.Fatalf("artifactPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCatalogRefreshAppliesVersionOverrides(t *testing.T) {
	base := testCatalog()
	base.VersionOverrides = []VersionOverride{{
		Name:    "nvcf_worker_utils",
		Version: "2.109.4",
		Source:  "helm-nvcf-api:1.22.5",
	}}
	updated := refreshCatalogFromArtifacts("0.6.0-rc.99", []Artifact{{
		Name:     "nvcf_worker_utils",
		Type:     ArtifactTypeImage,
		Registry: defaultImageRegistry,
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
	if path != "nvcr.io/nvidia/nvcf/nvcf_worker_utils:2.109.4" {
		t.Fatalf("path = %q, want overridden version at its stack-provided location", path)
	}
}

func TestValidateCatalogRejectsPublishedVersionOverrideDrift(t *testing.T) {
	catalog := testCatalog()
	catalog.Registries["public-images"] = Registry{Host: "nvcr.io", Namespace: "nvidia/nvcf"}
	catalog.Publications = []Publication{{
		Name:     "nvcf_worker_utils",
		Type:     ArtifactTypeImage,
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

func TestValidateCatalogRejectsPublicationWithoutType(t *testing.T) {
	catalog := testCatalog()
	catalog.Publications = []Publication{{
		Name:     "nvcf-cli",
		Version:  "1.2.3",
		Registry: defaultStackRegistry,
	}}

	err := ValidateCatalog(catalog)
	if err == nil || !strings.Contains(err.Error(), "has unsupported type") {
		t.Fatalf("ValidateCatalog error = %v, want missing publication type rejection", err)
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
	if !strings.Contains(got, `nvidia/nvcf/nvcf-self-managed-stack:${STACK_VERSION}`) {
		t.Fatalf("resource examples missing versioned stack ref:\n%s", got)
	}
	if !strings.Contains(got, `export COMPUTE_STACK_VERSION="0.5.0"`) {
		t.Fatalf("resource examples missing compute stack version:\n%s", got)
	}
	if !strings.Contains(got, `nvidia/nvcf/nvcf-compute-plane-stack:${COMPUTE_STACK_VERSION}`) {
		t.Fatalf("resource examples missing compute stack ref:\n%s", got)
	}
	if !strings.Contains(got, `export OBSERVABILITY_STACK_VERSION="0.5.0"`) {
		t.Fatalf("resource examples missing observability stack version:\n%s", got)
	}
	if !strings.Contains(got, `nvidia/nvcf/nvcf-observability-stack:${OBSERVABILITY_STACK_VERSION}`) {
		t.Fatalf("resource examples missing observability stack ref:\n%s", got)
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
	if !strings.Contains(stack, `nvidia/nvcf/nvcf-self-managed-stack:${VERSION}`) {
		t.Fatalf("stack snippet missing stack resource path:\n%s", stack)
	}
	computeStack, err := Render("image-mirroring-compute-stack-snippet", catalog)
	if err != nil {
		t.Fatalf("render compute stack snippet: %v", err)
	}
	if !strings.Contains(computeStack, `export COMPUTE_VERSION="0.5.0"`) {
		t.Fatalf("compute stack snippet missing version:\n%s", computeStack)
	}
	if !strings.Contains(computeStack, `nvidia/nvcf/nvcf-compute-plane-stack:${COMPUTE_VERSION}`) {
		t.Fatalf("compute stack snippet missing resource path:\n%s", computeStack)
	}
	observabilityStack, err := Render("image-mirroring-observability-stack-snippet", catalog)
	if err != nil {
		t.Fatalf("render observability stack snippet: %v", err)
	}
	if !strings.Contains(observabilityStack, `export OBSERVABILITY_VERSION="0.5.0"`) {
		t.Fatalf("observability stack snippet missing version:\n%s", observabilityStack)
	}
	if !strings.Contains(observabilityStack, `nvidia/nvcf/nvcf-observability-stack:${OBSERVABILITY_VERSION}`) {
		t.Fatalf("observability stack snippet missing resource path:\n%s", observabilityStack)
	}

	cli, err := Render("image-mirroring-cli-snippet", catalog)
	if err != nil {
		t.Fatalf("render cli snippet: %v", err)
	}
	if !strings.Contains(cli, `export VERSION="0.0.30"`) {
		t.Fatalf("cli snippet missing cli version:\n%s", cli)
	}
	if !strings.Contains(cli, `nvidia/nvcf/nvcf-cli:${VERSION}`) {
		t.Fatalf("cli snippet missing cli resource path:\n%s", cli)
	}
}

func TestRenderImageMirroringCLIContentMatchesPublicationState(t *testing.T) {
	pending := testCatalog()
	pending.PublicationPending = []string{"nvcf-cli"}
	pendingOutput, err := Render("image-mirroring-cli-snippet", pending)
	if err != nil {
		t.Fatalf("render pending CLI content: %v", err)
	}
	if strings.Contains(pendingOutput, "The extracted directory contains") {
		t.Fatalf("pending CLI content implies extraction already occurred:\n%s", pendingOutput)
	}
	if !strings.Contains(pendingOutput, "Package contents and extraction instructions will be available after publication or mirroring") {
		t.Fatalf("pending CLI content lacks actionable status guidance:\n%s", pendingOutput)
	}

	published := testCatalog()
	publishedOutput, err := Render("image-mirroring-cli-snippet", published)
	if err != nil {
		t.Fatalf("render published CLI content: %v", err)
	}
	if !strings.Contains(publishedOutput, "The extracted directory contains") || !strings.Contains(publishedOutput, "`.nvcf-cli.yaml.template`") {
		t.Fatalf("published CLI content omits extracted package details:\n%s", publishedOutput)
	}
}

func TestRenderSupplementalStackDownloadsMatchPublicationState(t *testing.T) {
	tests := []struct {
		name                       string
		artifact                   string
		renderer                   string
		resourceExamplesVersionEnv string
		snippetVersionEnv          string
	}{
		{
			name:                       "compute",
			artifact:                   computeStackResourceName,
			renderer:                   "image-mirroring-compute-stack-snippet",
			resourceExamplesVersionEnv: "COMPUTE_STACK_VERSION",
			snippetVersionEnv:          "COMPUTE_VERSION",
		},
		{
			name:                       "observability",
			artifact:                   observabilityStackResourceName,
			renderer:                   "image-mirroring-observability-stack-snippet",
			resourceExamplesVersionEnv: "OBSERVABILITY_STACK_VERSION",
			snippetVersionEnv:          "OBSERVABILITY_VERSION",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pending := testCatalog()
			pending.PublicationPending = []string{tt.artifact}
			for _, renderer := range []string{"image-mirroring-resource-examples", tt.renderer} {
				t.Run(renderer+"-pending", func(t *testing.T) {
					got, err := Render(renderer, pending)
					if err != nil {
						t.Fatalf("Render failed: %v", err)
					}
					if strings.Contains(got, tt.artifact+":${") {
						t.Fatalf("pending %s renders a download command:\n%s", tt.artifact, got)
					}
					want := "Publication pending: " + tt.artifact + " 0.5.0 is not yet available for download"
					if !strings.Contains(got, want) {
						t.Fatalf("pending %s lacks publication status:\n%s", tt.artifact, got)
					}
				})
			}

			published := testCatalog()
			published.Registries["public-resources"] = Registry{Host: "nvcr.io", Namespace: "nvidia/nvcf"}
			for i := range published.SupplementalArtifacts {
				if published.SupplementalArtifacts[i].Name == tt.artifact {
					published.SupplementalArtifacts[i].Registry = "public-resources"
				}
			}
			published.Publications = []Publication{{Name: tt.artifact, Type: ArtifactTypeResource, Version: "0.5.0", Registry: "public-resources"}}
			for _, renderer := range []struct {
				name       string
				versionEnv string
				multiline  bool
			}{
				{name: "image-mirroring-resource-examples", versionEnv: tt.resourceExamplesVersionEnv, multiline: true},
				{name: tt.renderer, versionEnv: tt.snippetVersionEnv},
			} {
				t.Run(renderer.name+"-published", func(t *testing.T) {
					got, err := Render(renderer.name, published)
					if err != nil {
						t.Fatalf("Render failed: %v", err)
					}
					separator := ""
					if renderer.multiline {
						separator = "\\\n  "
					}
					want := "ngc registry resource download-version " + separator +
						"\"nvidia/nvcf/" + tt.artifact + ":${" + renderer.versionEnv + "}\""
					if !strings.Contains(got, want) {
						t.Fatalf("published %s omits its public download command:\n%s", tt.artifact, got)
					}
				})
			}
		})
	}
}

func TestSyncInlineSelfManagedNVCAOperatorVersionTable(t *testing.T) {
	catalog := testCatalog()
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts,
		Artifact{Name: "nvca", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "3.0.0-rc.11"},
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.9.0"},
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
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.9.0"},
	)
	catalog.PublicationPending = []string{"helm-nvca-operator"}
	content := "helm pull oci://nvcr.io/nvidia/nvcf/helm-nvca-operator --version 1.4.7\n" +
		"# This creates: helm-nvca-operator-1.4.7.tgz\n" +
		"helm push helm-nvca-operator-1.4.7.tgz oci://example.test/repo\n" +
		"helm pull oci://nvcr.io/nvidia/nvcf/nvca-operator --version 1.2.9\n" +
		"# This creates: nvca-operator-1.2.9.tgz\n" +
		"helm push nvca-operator-1.2.9.tgz oci://example.test/repo\n"

	got, changed, err := SyncInlineVersions("docs/user/image-mirroring.md", content, catalog)
	if err != nil {
		t.Fatalf("SyncInlineVersions failed: %v", err)
	}
	if !changed {
		t.Fatal("SyncInlineVersions reported no change")
	}
	for _, stale := range []string{"1.4.7", "1.2.9", "nvcf/nvca-operator"} {
		if strings.Contains(got, stale) {
			t.Fatalf("updated content still contains %q:\n%s", stale, got)
		}
	}
	for _, want := range []string{
		"HELM_NVCA_OPERATOR_REFERENCE",
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
		Artifact{Name: "helm-nvca-operator", Type: ArtifactTypeChart, Registry: defaultChartRegistry, Version: "1.12.7"},
	)
	catalog.Publications = []Publication{{
		Name:        "helm-nvca-operator",
		Type:        ArtifactTypeChart,
		Version:     "1.12.7",
		Registry:    "public-helm",
		ChartFormat: ChartFormatHTTP,
	}}
	content := "helm pull oci://nvcr.io/nvidia/nvcf/helm-nvca-operator --version 1.12.6\n" +
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

func TestReplaceMarkedBlockAcceptsCompactMDXMarkers(t *testing.T) {
	got, changed, err := ReplaceMarkedBlock(`before
{/*docs-version-sync:BEGIN sample*/}
stale
{/*docs-version-sync:END sample*/}
after
`, "sample", "fresh")
	if err != nil {
		t.Fatalf("ReplaceMarkedBlock failed: %v", err)
	}
	if !changed {
		t.Fatal("ReplaceMarkedBlock reported no change")
	}
	for _, want := range []string{
		"{/*docs-version-sync:BEGIN sample*/}",
		"fresh",
		"{/*docs-version-sync:END sample*/}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated content missing %q:\n%s", want, got)
		}
	}
}

func TestFindRepoRootFromUsesPublicCheckoutSentinels(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "docs", "version-catalog", "main.yaml"), "version: 1\n")
	writeFile(t, filepath.Join(repo, "tools", "docs-version-sync", "go.mod"), "module docs-version-sync\n")
	nested := filepath.Join(repo, "tools", "docs-version-sync")

	got, err := findRepoRootFrom(nested)
	if err != nil {
		t.Fatal(err)
	}
	if got != repo {
		t.Fatalf("findRepoRootFrom(%s) = %s, want %s", nested, got, repo)
	}
}

func TestFindRepoRootFromRequiresBothPublicCheckoutSentinels(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "docs", "version-catalog", "main.yaml"), "version: 1\n")

	_, err := findRepoRootFrom(repo)
	if err == nil || !strings.Contains(err.Error(), "NVCF repository root not found") {
		t.Fatalf("findRepoRootFrom error = %v, want missing-root error", err)
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

func testCatalog() *Catalog {
	return &Catalog{
		Version: 1,
		Target:  "main",
		Registries: map[string]Registry{
			defaultStackRegistry: {
				Host:      "nvcr.io",
				Namespace: "nvidia/nvcf",
			},
			defaultImageRegistry: {
				Host:      "nvcr.io",
				Namespace: "nvidia/nvcf",
			},
			defaultChartRegistry: {
				Host:            "https://helm.ngc.nvidia.com",
				Namespace:       "nvidia/nvcf",
				RepositoryAlias: "nvcf",
			},
		},
		Stack: StackMetadata{
			Name:     "nvcf-self-managed-stack",
			Version:  "0.5.0",
			Registry: defaultStackRegistry,
		},
		Denylist: []DenylistEntry{
			{Name: "nvcf-base", Reason: "managed separately"},
			{Name: "samba", Reason: "internal base dependency"},
		},
		Artifacts: []Artifact{
			{Name: "nvcf-base", Type: ArtifactTypeResource, Registry: defaultStackRegistry, Version: "0.1.4"},
		},
		SupplementalArtifacts: []Artifact{
			{Name: "nvcf-compute-plane-stack", Type: ArtifactTypeResource, Registry: defaultStackRegistry, Version: "0.5.0"},
			{Name: "nvcf-observability-stack", Type: ArtifactTypeResource, Registry: defaultStackRegistry, Version: "0.5.0"},
			{Name: "nvcf-cli", Type: ArtifactTypeResource, Registry: defaultStackRegistry, Version: "0.0.30"},
			{Name: "llm-api-gateway", Type: ArtifactTypeImage, Registry: defaultImageRegistry, Version: "0.3.0"},
			{Name: "llm-request-router", Type: ArtifactTypeImage, Registry: defaultImageRegistry, RepositoryName: "stargate", Version: "0.2.0"},
		},
		Manifest: ManifestMetadata{Entries: []ManifestEntry{
			{ArtifactID: "nvcf-self-managed-stack", Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Control-plane deployment bundle."},
			{ArtifactID: "nvcf-compute-plane-stack", Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Compute-plane deployment bundle."},
			{ArtifactID: "nvcf-observability-stack", Plane: ManifestPlaneShared, Kind: ManifestKindResource, Description: "Observability deployment bundle."},
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

func manifestRow(t *testing.T, manifest, artifactName string) string {
	t.Helper()
	prefix := "| `" + artifactName + "` |"
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("manifest is missing row for %s:\n%s", artifactName, manifest)
	return ""
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
