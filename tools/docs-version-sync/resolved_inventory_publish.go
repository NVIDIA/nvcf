// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	resolvedInventoryRepository = "https://github.com/NVIDIA/nvcf/tree"
	resolvedInventoryTimeout    = 30 * time.Minute
)

var resolvedInventoryCommonOverrides = []string{
	"global.image.registry=nvcr.io",
	"global.image.repository=nvidia/nvcf",
}

type resolvedInventoryState struct {
	plane         string
	path          string
	baseOverrides []string
	fullOverrides []string
}

var resolvedInventoryStates = []resolvedInventoryState{
	{
		plane: "control-plane",
		path:  "deploy/stacks/self-managed/helmfile.d/01-dependencies.yaml.gotmpl",
		fullOverrides: []string{
			"addons.llm.enabled=true",
		},
	},
	{
		plane: "control-plane",
		path:  "deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl",
		fullOverrides: []string{
			"addons.llm.enabled=true",
			"addons.vanityGateway.enabled=true",
			"addons.nvcfUi.enabled=true",
		},
	},
	{
		plane: "observability",
		path:  "deploy/stacks/self-managed/helmfile.d/03-observability.yaml.gotmpl",
	},
	{
		plane: "observability",
		path:  "deploy/stacks/observability/helmfile.d/01-observability.yaml.gotmpl",
		baseOverrides: []string{
			"observability.profile=all",
		},
	},
	{
		plane: "compute-plane",
		path:  "deploy/stacks/nvcf-compute-plane/helmfile.d/01-dependencies.yaml.gotmpl",
		fullOverrides: []string{
			"addons.kaiScheduler.enabled=true",
			"addons.groveOperator.enabled=true",
			"addons.dynamoOperator.enabled=true",
			"addons.topologyAwareScheduling.enabled=true",
		},
	},
	{
		plane: "compute-plane",
		path:  "deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl",
	},
}

type resolvedInventoryCommandRunner interface {
	Output(dir string, env []string, args ...string) ([]byte, error)
	PrepareRepositories(env []string, repositories map[string]helmfileRepository, ngcAPIKey string) error
}

type execResolvedInventoryCommandRunner struct{}

func (execResolvedInventoryCommandRunner) Output(dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), resolvedInventoryTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "helmfile", args...)
	command.Dir = dir
	command.Env = env
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("helmfile %s timed out after %s", strings.Join(args, " "), resolvedInventoryTimeout)
		}
		return nil, fmt.Errorf("helmfile %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (execResolvedInventoryCommandRunner) PrepareRepositories(env []string, repositories map[string]helmfileRepository, ngcAPIKey string) error {
	names := make([]string, 0, len(repositories))
	for name := range repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		repository := repositories[name]
		if repository.OCI {
			continue
		}
		args := []string{"repo", "add", repository.Name, repository.URL, "--force-update"}
		var stdin io.Reader
		if strings.HasPrefix(repository.URL, "https://helm.ngc.nvidia.com/") {
			args = append(args, "--username", "$oauthtoken", "--password-stdin")
			stdin = strings.NewReader(ngcAPIKey + "\n")
		}
		ctx, cancel := context.WithTimeout(context.Background(), resolvedInventoryTimeout)
		command := exec.CommandContext(ctx, "helm", args...)
		command.Env = env
		command.Stdin = stdin
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("configure Helm repository %s: timed out after %s", repository.Name, resolvedInventoryTimeout)
			}
			return fmt.Errorf("configure Helm repository %s: %w\n%s", repository.Name, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

type helmfileRepository struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	OCI  bool   `yaml:"oci"`
}

type helmfileBuildDocument struct {
	Repositories []helmfileRepository `yaml:"repositories"`
}

type localChartMetadata struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

func writeResolvedStackInventory(repoRoot, outputPath string, source stackSourceRelease) error {
	if err := verifyResolvedInventoryCheckout(repoRoot, source); err != nil {
		return err
	}
	inventory, err := collectResolvedStackInventory(repoRoot, source, execResolvedInventoryCommandRunner{})
	if err != nil {
		return err
	}
	raw, err := marshalResolvedStackInventory(inventory)
	if err != nil {
		return fmt.Errorf("marshal resolved stack inventory: %w", err)
	}
	if err := writeFileAtomically(outputPath, raw, 0o644); err != nil {
		return fmt.Errorf("write resolved stack inventory: %w", err)
	}
	return nil
}

func verifyResolvedInventoryCheckout(repoRoot string, source stackSourceRelease) error {
	if err := validateStackSourceRelease(source); err != nil {
		return err
	}
	tagCommit, err := gitOutput(repoRoot, "rev-parse", "--verify", "refs/tags/"+source.Tag+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve stack source tag %s: %w", source.Tag, err)
	}
	if got := trimOutput(tagCommit); got != source.Commit {
		return fmt.Errorf("stack source tag %s resolves to %s, want %s", source.Tag, got, source.Commit)
	}
	head, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve inventory checkout HEAD: %w", err)
	}
	if got := trimOutput(head); got != source.Commit {
		return fmt.Errorf("inventory checkout HEAD is %s, want tagged commit %s", got, source.Commit)
	}
	status, err := gitOutput(repoRoot, "status", "--porcelain", "--untracked-files=normal", "--", "deploy/stacks")
	if err != nil {
		return fmt.Errorf("inspect stack checkout: %w", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		return fmt.Errorf("deploy/stacks has changes outside tagged commit %s", source.Commit)
	}
	return nil
}

func collectResolvedStackInventory(repoRoot string, source stackSourceRelease, runner resolvedInventoryCommandRunner) (resolvedStackInventory, error) {
	tempRoot, err := os.MkdirTemp("", "nvcf-resolved-stack-inventory-")
	if err != nil {
		return resolvedStackInventory{}, err
	}
	defer os.RemoveAll(tempRoot)

	copiedRepoRoot := filepath.Join(tempRoot, "repo")
	if err := copyResolvedInventoryInputs(repoRoot, copiedRepoRoot); err != nil {
		return resolvedStackInventory{}, err
	}
	env, ngcAPIKey, err := prepareResolvedInventoryEnvironment(copiedRepoRoot)
	if err != nil {
		return resolvedStackInventory{}, err
	}

	planes := make(map[string]*resolvedInventoryPlaneInput, len(resolvedStackPlanes))
	for _, name := range resolvedStackPlanes {
		planes[name] = &resolvedInventoryPlaneInput{
			Name:              name,
			ManifestByRelease: map[string][]byte{},
		}
	}
	for stateIndex, state := range resolvedInventoryStates {
		stateFile := filepath.Join(copiedRepoRoot, filepath.FromSlash(state.path))
		releases, manifests, err := collectResolvedInventoryState(
			copiedRepoRoot,
			stateFile,
			filepath.Join(tempRoot, "rendered", fmt.Sprintf("%02d", stateIndex)),
			source,
			state,
			env,
			ngcAPIKey,
			runner,
		)
		if err != nil {
			return resolvedStackInventory{}, fmt.Errorf("collect %s: %w", state.path, err)
		}
		plane := planes[state.plane]
		var existing []helmfileRelease
		if len(plane.ReleaseList) != 0 {
			existing, err = decodeHelmfileReleaseList(plane.ReleaseList)
			if err != nil {
				return resolvedStackInventory{}, err
			}
		}
		existing = append(existing, releases...)
		plane.ReleaseList, err = json.Marshal(existing)
		if err != nil {
			return resolvedStackInventory{}, err
		}
		for release, manifest := range manifests {
			if _, exists := plane.ManifestByRelease[release]; exists {
				return resolvedStackInventory{}, fmt.Errorf("duplicate %s release %s across Helmfile states", state.plane, release)
			}
			plane.ManifestByRelease[release] = manifest
		}
	}

	inputs := make([]resolvedInventoryPlaneInput, 0, len(resolvedStackPlanes))
	for _, name := range resolvedStackPlanes {
		inputs = append(inputs, *planes[name])
	}
	return generateResolvedStackInventory(source, inputs)
}

func copyResolvedInventoryInputs(repoRoot, copiedRepoRoot string) error {
	for _, stack := range []string{"self-managed", "nvcf-compute-plane", "observability"} {
		source := filepath.Join(repoRoot, "deploy", "stacks", stack)
		destination := filepath.Join(copiedRepoRoot, "deploy", "stacks", stack)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := copyResolvedInventoryStack(source, destination); err != nil {
			return fmt.Errorf("copy %s stack inputs: %w", stack, err)
		}
	}
	return nil
}

func copyResolvedInventoryStack(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && rel != "." && (entry.Name() == "testdata" || entry.Name() == "out") {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("stack input %s is a symbolic link", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm())
	})
}

func prepareResolvedInventoryEnvironment(copiedRepoRoot string) ([]string, string, error) {
	ngcAPIKey := strings.TrimSpace(os.Getenv("NVCF_RELEASE_NGC_API_KEY"))
	if ngcAPIKey == "" {
		ngcAPIKey = strings.TrimSpace(os.Getenv("NGC_API_KEY"))
	}
	if ngcAPIKey == "" {
		return nil, "", fmt.Errorf("NVCF_RELEASE_NGC_API_KEY is required to render public NGC charts")
	}
	values, err := yaml.Marshal(map[string]any{
		"global": map[string]any{
			"domain": "inventory.example.invalid",
			"helm": map[string]any{
				"sources": map[string]any{
					"url": "https://helm.ngc.nvidia.com/nvidia/nvcf",
				},
			},
			"image": map[string]any{
				"registry":   "nvcr.io",
				"repository": "nvidia/nvcf",
			},
		},
		"ingress": map[string]any{
			"gatewayApi": map[string]any{
				"controllerNamespace": "gateway-system",
				"gateways": map[string]any{
					"grpc": map[string]any{
						"name":      "inventory-grpc",
						"namespace": "gateway-system",
					},
					"shared": map[string]any{
						"name":      "inventory-shared",
						"namespace": "gateway-system",
					},
				},
			},
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("marshal inventory-only stack environment: %w", err)
	}
	for _, stack := range []string{"self-managed", "nvcf-compute-plane", "observability"} {
		environmentPath := filepath.Join(copiedRepoRoot, "deploy", "stacks", stack, "environments", "inventory.yaml")
		if err := os.MkdirAll(filepath.Dir(environmentPath), 0o755); err != nil {
			return nil, "", err
		}
		if err := os.WriteFile(environmentPath, values, 0o600); err != nil {
			return nil, "", fmt.Errorf("write %s inventory-only stack environment: %w", stack, err)
		}
	}
	secretsPath := filepath.Join(copiedRepoRoot, "deploy", "stacks", "self-managed", "secrets", "inventory-secrets.yaml")
	if err := os.WriteFile(secretsPath, []byte("{}\n"), 0o600); err != nil {
		return nil, "", fmt.Errorf("write inventory-only stack secrets: %w", err)
	}
	registrationDir := filepath.Join(copiedRepoRoot, "deploy", "stacks", "nvcf-compute-plane", "inventory-registration")
	if err := os.MkdirAll(registrationDir, 0o755); err != nil {
		return nil, "", err
	}
	registration := []byte("clusterName: inventory\nclusterID: 00000000-0000-0000-0000-000000000001\nclusterGroupID: 00000000-0000-0000-0000-000000000002\nncaID: inventory\nregion: inventory\nselfManaged:\n  identitySource: psat\n  icmsServiceURL: http://icms.example.invalid:8080\n  revalServiceURL: http://reval.example.invalid:8080\n  natsURL: nats://nats.example.invalid:4222\n")
	if err := os.WriteFile(filepath.Join(registrationDir, "inventory-register-values.yaml"), registration, 0o600); err != nil {
		return nil, "", fmt.Errorf("write inventory-only registration values: %w", err)
	}
	helmRoot := filepath.Join(copiedRepoRoot, ".helm")
	for _, path := range []string{filepath.Join(helmRoot, "cache"), filepath.Join(helmRoot, "config"), filepath.Join(helmRoot, "data")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, "", err
		}
	}
	env := append([]string{}, os.Environ()...)
	env = setResolvedInventoryEnvironment(env, map[string]string{
		"CLUSTER_NAME":     "inventory",
		"HELMFILE_ENV":     "inventory",
		"HELM_CACHE_HOME":  filepath.Join(helmRoot, "cache"),
		"HELM_CONFIG_HOME": filepath.Join(helmRoot, "config"),
		"HELM_DATA_HOME":   filepath.Join(helmRoot, "data"),
		"NCA_ID":           "inventory",
		"OUTPUT_DIR":       registrationDir,
	})
	return env, ngcAPIKey, nil
}

func setResolvedInventoryEnvironment(env []string, values map[string]string) []string {
	updated := make([]string, 0, len(env)+len(values))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if _, replace := values[name]; !replace {
			updated = append(updated, entry)
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		updated = append(updated, name+"="+values[name])
	}
	return updated
}

func collectResolvedInventoryState(
	copiedRepoRoot string,
	stateFile string,
	renderDir string,
	source stackSourceRelease,
	state resolvedInventoryState,
	env []string,
	ngcAPIKey string,
	runner resolvedInventoryCommandRunner,
) ([]helmfileRelease, map[string][]byte, error) {
	baseOverrides := append(append([]string{}, resolvedInventoryCommonOverrides...), state.baseOverrides...)
	fullOverrides := append(append([]string{}, baseOverrides...), state.fullOverrides...)
	baseList, err := runResolvedInventoryHelmfile(runner, filepath.Dir(stateFile), env, stateFile, baseOverrides, "list", "--output", "json")
	if err != nil {
		return nil, nil, fmt.Errorf("list default releases: %w", err)
	}
	fullList, err := runResolvedInventoryHelmfile(runner, filepath.Dir(stateFile), env, stateFile, fullOverrides, "list", "--output", "json")
	if err != nil {
		return nil, nil, fmt.Errorf("list releases with optional components: %w", err)
	}
	built, err := runResolvedInventoryHelmfile(runner, filepath.Dir(stateFile), env, stateFile, fullOverrides, "build")
	if err != nil {
		return nil, nil, fmt.Errorf("build resolved Helmfile state: %w", err)
	}
	repositories, err := parseHelmfileRepositories(built)
	if err != nil {
		return nil, nil, err
	}
	releases, err := mergeAndResolveHelmfileReleases(copiedRepoRoot, stateFile, source, baseList, fullList, repositories)
	if err != nil {
		return nil, nil, err
	}
	if err := runner.PrepareRepositories(env, repositories, ngcAPIKey); err != nil {
		return nil, nil, err
	}

	if err := os.MkdirAll(renderDir, 0o755); err != nil {
		return nil, nil, err
	}
	if _, err := runResolvedInventoryHelmfile(
		runner,
		filepath.Dir(stateFile),
		env,
		stateFile,
		fullOverrides,
		"template",
		"--include-crds",
		"--output-dir", renderDir,
		"--output-dir-template", "{{ .OutputDir }}/{{ .Release.Name }}",
	); err != nil {
		return nil, nil, fmt.Errorf("render releases: %w", err)
	}

	manifests := make(map[string][]byte, len(releases))
	for _, release := range releases {
		manifest, err := readRenderedReleaseManifest(filepath.Join(renderDir, release.Name))
		if err != nil {
			return nil, nil, fmt.Errorf("read rendered release %s: %w", release.Name, err)
		}
		manifests[release.Name] = manifest
	}
	return releases, manifests, nil
}

func runResolvedInventoryHelmfile(
	runner resolvedInventoryCommandRunner,
	dir string,
	env []string,
	stateFile string,
	overrides []string,
	command ...string,
) ([]byte, error) {
	args := []string{"--file", stateFile, "--environment", "default", "--quiet"}
	for _, override := range overrides {
		args = append(args, "--state-values-set", override)
	}
	args = append(args, "--skip-refresh")
	args = append(args, command...)
	return runner.Output(dir, env, args...)
}

func decodeHelmfileReleaseList(raw []byte) ([]helmfileRelease, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var releases []helmfileRelease
	if err := decoder.Decode(&releases); err != nil {
		return nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return releases, nil
}

func mergeAndResolveHelmfileReleases(
	copiedRepoRoot string,
	stateFile string,
	source stackSourceRelease,
	baseRaw []byte,
	fullRaw []byte,
	repositories map[string]helmfileRepository,
) ([]helmfileRelease, error) {
	base, err := decodeHelmfileReleaseList(baseRaw)
	if err != nil {
		return nil, fmt.Errorf("parse default release list: %w", err)
	}
	full, err := decodeHelmfileReleaseList(fullRaw)
	if err != nil {
		return nil, fmt.Errorf("parse full release list: %w", err)
	}
	if len(full) == 0 {
		return nil, fmt.Errorf("full release list cannot be empty")
	}
	baseByName := make(map[string]helmfileRelease, len(base))
	for _, release := range base {
		if _, exists := baseByName[release.Name]; exists {
			return nil, fmt.Errorf("duplicate default release %s", release.Name)
		}
		baseByName[release.Name] = release
	}
	fullByName := make(map[string]struct{}, len(full))
	for i := range full {
		if _, exists := fullByName[full[i].Name]; exists {
			return nil, fmt.Errorf("duplicate full release %s", full[i].Name)
		}
		fullByName[full[i].Name] = struct{}{}
		if defaultRelease, exists := baseByName[full[i].Name]; exists {
			full[i].Enabled = defaultRelease.Enabled
		} else {
			full[i].Enabled = false
		}
		if err := resolveHelmfileChartReference(copiedRepoRoot, stateFile, source, repositories, &full[i]); err != nil {
			return nil, fmt.Errorf("resolve release %s chart: %w", full[i].Name, err)
		}
	}
	for name := range baseByName {
		if _, exists := fullByName[name]; !exists {
			return nil, fmt.Errorf("default release %s is missing when optional components are enabled", name)
		}
	}
	sort.Slice(full, func(i, j int) bool { return full[i].Name < full[j].Name })
	raw, err := json.Marshal(full)
	if err != nil {
		return nil, err
	}
	return parseHelmfileReleaseList(raw)
}

func parseHelmfileRepositories(raw []byte) (map[string]helmfileRepository, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	repositories := map[string]helmfileRepository{}
	for {
		var document helmfileBuildDocument
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse built Helmfile state: %w", err)
		}
		for _, repository := range document.Repositories {
			if repository.Name == "" || repository.URL == "" {
				return nil, fmt.Errorf("built Helmfile repository requires name and URL")
			}
			if existing, ok := repositories[repository.Name]; ok && existing != repository {
				return nil, fmt.Errorf("repository alias %s has conflicting definitions", repository.Name)
			}
			repositories[repository.Name] = repository
		}
	}
	return repositories, nil
}

func resolveHelmfileChartReference(
	copiedRepoRoot string,
	stateFile string,
	source stackSourceRelease,
	repositories map[string]helmfileRepository,
	release *helmfileRelease,
) error {
	chart := strings.TrimSpace(release.Chart)
	if chart == "" || chart != release.Chart {
		return fmt.Errorf("chart %q is unresolved", release.Chart)
	}
	if filepath.IsAbs(chart) || strings.HasPrefix(chart, "./") || strings.HasPrefix(chart, "../") {
		chartPath := chart
		if !filepath.IsAbs(chartPath) {
			chartPath = filepath.Join(filepath.Dir(stateFile), filepath.FromSlash(chartPath))
		}
		chartPath, err := filepath.Abs(chartPath)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(copiedRepoRoot, chartPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("local chart %s is outside copied repository", chart)
		}
		metadataRaw, err := os.ReadFile(filepath.Join(chartPath, "Chart.yaml"))
		if err != nil {
			return fmt.Errorf("read local chart metadata: %w", err)
		}
		var metadata localChartMetadata
		if err := yaml.Unmarshal(metadataRaw, &metadata); err != nil {
			return fmt.Errorf("parse local chart metadata: %w", err)
		}
		if metadata.Name == "" || metadata.Version == "" {
			return fmt.Errorf("local chart metadata requires name and version")
		}
		release.Chart = resolvedInventoryRepository + "/" + source.Commit + "/" + filepath.ToSlash(rel)
		release.Version = metadata.Version
		return nil
	}
	if strings.Contains(chart, "://") {
		return nil
	}
	alias, name, found := strings.Cut(chart, "/")
	if !found || name == "" {
		return fmt.Errorf("chart %q has no repository alias", chart)
	}
	repository, ok := repositories[alias]
	if !ok {
		return fmt.Errorf("chart %q uses unknown repository alias %s", chart, alias)
	}
	base := strings.TrimSuffix(repository.URL, "/")
	if repository.OCI && !strings.Contains(base, "://") {
		base = "oci://" + base
	}
	release.Chart = base + "/" + name
	return nil
}

func readRenderedReleaseManifest(root string) ([]byte, error) {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no rendered manifest files found")
	}
	sort.Strings(paths)
	var manifest bytes.Buffer
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		if manifest.Len() != 0 {
			manifest.WriteString("\n---\n")
		}
		manifest.Write(raw)
	}
	if len(bytes.TrimSpace(manifest.Bytes())) == 0 {
		return nil, fmt.Errorf("rendered manifest files are empty")
	}
	return manifest.Bytes(), nil
}

func writeFileAtomically(path string, contents []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".resolved-inventory-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(contents); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
