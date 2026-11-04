// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func imageCatalogFixture(t *testing.T, values string) imageChartSnapshot {
	t.Helper()
	var tree map[string]any
	if err := yaml.Unmarshal([]byte(values), &tree); err != nil {
		t.Fatal(err)
	}
	return imageChartSnapshot{Values: tree, AppVersions: map[string]string{"dependency": "v8.2.1"}}
}

func TestImageCatalogResolveDefaultsAndOwnership(t *testing.T) {
	snapshot := imageCatalogFixture(t, "thirdPartyImages:\n  dependency: dependency.image\nownedImages:\n  server: server.image\ndependency:\n  imageRegistry: quay.io\n  imageNamespace: example\n  image:\n    name: dependency\nserver:\n  image:\n    registry: registry.example:5000\n    repository: team/server\n    tag: v2\n")
	images, err := resolveChartImageCatalog(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Reference != "quay.io/example/dependency:v8.2.1" ||
		images[1].Reference != "registry.example:5000/team/server:v2" ||
		images[1].Ownership != "ownedImages" {
		t.Fatalf("unexpected catalog: %+v", images)
	}
	// A fully qualified repository works even when the shared registry is empty.
	dependency := snapshot.Values["dependency"].(map[string]any)
	dependency["imageRegistry"] = ""
	dependency["image"].(map[string]any)["repository"] = "override.example/specific/image"
	images, err = resolveChartImageCatalog(snapshot)
	if err != nil || images[0].Reference != "override.example/specific/image:v8.2.1" {
		t.Fatalf("explicit repository: %+v, %v", images, err)
	}
}

func TestImageCatalogRejectsInvalidDeclarations(t *testing.T) {
	for _, input := range []string{
		"{}",
		"thirdPartyImages: []",
		"thirdPartyImages: {server: missing.image}",
		"thirdPartyImages: {server: server.image}\nserver: {image: broken}",
		"thirdPartyImages: {server: server.image}\nserver: {image: {repository: nginx, tag: latest}}",
		"thirdPartyImages: {server: server.image}\nownedImages: {server: server.image}\nserver: {image: {repository: nginx, tag: v1}}",
	} {
		if _, err := resolveChartImageCatalog(imageCatalogFixture(t, input)); err == nil {
			t.Errorf("accepted invalid declaration %s", input)
		}
	}
}

func TestImageCatalogCoverageIncludesIndirectImages(t *testing.T) {
	catalog := []catalogImage{{Reference: "mirror.example/server:v1"}}
	for _, manifest := range []string{
		"spec:\n  containers:\n    - image: mirror.example/unknown:v1\n",
		"spec:\n  containers:\n    - image: mirror.example/server:v1\n      args: [--acme-http01-solver-image=mirror.example/solver:v1]\n",
		"spec:\n  containers:\n    - image: mirror.example/server:v1\n      env:\n        - name: AGENT_INJECT_VAULT_IMAGE\n          value: mirror.example/agent:v1\n",
	} {
		if err := checkChartImageCoverage(catalog, []byte(manifest)); err == nil {
			t.Errorf("accepted undeclared effective image in %s", manifest)
		}
	}
	if err := checkChartImageCoverage(catalog, []byte("spec:\n  containers:\n    - image: mirror.example/server:v1\n")); err != nil {
		t.Fatal(err)
	}
	catalog = append(catalog, catalogImage{Reference: "mirror.example/agent:v1"})
	if err := checkChartImageCoverage(catalog, []byte("spec:\n  env:\n    - name: AGENT_INJECT_VAULT_IMAGE\n      value: mirror.example/agent:v1\n")); err != nil {
		t.Fatal(err)
	}
}

func TestImageMirrorOverridesRequireCompleteVerifiedMappings(t *testing.T) {
	images := []catalogImage{
		{ID: "server", Path: "server.image", Reference: "upstream.example/server:v1", split: true},
		{ID: "solver", Path: "dependency.acmesolver.image", Reference: "upstream.example/solver:v2", digest: true},
	}
	raw := []byte("images:\n  server:\n    source: upstream.example/server:v1\n    destination: mirror.example:5000/renamed/server:v1\n  solver:\n    source: upstream.example/solver:v2\n    destination: mirror.example:5000/renamed/solver:v2\n")
	var verified []string
	verify := func(source, destination string) error {
		verified = append(verified, source+" -> "+destination)
		return nil
	}
	overrides, err := generateImageMirrorOverrides(images, raw, verify)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := imageCatalogValue(overrides, "server.image")
	solver, _ := imageCatalogValue(overrides, "dependency.acmesolver.image")
	if server["registry"] != "mirror.example:5000" || server["repository"] != "renamed/server" ||
		solver["repository"] != "mirror.example:5000/renamed/solver" || solver["registry"] != "" ||
		len(verified) != 2 {
		t.Fatalf("unexpected overrides: %#v; verification: %v", overrides, verified)
	}
	for _, invalid := range [][]byte{
		[]byte("images: {}"),
		[]byte(strings.ReplaceAll(string(raw), "upstream.example/server:v1", "upstream.example/server:stale")),
		[]byte(strings.ReplaceAll(string(raw), "mirror.example:5000/renamed/server:v1", "unqualified/server:v1")),
		append(append([]byte{}, raw...), []byte("---\nimages: {}\n")...),
		[]byte(strings.ReplaceAll(string(raw), "source:", "unexpected:")),
	} {
		if _, err := generateImageMirrorOverrides(images, invalid, verify); err == nil {
			t.Errorf("accepted invalid mapping %s", invalid)
		}
	}
	if _, err := generateImageMirrorOverrides(images, raw, func(string, string) error {
		return errors.New("platform digests differ")
	}); err == nil || !strings.Contains(err.Error(), "platform digests differ") {
		t.Fatalf("verification error was lost: %v", err)
	}
}

func TestImageMirrorPreservesDigestInputShapes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name   string
		split  bool
		digest bool
	}{
		{"split", true, false},
		{"full", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			image := catalogImage{ID: "server", Path: "image", Reference: "source.example/server:v1@" + digest, split: test.split, digest: test.digest}
			raw := []byte("images:\n  server:\n    source: " + image.Reference + "\n    destination: mirror.example/renamed:v1@" + digest + "\n")
			values, err := generateImageMirrorOverrides([]catalogImage{image}, raw, func(string, string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			node := values["image"].(map[string]any)
			if test.digest && (node["digest"] != digest || node["tag"] != "v1") {
				t.Fatalf("digest fields lost: %#v", node)
			}
			if test.split && node["tag"] != "v1@"+digest {
				t.Fatalf("tag@digest lost: %#v", node)
			}
		})
	}
}

func TestImageMirrorRequiresUnambiguousPlatformIndexes(t *testing.T) {
	valid := func(architecture, digest string) map[string]any {
		return map[string]any{"digest": digest, "platform": map[string]any{"os": "linux", "architecture": architecture}}
	}
	amd64 := valid("amd64", "sha256:"+strings.Repeat("a", 64))
	arm64 := valid("arm64", "sha256:"+strings.Repeat("b", 64))
	for _, test := range []struct {
		entries []map[string]any
		valid   bool
	}{
		{[]map[string]any{amd64, arm64}, true},
		{[]map[string]any{amd64}, false},
		{[]map[string]any{amd64, arm64, arm64}, false},
		{[]map[string]any{amd64, valid("arm64", "invalid")}, false},
	} {
		raw, _ := json.Marshal(map[string]any{"manifests": test.entries})
		_, err := decodeImageMirrorPlatforms(raw)
		if (err == nil) != test.valid {
			t.Errorf("platform entries %+v: %v", test.entries, err)
		}
	}
}

func TestImageCatalogCharts(t *testing.T) {
	if os.Getenv("NVCF_IMAGE_CATALOG_E2E") != "1" {
		t.Skip("set NVCF_IMAGE_CATALOG_E2E=1 to render the four real charts with Helm")
	}
	for _, test := range []struct {
		name   string
		path   string
		count  int
		values string
	}{
		{"nats", "nats", 3, "{}"},
		{"cert-manager", "cert-manager", 5, "{}"},
		{"openbao", "openbao/helm", 4, "openbao:\n  server:\n    image: {registry: source.example, repository: nvcf/openbao}\n  migrations:\n    image: {registry: source.example, repository: nvcf/migrations}\n  injector:\n    image: {registry: source.example, repository: nvcf/injector}\n    agentImage: {registry: source.example, repository: nvcf/openbao}\n"},
		{"cassandra", "cassandra/helm", 2, "cassandra:\n  image: {registry: source.example, repository: nvcf/cassandra}\n  migrations:\n    image: {registry: source.example, repository: nvcf/migrations}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			chart := filepath.Join(t.TempDir(), "chart")
			source := filepath.Join("..", "..", "deploy", "helm", test.path)
			if err := copyResolvedInventoryStack(source, chart); err != nil {
				t.Fatal(err)
			}
			// Dependency downloads stay in the test-owned chart copy.
			if test.name == "nats" || test.name == "openbao" {
				if _, err := imageCatalogCommand("helm", "dependency", "build", "--skip-refresh", chart); err != nil {
					t.Fatal(err)
				}
			}
			valuesPath := filepath.Join(t.TempDir(), "values.yaml")
			if err := os.WriteFile(valuesPath, []byte(test.values), 0o600); err != nil {
				t.Fatal(err)
			}
			sourceSnapshot, err := renderImageChart(chart, []string{valuesPath})
			if err != nil {
				t.Fatal(err)
			}
			images, err := resolveChartImageCatalog(sourceSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if len(images) != test.count {
				t.Fatalf("catalog has %d identities, want %d", len(images), test.count)
			}
			if err := checkChartImageCoverage(images, sourceSnapshot.Manifest); err != nil {
				t.Fatal(err)
			}
			mappings := make(map[string]imageMirrorMapping)
			for _, image := range images {
				parsed, err := parseResolvedImageReference(image.Reference)
				if err != nil {
					t.Fatal(err)
				}
				mappings[image.ID] = imageMirrorMapping{
					Source:      image.Reference,
					Destination: "mirror.example:5000/renamed/" + strings.ToLower(image.ID) + ":" + parsed.Version + "@sha256:" + strings.Repeat("a", 64),
				}
			}
			raw, _ := yaml.Marshal(map[string]any{"images": mappings})
			overrides, err := generateImageMirrorOverrides(images, raw, func(string, string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			overridePath := filepath.Join(t.TempDir(), "mirror.yaml")
			encoded, _ := yaml.Marshal(overrides)
			if err := os.WriteFile(overridePath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			mirrorSnapshot, err := renderImageChart(chart, []string{valuesPath, overridePath})
			if err != nil {
				t.Fatal(err)
			}
			mirrored, err := resolveChartImageCatalog(mirrorSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkImageMirrorDestinations(images, mirrored, raw); err != nil {
				t.Fatal(err)
			}
			if err := checkChartImageCoverage(mirrored, mirrorSnapshot.Manifest); err != nil {
				t.Fatal(err)
			}
			// Values supplied after the generated profile remain explicit
			// operator overrides, including the indirect solver/agent paths.
			selected := images[0]
			explicitFields, _ := imageCatalogValue(overrides, selected.Path)
			explicitFields["repository"] = "explicit.example/customer/custom"
			if selected.split {
				explicitFields["registry"], explicitFields["repository"] = "explicit.example", "customer/custom"
			}
			explicitPath := filepath.Join(t.TempDir(), "explicit.yaml")
			explicit := make(map[string]any)
			if err := setImageCatalogOverride(explicit, selected.Path, explicitFields); err != nil {
				t.Fatal(err)
			}
			encoded, _ = yaml.Marshal(explicit)
			if err := os.WriteFile(explicitPath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			overrideSnapshot, err := renderImageChart(chart, []string{valuesPath, overridePath, explicitPath})
			if err != nil {
				t.Fatal(err)
			}
			overridden, err := resolveChartImageCatalog(overrideSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(overridden[0].Reference, "explicit.example/customer/custom:") {
				t.Fatalf("explicit override was lost: %+v", overridden[0])
			}
			if err := checkChartImageCoverage(overridden, overrideSnapshot.Manifest); err != nil {
				t.Fatal(err)
			}
			if test.name == "openbao" {
				for _, skew := range []string{
					"openbao:\n  injector:\n    agentImage:\n      tag: incompatible\n",
					"openbao:\n  server:\n    extraContainers:\n      - name: auto-unseal-sidecar\n        image: mirror.example:5000/server:incompatible\n",
				} {
					skewPath := filepath.Join(t.TempDir(), "version-skew.yaml")
					if err := os.WriteFile(skewPath, []byte(skew), 0o600); err != nil {
						t.Fatal(err)
					}
					_, err := renderImageChart(chart, []string{valuesPath, overridePath, skewPath})
					if err == nil || !strings.Contains(err.Error(), "OpenBao image tag mismatch") {
						t.Fatalf("OpenBao client version skew was accepted: %v", err)
					}
				}
			}
		})
	}
}
