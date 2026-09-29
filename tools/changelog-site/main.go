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

// Command changelog-site generates a static, browsable per-service changelog
// for the NVCF umbrella monorepo and publishes it via GitLab Pages.
//
// The monorepo MR log mixes every service together. This tool isolates each
// released service and renders an interactive version-diff browser.
//
// Two service sources are supported:
//
//   - Umbrella services: released from this monorepo with repo-relative
//     path tags (<service_path>/vX.Y.Z) and history under the service subtree.
//     Read from the umbrella checkout, path-scoped.
//   - Upstream services: released from a separate repo (e.g. nvca, whose
//     v3.0.x tags live in egx/intelligent-infra/nvca, not here). Declared via
//     a `changelog:` block in subproject-validations.yaml; the tool clones the
//     upstream repo and reads its tags + history directly.
//
// Usage:
//
//	go run -C tools/changelog-site . \
//	  --config ../ci/subproject-validations.yaml \
//	  --repo ../.. \
//	  --out public \
//	  --commit-base "$CI_PROJECT_URL/-/commit/"
//
// Output: <out>/changelog.json and <out>/index.html (the embedded UI).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"gopkg.in/yaml.v3"
)

//go:embed index.html
var indexHTML []byte

type configFile struct {
	Subprojects []configSubproject `yaml:"subprojects"`
}

type configSubproject struct {
	ID        string         `yaml:"id"`
	Path      string         `yaml:"path"`
	Release   *configRelease `yaml:"release"`
	Changelog *struct {
		UpstreamRepo string `yaml:"upstream_repo"`
		TagPrefix    string `yaml:"tag_prefix"` // upstream tag prefix, default "v"
		Path         string `yaml:"path"`       // optional subpath within the upstream repo
		// UmbrellaTagPrefix, when set, also reads native releases tagged
		// in the umbrella (e.g. "nvca-v"), path-scoped to the subtree, and
		// merges them with the frozen upstream history. Use for services
		// that were cut over to monorepo-native: the upstream line is
		// frozen and the umbrella line grows.
		UmbrellaTagPrefix string `yaml:"umbrella_tag_prefix"`
	} `yaml:"changelog"`
}

type configRelease struct {
	ServiceName     string `yaml:"service_name"`
	TagFormat       string `yaml:"tag_format"`
	LegacyTagPrefix string `yaml:"legacy_tag_prefix"`
	ArchiveRelease  *struct {
		Subtree string `yaml:"subtree"`
	} `yaml:"archive_release"`
}

type commit struct {
	SHA      string `json:"sha"`
	Short    string `json:"short"`
	URL      string `json:"url"` // full commit URL (per-commit so merged services link to the right repo)
	Date     string `json:"date"`
	Type     string `json:"type"`
	Scope    string `json:"scope"`
	Breaking bool   `json:"breaking"`
	Subject  string `json:"subject"`
}

type release struct {
	Version string   `json:"version"`
	Tag     string   `json:"tag"`
	Date    string   `json:"date"`
	Origin  string   `json:"origin"` // "gitlab" | "github" | "both": which host carries this release tag
	Commits []commit `json:"commits"`
}

type service struct {
	ID          string    `json:"id"`
	ServiceName string    `json:"service_name"`
	Path        string    `json:"path"`
	Source      string    `json:"source"` // "umbrella" | "upstream" | "upstream+native"
	Releases    []release `json:"releases"`
}

type output struct {
	GeneratedAt string    `json:"generated_at"`
	CommitBase  string    `json:"commit_base"`
	Services    []service `json:"services"`
}

// verRe parses the version portion of a tag after its prefix is stripped:
// X.Y.Z with an optional -prerelease suffix.
var verRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-(.+))?$`)

type semver struct {
	major, minor, patch int
	pre                 string // empty = release; non-empty sorts before release
	version             string // "X.Y.Z" or "X.Y.Z-pre"
	tag                 string
}

// parseTag strips prefix from tag and parses the remaining semver. Returns
// false for tags that do not match the prefix or are not valid versions.
func parseTag(tag, prefix string) (semver, bool) {
	if !strings.HasPrefix(tag, prefix) {
		return semver{}, false
	}
	m := verRe.FindStringSubmatch(tag[len(prefix):])
	if m == nil {
		return semver{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	ver := fmt.Sprintf("%d.%d.%d", maj, min, pat)
	if m[4] != "" {
		ver += "-" + m[4]
	}
	return semver{maj, min, pat, m[4], ver, tag}, true
}

func less(a, b semver) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	if a.patch != b.patch {
		return a.patch < b.patch
	}
	// A prerelease precedes its release (1.0.0-rc.1 < 1.0.0).
	if a.pre != b.pre {
		if a.pre == "" {
			return false
		}
		if b.pre == "" {
			return true
		}
		return a.pre < b.pre
	}
	return false
}

// ccRe parses a Conventional Commit subject: type(scope)!: description.
var ccRe = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^)]*)\))?(!)?:\s*(.*)$`)

func parseCommitSubject(subject string) (typ, scope string, breaking bool, desc string) {
	m := ccRe.FindStringSubmatch(subject)
	if m == nil {
		return "", "", false, subject
	}
	return strings.ToLower(m[1]), m[2], m[3] == "!", m[4]
}

func git(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// tagPrefixesForRelease returns the current release tag prefix plus any legacy
// prefix that should be retained for history continuity.
func tagPrefixesForRelease(releasePath, tagFormat, legacyPrefix string) []string {
	const placeholder = "${version}"
	tagFormat = strings.TrimSpace(tagFormat)
	var prefixes []string
	if tagFormat != "" && strings.HasSuffix(tagFormat, placeholder) {
		prefixes = append(prefixes, strings.TrimSuffix(tagFormat, placeholder))
	} else {
		prefix := strings.Trim(filepath.ToSlash(strings.TrimSpace(releasePath)), "/")
		if prefix == "" || prefix == "." {
			prefixes = append(prefixes, "v")
		} else {
			prefixes = append(prefixes, prefix+"/v")
		}
	}
	if legacyPrefix = strings.TrimSpace(legacyPrefix); legacyPrefix != "" {
		prefixes = append(prefixes, legacyPrefix)
	}
	return uniquePrefixes(prefixes)
}

func releasePathForSubproject(sp configSubproject) string {
	releasePath := sp.Path
	if sp.Release != nil && sp.Release.ArchiveRelease != nil {
		if subtree := strings.TrimSpace(sp.Release.ArchiveRelease.Subtree); subtree != "" {
			releasePath = subtree
		}
	}
	return filepath.ToSlash(releasePath)
}

func uniquePrefixes(prefixes []string) []string {
	seen := map[string]struct{}{}
	out := prefixes[:0]
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	return out
}

// buildReleases enumerates tags matching tagPrefixes in repoDir, sorts them, and
// builds one release per tag carrying the commits in (prevTag, thisTag],
// optionally scoped to pathFilter. The earliest tag carries no diff (the UI
// never needs commits "before the first release"). originFor labels each
// release with the host that carries its tag; when nil every release is
// "gitlab" (the default source for a plain clone).
func buildReleases(repoDir string, tagPrefixes []string, pathFilter, commitBase string, originFor func(tag string) string) ([]release, error) {
	var svs []semver
	for _, tagPrefix := range tagPrefixes {
		tagsOut, err := git(repoDir, "tag", "-l", tagPrefix+"*")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(tagsOut), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if sv, ok := parseTag(line, tagPrefix); ok {
				svs = append(svs, sv)
			}
		}
	}
	sort.SliceStable(svs, func(i, j int) bool { return less(svs[i], svs[j]) })

	svs, aliases := dedupeByVersion(svs)

	var releases []release
	for i, sv := range svs {
		origin := "gitlab"
		if originFor != nil {
			origin = originFor(sv.tag)
			// A release tagged under both the legacy and the path-scoped
			// prefix is still one release, and it may be carried by a
			// different host under each name. Fold the dropped tags in so it
			// is not reported as living on only one of them.
			for _, alias := range aliases[sv.version] {
				origin = mergeOrigin(origin, originFor(alias))
			}
		}
		rel := release{Version: sv.version, Tag: sv.tag, Origin: origin}
		if d, err := git(repoDir, "log", "-1", "--pretty=format:%cI", sv.tag); err == nil {
			rel.Date = strings.TrimSpace(d)
		}
		if i > 0 {
			args := []string{"log", "--no-merges", "--pretty=format:%H%x1f%cI%x1f%s", svs[i-1].tag + ".." + sv.tag}
			if pathFilter != "" {
				args = append(args, "--", pathFilter)
			}
			logOut, err := git(repoDir, args...)
			if err != nil {
				return nil, err
			}
			for _, line := range strings.Split(logOut, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				fields := strings.Split(line, "\x1f")
				if len(fields) != 3 {
					continue
				}
				typ, scope, breaking, desc := parseCommitSubject(fields[2])
				url := ""
				if commitBase != "" {
					url = commitBase + fields[0]
				}
				rel.Commits = append(rel.Commits, commit{
					SHA: fields[0], Short: shortSHA(fields[0]), URL: url, Date: fields[1],
					Type: typ, Scope: scope, Breaking: breaking, Subject: desc,
				})
			}
		}
		releases = append(releases, rel)
	}
	return releases, nil
}

// dedupeByVersion collapses tags that carry the same semantic version into a
// single entry, returning the surviving tags and, per version, the tag names
// that were dropped.
//
// A service can have two live tag prefixes for one release line: the legacy
// flat prefix (nvcf-<svc>-v) and the current path-scoped one
// (<path>/v). The same release is then tagged twice under different names, and
// emitting both produced two entries with an identical Version string. The UI
// keys its from/to pickers by version and defaults them to the last two
// entries, so when a service's two newest entries were the same release both
// pickers resolved to the same index and the diff came out empty. The page
// looked broken while the data behind it was merely duplicated.
//
// tagPrefixesForRelease returns the canonical prefix before the legacy one and
// the caller's sort is stable, so the first occurrence of a version is the
// canonical tag. Keeping it also keeps the commit ranges anchored to the tag
// scheme currently in use.
func dedupeByVersion(svs []semver) ([]semver, map[string][]string) {
	aliases := map[string][]string{}
	out := svs[:0:0]
	seen := map[string]struct{}{}
	for _, sv := range svs {
		if _, ok := seen[sv.version]; ok {
			aliases[sv.version] = append(aliases[sv.version], sv.tag)
			continue
		}
		seen[sv.version] = struct{}{}
		out = append(out, sv)
	}
	return out, aliases
}

// mergeOrigin combines two host labels for what is now known to be a single
// release. A release tagged on GitLab under one name and on GitHub under
// another is carried by both, even though neither tag alone says so.
func mergeOrigin(a, b string) string {
	if a == b {
		return a
	}
	return "both"
}

// sortReleases sorts a merged release list ascending by semantic version.
// Used when combining a frozen upstream line with native umbrella releases;
// the two version ranges do not overlap, so a stable version sort yields a
// continuous timeline.
func sortReleases(rels []release) {
	sort.SliceStable(rels, func(i, j int) bool {
		a, _ := parseTag(rels[i].Version, "")
		b, _ := parseTag(rels[j].Version, "")
		return less(a, b)
	})
}

// cloneUpstream makes a treeless full-history clone (commit graph + tags, no
// blobs) of repoURL into a temp dir. Uses CI_JOB_TOKEN or GITLAB_TOKEN for
// auth when present.
func cloneUpstream(repoURL string) (string, error) {
	dir, err := os.MkdirTemp("", "changelog-upstream-")
	if err != nil {
		return "", err
	}
	url := repoURL
	token := os.Getenv("CI_JOB_TOKEN")
	user := "gitlab-ci-token"
	if token == "" {
		token = os.Getenv("GITLAB_TOKEN")
		user = "oauth2"
	}
	if token != "" && strings.HasPrefix(url, "https://") {
		url = "https://" + user + ":" + token + "@" + strings.TrimPrefix(url, "https://")
	}
	cmd := exec.Command("git", "clone", "--filter=blob:none", "--no-checkout", "--quiet", url, dir)
	// Capture (do not live-forward) git's stderr: on auth failure git echoes
	// the credential-embedded remote URL, which would leak the token into CI
	// logs. Scrub the token before surfacing any of it.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		msg := strings.TrimSpace(stderr.String())
		if token != "" {
			msg = strings.ReplaceAll(msg, user+":"+token+"@", "")
			msg = strings.ReplaceAll(msg, token, "***")
		}
		return "", fmt.Errorf("clone %s: %w: %s", repoURL, err, msg)
	}
	return dir, nil
}

// commitBaseFromRepo turns a clone URL into a /-/commit/ base for linking.
func commitBaseFromRepo(repoURL string) string {
	u := strings.TrimSuffix(repoURL, ".git")
	u = strings.TrimSuffix(u, "/")
	return u + "/-/commit/"
}

// gitNetworkTimeout bounds a single network-facing git attempt. Generous enough
// for a full tag fetch on a large repository, short enough that three attempts
// plus backoff still finish well inside a CI job.
const gitNetworkTimeout = 2 * time.Minute

// fetchGitHubTags enumerates the tags on the GitHub mirror at base and fetches
// them into repoDir's refs/tags so their commits are reachable by tag name.
// Release tagging moved to GitHub and the GitLab->GitHub mirror is one-way, so
// GitHub-created tags never flow back into this checkout on their own. The
// fetch is forced (+refs/tags/*): a mirrored tag can carry a different
// tag-object SHA than its GitLab twin even on the same commit, and a non-forced
// fetch would abort on that; the target commit is identical so overwriting is
// safe. Origin labeling does not depend on which tag object wins, since it is
// computed from the pre-fetch GitLab set and this GitHub set. Returns the set
// of tag names present on GitHub so callers can label each release with its
// origin. GITHUB_TOKEN is used when set (the mirror is public, so it is
// optional and only lifts rate limits).
func fetchGitHubTags(repoDir, base string) (map[string]bool, error) {
	url := base
	token := os.Getenv("GITHUB_TOKEN")
	if token != "" && strings.HasPrefix(url, "https://") {
		url = "https://x-access-token:" + token + "@" + strings.TrimPrefix(url, "https://")
	}
	scrub := func(s string) string {
		s = strings.TrimSpace(s)
		if token != "" {
			s = strings.ReplaceAll(s, "x-access-token:"+token+"@", "")
			s = strings.ReplaceAll(s, token, "***")
		}
		return s
	}
	// Capture (do not live-forward) git's stderr: on auth failure git echoes
	// the credential-embedded remote URL, which would leak the token into CI
	// logs. Scrub it before surfacing anything.
	// Both calls below cross the network to GitHub. A single transient failure
	// used to drop the entire GitHub tag set, and because the caller only
	// warned, the site republished with every GitHub-only release missing --
	// services appeared and disappeared between half-hourly rebuilds. Retry
	// with a short backoff so a blip does not decide the contents of the site.
	// Both calls cross the network, so each attempt gets its own deadline.
	// Without one, a hung connection or a credential prompt blocks until the CI
	// job timeout an hour later, and the retry loop above never gets to run.
	// GIT_TERMINAL_PROMPT=0 is set here rather than relied on from the
	// environment: the publishing job does export it, but this tool is also run
	// by hand, and a prompt waiting on a closed stdin is the exact stall the
	// deadline then has to clean up.
	run := func(args ...string) (string, error) {
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
			}
			out, err := func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), gitNetworkTimeout)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoDir}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
				var stdout, stderr strings.Builder
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					if ctx.Err() == context.DeadlineExceeded {
						return "", fmt.Errorf("git %s: timed out after %s: %s", args[0], gitNetworkTimeout, scrub(stderr.String()))
					}
					return "", fmt.Errorf("git %s: %w: %s", args[0], err, scrub(stderr.String()))
				}
				return stdout.String(), nil
			}()
			if err != nil {
				lastErr = err
				continue
			}
			return out, nil
		}
		return "", lastErr
	}
	lsOut, err := run("ls-remote", "--tags", url)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(lsOut), "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, "refs/tags/")
		if i < 0 {
			continue
		}
		// Strip the annotated-tag peel suffix so foo^{} collapses onto foo.
		name := strings.TrimSuffix(line[i+len("refs/tags/"):], "^{}")
		if name != "" {
			set[name] = true
		}
	}
	// Bring the GitHub tag refs local so buildReleases can walk their commits.
	// Forced refspec (+): mirrored tags can carry a different tag-object SHA
	// than GitLab even on the same commit (annotated vs lightweight), which a
	// non-forced fetch rejects with a non-zero exit. The commit each tag points
	// to is identical, so overwriting is safe; origin labeling is computed from
	// the pre-fetch GitLab set and this GitHub set, not from which ref won.
	if _, err := run("fetch", "--quiet", url, "+refs/tags/*:refs/tags/*"); err != nil {
		return set, err
	}
	return set, nil
}

func main() {
	configPath := flag.String("config", "../ci/subproject-validations.yaml", "subproject-validations.yaml path")
	repo := flag.String("repo", "../..", "umbrella git checkout")
	out := flag.String("out", "public", "output directory")
	commitBase := flag.String("commit-base", "", "umbrella commit URL base, e.g. https://host/group/proj/-/commit/")
	githubRepo := flag.String("github-repo", envOr("NVCF_GITHUB_REMOTE", "https://github.com/NVIDIA/nvcf.git"),
		"GitHub mirror to also scan for release tags; empty disables the GitHub source")
	allowMissingGitHub := flag.Bool("allow-missing-github-tags", false,
		"publish even if the GitHub tag fetch fails, omitting every GitHub-only release")
	flag.Parse()

	raw, err := os.ReadFile(*configPath)
	if err != nil {
		fatal(err)
	}
	var cfg configFile
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		fatal(err)
	}

	// Merge in release tags created on the GitHub mirror. Release tagging moved
	// to GitHub and the GitLab->GitHub mirror is one-way, so those tags never
	// reach this checkout on their own. Record which tags exist on each host so
	// every release can be labeled gitlab / github / both for code audits.
	gitlabTags, githubTags := map[string]bool{}, map[string]bool{}
	if base := strings.TrimSpace(*githubRepo); base != "" {
		if before, err := git(*repo, "tag", "-l"); err == nil {
			for _, t := range strings.Split(strings.TrimSpace(before), "\n") {
				if t = strings.TrimSpace(t); t != "" {
					gitlabTags[t] = true
				}
			}
		}
		if set, err := fetchGitHubTags(*repo, base); err != nil {
			// Fail rather than degrade. Continuing here publishes a site with
			// every GitHub-only release silently missing, which overwrites a
			// good deployment with a worse one and reads as data loss rather
			// than as an outage. Failing leaves the previous deployment served.
			// Pass --allow-missing-github-tags to opt into the old behavior.
			if !*allowMissingGitHub {
				fatal(fmt.Errorf("github tags unavailable: %w (pass --allow-missing-github-tags to publish without them)", err))
			}
			fmt.Fprintf(os.Stderr, "changelog-site: github tags unavailable (%v)\n", err)
		} else {
			githubTags = set
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

	o := output{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		CommitBase:  *commitBase,
	}
	var cleanup []string
	defer func() {
		for _, d := range cleanup {
			os.RemoveAll(d)
		}
	}()

	for _, sp := range cfg.Subprojects {
		switch {
		case sp.Changelog != nil && strings.TrimSpace(sp.Changelog.UpstreamRepo) != "":
			// Dual-source service: a frozen upstream history (e.g. nvca's
			// v3.0.x line, only in the upstream repo) merged with native
			// releases tagged in the umbrella going forward. The
			// boundary release between the two repos carries no diff -- a
			// cross-repo git log is not possible -- which is the expected
			// limitation at a cutover point.
			repoURL := strings.TrimSpace(sp.Changelog.UpstreamRepo)
			prefix := strings.TrimSpace(sp.Changelog.TagPrefix)
			if prefix == "" {
				prefix = "v"
			}
			var releases []release
			source := "upstream"
			if dir, err := cloneUpstream(repoURL); err != nil {
				// Degrade gracefully: a missing upstream must not fail the
				// whole site build.
				fmt.Fprintf(os.Stderr, "changelog-site: %s upstream unavailable (%v)\n", sp.ID, err)
			} else {
				cleanup = append(cleanup, dir)
				if up, err := buildReleases(dir, []string{prefix}, sp.Changelog.Path, commitBaseFromRepo(repoURL), nil); err != nil {
					fmt.Fprintf(os.Stderr, "changelog-site: %s upstream history failed (%v)\n", sp.ID, err)
				} else {
					releases = append(releases, up...)
				}
			}
			nativePrefixes := []string{}
			if sp.Release != nil {
				nativePrefixes = append(nativePrefixes, tagPrefixesForRelease(releasePathForSubproject(sp), sp.Release.TagFormat, sp.Release.LegacyTagPrefix)...)
			}
			if utp := strings.TrimSpace(sp.Changelog.UmbrellaTagPrefix); utp != "" {
				nativePrefixes = append(nativePrefixes, utp)
			}
			if len(nativePrefixes) > 0 {
				// Degrade gracefully, matching the upstream path above: a git
				// error reading native tags must not abort the whole site.
				if um, err := buildReleases(*repo, uniquePrefixes(nativePrefixes), releasePathForSubproject(sp), *commitBase, originFor); err != nil {
					fmt.Fprintf(os.Stderr, "changelog-site: %s native releases failed (%v)\n", sp.ID, err)
				} else if len(um) > 0 {
					releases = append(releases, um...)
					source = "upstream+native"
				}
			}
			sortReleases(releases)
			o.Services = append(o.Services, service{
				ID:          sp.ID,
				ServiceName: sp.ID,
				Path:        repoURL,
				Source:      source,
				Releases:    releases,
			})
		case sp.Release != nil && strings.TrimSpace(sp.Release.ServiceName) != "" && sp.Path != "":
			// Umbrella-released service: tags + history in this repo,
			// path-scoped. Included even with zero releases so every
			// released service (including not-yet-tagged helm-* charts) is
			// listed.
			svcName := sp.Release.ServiceName
			releasePath := releasePathForSubproject(sp)
			releases, err := buildReleases(*repo, tagPrefixesForRelease(releasePath, sp.Release.TagFormat, sp.Release.LegacyTagPrefix), releasePath, *commitBase, originFor)
			if err != nil {
				fatal(err)
			}
			o.Services = append(o.Services, service{
				ID:          sp.ID,
				ServiceName: svcName,
				Path:        releasePath,
				Source:      "umbrella",
				Releases:    releases,
			})
		}
	}

	sort.SliceStable(o.Services, func(i, j int) bool { return o.Services[i].ID < o.Services[j].ID })

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "changelog.json"), data, 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "index.html"), indexHTML, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s/changelog.json (%d services) and index.html\n", *out, len(o.Services))
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// envOr returns the trimmed value of the environment variable key when it is
// set, and fallback only when it is unset. A variable set to an empty (or
// whitespace) value returns "", so callers can honor "set empty to disable"
// (e.g. NVCF_GITHUB_REMOTE="" disables the GitHub source). LookupEnv, not
// Getenv, is what distinguishes "set empty" from "unset".
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "changelog-site:", err)
	os.Exit(1)
}
