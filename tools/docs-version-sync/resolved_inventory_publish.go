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
	resolvedInventoryConfigPath = "deploy/stacks/self-managed/release-inventory.yaml"
)

var resolvedInventoryCommonOverrides = []string{
	"global.image.registry=nvcr.io",
	"global.image.repository=nvidia/nvcf",
}

type resolvedInventoryState struct {
	stack         string
	plane         string
	path          string
	baseOverrides []string
	fullOverrides []string
}

var resolvedInventoryStates = []resolvedInventoryState{
	{
		stack: "self-managed",
		plane: "control-plane",
		path:  "deploy/stacks/self-managed/helmfile.d/01-dependencies.yaml.gotmpl",
		fullOverrides: []string{
			"addons.llm.enabled=true",
		},
	},
	{
		stack: "self-managed",
		plane: "control-plane",
		path:  "deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl",
		fullOverrides: []string{
			"addons.llm.enabled=true",
			"addons.vanityGateway.enabled=true",
			"addons.nvcfUi.enabled=true",
		},
	},
	{
		stack: "self-managed",
		plane: "observability",
		path:  "deploy/stacks/self-managed/helmfile.d/03-observability.yaml.gotmpl",
	},
	{
		stack: "observability",
		plane: "observability",
		path:  "deploy/stacks/observability/helmfile.d/01-observability.yaml.gotmpl",
		baseOverrides: []string{
			"observability.profile=all",
		},
	},
	{
		stack: "nvcf-compute-plane",
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
		stack: "nvcf-compute-plane",
		plane: "compute-plane",
		path:  "deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl",
	},
}

type resolvedInventoryCommandRunner interface {
	Output(dir string, env []string, args ...string) ([]byte, error)
	PrepareRepositories(env []string, repositories map[string]helmfileRepository, ngcAPIKey string) error
}

type execResolvedInventoryCommandRunner struct{}

type resolvedInventoryHelmCommand struct {
	Repository    string
	Args          []string
	Authenticated bool
}

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
	commands, err := resolvedInventoryRepositoryCommands(repositories)
	if err != nil {
		return err
	}
	for _, helmCommand := range commands {
		var stdin io.Reader
		if helmCommand.Authenticated {
			stdin = strings.NewReader(ngcAPIKey + "\n")
		}
		ctx, cancel := context.WithTimeout(context.Background(), resolvedInventoryTimeout)
		command := exec.CommandContext(ctx, "helm", helmCommand.Args...)
		command.Env = env
		command.Stdin = stdin
		output, commandErr := command.CombinedOutput()
		cancel()
		if commandErr != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("configure Helm repository %s: timed out after %s", helmCommand.Repository, resolvedInventoryTimeout)
			}
			return fmt.Errorf("configure Helm repository %s: %w\n%s", helmCommand.Repository, commandErr, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func resolvedInventoryRepositoryCommands(repositories map[string]helmfileRepository) ([]resolvedInventoryHelmCommand, error) {
	names := make([]string, 0, len(repositories))
	for name := range repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	commands := make([]resolvedInventoryHelmCommand, 0, len(names))
	for _, name := range names {
		repository := repositories[name]
		if repository.OCI {
			if name != "nvcf" {
				continue
			}
			reference := strings.TrimPrefix(strings.TrimSuffix(repository.URL, "/"), "oci://")
			registry, _, found := strings.Cut(reference, "/")
			if !found || registry == "" {
				return nil, fmt.Errorf("Helm repository %s has an invalid OCI reference", name)
			}
			commands = append(commands, resolvedInventoryHelmCommand{
				Repository:    name,
				Args:          []string{"registry", "login", registry, "--username", "$oauthtoken", "--password-stdin"},
				Authenticated: true,
			})
			continue
		}
		args := []string{"repo", "add", repository.Name, repository.URL, "--force-update"}
		authenticated := strings.HasPrefix(repository.URL, "https://helm.ngc.nvidia.com/")
		if authenticated {
			args = append(args, "--username", "$oauthtoken", "--password-stdin")
		}
		commands = append(commands, resolvedInventoryHelmCommand{
			Repository:    name,
			Args:          args,
			Authenticated: authenticated,
		})
	}
	return commands, nil
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

type resolvedInventoryConfig struct {
	SchemaVersion                  int               `yaml:"schemaVersion"`
	PublishedChartRepository       string            `yaml:"publishedChartRepository"`
	RenderChartRepositoryOverrides map[string]string `yaml:"renderChartRepositoryOverrides"`
}

type resolvedInventoryHelmSource struct {
	Registry   string
	Repository string
}

func (source resolvedInventoryHelmSource) reference() string {
	return source.Registry + "/" + source.Repository
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
	config, err := loadResolvedInventoryConfig(copiedRepoRoot)
	if err != nil {
		return resolvedStackInventory{}, err
	}
	env, renderSources, ngcAPIKey, err := prepareResolvedInventoryEnvironment(copiedRepoRoot, config)
	if err != nil {
		return resolvedStackInventory{}, err
	}
	preparedRegistries := map[string]struct{}{}
	for _, stack := range resolvedInventoryStackNames() {
		renderSource := renderSources[stack]
		if _, prepared := preparedRegistries[renderSource.Registry]; prepared {
			continue
		}
		if err := runner.PrepareRepositories(env, map[string]helmfileRepository{
			"nvcf": {
				Name: "nvcf",
				URL:  renderSource.reference(),
				OCI:  true,
			},
		}, ngcAPIKey); err != nil {
			return resolvedStackInventory{}, fmt.Errorf("authenticate release chart registry for %s: %w", stack, err)
		}
		preparedRegistries[renderSource.Registry] = struct{}{}
	}

	planes := make(map[string]*resolvedInventoryPlaneInput, len(resolvedStackPlanes))
	for _, name := range resolvedStackPlanes {
		planes[name] = &resolvedInventoryPlaneInput{
			Name:              name,
			ManifestByRelease: map[string][]byte{},
		}
	}
	for stateIndex, state := range resolvedInventoryStates {
		renderSource, ok := renderSources[state.stack]
		if !ok {
			return resolvedStackInventory{}, fmt.Errorf("render chart repository is not configured for stack %s", state.stack)
		}
		stateFile := filepath.Join(copiedRepoRoot, filepath.FromSlash(state.path))
		releases, manifests, err := collectResolvedInventoryState(
			copiedRepoRoot,
			stateFile,
			filepath.Join(tempRoot, "rendered", fmt.Sprintf("%02d", stateIndex)),
			source,
			state,
			env,
			renderSource,
			config.PublishedChartRepository,
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

func loadResolvedInventoryConfig(repoRoot string) (resolvedInventoryConfig, error) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(resolvedInventoryConfigPath)))
	if err != nil {
		return resolvedInventoryConfig{}, fmt.Errorf("read resolved inventory config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var config resolvedInventoryConfig
	if err := decoder.Decode(&config); err != nil {
		return resolvedInventoryConfig{}, fmt.Errorf("parse resolved inventory config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return resolvedInventoryConfig{}, fmt.Errorf("parse resolved inventory config: %w", err)
		}
		return resolvedInventoryConfig{}, fmt.Errorf("resolved inventory config contains multiple YAML documents")
	}
	if config.SchemaVersion != 1 {
		return resolvedInventoryConfig{}, fmt.Errorf("resolved inventory config schemaVersion is %d, want 1", config.SchemaVersion)
	}
	repository := config.PublishedChartRepository
	if repository == "" || repository != strings.TrimSpace(repository) || strings.HasSuffix(repository, "/") ||
		!strings.HasPrefix(repository, "https://") || strings.ContainsAny(repository, "{}$?#") {
		return resolvedInventoryConfig{}, fmt.Errorf("resolved inventory publishedChartRepository must be a resolved HTTPS repository without a trailing slash")
	}
	validStacks := make(map[string]struct{}, len(resolvedInventoryStackNames()))
	for _, stack := range resolvedInventoryStackNames() {
		validStacks[stack] = struct{}{}
	}
	for stack, raw := range config.RenderChartRepositoryOverrides {
		if _, ok := validStacks[stack]; !ok {
			return resolvedInventoryConfig{}, fmt.Errorf("resolved inventory renderChartRepositoryOverrides contains unknown stack %s", stack)
		}
		if _, err := parseResolvedInventoryHelmSource(raw); err != nil {
			return resolvedInventoryConfig{}, fmt.Errorf("resolved inventory render chart repository for %s: %w", stack, err)
		}
	}
	return config, nil
}

func resolvedInventoryStackNames() []string {
	stacks := make(map[string]struct{})
	for _, state := range resolvedInventoryStates {
		stacks[state.stack] = struct{}{}
	}
	names := make([]string, 0, len(stacks))
	for stack := range stacks {
		names = append(names, stack)
	}
	sort.Strings(names)
	return names
}

func parseResolvedInventoryHelmSource(raw string) (resolvedInventoryHelmSource, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return resolvedInventoryHelmSource{}, fmt.Errorf("NVCF_RELEASE_HELM_REGISTRY is required to render release charts")
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "@?# \t\r\n") {
		return resolvedInventoryHelmSource{}, fmt.Errorf("NVCF_RELEASE_HELM_REGISTRY must use host/repository format without credentials or a URL scheme")
	}
	registry, repository, found := strings.Cut(value, "/")
	if !found || registry == "" || repository == "" {
		return resolvedInventoryHelmSource{}, fmt.Errorf("NVCF_RELEASE_HELM_REGISTRY must include a registry host and repository path")
	}
	for _, segment := range strings.Split(repository, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return resolvedInventoryHelmSource{}, fmt.Errorf("NVCF_RELEASE_HELM_REGISTRY contains an invalid repository path")
		}
	}
	return resolvedInventoryHelmSource{Registry: registry, Repository: repository}, nil
}

func prepareResolvedInventoryEnvironment(copiedRepoRoot string, config resolvedInventoryConfig) ([]string, map[string]resolvedInventoryHelmSource, string, error) {
	ngcAPIKey := strings.TrimSpace(os.Getenv("NVCF_RELEASE_NGC_API_KEY"))
	if ngcAPIKey == "" {
		ngcAPIKey = strings.TrimSpace(os.Getenv("NGC_API_KEY"))
	}
	if ngcAPIKey == "" {
		return nil, nil, "", fmt.Errorf("NVCF_RELEASE_NGC_API_KEY is required to render release charts")
	}
	defaultRenderSource, err := parseResolvedInventoryHelmSource(os.Getenv("NVCF_RELEASE_HELM_REGISTRY"))
	if err != nil {
		return nil, nil, "", err
	}
	renderSources := make(map[string]resolvedInventoryHelmSource, len(resolvedInventoryStackNames()))
	for _, stack := range resolvedInventoryStackNames() {
		renderSource := defaultRenderSource
		if override, ok := config.RenderChartRepositoryOverrides[stack]; ok {
			renderSource, err = parseResolvedInventoryHelmSource(override)
			if err != nil {
				return nil, nil, "", fmt.Errorf("parse render chart repository for %s: %w", stack, err)
			}
		}
		renderSources[stack] = renderSource
		values, err := yaml.Marshal(map[string]any{
			"global": map[string]any{
				"domain": "inventory.example.invalid",
				"helm": map[string]any{
					"sources": map[string]any{
						"registry":   renderSource.Registry,
						"repository": renderSource.Repository,
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
			return nil, nil, "", fmt.Errorf("marshal inventory-only stack environment for %s: %w", stack, err)
		}
		environmentPath := filepath.Join(copiedRepoRoot, "deploy", "stacks", stack, "environments", "inventory.yaml")
		if err := os.MkdirAll(filepath.Dir(environmentPath), 0o755); err != nil {
			return nil, nil, "", err
		}
		if err := os.WriteFile(environmentPath, values, 0o600); err != nil {
			return nil, nil, "", fmt.Errorf("write %s inventory-only stack environment: %w", stack, err)
		}
	}
	secretsPath := filepath.Join(copiedRepoRoot, "deploy", "stacks", "self-managed", "secrets", "inventory-secrets.yaml")
	if err := os.MkdirAll(filepath.Dir(secretsPath), 0o755); err != nil {
		return nil, nil, "", fmt.Errorf("create inventory-only stack secrets directory: %w", err)
	}
	if err := os.WriteFile(secretsPath, []byte("{}\n"), 0o600); err != nil {
		return nil, nil, "", fmt.Errorf("write inventory-only stack secrets: %w", err)
	}
	registrationDir := filepath.Join(copiedRepoRoot, "deploy", "stacks", "nvcf-compute-plane", "inventory-registration")
	if err := os.MkdirAll(registrationDir, 0o755); err != nil {
		return nil, nil, "", err
	}
	registration := []byte("clusterName: inventory\nclusterID: 00000000-0000-0000-0000-000000000001\nclusterGroupID: 00000000-0000-0000-0000-000000000002\nncaID: inventory\nregion: inventory\nselfManaged:\n  identitySource: psat\n  icmsServiceURL: http://icms.example.invalid:8080\n  revalServiceURL: http://reval.example.invalid:8080\n  natsURL: nats://nats.example.invalid:4222\n")
	if err := os.WriteFile(filepath.Join(registrationDir, "inventory-register-values.yaml"), registration, 0o600); err != nil {
		return nil, nil, "", fmt.Errorf("write inventory-only registration values: %w", err)
	}
	helmRoot := filepath.Join(copiedRepoRoot, ".helm")
	for _, path := range []string{filepath.Join(helmRoot, "cache"), filepath.Join(helmRoot, "config"), filepath.Join(helmRoot, "data")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, nil, "", err
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
	return env, renderSources, ngcAPIKey, nil
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
	renderSource resolvedInventoryHelmSource,
	publishedChartRepository string,
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
	if err := validateResolvedInventoryRenderRepository(repositories, renderSource); err != nil {
		return nil, nil, err
	}
	releases, err := mergeAndResolveHelmfileReleases(copiedRepoRoot, stateFile, source, baseList, fullList, repositories, publishedChartRepository)
	if err != nil {
		return nil, nil, err
	}
	publicRepositories := make(map[string]helmfileRepository, len(repositories))
	for name, repository := range repositories {
		if name != "nvcf" {
			publicRepositories[name] = repository
		}
	}
	if len(publicRepositories) != 0 {
		if err := runner.PrepareRepositories(env, publicRepositories, ngcAPIKey); err != nil {
			return nil, nil, err
		}
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
	// The stack states expose "default" while HELMFILE_ENV selects inventory.yaml within it.
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
	publishedChartRepository string,
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
		if err := resolveHelmfileChartReference(copiedRepoRoot, stateFile, source, repositories, publishedChartRepository, &full[i]); err != nil {
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

func validateResolvedInventoryRenderRepository(repositories map[string]helmfileRepository, source resolvedInventoryHelmSource) error {
	repository, ok := repositories["nvcf"]
	if !ok {
		// Some states contain only third-party or local charts and therefore do
		// not declare the NVCF repository. Chart alias validation below still
		// rejects any release that refers to an undeclared repository.
		return nil
	}
	if !repository.OCI {
		return fmt.Errorf("built Helmfile nvcf repository must use OCI for release inventory rendering")
	}
	reference := strings.TrimPrefix(strings.TrimSuffix(repository.URL, "/"), "oci://")
	if reference != source.reference() {
		return fmt.Errorf("built Helmfile nvcf repository does not match NVCF_RELEASE_HELM_REGISTRY")
	}
	return nil
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
	publishedChartRepository string,
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
	if alias == "nvcf" {
		release.Chart = publishedChartRepository + "/" + name
		return nil
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
