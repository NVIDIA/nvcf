// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUmbrellaCLICommandE2E(t *testing.T) {
	if os.Getenv("NVCF_UMBRELLA_CLI_E2E") != "1" {
		t.Skip("set NVCF_UMBRELLA_CLI_E2E=1 to build and render with the real CLI")
	}
	for _, tool := range []string{"helm", "helmfile"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal(err)
		}
	}
	fixture := umbrellaBundleFixture(t)
	if err := runUmbrella(fixture.prepareArgs()[1:]); err != nil {
		t.Fatal(err)
	}
	manifest, data, err := loadUmbrellaManifest(fixture.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(t.TempDir(), "assets")
	if err := assembleUmbrellaBundle(fixture.root, fixture.inventories, assets, manifest, data, nil); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(assets, umbrellaBundleName(manifest.Version))
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := umbrellaDigest(archive)
	module, err := filepath.Abs(filepath.Join("..", "..", "src/clis/nvcf-cli"))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "nvcf-cli")
	if output, err := exec.Command("go", "build", "-C", module, "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	workspace := t.TempDir()
	cache := filepath.Join(workspace, "cache")
	config := filepath.Join(workspace, "config.yaml")
	writeFile(t, config, "base_http_url: http://127.0.0.1:1\n")
	invoke := func(want string, args ...string) []byte {
		t.Helper()
		command := exec.Command(binary, append([]string{"--config", config, "self-hosted"}, args...)...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "KUBECONFIG="+filepath.Join(workspace, "unused-kubeconfig"), "HELM_CACHE_HOME="+filepath.Join(workspace, "helm-cache"), "HELM_CONFIG_HOME="+filepath.Join(workspace, "helm-config"), "HELM_DATA_HOME="+filepath.Join(workspace, "helm-data"))
		output, err := command.CombinedOutput()
		if want == "" && err != nil {
			t.Fatalf("CLI: %v\n%s", err, output)
		}
		if want != "" && (err == nil || !strings.Contains(string(output), want)) {
			t.Fatalf("want %s, got %v\n%s", want, err, output)
		}
		return output
	}
	selection := []string{"--umbrella-bundle", archivePath, "--umbrella-sha256", digest, "--umbrella-cache-dir", cache, "--env", "default", "--no-apply", "--non-interactive"}
	for run := 0; run < 2; run++ {
		output := invoke("", append(selection, "install", "--control-plane")...)
		if !bytes.Contains(output, []byte("fixture-control-plane")) || !bytes.Contains(output, []byte("NVCF "+manifest.Version)) {
			t.Fatalf("wrong control-plane selection\n%s", output)
		}
		values := filepath.Join(workspace, "register-values.yaml")
		writeFile(t, values, "clusterName: umbrella-fixture\nncaID: nvcf-default\n")
		output = invoke("", append(selection, "compute-plane", "install", "--values", values)...)
		if !bytes.Contains(output, []byte("fixture-compute-plane")) {
			t.Fatalf("wrong compute-plane selection\n%s", output)
		}
	}
	bundleRoot := filepath.Join(cache, "umbrella-"+strings.TrimPrefix(digest, "sha256:"), "source")
	for _, plane := range releaseSetStackNames {
		version, err := os.ReadFile(filepath.Join(bundleRoot, umbrellaStackDirectory(plane), "VERSION"))
		if err != nil || string(version) != manifest.Stacks[plane].Version+"\n" {
			t.Fatalf("wrong component receipt version %s", plane)
		}
	}
	// The selected sibling Observability source still renders independently.
	obs := exec.Command("helmfile", "--state-values-set", "observability.enabled=true", "-f", filepath.Join(bundleRoot, umbrellaStackDirectory(releaseSetStackObservability), "helmfile.d"), "template")
	obs.Env = append(os.Environ(), "KUBECONFIG="+filepath.Join(workspace, "unused-kubeconfig"))
	if output, err := obs.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("fixture-observability")) {
		t.Fatalf("sibling observability: %v\n%s", err, output)
	}
	invoke("cannot be combined", append(selection, "--control-plane-stack", bundleRoot, "install", "--control-plane")...)
	invoke("checksum mismatch", "--umbrella-bundle", archivePath, "--umbrella-sha256", umbrellaDigest(nil), "--umbrella-cache-dir", cache, "install", "--control-plane", "--no-apply")
	// Legacy explicit local selection still takes its original render path.
	invoke("", "--control-plane-stack", filepath.Join(bundleRoot, umbrellaStackDirectory(releaseSetStackControlPlane)), "--env", "default", "--no-apply", "install", "--control-plane")
	receipt, err := os.ReadFile(filepath.Join(filepath.Dir(bundleRoot), "selection.json"))
	if err != nil {
		t.Fatal(err)
	}
	var identity map[string]any
	if err := json.Unmarshal(receipt, &identity); err != nil || identity["sha256"] != digest {
		t.Fatal("selected bundle receipt does not round-trip")
	}
	corrupted := filepath.Join(workspace, "corrupt.tar.gz")
	writeFile(t, corrupted, "corrupt")
	invoke("checksum mismatch", "--umbrella-bundle", corrupted, "--umbrella-sha256", digest, "--umbrella-cache-dir", filepath.Join(workspace, "empty-cache"), "install", "--control-plane", "--no-apply")
	t.Run("isolated live install", func(t *testing.T) {
		liveUmbrellaCLI(t, binary, archivePath, digest, manifest)
	})
}

// This opt-in test only accepts an explicitly selected, empty disposable cluster.
// It validates packaging and CLI/Helm plumbing, not NVCF workload qualification.
func liveUmbrellaCLI(t *testing.T, binary, archive, digest string, manifest *umbrellaManifest) {
	t.Helper()
	kubeconfig := os.Getenv("NVCF_UMBRELLA_LIVE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set NVCF_UMBRELLA_LIVE_KUBECONFIG for disposable k3d install validation")
	}
	context := os.Getenv("NVCF_UMBRELLA_LIVE_CONTEXT")
	if !strings.HasPrefix(context, "k3d-nvcf-umbrella-") {
		t.Fatal("live validation requires an explicit k3d-nvcf-umbrella-* context")
	}
	workspace := t.TempDir()
	env := append(os.Environ(), "KUBECONFIG="+kubeconfig, "HELM_CACHE_HOME="+filepath.Join(workspace, "helm-cache"), "HELM_CONFIG_HOME="+filepath.Join(workspace, "helm-config"))
	run := func(name string, args ...string) []byte {
		t.Helper()
		command := exec.Command(name, args...)
		command.Env, command.Dir = env, workspace
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, output)
		}
		return output
	}
	kubectl := func(args ...string) []byte {
		t.Helper()
		return run("kubectl", append([]string{"--context", context}, args...)...)
	}
	if actual := run("kubectl", "config", "current-context"); strings.TrimSpace(string(actual)) != context {
		t.Fatal("isolated kubeconfig current context differs from the explicit test context")
	}
	if output := run("helm", "--kube-context", context, "list", "-A", "-q"); strings.TrimSpace(string(output)) != "" {
		t.Fatal("live validation requires a cluster with no Helm releases")
	}
	config := filepath.Join(workspace, "config.yaml")
	writeFile(t, config, "base_http_url: http://127.0.0.1:1\n")
	cache := filepath.Join(workspace, "cache")
	selection := []string{"--config", config, "self-hosted", "--umbrella-bundle", archive, "--umbrella-sha256", digest, "--umbrella-cache-dir", cache, "--env", "live", "--non-interactive"}
	// Populate the verified source without applying, then add deployment-specific values.
	run(binary, append(selection, "--no-apply", "install", "--control-plane")...)
	bundleRoot := filepath.Join(cache, "umbrella-"+strings.TrimPrefix(digest, "sha256:"), "source")
	control := filepath.Join(bundleRoot, umbrellaStackDirectory(releaseSetStackControlPlane))
	writeFile(t, filepath.Join(control, "environments/live.yaml"), "addons:\n  llm:\n    enabled: false\n")
	values := filepath.Join(workspace, "registration.yaml")
	writeFile(t, values, "clusterName: umbrella-fixture\nncaID: nvcf-default\n")
	apply := func() {
		t.Helper()
		// The fixture has no identity service; skip the optional post-install token mint.
		run(binary, append(selection, "--token", "fixture-unused-token", "install", "--control-plane")...)
		run(binary, append(selection, "compute-plane", "install", "--values", values, "--kube-context", context)...)
		run("helmfile", "--kube-context", context, "--state-values-set", "observability.enabled=true", "-f", filepath.Join(bundleRoot, umbrellaStackDirectory(releaseSetStackObservability), "helmfile.d"), "apply", "--skip-diff-on-install")
	}
	apply()
	identities := map[string]string{}
	for _, plane := range releaseSetStackNames {
		name := "fixture-" + plane
		version := kubectl("get", "configmap", name, "-o", "jsonpath={.data.version}")
		if string(version) != manifest.Stacks[plane].Version {
			t.Fatalf("installed %s version %s", plane, version)
		}
		identities[plane] = string(kubectl("get", "configmap", name, "-o", "jsonpath={.metadata.uid}"))
	}
	apply()
	for plane, uid := range identities {
		if actual := kubectl("get", "configmap", "fixture-"+plane, "-o", "jsonpath={.metadata.uid}"); string(actual) != uid {
			t.Fatalf("repeat apply replaced %s", plane)
		}
	}
	t.Log("cold/warm CLI apply preserved all three independent versions and Kubernetes object identities")
	t.Log(string(run("helm", "--kube-context", context, "list", "-A")))
}
