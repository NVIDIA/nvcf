// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// stackSourceSnapshot contains repository inputs read from one immutable stack release.
type stackSourceSnapshot struct {
	Release stackSourceRelease
	Files   map[string][]byte
}

// resolveStackSourceSnapshot selects a GitHub stack release and loads its requested repository inputs.
func (client *githubClient) resolveStackSourceSnapshot(repoRoot, sourceRef string, sourcePaths []string) (stackSourceSnapshot, error) {
	release, err := client.resolveStackSourceRelease(sourceRef)
	if err != nil {
		return stackSourceSnapshot{}, err
	}
	return loadStackSourceSnapshot(repoRoot, release, sourcePaths)
}

// loadStackSourceSnapshot verifies a release tag and reads requested files from its immutable commit.
func loadStackSourceSnapshot(repoRoot string, release stackSourceRelease, sourcePaths []string) (stackSourceSnapshot, error) {
	if err := validateStackSourceRelease(release); err != nil {
		return stackSourceSnapshot{}, err
	}
	paths, err := normalizeStackSourcePaths(sourcePaths)
	if err != nil {
		return stackSourceSnapshot{}, err
	}

	tagRef := "refs/tags/" + release.Tag
	commit, err := gitOutput(repoRoot, "rev-parse", "--verify", tagRef+"^{commit}")
	if err != nil {
		return stackSourceSnapshot{}, fmt.Errorf("resolve stack source tag %s to a commit: %w", release.Tag, err)
	}
	resolvedCommit := trimOutput(commit)
	if resolvedCommit != release.Commit {
		return stackSourceSnapshot{}, fmt.Errorf("stack source tag %s resolves to %s, want selected commit %s", release.Tag, resolvedCommit, release.Commit)
	}

	files := make(map[string][]byte, len(paths))
	for _, sourcePath := range paths {
		body, err := gitOutput(repoRoot, "cat-file", "blob", release.Commit+":"+sourcePath)
		if err != nil {
			return stackSourceSnapshot{}, fmt.Errorf("read %s from stack source commit %s: %w", sourcePath, release.Commit, err)
		}
		files[sourcePath] = body
	}
	return stackSourceSnapshot{Release: release, Files: files}, nil
}

// validateStackSourceRelease verifies the canonical identity of a selected stack release.
func validateStackSourceRelease(release stackSourceRelease) error {
	spec, err := stackInventorySpecByTag(release.Tag)
	if err != nil {
		return err
	}
	return validateStackSourceReleaseForSpec(release, spec)
}

func validateStackSourceReleaseForSpec(release stackSourceRelease, spec stackInventorySpec) error {
	if !validStackVersion(release.Version) {
		return fmt.Errorf("stack source version %q is not a semantic version", release.Version)
	}
	wantTag := spec.TagPrefix + release.Version
	if release.Tag != wantTag {
		return fmt.Errorf("stack source tag must be %s for source version %s", wantTag, release.Version)
	}
	if !fullLowercaseCommitSHARe.MatchString(release.Commit) {
		return fmt.Errorf("stack source commit must be a full lowercase commit SHA")
	}
	return nil
}

// normalizeStackSourcePaths validates, deduplicates, and orders repository-relative source paths.
func normalizeStackSourcePaths(sourcePaths []string) ([]string, error) {
	if len(sourcePaths) == 0 {
		return nil, fmt.Errorf("stack source paths cannot be empty")
	}

	paths := append([]string(nil), sourcePaths...)
	sort.Strings(paths)
	for i, sourcePath := range paths {
		if sourcePath == "" || strings.TrimSpace(sourcePath) != sourcePath || path.IsAbs(sourcePath) || path.Clean(sourcePath) != sourcePath || sourcePath == "." || strings.HasPrefix(sourcePath, "../") {
			return nil, fmt.Errorf("stack source path %q must be a clean repository-relative path", sourcePath)
		}
		if i > 0 && sourcePath == paths[i-1] {
			return nil, fmt.Errorf("duplicate stack source path %s", sourcePath)
		}
	}
	return paths, nil
}
