/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dsl

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// FilesContain recursively inspects regular files under root and fails if any
// required fixed string is absent. A non-empty directoryNamePattern limits the
// search to files below a directory whose name matches the shell
// pattern.
func FilesContain(root, directoryNamePattern string, needles []string) error {
	root = strings.TrimSpace(Interpolate(root))
	if root == "" {
		return fmt.Errorf("rendered manifests directory is empty")
	}
	if len(needles) == 0 {
		return fmt.Errorf("required text list is empty")
	}

	resolvedNeedles := make([]string, 0, len(needles))
	for _, rawNeedle := range needles {
		needle := Interpolate(rawNeedle)
		if needle == "" {
			return fmt.Errorf("required text is empty")
		}
		resolvedNeedles = append(resolvedNeedles, needle)
	}

	directoryNamePattern = strings.TrimSpace(Interpolate(directoryNamePattern))
	if strings.Contains(directoryNamePattern, "/") {
		return fmt.Errorf("rendered manifest directory name pattern %q must not contain a slash", directoryNamePattern)
	}
	if directoryNamePattern != "" {
		if _, err := path.Match(directoryNamePattern, "candidate"); err != nil {
			return fmt.Errorf("invalid rendered manifest directory name pattern %q: %w", directoryNamePattern, err)
		}
	}

	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect rendered manifests directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("rendered manifests path %q is not a directory", root)
	}

	found := make([]bool, len(resolvedNeedles))
	filesInspected := 0
	if err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect rendered manifest %q: %w", filePath, walkErr)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		matches, err := matchesDirectoryName(root, filePath, directoryNamePattern)
		if err != nil {
			return err
		}
		if !matches {
			return nil
		}
		filesInspected++
		body, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read rendered manifest %q: %w", filePath, err)
		}
		for index, needle := range resolvedNeedles {
			if !found[index] && bytes.Contains(body, []byte(needle)) {
				found[index] = true
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if filesInspected == 0 {
		if directoryNamePattern != "" {
			return fmt.Errorf("rendered manifests directory %q contains no regular files under directories matching %q", root, directoryNamePattern)
		}
		return fmt.Errorf("rendered manifests directory %q contains no regular files", root)
	}
	for index, matched := range found {
		if !matched {
			if directoryNamePattern != "" {
				return fmt.Errorf("rendered manifests in %q under directories matching %q do not contain required text %q", root, directoryNamePattern, resolvedNeedles[index])
			}
			return fmt.Errorf("rendered manifests in %q do not contain required text %q", root, resolvedNeedles[index])
		}
	}
	return nil
}

func matchesDirectoryName(root, filePath, pattern string) (bool, error) {
	if pattern == "" {
		return true, nil
	}
	relative, err := filepath.Rel(root, filePath)
	if err != nil {
		return false, fmt.Errorf("resolve rendered manifest path %q relative to %q: %w", filePath, root, err)
	}
	directory := filepath.Dir(relative)
	if directory == "." {
		return false, nil
	}
	for _, segment := range strings.Split(filepath.ToSlash(directory), "/") {
		matched, err := path.Match(pattern, segment)
		if err != nil {
			return false, fmt.Errorf("match rendered manifest directory name %q against %q: %w", segment, pattern, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

// FilesDoNotContain recursively inspects regular files under root and
// fails if any interpolated fixed string appears.
func FilesDoNotContain(root string, needles []string) error {
	root = strings.TrimSpace(Interpolate(root))
	if root == "" {
		return fmt.Errorf("rendered manifests directory is empty")
	}
	if len(needles) == 0 {
		return fmt.Errorf("excluded text list is empty")
	}

	resolvedNeedles := make([]string, 0, len(needles))
	for _, rawNeedle := range needles {
		needle := Interpolate(rawNeedle)
		if needle == "" {
			return fmt.Errorf("excluded text is empty")
		}
		resolvedNeedles = append(resolvedNeedles, needle)
	}

	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect rendered manifests directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("rendered manifests path %q is not a directory", root)
	}

	filesInspected := 0
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect rendered manifest %q: %w", path, walkErr)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		filesInspected++
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read rendered manifest %q: %w", path, err)
		}
		for _, needle := range resolvedNeedles {
			if bytes.Contains(body, []byte(needle)) {
				return fmt.Errorf("rendered manifest %q contains excluded text %q", path, needle)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if filesInspected == 0 {
		return fmt.Errorf("rendered manifests directory %q contains no regular files", root)
	}
	return nil
}

// OutputMatches reports whether the interpolated regular expression
// matches anywhere in text.
func OutputMatches(text, pattern string) (bool, error) {
	expression, err := compileOutputPattern(pattern)
	if err != nil {
		return false, err
	}
	return expression.MatchString(text), nil
}

// DistinctOutputMatches counts the unique substrings of text matched by
// the interpolated regular expression. Repeated occurrences of the same
// substring count once, so a feature can assert how many distinct
// identities an observation advertises.
func DistinctOutputMatches(text, pattern string) (int, error) {
	expression, err := compileOutputPattern(pattern)
	if err != nil {
		return 0, err
	}
	unique := make(map[string]struct{})
	for _, match := range expression.FindAllString(text, -1) {
		unique[match] = struct{}{}
	}
	return len(unique), nil
}

func compileOutputPattern(pattern string) (*regexp.Regexp, error) {
	resolved := strings.TrimSpace(Interpolate(pattern))
	if resolved == "" {
		return nil, fmt.Errorf("expected output pattern resolves to an empty value")
	}
	expression, err := regexp.Compile(resolved)
	if err != nil {
		return nil, fmt.Errorf("invalid output pattern %q: %w", resolved, err)
	}
	return expression, nil
}
