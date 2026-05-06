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
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "docs-version-sync: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("docs-version-sync", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	target := flags.String("target", "main", "documentation target to render")
	catalogPath := flags.String("catalog", "", "version catalog path")
	check := flags.Bool("check", false, "fail if generated docs differ from checked-in marker blocks")
	updateCatalog := flags.Bool("update-catalog", false, "fetch the stack artifact list from GitLab and update the catalog")
	stackVersion := flags.String("stack-version", "", "self-managed stack version to fetch when updating the catalog")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := ValidateTarget(*target); err != nil {
		return err
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	if *catalogPath == "" {
		*catalogPath = filepath.Join(repoRoot, "docs", "version-catalog", *target+".yaml")
	}
	if !filepath.IsAbs(*catalogPath) {
		*catalogPath = filepath.Join(repoRoot, *catalogPath)
	}

	var catalog *Catalog
	if *updateCatalog {
		base, err := loadCatalogIfPresent(*catalogPath)
		if err != nil {
			return err
		}
		updated, err := updateCatalogFromGitLab(*stackVersion, base)
		if err != nil {
			return err
		}
		catalog = updated
		if *check {
			if base == nil {
				return fmt.Errorf("--update-catalog --check requires an existing catalog at %s", relOrAbs(repoRoot, *catalogPath))
			}
			equal, err := catalogsEqual(base, updated)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("%w: %s does not match latest %s artifact manifest for stack %s", ErrCheckFailed, relOrAbs(repoRoot, *catalogPath), defaultPackageName, updated.Stack.Version)
			}
		} else {
			if err := WriteCatalog(*catalogPath, updated); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "updated %s for stack %s\n", relOrAbs(repoRoot, *catalogPath), updated.Stack.Version)
		}
	} else {
		loaded, err := LoadCatalog(*catalogPath)
		if err != nil {
			return err
		}
		catalog = loaded
	}

	if err := SyncDocs(repoRoot, catalog, *check); err != nil {
		return err
	}
	return nil
}

func updateCatalogFromGitLab(stackVersion string, base *Catalog) (*Catalog, error) {
	client, err := NewGitLabClientFromEnvironment()
	if err != nil {
		return nil, err
	}
	if stackVersion == "" {
		version, err := client.LatestStackVersion(defaultStackProjectID, defaultPackageName)
		if err != nil {
			return nil, err
		}
		stackVersion = version
	}
	rawArtifacts, err := client.FetchArtifactList(defaultStackProjectID, defaultPackageName, stackVersion)
	if err != nil {
		return nil, err
	}
	denylistSource := base
	if denylistSource == nil {
		denylistSource = BuildCatalogFromArtifacts(stackVersion, nil)
	}
	artifacts, err := ParseArtifactList(strings.NewReader(rawArtifacts), denylistSource.DenylistMap())
	if err != nil {
		return nil, err
	}
	catalog := BuildCatalogFromArtifactsWithBase(stackVersion, artifacts, base)
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func loadCatalogIfPresent(path string) (*Catalog, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return LoadCatalog(path)
}

func catalogsEqual(a, b *Catalog) (bool, error) {
	left, err := MarshalCatalog(a)
	if err != nil {
		return false, fmt.Errorf("marshal existing catalog: %w", err)
	}
	right, err := MarshalCatalog(b)
	if err != nil {
		return false, fmt.Errorf("marshal latest catalog: %w", err)
	}
	return bytes.Equal(left, right), nil
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

func relOrAbs(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
