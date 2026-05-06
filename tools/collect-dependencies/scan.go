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
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func importPathsFromManifest(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mf manifestFile
	if err := yaml.Unmarshal(raw, &mf); err != nil {
		return nil, err
	}
	var out []string
	for _, row := range mf.Imports {
		if strings.TrimSpace(row.Path) == "" {
			continue
		}
		out = append(out, filepath.Join(repoRoot, filepath.FromSlash(row.Path)))
	}
	return out, nil
}

func walkImportRoot(root string, fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip && path != root {
				return filepath.SkipDir
			}
			return fn(path, d)
		}
		return fn(path, d)
	})
}

func scanTree(root string, langs map[string]bool) (dependencyScan, error) {
	out := dependencyScan{
		Go:     map[string]struct{}{},
		Rust:   map[string]struct{}{},
		Python: map[string]struct{}{},
		Java:   map[string]struct{}{},
		Helm:   map[string]struct{}{},
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return out, nil
	}

	err := walkImportRoot(root, func(path string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		switch {
		case langs["go"] && name == "go.mod":
			for mod := range parseGoModRequires(path) {
				out.Go[mod] = struct{}{}
			}
		case langs["rust"] && name == "Cargo.toml":
			for dep := range parseCargoTOML(path) {
				out.Rust[dep] = struct{}{}
			}
		case langs["python"] && name == "pyproject.toml":
			for dep := range parsePyproject(path) {
				out.Python[dep] = struct{}{}
			}
		case langs["python"] && strings.HasPrefix(name, "requirements") && strings.HasSuffix(name, ".txt"):
			for dep := range parseRequirementsTxt(path) {
				out.Python[dep] = struct{}{}
			}
		case langs["python"] && name == "Pipfile":
			for dep := range parsePipfile(path) {
				out.Python[dep] = struct{}{}
			}
		case langs["java"] && name == "pom.xml":
			for dep := range parsePOMDirectDependencies(path) {
				out.Java[dep] = struct{}{}
			}
		}
		return nil
	})
	return out, err
}

func collectHelmDependencies(importRoots []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	seenChartDirs := map[string]struct{}{}
	for _, root := range importRoots {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		err := walkImportRoot(root, func(path string, d fs.DirEntry) error {
			if !d.IsDir() {
				return nil
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil
			}
			hasChart := false
			hasLock := false
			for _, entry := range entries {
				if entry.Name() == "Chart.yaml" {
					hasChart = true
				}
				if entry.Name() == "Chart.lock" {
					hasLock = true
				}
			}
			if !hasChart && !hasLock {
				return nil
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				resolved = path
			}
			if _, seen := seenChartDirs[resolved]; seen {
				return nil
			}
			seenChartDirs[resolved] = struct{}{}
			manifest := filepath.Join(path, "Chart.yaml")
			if hasLock {
				manifest = filepath.Join(path, "Chart.lock")
			}
			mergeSet(out, parseHelmDependencyManifest(manifest))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
