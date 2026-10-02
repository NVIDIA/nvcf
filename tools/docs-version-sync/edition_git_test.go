// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editionGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	data, err := gitOutput(root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return trimOutput(data)
}

func editionGitFixture(t *testing.T) (string, *Catalog, string) {
	t.Helper()
	root := initTestGitRepo(t)
	writeFile(t, filepath.Join(root, "README.md"), "edition test repository\n")
	editionGit(t, root, "add", ".")
	editionGit(t, root, "commit", "-m", "source")
	source := editionGit(t, root, "rev-parse", "HEAD")
	editionGit(t, root, "remote", "add", "origin", root)
	catalog := editionTestCatalog()
	catalog.DocsEdition.Status = ReleaseSetQualified
	catalog.DocsEdition.Qualification = "https://github.com/NVIDIA/nvcf/pull/123"
	for _, name := range releaseSetStackNames {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		stack.SourceCommit = source
		editionGit(t, root, "tag", stack.SourceTag)
	}
	if err := WriteCatalog(filepath.Join(root, "docs", "version-catalog", "main.yaml"), catalog); err != nil {
		t.Fatal(err)
	}
	if err := syncEditionManifest(root, catalog, false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "docs", "page.md"), "# Edition fixture\n")
	writeFile(t, filepath.Join(root, "fern", "docs.yml"), "versions:\n  - display-name: '1.0.0'\n    path: navigation.yml\n")
	writeFile(t, filepath.Join(root, "fern", "navigation.yml"), "navigation:\n  - page: Fixture\n    path: ../docs/page.md\n")
	runner := filepath.Join(root, "tools", "ci", "run-fern")
	writeFile(t, runner, "#!/bin/sh\nset -eu\ntest \"$1\" = check\ntest -f docs/page.md\ntest -f docs/edition-manifest.json\n")
	if err := os.Chmod(runner, 0o755); err != nil {
		t.Fatal(err)
	}
	editionGit(t, root, "add", ".")
	editionGit(t, root, "commit", "-m", "qualified docs fixture")
	commit := editionGit(t, root, "rev-parse", "HEAD")
	editionGit(t, root, "branch", "docs/releases/1.0.0")
	return root, catalog, commit
}

func TestEditionPrepareIsIsolatedAndIdempotent(t *testing.T) {
	root, catalog, commit := editionGitFixture(t)
	out := filepath.Join(t.TempDir(), "candidate")
	plan := editionPreparation{Source: commit, Version: "1.0.0", Change: "initial", SelfManaged: catalog.ReleaseSet.Stacks.ControlPlane.Version, ComputePlane: catalog.ReleaseSet.Stacks.ComputePlane.Version, Observability: catalog.ReleaseSet.Stacks.Observability.Version, Navigation: "navigation.yml"}
	before := editionGit(t, root, "status", "--porcelain")
	if err := prepareEdition(root, out, plan); err != nil {
		t.Fatal(err)
	}
	staged, err := LoadCatalog(filepath.Join(out, "docs", "version-catalog", "main.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if staged.DocsEdition.Status != ReleaseSetDevelopment || staged.DocsEdition.Qualification != "" {
		t.Fatal("preparation inherited qualification")
	}
	if editionGit(t, root, "status", "--porcelain") != before {
		t.Fatal("preparation changed the source checkout")
	}
	if editionGit(t, out, "remote", "get-url", "origin") != root {
		t.Fatal("original remote was not retained")
	}
	if err := prepareEdition(root, out, plan); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	writeFile(t, filepath.Join(out, "docs", "page.md"), "edited after preparation\n")
	if err := prepareEdition(root, out, plan); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("modified preparation overwritten: %v", err)
	}
	if err := prepareEdition(root, filepath.Join(root, "generated"), plan); err == nil {
		t.Fatal("accepted in-repository snapshot directory")
	}
	plan.ComputePlane = "99.0.0"
	if err := prepareEdition(root, filepath.Join(t.TempDir(), "other"), plan); err == nil || !strings.Contains(err.Error(), "synchronize and review") {
		t.Fatalf("silently changed stack selection: %v", err)
	}
}

func TestEditionRegisterAndMovedRef(t *testing.T) {
	root, _, commit := editionGitFixture(t)
	release := editionRelease{Version: "1.0.0", Ref: "docs/releases/1.0.0", Commit: commit}
	if err := registerEdition(root, release, false); err != nil {
		t.Fatal(err)
	}
	if err := registerEdition(root, release, false); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if err := validateEditionRegistry(root, true); err != nil {
		t.Fatalf("remote ref rehearsal: %v", err)
	}
	changed := release
	changed.Commit = strings.Repeat("a", 40)
	if err := registerEdition(root, changed, false); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("registered version overwritten: %v", err)
	}
	editionGit(t, root, "branch", "-f", release.Ref, "HEAD~1")
	if _, err := verifyEditionRef(root, release, true); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("moved remote branch accepted: %v", err)
	}
	editionGit(t, root, "branch", "-D", release.Ref)
	if _, err := verifyEditionRef(root, release, true); err == nil {
		t.Fatal("deleted branch accepted")
	}
}

func TestEditionRefRejectsWrongDefaultAndStaleContent(t *testing.T) {
	for _, test := range []struct{ name, path, content, want string }{
		{"recursive default", "fern/docs.yml", "versions:\n  - display-name: old\n    ref: docs/releases/0.9.0\n", "default must use path"},
		{"stale manifest", "docs/edition-manifest.json", "{}\n", "does not match"},
		{"missing asset", "docs/page.md", "", "content validation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _, _ := editionGitFixture(t)
			if test.content == "" {
				if err := os.Remove(filepath.Join(root, test.path)); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, filepath.Join(root, test.path), test.content)
			}
			editionGit(t, root, "add", ".")
			editionGit(t, root, "commit", "-m", test.name)
			commit := editionGit(t, root, "rev-parse", "HEAD")
			editionGit(t, root, "branch", "-f", "docs/releases/1.0.0")
			_, err := verifyEditionRef(root, editionRelease{Version: "1.0.0", Ref: "docs/releases/1.0.0", Commit: commit}, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestEditionRegisterDocsOnlyPatch(t *testing.T) {
	root, catalog, commit := editionGitFixture(t)
	if err := registerEdition(root, editionRelease{Version: "1.0.0", Ref: "docs/releases/1.0.0", Commit: commit}, false); err != nil {
		t.Fatal(err)
	}
	catalog.DocsEdition.Version = "1.0.1"
	catalog.DocsEdition.PreviousVersion = "1.0.0"
	catalog.DocsEdition.Change = "patch"
	if err := WriteCatalog(filepath.Join(root, "docs", "version-catalog", "main.yaml"), catalog); err != nil {
		t.Fatal(err)
	}
	if err := syncEditionManifest(root, catalog, false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "fern", "docs.yml"), "versions:\n  - display-name: '1.0.1'\n    path: navigation.yml\n")
	editionGit(t, root, "add", "docs", "fern/docs.yml")
	editionGit(t, root, "commit", "-m", "docs patch")
	commit = editionGit(t, root, "rev-parse", "HEAD")
	editionGit(t, root, "branch", "docs/releases/1.0.1")
	if err := registerEdition(root, editionRelease{Version: "1.0.1", Ref: "docs/releases/1.0.1", Commit: commit}, false); err != nil {
		t.Fatal(err)
	}
	registry, err := readEditionRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if registry.Default != "1.0.1" || len(registry.Editions) != 2 {
		t.Fatalf("unexpected registry %+v", registry)
	}
	if err := validateEditionRegistry(root, true); err != nil {
		t.Fatal(err)
	}
}

func TestEditionRejectsLegacyFolderFreeze(t *testing.T) {
	root, _, _ := editionGitFixture(t)
	_, err := freezeStackDocumentation(root, filepath.Join(root, "docs", "version-catalog", "main.yaml"), releaseSetStackControlPlane, "1.2.3")
	if err == nil || !strings.Contains(err.Error(), "folder snapshots are disabled") {
		t.Fatalf("legacy snapshot path accepted edition catalog: %v", err)
	}
}
