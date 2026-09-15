// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type effectiveStackPin struct {
	artifact             string
	path                 string
	blockPattern         string
	blockBoundaryPattern string
	pattern              string
}

var effectiveStackPins = []effectiveStackPin{
	{
		artifact:             "helm-nvcf-llm-request-router",
		path:                 "deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl",
		blockPattern:         `(?m)^  - name: llm-request-router[ \t]*$`,
		blockBoundaryPattern: `(?m)^  -[ \t]+`,
		pattern:              `(?m)^    version:[ \t]*"?([^"\s]+)"?[ \t]*$`,
	},
	{
		artifact:             "nvcf-gateway-routes",
		path:                 "deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl",
		blockPattern:         `(?m)^  - name: ingress[ \t]*$`,
		blockBoundaryPattern: `(?m)^  -[ \t]+`,
		pattern:              `(?m)^    version:[ \t]*"?([^"\s]+)"?[ \t]*$`,
	},
	{
		artifact: "pylon",
		path:     "deploy/stacks/self-managed/global.yaml.gotmpl",
		pattern:  `pylon:([0-9][^"\s]*)`,
	},
	{
		artifact:             "helm-nvca-operator",
		path:                 "deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl",
		blockPattern:         `(?m)^  - name: nvca-operator[ \t]*$`,
		blockBoundaryPattern: `(?m)^  -[ \t]+`,
		pattern:              `(?m)^    version:[ \t]*"?([^"\s]+)"?[ \t]*$`,
	},
	{
		artifact:             "nvca-operator",
		path:                 "deploy/stacks/nvcf-compute-plane/environments/base.yaml",
		blockPattern:         `(?m)^  nvcaOperator:[ \t]*$`,
		blockBoundaryPattern: `(?m)^  [A-Za-z0-9_-]+:[ \t]*`,
		pattern:              `(?m)^    imageTag:[ \t]*"([^"]+)"[ \t]*$`,
	},
	{
		artifact:             "nvca",
		path:                 "deploy/stacks/nvcf-compute-plane/environments/base.yaml",
		blockPattern:         `(?m)^  nvcaOperator:[ \t]*$`,
		blockBoundaryPattern: `(?m)^  [A-Za-z0-9_-]+:[ \t]*`,
		pattern:              `(?m)^      nvcaVersion:[ \t]*"([^"]+)"[ \t]*$`,
	},
}

func extractEffectiveStackPins(sources map[string][]byte, pins []effectiveStackPin) (map[string]string, error) {
	versions := make(map[string]string, len(pins))
	matchedSources := make(map[string]struct{}, len(sources))
	for _, pin := range pins {
		body, ok := sources[pin.path]
		if !ok {
			return nil, fmt.Errorf("pin source %s for %s is not declared", pin.path, pin.artifact)
		}
		matchBody := body
		if pin.blockPattern != "" {
			blocks := regexp.MustCompile(pin.blockPattern).FindAllIndex(body, -1)
			if len(blocks) != 1 {
				return nil, fmt.Errorf("resolved %d target blocks for %s from %s; want exactly one", len(blocks), pin.artifact, pin.path)
			}
			blockStart, blockEnd := blocks[0][0], len(body)
			if pin.blockBoundaryPattern != "" {
				remainderStart := blocks[0][1]
				if next := regexp.MustCompile(pin.blockBoundaryPattern).FindIndex(body[remainderStart:]); next != nil {
					blockEnd = remainderStart + next[0]
				}
			}
			matchBody = body[blockStart:blockEnd]
		}
		matches := regexp.MustCompile(pin.pattern).FindAllSubmatch(matchBody, -1)
		if len(matches) != 1 {
			if pin.blockPattern != "" {
				return nil, fmt.Errorf("resolved %d versions for %s within target block from %s; want exactly one", len(matches), pin.artifact, pin.path)
			}
			return nil, fmt.Errorf("resolved %d definitions for %s from %s; want exactly one", len(matches), pin.artifact, pin.path)
		}
		if len(matches[0]) != 2 {
			return nil, fmt.Errorf("pin matcher for %s from %s must capture exactly one version", pin.artifact, pin.path)
		}
		if _, exists := versions[pin.artifact]; exists {
			return nil, fmt.Errorf("duplicate pin matcher for artifact %s", pin.artifact)
		}
		versions[pin.artifact] = string(matches[0][1])
		matchedSources[pin.path] = struct{}{}
	}

	sourcePaths := make([]string, 0, len(sources))
	for sourcePath := range sources {
		sourcePaths = append(sourcePaths, sourcePath)
	}
	sort.Strings(sourcePaths)
	for _, sourcePath := range sourcePaths {
		if _, matched := matchedSources[sourcePath]; !matched {
			return nil, fmt.Errorf("declared pin source %s has no matcher", sourcePath)
		}
	}
	return versions, nil
}

func pinSourceDigest(sources map[string][]byte, pins []effectiveStackPin) (string, error) {
	versions, err := extractEffectiveStackPins(sources, pins)
	if err != nil {
		return "", err
	}
	pins = append([]effectiveStackPin(nil), pins...)
	sort.Slice(pins, func(i, j int) bool { return pins[i].artifact < pins[j].artifact })

	digest := sha256.New()
	for _, pin := range pins {
		version, ok := versions[pin.artifact]
		if !ok {
			continue
		}
		digest.Write([]byte(pin.artifact))
		digest.Write([]byte{0})
		digest.Write([]byte(pin.path))
		digest.Write([]byte{0})
		digest.Write([]byte(version))
		digest.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func TestPinSourceDigestDetectsUnreleasedSourceMutation(t *testing.T) {
	sources := map[string][]byte{
		"b.yaml": []byte("image: example:4.5.6\n"),
		"a.yaml": []byte("version: 1.2.3\n"),
	}
	pins := []effectiveStackPin{
		{artifact: "chart", path: "a.yaml", pattern: `version: ([^\s]+)`},
		{artifact: "image", path: "b.yaml", pattern: `image: example:([^\s]+)`},
	}
	const releasedDigest = "sha256:62f9f9f2d48a7798d50dc032ed7db77b8afcbb6eb8189c62cff9590572ff4f50"
	got, err := pinSourceDigest(sources, pins)
	if err != nil {
		t.Fatal(err)
	}
	if got != releasedDigest {
		t.Fatalf("released source digest = %q, want independently calculated %q", got, releasedDigest)
	}

	sources["a.yaml"] = []byte("version: 9.9.9\n")
	got, err = pinSourceDigest(sources, pins)
	if err != nil {
		t.Fatal(err)
	}
	if got == releasedDigest {
		t.Fatalf("unreleased source mutation retained released digest %q", got)
	}

	sources["a.yaml"] = []byte("# unrelated setting\nversion: 1.2.3\n")
	got, err = pinSourceDigest(sources, pins)
	if err != nil {
		t.Fatal(err)
	}
	if got != releasedDigest {
		t.Fatalf("non-pin source mutation changed release digest to %q", got)
	}
}

func TestExtractEffectiveStackPinsRejectsDuplicateArtifactDefinition(t *testing.T) {
	sources := map[string][]byte{
		"stack.yaml": []byte("version: 1.2.3\nversion: 4.5.6\n"),
	}
	pins := []effectiveStackPin{
		{artifact: "chart", path: "stack.yaml", pattern: `(?m)^version: ([^\s]+)$`},
	}

	_, err := extractEffectiveStackPins(sources, pins)
	if err == nil || !strings.Contains(err.Error(), "resolved 2 definitions for chart from stack.yaml") {
		t.Fatalf("extractEffectiveStackPins error = %v, want duplicate-definition rejection", err)
	}
}

func TestExtractEffectiveStackPinsRejectsMissingVersionInTargetBlock(t *testing.T) {
	tests := []struct {
		artifact string
		body     string
	}{
		{
			artifact: "helm-nvcf-llm-request-router",
			body: `releases:
  - name: llm-request-router
    chart: nvcf/llm-request-router
  - chart: nvcf/unrelated
    version: 9.9.9
	`,
		},
		{
			artifact: "nvca",
			body: `  nvcaOperator:
    imageTag: "3.2.19"
  unrelated:
      nvcaVersion: "9.9.9"
`,
		},
	}
	for _, test := range tests {
		t.Run(test.artifact, func(t *testing.T) {
			pin := effectiveStackPinForArtifact(t, test.artifact)
			sources := map[string][]byte{pin.path: []byte(test.body)}

			_, err := extractEffectiveStackPins(sources, []effectiveStackPin{pin})
			want := "resolved 0 versions for " + test.artifact + " within target block"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("extractEffectiveStackPins error = %v, want missing-target-version rejection", err)
			}
		})
	}
}

func TestExtractEffectiveStackPinsRejectsDuplicateVersionsInTargetBlock(t *testing.T) {
	tests := []struct {
		artifact string
		body     string
	}{
		{
			artifact: "helm-nvcf-llm-request-router",
			body: `releases:
  - name: llm-request-router
    version: 1.12.2
    version: 1.12.3
  - name: unrelated
    version: 9.9.9
	`,
		},
		{
			artifact: "nvca",
			body: `  nvcaOperator:
    nvca:
      nvcaVersion: "3.2.19"
      nvcaVersion: "3.2.20"
  unrelated:
    enabled: true
`,
		},
	}
	for _, test := range tests {
		t.Run(test.artifact, func(t *testing.T) {
			pin := effectiveStackPinForArtifact(t, test.artifact)
			sources := map[string][]byte{pin.path: []byte(test.body)}

			_, err := extractEffectiveStackPins(sources, []effectiveStackPin{pin})
			want := "resolved 2 versions for " + test.artifact + " within target block"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("extractEffectiveStackPins error = %v, want duplicate-target-version rejection", err)
			}
		})
	}
}

func effectiveStackPinForArtifact(t *testing.T, artifact string) effectiveStackPin {
	t.Helper()
	for _, pin := range effectiveStackPins {
		if pin.artifact == artifact {
			return pin
		}
	}
	t.Fatalf("effective stack pin for %s is not declared", artifact)
	return effectiveStackPin{}
}

func TestExtractEffectiveStackPinsRejectsDeclaredSourceWithoutMatcher(t *testing.T) {
	sources := map[string][]byte{
		"stack.yaml":     []byte("version: 1.2.3\n"),
		"unhandled.yaml": []byte("version: 9.9.9\n"),
	}
	pins := []effectiveStackPin{
		{artifact: "chart", path: "stack.yaml", pattern: `(?m)^version: ([^\s]+)$`},
	}

	_, err := extractEffectiveStackPins(sources, pins)
	if err == nil || !strings.Contains(err.Error(), "declared pin source unhandled.yaml has no matcher") {
		t.Fatalf("extractEffectiveStackPins error = %v, want unhandled-source rejection", err)
	}
}

func TestMainCatalogMatchesDeclaredReleaseStackPins(t *testing.T) {
	root := docsVersionSyncRepoRoot(t)
	catalog, err := LoadCatalog(filepath.Join(root, "docs", "version-catalog", "main.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "artifacts-" + catalog.Stack.Version + ".txt"; catalog.Stack.ArtifactsFile != want {
		t.Errorf("stack artifacts_file = %q, want %q for catalog stack version %s", catalog.Stack.ArtifactsFile, want, catalog.Stack.Version)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(catalog.Stack.SourceCommit) {
		t.Fatalf("stack source_commit = %q, want immutable 40-character commit SHA", catalog.Stack.SourceCommit)
	}
	if len(catalog.Stack.PinSources) == 0 {
		t.Fatal("stack pin_sources must declare the release files used to resolve effective versions")
	}

	contents := make(map[string][]byte, len(catalog.Stack.PinSources))
	for _, sourcePath := range catalog.Stack.PinSources {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sourcePath)))
		if err != nil {
			t.Fatalf("read declared pin source %s: %v", sourcePath, err)
		}
		contents[sourcePath] = body
	}
	pins := effectiveStackPins
	versions, err := extractEffectiveStackPins(contents, pins)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pinSourceDigest(contents, pins)
	if err != nil {
		t.Fatal(err)
	}
	if got != catalog.Stack.PinSourceDigest {
		t.Fatalf("effective stack pins have digest %s, want release snapshot %s for stack %s at %s; update the version, source commit, digest, and catalog together", got, catalog.Stack.PinSourceDigest, catalog.Stack.Version, catalog.Stack.SourceCommit)
	}

	for _, pin := range pins {
		pin := pin
		t.Run(pin.artifact, func(t *testing.T) {
			_, ok := contents[pin.path]
			if !ok {
				t.Fatalf("pin source %s is not declared in catalog stack pin_sources", pin.path)
			}
			want, ok := versions[pin.artifact]
			if !ok {
				t.Fatalf("could not resolve %s from %s", pin.artifact, pin.path)
			}
			artifact, ok := catalog.findArtifact(pin.artifact)
			if !ok {
				t.Fatalf("catalog is missing stack-pinned artifact %s", pin.artifact)
			}
			if artifact.Version != want {
				t.Errorf("catalog %s version = %s, want effective stack pin %s from %s", pin.artifact, artifact.Version, want, pin.path)
			}
		})
	}

	t.Run("nvcf-cli-independent-release", func(t *testing.T) {
		artifact, ok := catalog.findArtifact("nvcf-cli")
		if !ok {
			t.Fatal("catalog is missing nvcf-cli")
		}
		var source *VersionOverride
		for i := range catalog.VersionOverrides {
			if catalog.VersionOverrides[i].Name == "nvcf-cli" {
				source = &catalog.VersionOverrides[i]
				break
			}
		}
		if source == nil {
			t.Fatal("nvcf-cli has no direct stack pin; declare its independent release in version_overrides")
		}
		if artifact.Version != source.Version {
			t.Errorf("catalog nvcf-cli version = %s, want independent release %s", artifact.Version, source.Version)
		}
		if !strings.Contains(source.Source, "not pinned by stack") {
			t.Errorf("nvcf-cli override source %q must explicitly say it is not pinned by stack", source.Source)
		}
	})
}

func TestPendingPublicationsNeverRenderPrivateRegistryPaths(t *testing.T) {
	root := docsVersionSyncRepoRoot(t)
	catalog, err := LoadCatalog(filepath.Join(root, "docs", "version-catalog", "main.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncDocs(root, catalog, true); err != nil {
		t.Fatalf("checked-in generated docs do not match the catalog: %v", err)
	}

	manifest, err := renderManifestArtifactRegistryPaths(catalog)
	if err != nil {
		t.Fatal(err)
	}
	imageMirroringPath := filepath.Join(root, "docs", "user", "image-mirroring.md")
	imageMirroring, err := os.ReadFile(imageMirroringPath)
	if err != nil {
		t.Fatal(err)
	}
	imageMirroringOutput, _, err := syncImageMirroring(string(imageMirroring), catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, renderer := range []string{
		"image-mirroring-resource-examples",
		"image-mirroring-stack-snippet",
		"image-mirroring-cli-snippet",
	} {
		output, err := Render(renderer, catalog)
		if err != nil {
			t.Fatalf("render %s: %v", renderer, err)
		}
		imageMirroringOutput += output
	}

	allPublicOutput := manifest + imageMirroringOutput
	staging := catalog.Registries[defaultStackRegistry]
	privateRegistryPath := staging.Host + "/" + staging.Namespace
	if strings.Contains(allPublicOutput, privateRegistryPath) {
		t.Fatalf("generated public docs expose private registry path %q", privateRegistryPath)
	}
	if !strings.Contains(manifest, "Publication pending") {
		t.Fatal("manifest does not identify versions awaiting publication")
	}
	if !strings.Contains(imageMirroringOutput, "HELM_NVCA_OPERATOR_REFERENCE") {
		t.Fatal("mirroring instructions do not request an explicit chart reference while publication is pending")
	}
}

func TestCatalogRefreshPreservesPublicationPending(t *testing.T) {
	base := testCatalog()
	base.PublicationPending = []string{"nvcf-self-managed-stack", "nvcf-cli"}

	updated := BuildCatalogFromArtifactsWithBase("0.9.2", nil, base)

	pending := make(map[string]struct{}, len(updated.PublicationPending))
	for _, name := range updated.PublicationPending {
		pending[name] = struct{}{}
	}
	for _, name := range []string{"nvcf-self-managed-stack", "nvcf-cli"} {
		if _, ok := pending[name]; !ok {
			t.Errorf("publication_pending = %v, want preserved entry %s", updated.PublicationPending, name)
		}
	}
	if err := ValidateCatalog(updated); err != nil {
		t.Fatalf("ValidateCatalog failed after refresh: %v", err)
	}
}

func TestCatalogRefreshKeepsSnapshotOnlyForSameStackVersion(t *testing.T) {
	base := testCatalog()
	base.Stack.SourceCommit = "1111111111111111111111111111111111111111"
	base.Stack.PinSources = []string{"stack.yaml"}
	base.Stack.PinSourceDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	sameRelease := BuildCatalogFromArtifactsWithBase(base.Stack.Version, nil, base)
	if sameRelease.Stack.SourceCommit != base.Stack.SourceCommit ||
		strings.Join(sameRelease.Stack.PinSources, ",") != "stack.yaml" ||
		sameRelease.Stack.PinSourceDigest != base.Stack.PinSourceDigest {
		t.Fatalf("same-release snapshot was not preserved: %#v", sameRelease.Stack)
	}

	newRelease := BuildCatalogFromArtifactsWithBase("0.9.2", nil, base)
	if newRelease.Stack.SourceCommit != "" || len(newRelease.Stack.PinSources) != 0 || newRelease.Stack.PinSourceDigest != "" {
		t.Fatalf("new release retained stale source snapshot: %#v", newRelease.Stack)
	}
}

func TestValidateCatalogRejectsIncompleteStackSourceSnapshot(t *testing.T) {
	catalog := testCatalog()
	catalog.Stack.SourceCommit = "1111111111111111111111111111111111111111"

	err := ValidateCatalog(catalog)
	if err == nil || !strings.Contains(err.Error(), "source_commit, pin_sources, and pin_source_digest together") {
		t.Fatalf("ValidateCatalog error = %v, want incomplete stack source snapshot", err)
	}
}

func TestValidateCatalogRejectsInvalidStackSourceSnapshot(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StackMetadata)
		want   string
	}{
		{
			name: "non-immutable commit",
			mutate: func(stack *StackMetadata) {
				stack.SourceCommit = "main"
			},
			want: "source_commit must be a full lowercase commit SHA",
		},
		{
			name: "malformed digest",
			mutate: func(stack *StackMetadata) {
				stack.PinSourceDigest = "sha256:invalid"
			},
			want: "pin_source_digest must be a lowercase SHA-256 digest",
		},
		{
			name: "duplicate source",
			mutate: func(stack *StackMetadata) {
				stack.PinSources = append(stack.PinSources, stack.PinSources[0])
			},
			want: "duplicate stack pin source stack.yaml",
		},
		{
			name: "empty source",
			mutate: func(stack *StackMetadata) {
				stack.PinSources = []string{""}
			},
			want: "stack pin source must be non-empty and trimmed",
		},
		{
			name: "whitespace-padded source",
			mutate: func(stack *StackMetadata) {
				stack.PinSources = []string{" stack.yaml "}
			},
			want: "stack pin source must be non-empty and trimmed",
		},
		{
			name: "trim-equivalent duplicate source",
			mutate: func(stack *StackMetadata) {
				stack.PinSources = []string{"stack.yaml", " stack.yaml "}
			},
			want: "stack pin source must be non-empty and trimmed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := testCatalog()
			catalog.Stack.SourceCommit = "1111111111111111111111111111111111111111"
			catalog.Stack.PinSources = []string{"stack.yaml"}
			catalog.Stack.PinSourceDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
			tt.mutate(&catalog.Stack)

			err := ValidateCatalog(catalog)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCatalog error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateCatalogAcceptsExactPinSourcePaths(t *testing.T) {
	catalog := testCatalog()
	catalog.Stack.SourceCommit = "1111111111111111111111111111111111111111"
	catalog.Stack.PinSources = []string{"deploy/stack.yaml", "deploy/env.yaml"}
	catalog.Stack.PinSourceDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("ValidateCatalog rejected exact pin source paths: %v", err)
	}
}

func TestValidateCatalogRejectsPublishedArtifactMarkedPending(t *testing.T) {
	catalog := testCatalog()
	cli, ok := catalog.findArtifact("nvcf-cli")
	if !ok {
		t.Fatal("test catalog is missing nvcf-cli")
	}
	catalog.Publications = []Publication{{
		Name:     cli.Name,
		Version:  cli.Version,
		Registry: cli.Registry,
	}}
	catalog.PublicationPending = []string{cli.Name}

	err := ValidateCatalog(catalog)
	if err == nil || !strings.Contains(err.Error(), "cannot be both published and publication_pending") {
		t.Fatalf("ValidateCatalog error = %v, want conflicting publication state", err)
	}
}

func TestValidateCatalogAcceptsPendingArtifactID(t *testing.T) {
	catalog := testCatalog()
	catalog.SupplementalArtifacts = append(catalog.SupplementalArtifacts, Artifact{
		ID:       "cache-image",
		Name:     "cache",
		Type:     ArtifactTypeImage,
		Registry: "staging",
		Version:  "1.2.3",
	})
	catalog.PublicationPending = []string{"cache-image"}

	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("ValidateCatalog rejected pending artifact ID: %v", err)
	}
}

func docsVersionSyncRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "docs", "version-catalog", "main.yaml")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repository root above %s", dir)
		}
		dir = parent
	}
}
