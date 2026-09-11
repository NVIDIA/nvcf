// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if want := "artifacts-" + catalog.Stack.PublicationVersion + ".txt"; catalog.Stack.ArtifactsFile != want {
		t.Errorf("stack artifacts_file = %q, want %q for publication version %s", catalog.Stack.ArtifactsFile, want, catalog.Stack.PublicationVersion)
	}
	if err := validateStackSourceSnapshot(root, catalog); err != nil {
		t.Fatal(err)
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

func TestCatalogRefreshPreservesSourceSnapshotAcrossPublicationVersion(t *testing.T) {
	base := testCatalog()
	base.Stack.SourceVersion = "0.14.5"
	base.Stack.SourceTag = "deploy/stacks/self-managed/v" + base.Stack.SourceVersion
	base.Stack.SourceCommit = "1111111111111111111111111111111111111111"
	base.Stack.PinSources = []string{"stack.yaml"}
	base.Stack.PinSourceDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	sameRelease := BuildCatalogFromArtifactsWithBase(base.Stack.PublicationVersion, nil, base)
	if sameRelease.Stack.SourceTag != base.Stack.SourceTag ||
		sameRelease.Stack.SourceVersion != base.Stack.SourceVersion ||
		sameRelease.Stack.SourceCommit != base.Stack.SourceCommit ||
		strings.Join(sameRelease.Stack.PinSources, ",") != "stack.yaml" ||
		sameRelease.Stack.PinSourceDigest != base.Stack.PinSourceDigest {
		t.Fatalf("same-release snapshot was not preserved: %#v", sameRelease.Stack)
	}

	newPublication := BuildCatalogFromArtifactsWithBase("0.9.2", nil, base)
	if newPublication.Stack.PublicationVersion != "0.9.2" {
		t.Fatalf("publication version = %q, want 0.9.2", newPublication.Stack.PublicationVersion)
	}
	if newPublication.Stack.SourceVersion != base.Stack.SourceVersion ||
		newPublication.Stack.SourceTag != base.Stack.SourceTag ||
		newPublication.Stack.SourceCommit != base.Stack.SourceCommit ||
		strings.Join(newPublication.Stack.PinSources, ",") != "stack.yaml" ||
		newPublication.Stack.PinSourceDigest != base.Stack.PinSourceDigest {
		t.Fatalf("publication refresh did not preserve source snapshot: %#v", newPublication.Stack)
	}
	if err := ValidateCatalog(newPublication); err != nil {
		t.Fatalf("ValidateCatalog rejected independent source and publication versions: %v", err)
	}
}

func TestValidateCatalogRejectsIncompleteStackSourceSnapshot(t *testing.T) {
	catalog := testCatalog()
	catalog.Stack.SourceCommit = "1111111111111111111111111111111111111111"

	err := ValidateCatalog(catalog)
	if err == nil || !strings.Contains(err.Error(), "source_version, source_tag, source_commit, pin_sources, and pin_source_digest together") {
		t.Fatalf("ValidateCatalog error = %v, want incomplete stack source snapshot", err)
	}
}

func TestValidateCatalogRejectsInvalidStackSourceSnapshot(t *testing.T) {
	const sourceVersion = "0.14.5"

	tests := []struct {
		name   string
		mutate func(*StackMetadata)
		want   string
	}{
		{
			name: "tag for another release",
			mutate: func(stack *StackMetadata) {
				stack.SourceTag = "deploy/stacks/self-managed/v9.9.9"
			},
			want: "source_tag must be deploy/stacks/self-managed/v" + sourceVersion,
		},
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
			catalog.Stack.SourceVersion = sourceVersion
			catalog.Stack.SourceTag = "deploy/stacks/self-managed/v" + catalog.Stack.SourceVersion
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
	catalog.Stack.SourceVersion = catalog.Stack.PublicationVersion
	catalog.Stack.SourceTag = "deploy/stacks/self-managed/v" + catalog.Stack.SourceVersion
	catalog.Stack.SourceCommit = "1111111111111111111111111111111111111111"
	catalog.Stack.PinSources = []string{"deploy/stack.yaml", "deploy/env.yaml"}
	catalog.Stack.PinSourceDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("ValidateCatalog rejected exact pin source paths: %v", err)
	}
}

func TestValidateStackSourceSnapshotReadsRecordedCommit(t *testing.T) {
	repo := initTestGitRepo(t)

	const sourcePath = "stack.yaml"
	writeFile(t, filepath.Join(repo, sourcePath), "version: 1.2.3\n")
	if _, err := gitOutput(repo, "add", sourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(repo, "commit", "-m", "test fixture"); err != nil {
		t.Fatal(err)
	}
	commitBytes, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := trimOutput(commitBytes)
	const sourceTag = "deploy/stacks/self-managed/v1.2.3"
	if _, err := gitOutput(repo, "tag", sourceTag); err != nil {
		t.Fatal(err)
	}

	pins := []effectiveStackPin{{artifact: "chart", artifactType: ArtifactTypeChart, path: sourcePath, pattern: `(?m)^version: ([^\s]+)$`}}
	digest, err := pinSourceDigest(map[string][]byte{sourcePath: []byte("version: 1.2.3\n")}, pins)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{
		Stack: StackMetadata{
			PublicationVersion: "9.9.9",
			SourceVersion:      "1.2.3",
			SourceTag:          sourceTag,
			SourceCommit:       commit,
			PinSources:         []string{sourcePath},
			PinSourceDigest:    digest,
		},
		Artifacts: []Artifact{
			{Name: "chart", Type: ArtifactTypeImage, Version: "9.9.9"},
			{Name: "chart", Type: ArtifactTypeChart, Version: "1.2.3"},
		},
	}

	writeFile(t, filepath.Join(repo, sourcePath), "version: 9.9.9\n")
	if err := validateStackSourceSnapshotWithPins(repo, catalog, pins); err != nil {
		t.Fatalf("working-tree mutation changed the recorded release snapshot: %v", err)
	}
}

func TestValidateStackSourceSnapshotRequiresTagRef(t *testing.T) {
	repo := initTestGitRepo(t)

	const sourcePath = "stack.yaml"
	writeFile(t, filepath.Join(repo, sourcePath), "version: 1.2.3\n")
	if _, err := gitOutput(repo, "add", sourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(repo, "commit", "-m", "test fixture"); err != nil {
		t.Fatal(err)
	}
	commitBytes, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := trimOutput(commitBytes)
	const sourceTag = "deploy/stacks/self-managed/v1.2.3"
	if _, err := gitOutput(repo, "branch", sourceTag); err != nil {
		t.Fatal(err)
	}

	catalog := &Catalog{
		Stack: StackMetadata{
			PublicationVersion: "9.9.9",
			SourceVersion:      "1.2.3",
			SourceTag:          sourceTag,
			SourceCommit:       commit,
			PinSources:         []string{sourcePath},
			PinSourceDigest:    "sha256:" + strings.Repeat("0", 64),
		},
	}
	pins := []effectiveStackPin{{artifact: "chart", artifactType: ArtifactTypeChart, path: sourcePath, pattern: `(?m)^version: ([^\s]+)$`}}

	err = validateStackSourceSnapshotWithPins(repo, catalog, pins)
	if err == nil || !strings.Contains(err.Error(), "resolve stack source tag") {
		t.Fatalf("validation error = %v, want missing tag rejection", err)
	}
}

func TestWriteCatalogAfterStackSourceValidationLeavesFileUnchanged(t *testing.T) {
	repo := initTestGitRepo(t)

	catalogPath := filepath.Join(repo, "catalog.yaml")
	const original = "original catalog\n"
	writeFile(t, catalogPath, original)
	catalog := testCatalog()
	catalog.Stack.SourceVersion = catalog.Stack.PublicationVersion
	catalog.Stack.SourceTag = "deploy/stacks/self-managed/v" + catalog.Stack.SourceVersion
	catalog.Stack.SourceCommit = strings.Repeat("1", 40)
	catalog.Stack.PinSources = []string{"stack.yaml"}
	catalog.Stack.PinSourceDigest = "sha256:" + strings.Repeat("2", 64)

	err := writeCatalogAfterStackSourceValidation(repo, catalogPath, catalog)
	if err == nil || !strings.Contains(err.Error(), "validate stack source snapshot") {
		t.Fatalf("write error = %v, want source validation failure", err)
	}
	body, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("catalog changed after validation failure:\n%s", body)
	}
}

func TestWriteCatalogAfterStackSourceValidationRequiresSnapshot(t *testing.T) {
	repo := initTestGitRepo(t)
	catalogPath := filepath.Join(repo, "catalog.yaml")
	const original = "original catalog\n"
	writeFile(t, catalogPath, original)

	err := writeCatalogAfterStackSourceValidation(repo, catalogPath, testCatalog())
	if err == nil || !strings.Contains(err.Error(), "without an immutable source snapshot") {
		t.Fatalf("write error = %v, want missing snapshot rejection", err)
	}
	body, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("catalog changed without a source snapshot:\n%s", body)
	}
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if _, err := gitOutput(repo, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(repo, "config", "user.email", "docs-version-sync@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(repo, "config", "user.name", "Docs Version Sync Test"); err != nil {
		t.Fatal(err)
	}
	return repo
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
