// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Synthetic import sync for the nvcf umbrella repo.
//
// Reads imports.yaml, checks each upstream repo's remote HEAD (SSH URL derived from HTTPS),
// materializes nested git submodules, mirrors the default-branch tip into the
// manifest path recorded in imports.yaml, skips cloning when the pinned commit
// already matches remote HEAD and the local import directory is complete, rewrites
// commit SHAs for upstream-owned roots, and preserves monorepo-native roots.
//
// Requires: git, bash, tar on PATH. Assumes ssh-agent (or GIT_SSH_COMMAND) for GitLab SSH.
// If git-lfs is on PATH, runs "git lfs pull" in each clone so mirrored LFS files are real
// binaries, not pointer stubs (see README “Git LFS”).
//
// Build from repo root (-o is relative to -C dir, so use ../scripts/...):
//
//	go build -C tools/sync-synthetic-imports -ldflags="-s -w" -o ../scripts/sync-synthetic-imports ./cmd
//
// Run from repo root:
//
//	./tools/scripts/sync-synthetic-imports
//	./tools/scripts/sync-synthetic-imports --force
//	./tools/scripts/sync-synthetic-imports --verbose
//
// If the Go toolchain tries to download a newer release (e.g. “toolchain not available”
// offline), use GOTOOLCHAIN=local so the module’s `go` version matches your installed SDK.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type manifestFile struct {
	Imports []importEntry `yaml:"imports"`
}

type importEntry struct {
	Path                string `yaml:"path"`
	Repo                string `yaml:"repo,omitempty"`
	Commit              string `yaml:"commit,omitempty"`
	AuthoritativeSource string `yaml:"authoritative_source"`
	Notes               string `yaml:"notes,omitempty"`
}

type entryOut struct {
	Path, Repo, Commit, AuthoritativeSource, Notes string
}

type syncOptions struct {
	force   bool
	verbose bool
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		candidate := filepath.Join(dir, "imports.yaml")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("imports.yaml not found (started from %s)", wd)
		}
		dir = parent
	}
}

func httpsToSSH(httpsURL string) string {
	rest := strings.TrimSuffix(strings.TrimPrefix(httpsURL, gitLabHTTPSPrefix), ".git")
	return gitLabSSHPrefix + rest + ".git"
}

func importPath(currentPath string) string {
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(currentPath)))
}

func normalizeAuthoritativeSource(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "upstream", nil
	}
	switch value {
	case "upstream":
		return "upstream", nil
	case "native", "monorepo":
		return "native", nil
	default:
		return "", fmt.Errorf("invalid authoritative_source %q", raw)
	}
}

func isUpstreamAuthoritativeSource(raw string) (bool, error) {
	normalized, err := normalizeAuthoritativeSource(raw)
	if err != nil {
		return false, err
	}
	return normalized == "upstream", nil
}

var safeNameRe = regexp.MustCompile(`[^\w.-]+`)

func mirror(clone, dest string) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	script := fmt.Sprintf(`(cd %q && tar cf - --exclude .git .) | (cd %q && tar xf -)`, clone, dest)
	cmd := exec.Command("bash", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func gitOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func runGit(opts syncOptions, args ...string) error {
	cmd := exec.Command("git", args...)
	if opts.verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		printCondensedCommandOutput(out)
	}
	return err
}

func printCondensedCommandOutput(out []byte) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return
	}
	var important []string
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "fatal:") || strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "warning:") {
			important = append(important, line)
		}
	}
	if len(important) == 0 {
		fmt.Fprintln(os.Stderr, trimmed)
		return
	}
	for _, line := range important {
		fmt.Fprintln(os.Stderr, line)
	}
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func remoteHeadSHA(sshURL string) (string, error) {
	out, err := gitOutput("git", "ls-remote", sshURL, "HEAD")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("git ls-remote returned no HEAD for %s", sshURL)
	}
	return fields[0], nil
}

// syncOneEntry checks remote HEAD, skips cloning when the local mirror is already
// current, otherwise clones and mirrors into the manifest path under repoRoot.
// It returns the resolved manifest path plus whether a clone/mirror was performed.
func syncOneEntry(workDir string, idx int, e entryOut, repoRoot string, opts syncOptions) (sha, resolvedPath string, didClone bool, err error) {
	sshURL := httpsToSSH(e.Repo)
	currentDir := filepath.Join(repoRoot, filepath.FromSlash(e.Path))
	headSHA, err := remoteHeadSHA(sshURL)
	if err != nil {
		return "", "", false, err
	}
	currentReady := false
	if dirExists(currentDir) {
		currentReady, err = importHasMaterializedSubmodules(currentDir)
		if err != nil {
			return "", "", false, err
		}
	}
	if !opts.force && e.Commit != "" && headSHA == e.Commit && currentReady {
		canonical := importPath(e.Path)
		fmt.Fprintf(os.Stderr, "up-to-date %s @ %s; skipping clone\n", e.Path, headSHA)
		return headSHA, canonical, false, nil
	}
	if opts.force && dirExists(currentDir) {
		fmt.Fprintf(os.Stderr, "force reloading %s @ %s\n", e.Path, headSHA)
	}
	if e.Commit != "" && headSHA == e.Commit && dirExists(currentDir) && !currentReady {
		fmt.Fprintf(os.Stderr, "refreshing %s @ %s to materialize submodule contents\n", e.Path, headSHA)
	}

	cloneDir := filepath.Join(workDir, fmt.Sprintf("%d-%s", idx, safeNameRe.ReplaceAllString(e.Path, "_")))
	fmt.Fprintf(os.Stderr, "cloning %s <- %s\n", e.Path, sshURL)
	cloneArgs := []string{"clone", "--depth", "1"}
	if !opts.verbose {
		cloneArgs = append(cloneArgs, "--quiet")
	}
	cloneArgs = append(cloneArgs, sshURL, cloneDir)
	if err := runGit(opts, cloneArgs...); err != nil {
		return "", "", false, err
	}
	hasGitLFS := false
	if _, err := exec.LookPath("git-lfs"); err == nil {
		hasGitLFS = true
		if err := runGit(opts, "-C", cloneDir, "lfs", "pull"); err != nil {
			return "", "", false, fmt.Errorf("git lfs pull in clone %s: %w", cloneDir, err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "  warning: git-lfs not on PATH; %s may mirror LFS pointer files only\n", e.Path)
	}
	if err := materializeSubmodules(opts, e.Path, cloneDir, hasGitLFS); err != nil {
		return "", "", false, err
	}
	sha, err = gitOutput("git", "-C", cloneDir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", false, err
	}
	canonical := importPath(e.Path)
	dest := filepath.Join(repoRoot, filepath.FromSlash(canonical))
	if err := mirror(cloneDir, dest); err != nil {
		return "", "", false, err
	}
	return sha, canonical, true, nil
}

func loadManifest(path string) ([]entryOut, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mf manifestFile
	if err := yaml.Unmarshal(b, &mf); err != nil {
		return nil, err
	}
	var out []entryOut
	for _, row := range mf.Imports {
		auth, err := normalizeAuthoritativeSource(row.AuthoritativeSource)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", row.Path, err)
		}
		out = append(out, entryOut{
			Path:                row.Path,
			Repo:                row.Repo,
			Commit:              row.Commit,
			AuthoritativeSource: auth,
			Notes:               row.Notes,
		})
	}
	return out, nil
}

func writeManifest(repoRoot string, entries []entryOut) error {
	mf := manifestFile{Imports: make([]importEntry, len(entries))}
	for i, e := range entries {
		mf.Imports[i] = importEntry{
			Path:                e.Path,
			Repo:                e.Repo,
			Commit:              e.Commit,
			AuthoritativeSource: e.AuthoritativeSource,
			Notes:               e.Notes,
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&mf); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repoRoot, "imports.yaml"), buf.Bytes(), 0o644)
}

func cmdSyncFromManifest(repoRoot string, opts syncOptions) error {
	entriesIn, err := loadManifest(filepath.Join(repoRoot, "imports.yaml"))
	if err != nil {
		return err
	}
	workDir, err := os.MkdirTemp("", "nvcf-import-sync-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	var failures []string
	var clonedCount int
	var skippedCount int
	var entriesOut []entryOut
	for i, e := range entriesIn {
		upstreamSource, err := isUpstreamAuthoritativeSource(e.AuthoritativeSource)
		if err != nil {
			return err
		}
		if !upstreamSource {
			canonical := importPath(e.Path)
			fmt.Fprintf(os.Stderr, "native root %s authoritative_source=%s; skipping sync\n", canonical, e.AuthoritativeSource)
			skippedCount++
			entriesOut = append(entriesOut, entryOut{
				Path:                canonical,
				Repo:                e.Repo,
				Commit:              e.Commit,
				AuthoritativeSource: e.AuthoritativeSource,
				Notes:               e.Notes,
			})
			continue
		}

		sha, resolvedPath, didClone, err := syncOneEntry(workDir, i, e, repoRoot, opts)
		if err != nil {
			msg := fmt.Sprintf("%s (%s): %v", e.Path, e.Repo, err)
			fmt.Fprintf(os.Stderr, "FAIL %s\n", msg)
			failures = append(failures, msg)
			entriesOut = append(entriesOut, entryOut{
				Path:                e.Path,
				Repo:                e.Repo,
				Commit:              e.Commit,
				AuthoritativeSource: e.AuthoritativeSource,
				Notes:               e.Notes,
			})
			continue
		}
		if didClone {
			clonedCount++
		} else {
			skippedCount++
		}
		entriesOut = append(entriesOut, entryOut{
			Path:                resolvedPath,
			Repo:                e.Repo,
			Commit:              sha,
			AuthoritativeSource: e.AuthoritativeSource,
			Notes:               e.Notes,
		})
	}
	if err := writeManifest(repoRoot, entriesOut); err != nil {
		return err
	}
	ok := len(entriesIn) - len(failures)
	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "\nsync: %d succeeded (%d cloned, %d skipped), %d failed (manifest updated for all rows; failed paths keep previous commit).\n", ok, clonedCount, skippedCount, len(failures))
		return fmt.Errorf("sync: %d failure(s)", len(failures))
	}
	fmt.Fprintf(os.Stderr, "sync: %d succeeded (%d cloned, %d skipped).\n", ok, clonedCount, skippedCount)
	return nil
}

func parseArgs(args []string) (syncOptions, error) {
	var opts syncOptions
	fs := flag.NewFlagSet("sync-synthetic-imports", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&opts.force, "f", false, "reload every synthetic import even if it already matches remote HEAD")
	fs.BoolVar(&opts.force, "force", false, "reload every synthetic import even if it already matches remote HEAD")
	fs.BoolVar(&opts.verbose, "v", false, "stream full git command output")
	fs.BoolVar(&opts.verbose, "verbose", false, "stream full git command output")
	if err := fs.Parse(args); err != nil {
		return syncOptions{}, err
	}
	if fs.NArg() != 0 {
		return syncOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return opts, nil
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cmdSyncFromManifest(repoRoot, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
