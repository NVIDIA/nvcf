// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func umbrellaBundleFixture(t *testing.T) umbrellaFixture {
	t.Helper()
	sources := map[string]map[string]string{}
	catalog := editionTestCatalog()
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		directory := umbrellaStackDirectory(name)
		chart := "apiVersion: v2\nname: fixture\nversion: 0.1.0\n"
		state := "environments:\n  default:\n    values:\n      - ../environments/base.yaml\n---\n"
		if name == releaseSetStackObservability {
			state += "{{ if .Values.observability.enabled }}\n"
		}
		state += "releases:\n  - name: fixture-" + name + "\n    chart: ../charts/fixture\n    namespace: default\n"
		if name == releaseSetStackObservability {
			state += "{{ end }}\n"
		}
		sources[name] = map[string]string{
			directory + "/VERSION":                              "0.0.0\n",
			directory + "/environments/base.yaml":               "observability:\n  enabled: false\n",
			directory + "/helmfile.d/01-stack.yaml.gotmpl":      state,
			directory + "/charts/fixture/Chart.yaml":            chart,
			directory + "/charts/fixture/templates/config.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fixture-" + name + "\ndata:\n  version: " + fmt.Sprintf("%q", stack.Version) + "\n",
			directory + "/Makefile.dist":                        "template:\n\thelmfile -f helmfile.d template\n",
			directory + "/Makefile":                             "include development-only.mk\n",
			directory + "/secrets/secrets.yaml.template":        "apiKey: replace-me\n",
			directory + "/secrets/customer-secrets.yaml":        "do-not-package: fixture-only\n",
			directory + "/environments/private.yaml":            "do-not-package: fixture-only\n",
			directory + "/tests/fixture.sh":                     "do-not-package\n",
			directory + "/.env":                                 "do-not-package\n",
		}
	}
	control := umbrellaStackDirectory(releaseSetStackControlPlane)
	// The first public compute-plane release predates the source VERSION file.
	delete(sources[releaseSetStackComputePlane], umbrellaStackDirectory(releaseSetStackComputePlane)+"/VERSION")
	sources[releaseSetStackControlPlane][control+"/helmfile.d/00-observability.yaml.gotmpl"] = `environments:
  default:
    values:
      - ../environments/base.yaml
---
{{ if .Values.observability.enabled }}
helmfiles:
  - path: ../../observability/helmfile.d/01-stack.yaml.gotmpl
    values:
      - observability:
          enabled: true
{{ end }}
`
	// The control-plane tag contains the wrong incidental observability checkout.
	obsChart := umbrellaStackDirectory(releaseSetStackObservability) + "/charts/fixture/templates/config.yaml"
	sources[releaseSetStackControlPlane][obsChart] = "incorrect incidental observability source\n"
	return newUmbrellaFixtureWithSources(t, sources)
}

func TestUmbrellaBundleCommandE2E(t *testing.T) {
	fixture := umbrellaBundleFixture(t)
	binary := filepath.Join(t.TempDir(), "docs-version-sync")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	invoke := func(want string, args ...string) {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = t.TempDir()
		output, err := command.CombinedOutput()
		if want == "" && err != nil {
			t.Fatalf("command failed: %v\n%s", err, output)
		}
		if want != "" && (err == nil || !strings.Contains(string(output), want)) {
			t.Fatalf("want failure %q, got %v\n%s", want, err, output)
		}
	}
	invoke("", fixture.prepareArgs()...)
	manifest, manifestData, err := loadUmbrellaManifest(fixture.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	assemble := func(out string) {
		t.Helper()
		invoke("", "umbrella", "bundle", "--repo", fixture.root, "--manifest", fixture.manifestPath, "--inventories", fixture.inventories, "--out", out)
	}
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	assemble(first)
	// Reassembly must ignore the working tree, including modified tracked runtime files.
	writeFile(t, filepath.Join(fixture.root, umbrellaStackDirectory(releaseSetStackObservability), "charts/fixture/templates/config.yaml"), "unreviewed worktree content\n")
	assemble(second)
	assemble(first)
	var assets umbrellaAssetIndex
	indexData, err := os.ReadFile(filepath.Join(first, "release-assets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(indexData, &assets); err != nil {
		t.Fatal(err)
	}
	for name, expected := range assets.Assets {
		left, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(second, name))
		if err != nil || !bytes.Equal(left, right) || umbrellaDigest(left) != expected {
			t.Fatalf("asset %s is not reproducible: %v", name, err)
		}
	}
	bundlePath := filepath.Join(first, umbrellaBundleName(manifest.Version))
	digest := assets.Assets[umbrellaBundleName(manifest.Version)]
	invoke("", "umbrella", "verify-bundle", "--manifest", fixture.manifestPath, "--bundle", bundlePath, "--sha256", digest)
	invoke("checksum mismatch", "umbrella", "verify-bundle", "--manifest", fixture.manifestPath, "--bundle", bundlePath, "--sha256", umbrellaDigest([]byte("wrong")))
	archive, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readUmbrellaArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range releaseSetStackNames {
		directory := umbrellaStackDirectory(name)
		if string(files[directory+"/VERSION"].data) != manifest.Stacks[name].Version+"\n" {
			t.Fatalf("%s was stamped with the wrong component version", name)
		}
		if !bytes.Equal(files[directory+"/Makefile"].data, files[directory+"/Makefile.dist"].data) {
			t.Fatalf("%s retained its development Makefile", name)
		}
		for _, excluded := range []string{"secrets/customer-secrets.yaml", "environments/private.yaml", "tests/fixture.sh", ".env"} {
			if _, exists := files[directory+"/"+excluded]; exists {
				t.Fatalf("included %s", excluded)
			}
		}
	}
	for _, file := range files {
		if bytes.Contains(file.data, []byte("incorrect incidental")) || bytes.Contains(file.data, []byte("unreviewed worktree")) {
			t.Fatal("bundle used an incidental source checkout")
		}
	}
	t.Run("render extracted bundle", func(t *testing.T) { renderUmbrellaFixture(t, archive, manifest) })
	delete(files, umbrellaStackDirectory(releaseSetStackComputePlane)+"/helmfile.d/01-stack.yaml.gotmpl")
	broken, err := archiveUmbrellaFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyUmbrellaArchive(broken, manifestData, umbrellaDigest(broken)); err == nil {
		t.Fatal("accepted a bundle with a missing indexed runtime file")
	}
}

func renderUmbrellaFixture(t *testing.T, archive []byte, manifest *umbrellaManifest) {
	t.Helper()
	for _, tool := range []string{"helm", "helmfile"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("NVCF_UMBRELLA_RENDER_E2E") == "1" {
				t.Fatalf("required bundle render E2E tool %s: %v", tool, err)
			}
			t.Skipf("%s unavailable; require this gate with NVCF_UMBRELLA_RENDER_E2E=1", tool)
		}
	}
	directory := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("tar", "-xzf", archivePath, "-C", directory).CombinedOutput(); err != nil {
		t.Fatalf("extract: %v\n%s", err, output)
	}
	for _, test := range []struct {
		name    string
		stack   string
		enabled bool
	}{
		{"control with observability", releaseSetStackControlPlane, true},
		{"control without observability", releaseSetStackControlPlane, false},
		{"separate compute", releaseSetStackComputePlane, false},
		{"separate observability", releaseSetStackObservability, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("helmfile", "--allow-no-matching-release", "--state-values-set", fmt.Sprintf("observability.enabled=%t", test.enabled), "-f", umbrellaStackDirectory(test.stack)+"/helmfile.d", "template")
			command.Dir = directory
			command.Env = append(os.Environ(), "HELM_CACHE_HOME="+t.TempDir(), "HELM_CONFIG_HOME="+t.TempDir(), "HELM_DATA_HOME="+t.TempDir(), "KUBECONFIG="+filepath.Join(t.TempDir(), "unused"))
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("render: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), manifest.Stacks[test.stack].Version) {
				t.Fatalf("render did not contain the selected %s version:\n%s", test.stack, output)
			}
			if test.stack == releaseSetStackControlPlane {
				found := strings.Contains(string(output), "fixture-observability")
				if found != test.enabled {
					t.Fatalf("observability enabled=%t, found=%t:\n%s", test.enabled, found, output)
				}
				if found && !strings.Contains(string(output), manifest.Stacks[releaseSetStackObservability].Version) {
					t.Fatalf("sibling observability used the wrong release:\n%s", output)
				}
			}
		})
	}
}

func TestUmbrellaArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
		typeflag byte
	}{
		{"traversal", "../escape", tar.TypeReg},
		{"absolute", "/escape", tar.TypeReg},
		{"symlink", "link", tar.TypeSymlink},
		{"hardlink", "link", tar.TypeLink},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buffer bytes.Buffer
			compressed := gzip.NewWriter(&buffer)
			writer := tar.NewWriter(compressed)
			if err := writer.WriteHeader(&tar.Header{Name: test.filename, Mode: 0o644, Typeflag: test.typeflag, Linkname: "../escape"}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := readUmbrellaArchive(buffer.Bytes()); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
