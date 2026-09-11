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
	updateCatalog := flags.Bool("update-catalog", false, "fetch the resolved GitHub stack inventory and update the catalog")
	stackVersion := flags.String("stack-version", "", "self-managed stack version to fetch or inventory")
	inventoryOutput := flags.String("generate-stack-inventory", "", "write a resolved stack inventory to this path")
	inventoryConfig := flags.String("inventory-config", "", "release inventory config path; defaults to the stack checkout")
	stackSourceTag := flags.String("stack-source-tag", "", "immutable self-managed stack source tag for inventory generation")
	stackSourceCommit := flags.String("stack-source-commit", "", "immutable self-managed stack source commit for inventory generation")

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
	if *inventoryOutput != "" {
		if *updateCatalog || *check {
			return fmt.Errorf("--generate-stack-inventory cannot be combined with --update-catalog or --check")
		}
		if *stackVersion == "" || *stackSourceTag == "" || *stackSourceCommit == "" {
			return fmt.Errorf("--generate-stack-inventory requires --stack-version, --stack-source-tag, and --stack-source-commit")
		}
		outputPath := *inventoryOutput
		if !filepath.IsAbs(outputPath) {
			outputPath = filepath.Join(repoRoot, outputPath)
		}
		configPath := *inventoryConfig
		if configPath != "" && !filepath.IsAbs(configPath) {
			configPath = filepath.Join(repoRoot, configPath)
		}
		return writeResolvedStackInventory(repoRoot, outputPath, configPath, stackSourceRelease{
			Version: *stackVersion,
			Tag:     *stackSourceTag,
			Commit:  *stackSourceCommit,
		})
	}
	if *stackSourceTag != "" || *stackSourceCommit != "" || *inventoryConfig != "" {
		return fmt.Errorf("--stack-source-tag, --stack-source-commit, and --inventory-config require --generate-stack-inventory")
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
		updated, err := updateCatalogFromGitHub(repoRoot, *stackVersion, base)
		if err != nil {
			return err
		}
		catalog = updated
		if *check {
			if err := validateStackSourceSnapshot(repoRoot, catalog); err != nil {
				return fmt.Errorf("validate stack source snapshot: %w", err)
			}
			if base == nil {
				return fmt.Errorf("--update-catalog --check requires an existing catalog at %s", relOrAbs(repoRoot, *catalogPath))
			}
			equal, err := catalogsEqual(base, updated)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("%w: %s does not match the resolved GitHub inventory for stack release %s", ErrCheckFailed, relOrAbs(repoRoot, *catalogPath), updated.Stack.PublicationVersion)
			}
		} else {
			if err := writeCatalogAfterStackSourceValidation(repoRoot, *catalogPath, updated); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "updated %s for stack publication %s\n", relOrAbs(repoRoot, *catalogPath), updated.Stack.PublicationVersion)
		}
	} else {
		loaded, err := LoadCatalog(*catalogPath)
		if err != nil {
			return err
		}
		catalog = loaded
		if err := validateStackSourceSnapshot(repoRoot, catalog); err != nil {
			return fmt.Errorf("validate stack source snapshot: %w", err)
		}
	}
	if err := SyncDocs(repoRoot, catalog, *check); err != nil {
		return err
	}
	return nil
}

func writeCatalogAfterStackSourceValidation(repoRoot, catalogPath string, catalog *Catalog) error {
	if catalog.Stack.SourceCommit == "" {
		return fmt.Errorf("cannot write updated catalog for stack publication %s without an immutable source snapshot", catalog.Stack.PublicationVersion)
	}
	if err := validateStackSourceSnapshot(repoRoot, catalog); err != nil {
		return fmt.Errorf("validate stack source snapshot: %w", err)
	}
	return WriteCatalog(catalogPath, catalog)
}

func setArtifactVersion(catalog *Catalog, name, version string) bool {
	changed := false
	for i := range catalog.Artifacts {
		if catalog.Artifacts[i].Name == name {
			catalog.Artifacts[i].Version = version
			changed = true
		}
	}
	for i := range catalog.SupplementalArtifacts {
		if catalog.SupplementalArtifacts[i].Name == name {
			catalog.SupplementalArtifacts[i].Version = version
			changed = true
		}
	}
	return changed
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
	return findRepoRootFrom(wd)
}

func findRepoRootFrom(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		catalog := filepath.Join(dir, "docs", "version-catalog", "main.yaml")
		module := filepath.Join(dir, "tools", "docs-version-sync", "go.mod")
		if isRegularFile(catalog) && isRegularFile(module) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("NVCF repository root not found (started from %s)", start)
		}
		dir = parent
	}
}

func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func relOrAbs(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
