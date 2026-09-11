// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateResolvedStackInventoryIncludesAllPlanesAndOptionalArtifacts(t *testing.T) {
	inputs := resolvedInventoryTestInputs(t)
	inventory, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err != nil {
		t.Fatal(err)
	}

	if len(inventory.Releases) != 4 {
		t.Fatalf("got %d releases, want 4", len(inventory.Releases))
	}
	autoscaler := findResolvedInventoryRelease(t, inventory, "observability", "function-autoscaler")
	if autoscaler.Required {
		t.Fatal("function-autoscaler must remain optional when its Helmfile release is disabled")
	}
	if autoscaler.Chart != "nvcf/helm-nvcf-function-autoscaler" || autoscaler.Version != "0.3.2" {
		t.Fatalf("unexpected function-autoscaler chart: %+v", autoscaler)
	}

	notary := findResolvedInventoryArtifact(t, inventory, "container-image", "registry.example.com/nvcf-notary:1.14.0")
	if notary.Name != "nvcf-notary" || notary.Repository != "registry.example.com/nvcf-notary" || notary.Version != "1.14.0" {
		t.Fatalf("repository override was not preserved: %+v", notary)
	}
	findResolvedInventoryArtifact(t, inventory, "container-image", "registry.example.com/function-autoscaler:0.3.2")
	findResolvedInventoryArtifact(t, inventory, "container-image", "registry.example.com/nvca:4.1.0")
	findResolvedInventoryArtifact(t, inventory, "helm-chart", "nvcf/helm-resource-config@1.2.0")
}

func TestGenerateResolvedStackInventoryFindsChartOwnedImagesAndDeduplicatesSources(t *testing.T) {
	inputs := resolvedInventoryTestInputs(t)
	inputs[1].ManifestByRelease["resource-config"] = []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    release-artifact-worker-image: registry.example.com/shared-worker:2.7.1
`)
	inputs[1].ManifestByRelease["notary-service"] = append(inputs[1].ManifestByRelease["notary-service"], []byte(`
---
apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    release-artifact-worker-image: registry.example.com/shared-worker:2.7.1
`)...)

	inventory, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	worker := findResolvedInventoryArtifact(t, inventory, "container-image", "registry.example.com/shared-worker:2.7.1")
	if len(worker.Sources) != 2 {
		t.Fatalf("got %d shared worker sources, want 2: %+v", len(worker.Sources), worker.Sources)
	}
	if worker.Sources[0].Release != "notary-service" || worker.Sources[1].Release != "resource-config" {
		t.Fatalf("shared worker sources are not deterministic: %+v", worker.Sources)
	}
}

func TestGenerateResolvedStackInventoryIsDeterministic(t *testing.T) {
	inputs := resolvedInventoryTestInputs(t)
	first, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := marshalResolvedStackInventory(first)
	if err != nil {
		t.Fatal(err)
	}

	inputs[0], inputs[2] = inputs[2], inputs[0]
	second, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := marshalResolvedStackInventory(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("inventory changed with input order:\nfirst:\n%s\nsecond:\n%s", firstJSON, secondJSON)
	}
	if !bytes.HasSuffix(firstJSON, []byte("\n")) {
		t.Fatal("serialized inventory must end with a newline")
	}

	parsed, err := parseResolvedStackInventory(firstJSON)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := marshalResolvedStackInventory(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, roundTrip) {
		t.Fatal("inventory JSON did not round trip deterministically")
	}
}

func TestGenerateResolvedStackInventoryRejectsIncompleteReleaseRenders(t *testing.T) {
	inputs := resolvedInventoryTestInputs(t)
	delete(inputs[2].ManifestByRelease, "function-autoscaler")
	_, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err == nil || !strings.Contains(err.Error(), "function-autoscaler has no rendered manifest entry") {
		t.Fatalf("got error %v, want missing optional release render", err)
	}

	inputs = resolvedInventoryTestInputs(t)
	inputs[0].ManifestByRelease["unknown"] = nil
	_, err = generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err == nil || !strings.Contains(err.Error(), "unknown release unknown") {
		t.Fatalf("got error %v, want unknown release render", err)
	}

	inputs = resolvedInventoryTestInputs(t)
	inputs[1].ManifestByRelease["resource-config"] = nil
	_, err = generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err == nil || !strings.Contains(err.Error(), "resource-config has empty rendered manifests") {
		t.Fatalf("got error %v, want empty release render", err)
	}
}

func TestGenerateResolvedStackInventoryRejectsDuplicateHelmfileReleases(t *testing.T) {
	inputs := resolvedInventoryTestInputs(t)
	releases := []helmfileRelease{
		{Name: "nvca", Namespace: "nvca", Enabled: true, Installed: true, Chart: "nvcf/helm-nvca", Version: "4.1.0"},
		{Name: "nvca", Namespace: "other", Enabled: true, Installed: true, Chart: "nvcf/helm-nvca", Version: "4.1.0"},
	}
	inputs[0].ReleaseList = mustMarshalHelmfileReleases(t, releases)
	_, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
	if err == nil || !strings.Contains(err.Error(), "duplicate Helmfile release nvca") {
		t.Fatalf("got error %v, want duplicate release", err)
	}
}

func TestGenerateResolvedStackInventoryRejectsUnresolvedImages(t *testing.T) {
	tests := []struct {
		name      string
		reference string
	}{
		{name: "chart placeholder", reference: "${registry}/nvcf-notary:1.14.0"},
		{name: "missing tag", reference: "registry.example.com/nvcf-notary"},
		{name: "latest tag", reference: "registry.example.com/nvcf-notary:latest"},
		{name: "null image", reference: "null"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inputs := resolvedInventoryTestInputs(t)
			inputs[1].ManifestByRelease["notary-service"] = []byte("apiVersion: v1\nkind: Pod\nspec:\n  containers:\n    - name: notary\n      image: " + test.reference + "\n")
			_, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
			if err == nil {
				t.Fatalf("unresolved image %q was accepted", test.reference)
			}
		})
	}
}

func TestValidateResolvedStackInventoryRejectsMissingAndDuplicateArtifacts(t *testing.T) {
	inventory, err := generateResolvedStackInventory(resolvedInventoryTestSource(), resolvedInventoryTestInputs(t))
	if err != nil {
		t.Fatal(err)
	}

	missing := inventory
	missing.Artifacts = append([]resolvedInventoryArtifact(nil), inventory.Artifacts...)
	for i, artifact := range missing.Artifacts {
		if artifact.Reference == "nvcf/helm-nvcf-function-autoscaler@0.3.2" {
			missing.Artifacts = append(missing.Artifacts[:i], missing.Artifacts[i+1:]...)
			break
		}
	}
	if err := validateResolvedStackInventory(missing); err == nil || !strings.Contains(err.Error(), "has no matching chart artifact") {
		t.Fatalf("got error %v, want missing chart artifact", err)
	}

	duplicate := inventory
	duplicate.Artifacts = append([]resolvedInventoryArtifact(nil), inventory.Artifacts...)
	duplicate.Artifacts = append(duplicate.Artifacts, inventory.Artifacts[len(inventory.Artifacts)-1])
	if err := validateResolvedStackInventory(duplicate); err == nil {
		t.Fatal("duplicate artifact was accepted")
	}
}

func TestValidateResolvedStackInventoryRejectsStaleRepositoryName(t *testing.T) {
	inventory, err := generateResolvedStackInventory(resolvedInventoryTestSource(), resolvedInventoryTestInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := range inventory.Artifacts {
		if inventory.Artifacts[i].Reference == "registry.example.com/nvcf-notary:1.14.0" {
			inventory.Artifacts[i].Name = "notary-service"
			break
		}
	}
	if err := validateResolvedStackInventory(inventory); err == nil || !strings.Contains(err.Error(), "stale repository name") {
		t.Fatalf("got error %v, want stale repository name", err)
	}
}

func TestParseResolvedStackInventoryRejectsUnknownFields(t *testing.T) {
	inventory, err := generateResolvedStackInventory(resolvedInventoryTestSource(), resolvedInventoryTestInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := marshalResolvedStackInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"schema_version": 1`), []byte(`"schema_version": 1, "unexpected": true`), 1)
	if _, err := parseResolvedStackInventory(body); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("got error %v, want unknown field", err)
	}
}

func resolvedInventoryTestInputs(t *testing.T) []resolvedInventoryPlaneInput {
	t.Helper()
	computeReleases := []helmfileRelease{
		{Name: "nvca", Namespace: "nvca", Enabled: true, Installed: true, Labels: "release-group:compute", Chart: "nvcf/helm-nvca", Version: "4.1.0"},
	}
	controlReleases := []helmfileRelease{
		{Name: "resource-config", Namespace: "nvcf", Enabled: true, Installed: true, Labels: "release-group:config", Chart: "nvcf/helm-resource-config", Version: "1.2.0"},
		{Name: "notary-service", Namespace: "nvcf", Enabled: true, Installed: true, Labels: "release-group:services", Chart: "nvcf/helm-nvcf-notary-service", Version: "1.5.1"},
	}
	observabilityReleases := []helmfileRelease{
		{Name: "function-autoscaler", Namespace: "function-autoscaler", Enabled: false, Installed: true, Labels: "release-group:observability", Chart: "nvcf/helm-nvcf-function-autoscaler", Version: "0.3.2"},
	}
	return []resolvedInventoryPlaneInput{
		{
			Name:        "compute-plane",
			ReleaseList: mustMarshalHelmfileReleases(t, computeReleases),
			ManifestByRelease: map[string][]byte{
				"nvca": []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    release-artifact-nvca-image: registry.example.com/nvca:4.1.0
`),
			},
		},
		{
			Name:        "control-plane",
			ReleaseList: mustMarshalHelmfileReleases(t, controlReleases),
			ManifestByRelease: map[string][]byte{
				"notary-service": []byte(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: notary
          image: registry.example.com/nvcf-notary:1.14.0
`),
				"resource-config": []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: resource-config
`),
			},
		},
		{
			Name:        "observability",
			ReleaseList: mustMarshalHelmfileReleases(t, observabilityReleases),
			ManifestByRelease: map[string][]byte{
				"function-autoscaler": []byte(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: autoscaler
          image: registry.example.com/function-autoscaler:0.3.2
`),
			},
		},
	}
}

func resolvedInventoryTestSource() stackSourceRelease {
	return stackSourceRelease{
		Version: "1.2.3",
		Tag:     stackTagPrefix + "1.2.3",
		Commit:  strings.Repeat("a", 40),
	}
}

func mustMarshalHelmfileReleases(t *testing.T, releases []helmfileRelease) []byte {
	t.Helper()
	body, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func findResolvedInventoryRelease(t *testing.T, inventory resolvedStackInventory, plane, name string) resolvedInventoryRelease {
	t.Helper()
	for _, release := range inventory.Releases {
		if release.Plane == plane && release.Name == name {
			return release
		}
	}
	t.Fatalf("release %s/%s not found", plane, name)
	return resolvedInventoryRelease{}
}

func findResolvedInventoryArtifact(t *testing.T, inventory resolvedStackInventory, artifactType, reference string) resolvedInventoryArtifact {
	t.Helper()
	for _, artifact := range inventory.Artifacts {
		if artifact.Type == artifactType && artifact.Reference == reference {
			return artifact
		}
	}
	t.Fatalf("artifact %s %s not found", artifactType, reference)
	return resolvedInventoryArtifact{}
}
