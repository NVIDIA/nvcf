// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTagPrefix(t *testing.T) {
	cases := []struct {
		tag, prefix, wantVer string
		ok                   bool
	}{
		{"nvcf-grpc-proxy-v1.2.3", "nvcf-grpc-proxy-v", "1.2.3", true},
		{"v3.0.2", "v", "3.0.2", true},           // upstream nvca legacy line
		{"nvca-v3.1.0", "nvca-v", "3.1.0", true}, // native umbrella line
		{"v3.0.0-rc.1", "v", "3.0.0-rc.1", true}, // prerelease
		{"3.0.2", "", "3.0.2", true},             // bare version (merge sort path)
		{"nvca-v3.1.0", "v", "", false},          // prefix mismatch (n != v)
		{"v3.0", "v", "", false},                 // not MAJOR.MINOR.PATCH
		{"vanity", "v", "", false},               // non-version tag
	}
	for _, c := range cases {
		sv, ok := parseTag(c.tag, c.prefix)
		if ok != c.ok {
			t.Errorf("parseTag(%q,%q) ok=%v, want %v", c.tag, c.prefix, ok, c.ok)
			continue
		}
		if ok && sv.version != c.wantVer {
			t.Errorf("parseTag(%q,%q) version=%q, want %q", c.tag, c.prefix, sv.version, c.wantVer)
		}
	}
}

func TestSortReleasesMergesSources(t *testing.T) {
	// Simulate a frozen upstream line merged with a native umbrella line.
	rels := []release{
		{Version: "3.1.0"}, // native
		{Version: "3.0.2"}, // upstream
		{Version: "3.0.0"},
		{Version: "3.1.1"},
		{Version: "2.14.0"},
	}
	sortReleases(rels)
	want := []string{"2.14.0", "3.0.0", "3.0.2", "3.1.0", "3.1.1"}
	for i, w := range want {
		if rels[i].Version != w {
			t.Fatalf("sortReleases position %d = %q, want %q (got %v)", i, rels[i].Version, w,
				func() []string {
					out := make([]string, len(rels))
					for j, r := range rels {
						out[j] = r.Version
					}
					return out
				}())
		}
	}
}

// runGit runs git in dir with a deterministic identity, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir,
		"-c", "user.email=t@t", "-c", "user.name=t",
		"-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestFetchGitHubTagsLabelsOrigin builds a "gitlab" repo and a "github" remote
// that shares the commit graph but carries one extra release tag, then asserts
// fetchGitHubTags brings that tag in and each release is labeled with the host
// that carries its tag (both / github).
func TestFetchGitHubTagsLabelsOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gl := t.TempDir()
	runGit(t, gl, "init", "-q", "-b", "main")
	commit := func(msg string) {
		if err := os.WriteFile(filepath.Join(gl, "f"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, gl, "add", "f")
		runGit(t, gl, "commit", "-q", "-m", msg)
	}
	commit("a")
	runGit(t, gl, "tag", "svc/v1.0.0")
	commit("b")
	runGit(t, gl, "tag", "svc/v1.1.0")
	commit("c") // on main, untagged in gitlab (the GitHub-only release commit)

	// The GitHub mirror: a clone of gitlab (shares a,b,c and the two tags) with
	// one extra release tag cut on the shared commit c.
	gh := t.TempDir()
	runGit(t, t.TempDir(), "clone", "-q", gl, gh)
	runGit(t, gh, "tag", "svc/v1.2.0", "main")

	gitlabTags := map[string]bool{}
	for _, tag := range []string{"svc/v1.0.0", "svc/v1.1.0"} {
		gitlabTags[tag] = true
	}
	githubTags, err := fetchGitHubTags(gl, gh)
	if err != nil {
		t.Fatalf("fetchGitHubTags: %v", err)
	}
	for _, want := range []string{"svc/v1.0.0", "svc/v1.1.0", "svc/v1.2.0"} {
		if !githubTags[want] {
			t.Errorf("github set missing %q (got %v)", want, githubTags)
		}
	}

	originFor := func(tag string) string {
		inGL, inGH := gitlabTags[tag], githubTags[tag]
		switch {
		case inGL && inGH:
			return "both"
		case inGH:
			return "github"
		default:
			return "gitlab"
		}
	}
	rels, err := buildReleases(gl, []string{"svc/v"}, "", "", originFor)
	if err != nil {
		t.Fatalf("buildReleases: %v", err)
	}
	want := map[string]string{"1.0.0": "both", "1.1.0": "both", "1.2.0": "github"}
	if len(rels) != len(want) {
		t.Fatalf("got %d releases, want %d: %v", len(rels), len(want), rels)
	}
	for _, r := range rels {
		if r.Origin != want[r.Version] {
			t.Errorf("release %s origin = %q, want %q", r.Version, r.Origin, want[r.Version])
		}
	}
}

func TestEnvOrDistinguishesEmptyFromUnset(t *testing.T) {
	const key = "CHANGELOG_ENVOR_TEST"
	os.Unsetenv(key)
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("unset: got %q, want fallback", got)
	}
	t.Setenv(key, "")
	if got := envOr(key, "fallback"); got != "" {
		t.Errorf("set empty must disable (return \"\"), got %q", got)
	}
	t.Setenv(key, "  value  ")
	if got := envOr(key, "fallback"); got != "value" {
		t.Errorf("set value must win (trimmed), got %q", got)
	}
}

func TestTagPrefixesForReleaseIncludesPathAndLegacyPrefixes(t *testing.T) {
	got := tagPrefixesForRelease(
		"src/invocation-plane-services/grpc-proxy",
		"",
		"nvcf-grpc-proxy-v",
	)
	want := []string{"src/invocation-plane-services/grpc-proxy/v", "nvcf-grpc-proxy-v"}
	if len(got) != len(want) {
		t.Fatalf("prefix len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prefix %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestBuildReleasesCollapsesDuplicateVersions covers the case that made the
// site's from/to pickers useless: a service carrying both the legacy flat tag
// prefix and the current path-scoped one has each release tagged twice, so the
// version list contained the same version consecutively. The UI keys those
// pickers by version and defaults them to the last two entries, so both
// resolved to the same index and rendered an empty diff.
//
// Built against a real repo rather than by calling dedupeByVersion directly:
// the ordering that makes the canonical tag win is a property of buildReleases
// (prefix order from tagPrefixesForRelease plus a stable sort), and a unit test
// on the helper alone would not notice that ordering breaking.
func TestBuildReleasesCollapsesDuplicateVersions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	commit := func(msg string) {
		if err := os.WriteFile(filepath.Join(repo, "f"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "f")
		runGit(t, repo, "commit", "-q", "-m", msg)
	}

	commit("a")
	runGit(t, repo, "tag", "svc/v1.0.0")
	runGit(t, repo, "tag", "legacy-v1.0.0") // same release, both schemes
	commit("b")
	runGit(t, repo, "tag", "svc/v1.1.0")
	commit("c")
	runGit(t, repo, "tag", "svc/v1.2.0")

	// Canonical prefix first, matching tagPrefixesForRelease.
	prefixes := []string{"svc/v", "legacy-v"}

	// Legacy tag on GitLab only, path-scoped tag on GitHub only: the merged
	// release is carried by both even though neither tag alone says so.
	originFor := func(tag string) string {
		if strings.HasPrefix(tag, "legacy-") {
			return "gitlab"
		}
		return "github"
	}

	rels, err := buildReleases(repo, prefixes, "", "", originFor)
	if err != nil {
		t.Fatalf("buildReleases: %v", err)
	}

	var versions []string
	for _, r := range rels {
		versions = append(versions, r.Version)
	}
	want := []string{"1.0.0", "1.1.0", "1.2.0"}
	if len(versions) != len(want) {
		t.Fatalf("versions = %v, want %v", versions, want)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("versions = %v, want %v", versions, want)
		}
	}

	// The surviving tag must be the path-scoped one, so commit ranges stay
	// anchored to the scheme in current use.
	if rels[0].Tag != "svc/v1.0.0" {
		t.Errorf("kept tag = %q, want the canonical svc/v1.0.0", rels[0].Tag)
	}
	if rels[0].Origin != "both" {
		t.Errorf("origin = %q, want %q (each host carried a different tag name)", rels[0].Origin, "both")
	}

	// The regression itself: the last two entries must differ, or the UI's
	// default from/to selection collapses to an empty range.
	last, prev := versions[len(versions)-1], versions[len(versions)-2]
	if last == prev {
		t.Errorf("last two versions are both %q; from/to pickers would collapse", last)
	}
}

// TestDedupeByVersionKeepsDistinctVersions guards the other direction: the
// collapse must not swallow genuinely different releases.
func TestDedupeByVersionKeepsDistinctVersions(t *testing.T) {
	in := []semver{
		{version: "1.0.0", tag: "svc/v1.0.0"},
		{version: "1.0.1", tag: "svc/v1.0.1"},
		{version: "1.1.0", tag: "svc/v1.1.0"},
	}
	out, aliases := dedupeByVersion(in)
	if len(out) != 3 {
		t.Fatalf("kept %d releases, want 3: %v", len(out), out)
	}
	if len(aliases) != 0 {
		t.Errorf("aliases = %v, want none", aliases)
	}
}

// TestMergeOriginCombinesHosts pins the labelling rule used when duplicate tags
// are folded together.
func TestMergeOriginCombinesHosts(t *testing.T) {
	for _, tc := range []struct{ a, b, want string }{
		{"gitlab", "gitlab", "gitlab"},
		{"github", "github", "github"},
		{"gitlab", "github", "both"},
		{"github", "gitlab", "both"},
		{"both", "gitlab", "both"},
	} {
		if got := mergeOrigin(tc.a, tc.b); got != tc.want {
			t.Errorf("mergeOrigin(%q,%q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// A network-facing git attempt must not be able to hang the run. Without a
// per-attempt deadline a stalled connection or a credential prompt blocks until
// the CI job times out an hour later, and the retry loop never gets to run.
func TestFetchGitHubTagsBoundsEachAttempt(t *testing.T) {
	if gitNetworkTimeout <= 0 {
		t.Fatal("gitNetworkTimeout must be positive; an unbounded git attempt can hang the whole run")
	}
	// Three attempts plus backoff has to leave room inside a CI job.
	worst := 3*gitNetworkTimeout + (0+2+4)*time.Second
	if worst > 30*time.Minute {
		t.Fatalf("worst-case retry budget %s is too large for a CI job", worst)
	}
}

// The tool is also run by hand, where the publishing job's GIT_TERMINAL_PROMPT
// export is absent. A prompt on a closed stdin is exactly the stall the deadline
// then has to clean up, so the command sets it itself.
func TestFetchGitHubTagsDisablesCredentialPrompt(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(src), `"GIT_TERMINAL_PROMPT=0"`) {
		t.Fatal("fetchGitHubTags must set GIT_TERMINAL_PROMPT=0 on each git command")
	}
	if !strings.Contains(string(src), "exec.CommandContext") {
		t.Fatal("fetchGitHubTags must use exec.CommandContext so each attempt is bounded")
	}
}
