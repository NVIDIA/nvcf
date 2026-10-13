// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUmbrellaDocsRevisionE2E(t *testing.T) {
	root, catalog, legacyCommit := editionGitFixture(t)
	if err := registerEdition(root, editionRelease{Version: "1.0.0", Ref: "docs/releases/1.0.0", Commit: legacyCommit}, true); err != nil {
		t.Fatal(err)
	}
	manifest := umbrellaTestManifest()
	manifest.SourceCommit = legacyCommit
	manifest.Status = ReleaseSetQualified
	manifest.Qualification = catalog.DocsEdition.Qualification
	for _, name := range releaseSetStackNames {
		old, _ := catalog.ReleaseSet.Stacks.byName(name)
		stack := manifest.Stacks[name]
		stack.SourceCommit = old.SourceCommit
		manifest.Stacks[name] = stack
	}
	data, err := marshalUmbrellaManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, umbrellaManifestPath(manifest.Version)), string(data))
	editionGit(t, root, "add", ".")
	editionGit(t, root, "commit", "-m", "qualified product")
	product := editionGit(t, root, "rev-parse", "HEAD")
	editionGit(t, root, "tag", umbrellaTag(manifest.Version))
	binary := filepath.Join(t.TempDir(), "docs-version-sync")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	invoke := func(want string, args ...string) {
		t.Helper()
		c := exec.Command(binary, args...)
		c.Dir = t.TempDir()
		output, err := c.CombinedOutput()
		if want == "" && err != nil {
			t.Fatalf("command: %v\n%s", err, output)
		}
		if want != "" && (err == nil || !strings.Contains(string(output), want)) {
			t.Fatalf("want %s, got %v\n%s", want, err, output)
		}
	}
	common := []string{"--repo", root, "--version", manifest.Version, "--product-commit", product, "--manifest-sha256", umbrellaDigest(data)}
	prep := filepath.Join(t.TempDir(), "prepared")
	args := append([]string{"umbrella", "docs-prepare"}, common...)
	args = append(args, "--commit", legacyCommit, "--out", prep)
	invoke("", args...)
	editionGit(t, prep, "config", "user.name", "Fixture")
	editionGit(t, prep, "config", "user.email", "fixture@example.com")
	commitDocs := func(text string) string {
		t.Helper()
		writeFile(t, filepath.Join(prep, "docs/page.md"), text)
		editionGit(t, prep, "add", ".")
		editionGit(t, prep, "commit", "-m", "docs correction")
		commit := editionGit(t, prep, "rev-parse", "HEAD")
		editionGit(t, prep, "push", "origin", "HEAD:refs/heads/docs/versions/"+manifest.Version)
		return commit
	}
	first := commitDocs("# Initial product docs\n")
	revise := func(want, commit string) {
		t.Helper()
		args := append([]string{"umbrella", "docs-revise"}, common...)
		invoke(want, append(args, "--commit", commit)...)
	}
	revise("", first)
	second := commitDocs("# Corrected product docs\n")
	revise("", second)
	// The selector remains the same; registration does not activate it or change default.
	registry, err := readEditionRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if registry.Default != "1.0.0" || len(registry.Editions) != 2 {
		t.Fatalf("legacy default or entries changed: %+v", registry)
	}
	release := registry.Editions[1]
	if release.Commit != second || len(release.Umbrella.Revisions) != 2 || release.Umbrella.ManifestSHA256 != umbrellaDigest(data) {
		t.Fatal("wrong revision history or product binding")
	}
	writeFile(t, filepath.Join(root, "fern/docs.yml"), "versions:\n  - display-name: '1.0.0'\n    ref: docs/releases/1.0.0\n  - display-name: '1.0.3'\n    slug: '1.0.3'\n    ref: docs/versions/1.0.3\n")
	_ = commitDocs("# Unapproved next correction\n")
	if err := validateEditionRegistry(root, true); err != nil {
		t.Fatalf("advanced branch invalidated approved revision: %v", err)
	}
	staged := filepath.Join(t.TempDir(), "approved")
	if err := stageEditionSource(root, staged, release.Commit); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(staged, "docs/page.md"))
	if err != nil || !bytes.Contains(content, []byte("Corrected product")) || bytes.Contains(content, []byte("Unapproved")) {
		t.Fatal("did not stage approved revision")
	}
	before := editionGit(t, root, "tag", "--list")
	revise("", first)
	if err := validateEditionRegistry(root, true); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if before != editionGit(t, root, "tag", "--list") {
		t.Fatal("docs correction changed product tags")
	}
	changed, err := LoadCatalog(filepath.Join(prep, "docs/version-catalog/main.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	changed.Artifacts[0].Version = "99.0.0"
	if err := WriteCatalog(filepath.Join(prep, "docs/version-catalog/main.yaml"), changed); err != nil {
		t.Fatal(err)
	}
	bad := commitDocs("# Attempted product mutation\n")
	revise("immutable product catalog", bad)
	// Restore the catalog, then stale generated output must also fail.
	editionGit(t, prep, "checkout", second, "--", "docs/version-catalog/main.yaml")
	writeFile(t, filepath.Join(prep, "docs/edition-manifest.json"), "{}\n")
	stale := commitDocs("# Stale generated manifest\n")
	revise("does not match", stale)
	revise("missing", strings.Repeat("e", 40))
	// A maintained source never relaxes legacy ref checks.
	editionGit(t, root, "branch", "-f", "docs/releases/1.0.0", product)
	if err := validateEditionRegistry(root, true); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("accepted moved legacy branch: %v", err)
	}
}
