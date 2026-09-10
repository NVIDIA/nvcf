// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testPublishedChartRepository = "https://helm.example.test/nvcf"

func TestVerifyResolvedInventoryCheckoutRequiresTaggedCleanStack(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", map[string]string{
		"deploy/stacks/self-managed/helmfile.d/01-dependencies.yaml.gotmpl": "releases: []\n",
	})
	if err := verifyResolvedInventoryCheckout(repo, release); err != nil {
		t.Fatalf("verifyResolvedInventoryCheckout failed: %v", err)
	}

	writeFile(t, filepath.Join(repo, "deploy/stacks/self-managed/untracked.yaml"), "changed\n")
	if err := verifyResolvedInventoryCheckout(repo, release); err == nil || !strings.Contains(err.Error(), "changes outside tagged commit") {
		t.Fatalf("dirty checkout error = %v, want tagged commit error", err)
	}
}

func TestMergeAndResolveHelmfileReleasesPreservesOptionalStatus(t *testing.T) {
	repo := t.TempDir()
	stateFile := filepath.Join(repo, "deploy/stacks/example/helmfile.d/01.yaml.gotmpl")
	writeFile(t, stateFile, "releases: []\n")
	writeFile(t, filepath.Join(repo, "deploy/stacks/example/charts/local/Chart.yaml"), "name: local-chart\nversion: 2.3.4\n")

	base := mustJSON(t, []helmfileRelease{{
		Name: "required", Chart: "nvcf/required", Version: "1.0.0", Enabled: true, Installed: true,
	}})
	full := mustJSON(t, []helmfileRelease{
		{Name: "required", Chart: "nvcf/required", Version: "1.0.0", Enabled: true, Installed: true},
		{Name: "optional", Chart: "../charts/local", Enabled: true, Installed: true},
	})
	built := []byte("repositories:\n  - name: nvcf\n    url: registry.example.test/release/charts\n    oci: true\n")
	repositories, err := parseHelmfileRepositories(built)
	if err != nil {
		t.Fatal(err)
	}
	source := stackSourceRelease{
		Version: "1.2.3",
		Tag:     stackTagPrefix + "1.2.3",
		Commit:  strings.Repeat("a", 40),
	}

	releases, err := mergeAndResolveHelmfileReleases(repo, stateFile, source, base, full, repositories, testPublishedChartRepository)
	if err != nil {
		t.Fatalf("mergeAndResolveHelmfileReleases failed: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("got %d releases, want 2", len(releases))
	}
	byName := map[string]helmfileRelease{}
	for _, release := range releases {
		byName[release.Name] = release
	}
	if got := byName["required"].Chart; got != testPublishedChartRepository+"/required" {
		t.Fatalf("required chart = %q", got)
	}
	optional := byName["optional"]
	if optional.Enabled {
		t.Fatal("optional release was marked enabled by default")
	}
	wantLocal := resolvedInventoryRepository + "/" + source.Commit + "/deploy/stacks/example/charts/local"
	if optional.Chart != wantLocal || optional.Version != "2.3.4" {
		t.Fatalf("optional local chart = %q@%q, want %q@2.3.4", optional.Chart, optional.Version, wantLocal)
	}
}

func TestCollectResolvedStackInventoryBuildsEveryPlane(t *testing.T) {
	t.Setenv("NVCF_RELEASE_NGC_API_KEY", "test-api-key")
	t.Setenv("NVCF_RELEASE_HELM_REGISTRY", "registry.example.test/release/charts")
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, filepath.FromSlash(resolvedInventoryConfigPath)), "schemaVersion: 1\npublishedChartRepository: "+testPublishedChartRepository+"\n")
	for _, state := range resolvedInventoryStates {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(state.path)), "releases: []\n")
	}

	source := stackSourceRelease{
		Version: "1.2.3",
		Tag:     stackTagPrefix + "1.2.3",
		Commit:  strings.Repeat("b", 40),
	}
	runner := &fakeResolvedInventoryRunner{t: t}
	inventory, err := collectResolvedStackInventory(repo, source, runner)
	if err != nil {
		t.Fatalf("collectResolvedStackInventory failed: %v", err)
	}
	if err := validateResolvedStackInventory(inventory); err != nil {
		t.Fatalf("inventory validation failed: %v", err)
	}
	if len(inventory.Releases) != 8 {
		t.Fatalf("got %d releases, want 8", len(inventory.Releases))
	}
	for _, release := range inventory.Releases {
		if release.Name == "nvcf-pki" && release.Required {
			t.Fatal("nvcf-pki should remain optional after the full render")
		}
	}
	if runner.templateCalls != len(resolvedInventoryStates) {
		t.Fatalf("template calls = %d, want %d", runner.templateCalls, len(resolvedInventoryStates))
	}
	if runner.prepareCalls != 2 {
		t.Fatalf("repository preparation calls = %d, want source and public repository preparation", runner.prepareCalls)
	}
	wantPrefix := []string{"prepare-source", "list", "list", "build", "prepare-public", "template"}
	if len(runner.operations) < len(wantPrefix) || !reflect.DeepEqual(runner.operations[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("operation prefix = %v, want %v", runner.operations, wantPrefix)
	}
}

func TestParseResolvedInventoryHelmSource(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    resolvedInventoryHelmSource
		wantErr string
	}{
		{
			name: "registry and nested repository",
			raw:  " registry.example.test/team/charts/ ",
			want: resolvedInventoryHelmSource{Registry: "registry.example.test", Repository: "team/charts"},
		},
		{name: "missing", wantErr: "is required"},
		{name: "URL scheme", raw: "https://registry.example.test/team/charts", wantErr: "without credentials or a URL scheme"},
		{name: "missing repository", raw: "registry.example.test", wantErr: "registry host and repository path"},
		{name: "empty segment", raw: "registry.example.test/team//charts", wantErr: "invalid repository path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseResolvedInventoryHelmSource(test.raw)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("source = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestLoadResolvedInventoryConfig(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(repo, filepath.FromSlash(resolvedInventoryConfigPath))
	writeFile(t, configPath, "schemaVersion: 1\npublishedChartRepository: "+testPublishedChartRepository+"\n")
	config, err := loadResolvedInventoryConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	if config.PublishedChartRepository != testPublishedChartRepository {
		t.Fatalf("published repository = %q", config.PublishedChartRepository)
	}

	writeFile(t, configPath, "schemaVersion: 1\npublishedChartRepository: oci://registry.example.test/charts\n")
	if _, err := loadResolvedInventoryConfig(repo); err == nil || !strings.Contains(err.Error(), "resolved HTTPS repository") {
		t.Fatalf("non-HTTPS repository error = %v", err)
	}
}

func TestResolvedInventoryRepositoryCommandsAuthenticatesNVCFRegistry(t *testing.T) {
	commands, err := resolvedInventoryRepositoryCommands(map[string]helmfileRepository{
		"nvcf": {
			Name: "nvcf", URL: "registry.example.test/release/charts", OCI: true,
		},
		"public": {
			Name: "public", URL: "https://charts.example.test", OCI: false,
		},
		"unrelated-oci": {
			Name: "unrelated-oci", URL: "registry.example.test/public/charts", OCI: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []resolvedInventoryHelmCommand{
		{
			Repository:    "nvcf",
			Args:          []string{"registry", "login", "registry.example.test", "--username", "$oauthtoken", "--password-stdin"},
			Authenticated: true,
		},
		{
			Repository: "public",
			Args:       []string{"repo", "add", "public", "https://charts.example.test", "--force-update"},
		},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestReadRenderedReleaseManifestIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "z.yaml"), "kind: Service\n")
	writeFile(t, filepath.Join(root, "nested/a.yaml"), "kind: Deployment\n")
	manifest, err := readRenderedReleaseManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(manifest), "kind: Deployment\n\n---\nkind: Service\n"; got != want {
		t.Fatalf("manifest = %q, want %q", got, want)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fakeResolvedInventoryRunner struct {
	t              *testing.T
	prepareCalls   int
	templateCalls  int
	sourcePrepared bool
	publicPrepared bool
	operations     []string
}

func (runner *fakeResolvedInventoryRunner) PrepareRepositories(_ []string, repositories map[string]helmfileRepository, ngcAPIKey string) error {
	runner.t.Helper()
	runner.prepareCalls++
	if ngcAPIKey != "test-api-key" {
		runner.t.Fatalf("NGC API key = %q", ngcAPIKey)
	}
	if repository, ok := repositories["nvcf"]; ok {
		if len(repositories) != 1 || !repository.OCI || repository.URL != "registry.example.test/release/charts" {
			runner.t.Fatalf("source repositories = %#v", repositories)
		}
		runner.sourcePrepared = true
		runner.operations = append(runner.operations, "prepare-source")
		return nil
	}
	if repository, ok := repositories["public"]; ok {
		if len(repositories) != 1 || repository.OCI || repository.URL != "https://charts.example.test" {
			runner.t.Fatalf("public repositories = %#v", repositories)
		}
		runner.publicPrepared = true
		runner.operations = append(runner.operations, "prepare-public")
		return nil
	}
	runner.t.Fatalf("unexpected repositories = %#v", repositories)
	return nil
}

func (runner *fakeResolvedInventoryRunner) Output(_ string, env []string, args ...string) ([]byte, error) {
	runner.t.Helper()
	if got := argumentAfter(runner.t, args, "--environment"); got != "default" {
		runner.t.Fatalf("Helmfile environment = %q, want default", got)
	}
	if got := environmentValue(env, "HELMFILE_ENV"); got != "inventory" {
		runner.t.Fatalf("HELMFILE_ENV = %q, want inventory", got)
	}
	stateFile := argumentAfter(runner.t, args, "--file")
	action := resolvedInventoryAction(args)
	if (action == "list" || action == "build") && !runner.sourcePrepared {
		runner.t.Fatalf("Helmfile %s ran before source-registry authentication", action)
	}
	runner.operations = append(runner.operations, action)
	releases := fakeResolvedInventoryReleases(stateFile, hasResolvedInventoryFullOverrides(args))
	switch action {
	case "list":
		return mustJSON(runner.t, releases), nil
	case "build":
		built := "repositories:\n  - name: nvcf\n    url: registry.example.test/release/charts\n    oci: true\n"
		if strings.Contains(filepath.ToSlash(stateFile), "self-managed/helmfile.d/01-") {
			built += "  - name: public\n    url: https://charts.example.test\n    oci: false\n"
		}
		return []byte(built), nil
	case "template":
		if strings.Contains(filepath.ToSlash(stateFile), "self-managed/helmfile.d/01-") && !runner.publicPrepared {
			runner.t.Fatal("Helmfile template ran before public repository preparation")
		}
		runner.templateCalls++
		outputDir := argumentAfter(runner.t, args, "--output-dir")
		for _, release := range releases {
			manifest := fmt.Sprintf("apiVersion: v1\nkind: Pod\nspec:\n  containers:\n    - image: nvcr.io/nvidia/nvcf/%s:1.0.0\n", release.Name)
			writeFile(runner.t, filepath.Join(outputDir, release.Name, "manifest.yaml"), manifest)
		}
		return nil, nil
	default:
		runner.t.Fatalf("unexpected Helmfile action in %v", args)
		return nil, nil
	}
}

func fakeResolvedInventoryReleases(stateFile string, full bool) []helmfileRelease {
	release := func(name string) helmfileRelease {
		return helmfileRelease{
			Name: name, Chart: "nvcf/" + name, Version: "1.0.0", Enabled: true, Installed: true,
		}
	}
	slashPath := filepath.ToSlash(stateFile)
	switch {
	case strings.Contains(slashPath, "self-managed/helmfile.d/01-"):
		releases := []helmfileRelease{release("nats")}
		if full {
			releases = append(releases, release("nvcf-pki"))
		}
		return releases
	case strings.Contains(slashPath, "self-managed/helmfile.d/02-"):
		return []helmfileRelease{release("api")}
	case strings.Contains(slashPath, "self-managed/helmfile.d/03-"):
		return []helmfileRelease{release("state-metrics")}
	case strings.Contains(slashPath, "observability/helmfile.d/01-"):
		return []helmfileRelease{release("otel-collector")}
	case strings.Contains(slashPath, "nvcf-compute-plane/helmfile.d/01-"):
		releases := []helmfileRelease{release("kai-scheduler")}
		if full {
			releases = append(releases, release("grove-operator"))
		} else {
			releases[0].Enabled = false
		}
		return releases
	case strings.Contains(slashPath, "nvcf-compute-plane/helmfile.d/02-"):
		return []helmfileRelease{release("nvca-operator")}
	default:
		panic("unexpected state " + stateFile)
	}
}

func hasResolvedInventoryFullOverrides(args []string) bool {
	for _, arg := range args {
		if arg == "addons.llm.enabled=true" || arg == "addons.kaiScheduler.enabled=true" {
			return true
		}
	}
	return false
}

func resolvedInventoryAction(args []string) string {
	for _, arg := range args {
		switch arg {
		case "list", "build", "template":
			return arg
		}
	}
	return ""
}

func argumentAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("missing %s in %v", flag, args)
	return ""
}

func environmentValue(env []string, name string) string {
	for _, entry := range env {
		entryName, value, found := strings.Cut(entry, "=")
		if found && entryName == name {
			return value
		}
	}
	return ""
}
