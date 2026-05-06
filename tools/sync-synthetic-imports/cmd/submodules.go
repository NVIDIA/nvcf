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

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	gitLabHTTPSPrefix = "https://github.com/NVIDIA/"
	gitLabSSHPrefix   = "git@github.com/NVIDIA:"
)

var gitLabInsteadOfConfig = "url." + gitLabSSHPrefix + ".insteadOf=" + gitLabHTTPSPrefix

func gitmodulesPath(repoDir string) string {
	return filepath.Join(repoDir, ".gitmodules")
}

func submodulePaths(repoDir string) ([]string, error) {
	b, err := os.ReadFile(gitmodulesPath(repoDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", gitmodulesPath(repoDir), err)
	}

	var paths []string
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "[") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "path" {
			continue
		}
		path := filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))
		if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("invalid submodule path %q in %s", strings.TrimSpace(value), gitmodulesPath(repoDir))
		}
		paths = append(paths, filepath.ToSlash(path))
	}
	return paths, nil
}

func dirHasMaterializedContent(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		return true, nil
	}
	return false, nil
}

func importHasMaterializedSubmodules(importDir string) (bool, error) {
	paths, err := submodulePaths(importDir)
	if err != nil {
		return false, err
	}
	for _, submodulePath := range paths {
		hasContent, err := dirHasMaterializedContent(filepath.Join(importDir, filepath.FromSlash(submodulePath)))
		if err != nil {
			return false, fmt.Errorf("inspect submodule path %s: %w", submodulePath, err)
		}
		if !hasContent {
			return false, nil
		}
	}
	return true, nil
}

func runGitInDirWithConfig(opts syncOptions, dir string, config string, args ...string) error {
	gitArgs := []string{"-C", dir}
	if config != "" {
		gitArgs = append(gitArgs, "-c", config)
	}
	gitArgs = append(gitArgs, args...)
	return runGit(opts, gitArgs...)
}

func materializeSubmodules(opts syncOptions, importPath, cloneDir string, hasGitLFS bool) error {
	paths, err := submodulePaths(cloneDir)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}

	for _, submodulePath := range paths {
		fmt.Fprintf(os.Stderr, "  submodule %s -> %s\n", importPath, submodulePath)
	}
	updateArgs := []string{"submodule", "update", "--init", "--recursive"}
	if !opts.verbose {
		updateArgs = append(updateArgs, "--quiet")
	}
	if err := runGitInDirWithConfig(opts, cloneDir, gitLabInsteadOfConfig, updateArgs...); err != nil {
		return fmt.Errorf("git submodule update in clone %s: %w", cloneDir, err)
	}
	if !hasGitLFS {
		return nil
	}
	if err := runGitInDirWithConfig(opts, cloneDir, gitLabInsteadOfConfig, "submodule", "foreach", "--recursive", "git lfs pull"); err != nil {
		return fmt.Errorf("git lfs pull in submodules under %s: %w", cloneDir, err)
	}
	return nil
}
