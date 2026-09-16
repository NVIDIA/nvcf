// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStackSourceSnapshotReadsSelectedCommit(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", map[string]string{
		"deploy/stack.yaml":  "version: 1.2.3\n",
		"deploy/values.yaml": "image: example:4.5.6\n",
	})
	writeFile(t, filepath.Join(repo, "deploy", "stack.yaml"), "version: 9.9.9\n")
	if err := os.Remove(filepath.Join(repo, "deploy", "values.yaml")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := loadStackSourceSnapshot(repo, release, []string{"deploy/values.yaml", "deploy/stack.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Release != release {
		t.Fatalf("release = %#v, want %#v", snapshot.Release, release)
	}
	if got := string(snapshot.Files["deploy/stack.yaml"]); got != "version: 1.2.3\n" {
		t.Fatalf("stack input = %q, want committed content", got)
	}
	if got := string(snapshot.Files["deploy/values.yaml"]); got != "image: example:4.5.6\n" {
		t.Fatalf("values input = %q, want committed content", got)
	}
}

func TestResolveStackSourceSnapshotAcceptsExplicitSelector(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", map[string]string{"stack.yaml": "version: 1.2.3\n"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/repos/NVIDIA/nvcf/git/ref/tags/deploy/stacks/self-managed/v1.2.3"
		if r.URL.Path != wantPath {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if _, err := fmt.Fprintf(w, `{"ref":"refs/tags/%s","object":{"type":"commit","sha":"%s"}}`, release.Tag, release.Commit); err != nil {
			t.Errorf("write GitHub response: %v", err)
		}
	}))
	defer server.Close()

	snapshot, err := testGitHubClient(server, "").resolveStackSourceSnapshot(repo, "1.2.3", []string{"stack.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Release != release || string(snapshot.Files["stack.yaml"]) != "version: 1.2.3\n" {
		t.Fatalf("snapshot = %#v, want exact selected release input", snapshot)
	}
}

func TestLoadStackSourceSnapshotRejectsMissingFile(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", map[string]string{"stack.yaml": "version: 1.2.3\n"})

	_, err := loadStackSourceSnapshot(repo, release, []string{"missing.yaml"})
	want := "read missing.yaml from stack source commit " + release.Commit
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("loadStackSourceSnapshot error = %v, want %q", err, want)
	}
}

func TestLoadStackSourceSnapshotRejectsDirectory(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", map[string]string{"deploy/stack.yaml": "version: 1.2.3\n"})

	_, err := loadStackSourceSnapshot(repo, release, []string{"deploy"})
	want := "read deploy from stack source commit " + release.Commit
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("loadStackSourceSnapshot error = %v, want directory rejection %q", err, want)
	}
}

func TestLoadStackSourceSnapshotRejectsMismatchedTag(t *testing.T) {
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", map[string]string{"stack.yaml": "version: 1.2.3\n"})
	writeFile(t, filepath.Join(repo, "stack.yaml"), "version: 1.2.4\n")
	if _, err := gitOutput(repo, "add", "stack.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(repo, "commit", "-m", "newer fixture"); err != nil {
		t.Fatal(err)
	}
	commit, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	release.Commit = trimOutput(commit)

	_, err = loadStackSourceSnapshot(repo, release, []string{"stack.yaml"})
	if err == nil || !strings.Contains(err.Error(), "want selected commit "+release.Commit) {
		t.Fatalf("loadStackSourceSnapshot error = %v, want mismatched-tag rejection", err)
	}
}

func TestLoadStackSourceSnapshotRejectsNonCommitTag(t *testing.T) {
	repo := initTestGitRepo(t)
	writeFile(t, filepath.Join(repo, "stack.yaml"), "version: 1.2.3\n")
	blob, err := gitOutput(repo, "hash-object", "-w", "stack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const version = "1.2.3"
	if _, err := gitOutput(repo, "tag", stackTagPrefix+version, trimOutput(blob)); err != nil {
		t.Fatal(err)
	}
	release := stackSourceRelease{Version: version, Tag: stackTagPrefix + version, Commit: strings.Repeat("1", 40)}

	_, err = loadStackSourceSnapshot(repo, release, []string{"stack.yaml"})
	if err == nil || !strings.Contains(err.Error(), "resolve stack source tag "+release.Tag+" to a commit") {
		t.Fatalf("loadStackSourceSnapshot error = %v, want non-commit-tag rejection", err)
	}
}

func TestNormalizeStackSourcePathsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{name: "empty list", want: "paths cannot be empty"},
		{name: "absolute", paths: []string{"/stack.yaml"}, want: "clean repository-relative path"},
		{name: "parent traversal", paths: []string{"../stack.yaml"}, want: "clean repository-relative path"},
		{name: "unclean", paths: []string{"deploy/../stack.yaml"}, want: "clean repository-relative path"},
		{name: "duplicate", paths: []string{"stack.yaml", "stack.yaml"}, want: "duplicate stack source path stack.yaml"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeStackSourcePaths(test.paths)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalizeStackSourcePaths error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateStackSourceReleaseRejectsInvalidIdentity(t *testing.T) {
	valid := stackSourceRelease{
		Version: "1.2.3",
		Tag:     stackTagPrefix + "1.2.3",
		Commit:  strings.Repeat("a", 40),
	}
	tests := []struct {
		name    string
		mutate  func(*stackSourceRelease)
		wantErr string
	}{
		{
			name:    "invalid semantic version",
			mutate:  func(release *stackSourceRelease) { release.Version = "release-1.2.3" },
			wantErr: "is not a semantic version",
		},
		{
			name:    "non-canonical tag",
			mutate:  func(release *stackSourceRelease) { release.Tag = "v1.2.3" },
			wantErr: "does not match a released stack",
		},
		{
			name:    "malformed commit",
			mutate:  func(release *stackSourceRelease) { release.Commit = "abc123" },
			wantErr: "full lowercase commit SHA",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			release := valid
			test.mutate(&release)
			if err := validateStackSourceRelease(release); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateStackSourceRelease error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// commitTestStackSource writes and tags a stack source fixture.
func commitTestStackSource(t *testing.T, repo, version string, files map[string]string) stackSourceRelease {
	t.Helper()
	for name, body := range files {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(name)), body)
	}
	if _, err := gitOutput(repo, "add", "--all"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(repo, "commit", "-m", "stack source fixture"); err != nil {
		t.Fatal(err)
	}
	commit, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tag := stackTagPrefix + version
	if _, err := gitOutput(repo, "tag", tag); err != nil {
		t.Fatal(err)
	}
	return stackSourceRelease{Version: version, Tag: tag, Commit: trimOutput(commit)}
}
