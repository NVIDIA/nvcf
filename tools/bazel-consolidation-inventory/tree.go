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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tree is the set of git-tracked files at one repository, and their contents.
type Tree struct {
	Root  string
	Head  string
	Date  string
	Dirty bool

	paths    []string
	contents map[string]string
}

// NewTree resolves the repository, verifies it is in a state whose measurements
// can be attributed to a commit, and lists its tracked files.
func NewTree(root string, allowDirty bool) (*Tree, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", root, err)
	}
	t := &Tree{Root: abs, contents: map[string]string{}}

	head, err := t.git("rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, err
	}
	t.Head = strings.TrimSpace(head)

	date, err := t.git("log", "-1", "--format=%cs")
	if err != nil {
		return nil, err
	}
	t.Date = strings.TrimSpace(date)

	status, err := t.git("status", "--porcelain")
	if err != nil {
		return nil, err
	}
	t.Dirty = strings.TrimSpace(status) != ""
	if t.Dirty && !allowDirty {
		return nil, fmt.Errorf(
			"working tree is dirty, so measurements would not match commit %s.\n"+
				"       Commit or stash your changes, or pass --allow-dirty to measure anyway",
			t.Head)
	}

	listing, err := t.git("ls-files", "-z")
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(listing, "\x00") {
		if p != "" {
			t.paths = append(t.paths, p)
		}
	}
	return t, nil
}

// git runs git and returns its output, failing on a non-zero status.
//
// An empty result from a failed call is indistinguishable from a repository
// with no tracked files, and every count downstream would report a plausible
// zero.
func (t *Tree) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = t.Root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "no stderr"
		}
		return "", fmt.Errorf("git %s failed: %v: %s", strings.Join(args, " "), err, msg)
	}
	return string(out), nil
}

// Select returns tracked paths matching pattern, sorted for determinism.
func (t *Tree) Select(pattern *regexp.Regexp, includeVendor bool) []string {
	var out []string
	for _, p := range t.paths {
		if !pattern.MatchString(p) {
			continue
		}
		if !includeVendor && vendorRe.MatchString(p) {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Read returns the contents of a tracked file.
//
// Only regular files are read. A tracked symlink is rejected rather than
// followed: its target can be outside the repository, in which case changing
// that target changes the measurements while leaving `git status` clean, so the
// report would no longer correspond to the commit it stamps. The same applies to
// any other non-regular file that might be committed.
func (t *Tree) Read(path string) (string, error) {
	if text, ok := t.contents[path]; ok {
		return text, nil
	}
	full := filepath.Join(t.Root, path)
	info, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("cannot stat tracked file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, linkErr := os.Readlink(full)
		if linkErr != nil {
			target = "unreadable"
		}
		return "", fmt.Errorf(
			"tracked file %s is a symlink (to %s); refusing to measure it because "+
				"its target can change without changing this repository",
			path, target)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf(
			"tracked file %s is not a regular file (mode %s); refusing to measure it",
			path, info.Mode())
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("cannot read tracked file %s: %w", path, err)
	}
	text := string(raw)
	t.contents[path] = text
	return text, nil
}

// Occurrences counts lines containing needle across paths.
func (t *Tree) Occurrences(paths []string, needle string) (int, error) {
	total := 0
	for _, path := range paths {
		text, err := t.Read(path)
		if err != nil {
			return 0, err
		}
		for _, line := range splitLines(text) {
			if strings.Contains(line, needle) {
				total++
			}
		}
	}
	return total, nil
}

// Distinct counts distinct file contents across paths.
func (t *Tree) Distinct(paths []string) (int, error) {
	seen := map[string]bool{}
	for _, path := range paths {
		text, err := t.Read(path)
		if err != nil {
			return 0, err
		}
		seen[text] = true
	}
	return len(seen), nil
}

// TotalBytes sums the byte length of paths.
func (t *Tree) TotalBytes(paths []string) (int, error) {
	total := 0
	for _, path := range paths {
		text, err := t.Read(path)
		if err != nil {
			return 0, err
		}
		total += len(text)
	}
	return total, nil
}

// TotalLines sums the line count of paths.
func (t *Tree) TotalLines(paths []string) (int, error) {
	total := 0
	for _, path := range paths {
		text, err := t.Read(path)
		if err != nil {
			return 0, err
		}
		total += len(splitLines(text))
	}
	return total, nil
}

// splitLines counts lines the way a text tool would: a trailing newline does
// not introduce an extra empty line.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(text, "\n")
	return strings.Split(trimmed, "\n")
}
