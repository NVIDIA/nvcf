// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// imageCatalogFixture supplies a synthetic dependency version for inheritance tests.
func imageCatalogFixture(t *testing.T, values string) imageChartSnapshot {
	t.Helper()
	var tree map[string]any
	if err := yaml.Unmarshal([]byte(values), &tree); err != nil {
		t.Fatal(err)
	}
	return imageChartSnapshot{Values: tree, AppVersions: map[string]string{"dependency": "v8.2.1"}}
}

// TestImageCatalogResolveDefaultsAndOwnership checks inherited versions and fully qualified overrides.
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

// TestImageCatalogResolvesNativeImagePrecedence distinguishes NATS digest/full
// references from cert-manager's support for a tag and digest together.
func TestImageCatalogResolvesNativeImagePrecedence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name   string
		values string
		want   string
	}{
		{
			name:   "nats full reference",
			values: "thirdPartyImages: {server: nats.container.image}\nnats:\n  container:\n    image: {registry: stale.example, repository: ignored, tag: stale, digest: " + digest + ", fullImageName: selected.example/nats:custom}\n",
			want:   "selected.example/nats:custom",
		},
		{
			name:   "nats digest precedes tag",
			values: "thirdPartyImages: {server: nats.container.image}\nnats:\n  container:\n    image: {registry: selected.example, repository: nats, tag: stale, digest: " + digest + "}\n",
			want:   "selected.example/nats@" + digest,
		},
		{
			name:   "cert-manager tag and digest",
			values: "thirdPartyImages: {server: cert-manager.image}\ncert-manager:\n  imageRegistry: selected.example\n  imageNamespace: team\n  image: {name: controller, tag: fixture, digest: " + digest + "}\n",
			want:   "selected.example/team/controller:fixture@" + digest,
		},
		{
			name:   "invalid nats full reference",
			values: "thirdPartyImages: {server: nats.container.image}\nnats:\n  container:\n    image: {registry: source.example, repository: nats, tag: fixture, fullImageName: missing-tag}\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			images, err := resolveChartImageCatalog(imageCatalogFixture(t, test.values))
			if test.want == "" {
				if err == nil {
					t.Fatal("expected invalid full reference to fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(images) != 1 || images[0].Reference != test.want {
				t.Fatalf("image precedence: got %+v, want %s", images, test.want)
			}
		})
	}
}

// TestImageCatalogRejectsInvalidDeclarations requires complete, uniquely indexed images.
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

// TestImageCatalogCoverageIncludesIndirectImages catches undeclared solver and injected-agent images.
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

// TestImageCatalogCharts exercises real Helm defaults and ordinary values overrides
// from an isolated Helm configuration, including indirect workload settings.
func TestImageCatalogCharts(t *testing.T) {
	if os.Getenv("NVCF_IMAGE_CATALOG_E2E") != "1" {
		t.Skip("set NVCF_IMAGE_CATALOG_E2E=1 to render the four real charts with Helm")
	}
	helmConfiguration := t.TempDir()
	t.Setenv("HELM_REPOSITORY_CONFIG", filepath.Join(helmConfiguration, "repositories.yaml"))
	t.Setenv("HELM_REPOSITORY_CACHE", filepath.Join(helmConfiguration, "cache"))
	t.Setenv("HELM_REGISTRY_CONFIG", filepath.Join(helmConfiguration, "registry.json"))
	for _, test := range []struct {
		name   string
		path   string
		count  int
		values string
	}{
		{"nats", "nats", 3, "{}"},
		{"cert-manager", "cert-manager", 5, "{}"},
		{"openbao", "openbao/helm", 4, "{}"},
		{"cassandra", "cassandra/helm", 2, "{}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			chart := filepath.Join(t.TempDir(), "chart")
			source := filepath.Join("..", "..", "deploy", "helm", test.path)
			if err := copyResolvedInventoryStack(source, chart); err != nil {
				t.Fatal(err)
			}
			// Dependency downloads stay in the test-owned chart copy.
			if test.name == "nats" || test.name == "openbao" {
				registerImageCatalogRepositories(t, chart)
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
			// Every indexed image must be visible in the wrapper values,
			// rather than requiring an operator to discover dependency defaults.
			rawDefaults, err := os.ReadFile(filepath.Join(chart, "values.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			defaults := imageCatalogFixture(t, string(rawDefaults))
			defaults.AppVersions = sourceSnapshot.AppVersions
			prefilled, err := resolveChartImageCatalog(defaults)
			if err != nil {
				t.Fatalf("wrapper image defaults are incomplete: %v", err)
			}
			if len(prefilled) != len(images) {
				t.Fatal("wrapper and effective image catalogs differ")
			}
			for i, image := range images {
				if prefilled[i].Reference != image.Reference {
					t.Fatalf("%s wrapper default %s differs from effective image %s", image.ID, prefilled[i].Reference, image.Reference)
				}
			}
			if len(images) != test.count {
				t.Fatalf("catalog has %d identities, want %d", len(images), test.count)
			}
			if err := checkChartImageCoverage(images, sourceSnapshot.Manifest); err != nil {
				t.Fatal(err)
			}
			// Use ordinary Helm values, without a mapping file or generator.
			overrides := make(map[string]any)
			want := make(map[string]string)
			for _, image := range images {
				parsed, err := parseResolvedImageReference(image.Reference)
				if err != nil {
					t.Fatal(err)
				}
				repository := "renamed/" + strings.ToLower(image.ID)
				digest := "sha256:" + strings.Repeat("a", 64)
				fields := map[string]any{"registry": "mirror.example:5000", "repository": repository, "tag": parsed.Version + "@" + digest}
				if image.digest {
					fields = map[string]any{"registry": "", "repository": "mirror.example:5000/" + repository, "tag": parsed.Version, "digest": digest}
				}
				setTestImageValues(t, overrides, image.Path, fields)
				want[image.ID] = "mirror.example:5000/" + repository + ":" + parsed.Version + "@" + digest
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
			for _, image := range mirrored {
				if image.Reference != want[image.ID] {
					t.Fatalf("%s override: got %s, want %s", image.ID, image.Reference, want[image.ID])
				}
				if !strings.Contains(string(mirrorSnapshot.Manifest), image.Reference) {
					t.Fatalf("%s override did not reach a workload or indirect image setting", image.ID)
				}
			}
			if err := checkChartImageCoverage(mirrored, mirrorSnapshot.Manifest); err != nil {
				t.Fatal(err)
			}
			// Values supplied after a registry profile remain explicit
			// operator overrides, including the indirect solver/agent paths.
			selected := images[0]
			explicitFields, _ := imageCatalogValue(overrides, selected.Path)
			explicitFields["repository"] = "explicit.example/customer/custom"
			if selected.split {
				explicitFields["registry"], explicitFields["repository"] = "explicit.example", "customer/custom"
			}
			explicitPath := filepath.Join(t.TempDir(), "explicit.yaml")
			explicit := make(map[string]any)
			setTestImageValues(t, explicit, selected.Path, explicitFields)
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
			if test.name == "nats" {
				for _, fields := range []map[string]any{
					{"fullImageName": "explicit.example/customer/box:custom"},
					{"registry": "explicit.example", "repository": "customer/box", "digest": "sha256:" + strings.Repeat("b", 64)},
				} {
					values := make(map[string]any)
					setTestImageValues(t, values, "nats.natsBox.container.image", fields)
					encoded, _ := yaml.Marshal(values)
					path := filepath.Join(t.TempDir(), "native-image-override.yaml")
					if err := os.WriteFile(path, encoded, 0o600); err != nil {
						t.Fatal(err)
					}
					rendered, err := renderImageChart(chart, []string{path})
					if err != nil {
						t.Fatal(err)
					}
					catalog, err := resolveChartImageCatalog(rendered)
					if err != nil {
						t.Fatal(err)
					}
					if err := checkChartImageCoverage(catalog, rendered.Manifest); err != nil {
						t.Fatal(err)
					}
					want := "explicit.example/customer/box@" + fmt.Sprint(fields["digest"])
					if full, ok := fields["fullImageName"].(string); ok {
						want = full
					}
					actual, err := extractResolvedImages(rendered.Manifest)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, image := range actual {
						found = found || image.Reference == want
					}
					if !found {
						t.Fatalf("native NATS image override %s did not reach the box workload", want)
					}
				}
			}
			if test.name == "openbao" {
				for _, image := range []string{
					"",
					"mirror.example/openbao",
					"mirror.example:5000/openbao",
					"mirror.example:5000/openbao:",
					"mirror.example:5000/openbao@sha256:" + strings.Repeat("a", 64),
				} {
					t.Run("untagged sidecar "+image, func(t *testing.T) {
						values := fmt.Sprintf("openbao:\n  server:\n    extraContainers:\n      - name: auto-unseal-sidecar\n        image: %q\n", image)
						path := filepath.Join(t.TempDir(), "untagged-sidecar.yaml")
						if err := os.WriteFile(path, []byte(values), 0o600); err != nil {
							t.Fatal(err)
						}
						_, err := renderImageChart(chart, []string{valuesPath, overridePath, path})
						if err == nil || !strings.Contains(err.Error(), "auto-unseal sidecar image requires an explicit tag") {
							t.Fatalf("sidecar image %q did not fail the explicit-tag guard: %v", image, err)
						}
					})
				}
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

// registerImageCatalogRepositories registers HTTPS dependencies in the test's
// isolated Helm configuration. OCI dependencies need no repository registration.
func registerImageCatalogRepositories(t *testing.T, chart string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(chart, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Dependencies []struct {
			Repository string
		}
	}
	if err := yaml.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	for index, dependency := range metadata.Dependencies {
		if !strings.HasPrefix(dependency.Repository, "https://") {
			continue
		}
		name := "image-catalog-" + fmt.Sprint(index)
		if _, err := imageCatalogCommand("helm", "repo", "add", name, dependency.Repository); err != nil {
			t.Fatal(err)
		}
	}
}

// setTestImageValues replaces an image object in an override file; Helm performs
// the merge with chart defaults when that file is rendered.
func setTestImageValues(t *testing.T, values map[string]any, path string, fields map[string]any) {
	t.Helper()
	keys := strings.Split(path, ".")
	for _, key := range keys[:len(keys)-1] {
		if values[key] == nil {
			values[key] = make(map[string]any)
		}
		values = values[key].(map[string]any)
	}
	values[keys[len(keys)-1]] = fields
}
