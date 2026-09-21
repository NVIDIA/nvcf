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
	computeStackVersion := flags.String("compute-stack-version", "", "compute-plane stack version to fetch")
	observabilityStackVersion := flags.String("observability-stack-version", "", "observability stack version to fetch")
	freezeStack := flags.String("freeze-stack", "", "release_set stack whose documentation train is being frozen (control-plane, compute-plane, or observability)")
	freezeTrain := flags.String("freeze-train", "", "documentation train to freeze for --freeze-stack ("+documentationTrainFormat+")")
	inventoryOutput := flags.String("generate-stack-inventory", "", "write a resolved stack inventory to this path")
	inventoryConfig := flags.String("inventory-config", "", "release inventory config path; defaults to the stack checkout")
	allowUnavailableSourceCharts := flags.Bool("allow-unavailable-source-charts", false, "use published charts when configured source paths are unavailable in a historical tag")
	stackSourceTag := flags.String("stack-source-tag", "", "immutable owning stack source tag for inventory generation")
	stackSourceCommit := flags.String("stack-source-commit", "", "immutable owning stack source commit for inventory generation")
	compareFrom := flags.String("compare-release-set-from", "", "directory containing previous release-set inventory JSON files")
	compareTo := flags.String("compare-release-set-to", "", "directory containing current release-set inventory JSON files")

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
	if *compareFrom != "" || *compareTo != "" {
		if *compareFrom == "" || *compareTo == "" {
			return fmt.Errorf("--compare-release-set-from and --compare-release-set-to must be used together")
		}
		if *updateCatalog || *check || *inventoryOutput != "" || *freezeStack != "" || *freezeTrain != "" ||
			*stackVersion != "" || *computeStackVersion != "" || *observabilityStackVersion != "" ||
			*inventoryConfig != "" || *allowUnavailableSourceCharts || *stackSourceTag != "" || *stackSourceCommit != "" {
			return fmt.Errorf("release-set comparison cannot be combined with catalog update, check, or inventory generation flags")
		}
		fromPath := resolveRepoPath(repoRoot, *compareFrom)
		toPath := resolveRepoPath(repoRoot, *compareTo)
		report, err := compareInventorySetDirectories(fromPath, toPath)
		if err != nil {
			return err
		}
		if _, err := fmt.Print(report); err != nil {
			return fmt.Errorf("write release-set comparison: %w", err)
		}
		return nil
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
		}, resolvedInventoryGenerationOptions{
			AllowUnavailableSourceCharts: *allowUnavailableSourceCharts,
		})
	}
	if !*updateCatalog && (*computeStackVersion != "" || *observabilityStackVersion != "") {
		return fmt.Errorf("--compute-stack-version and --observability-stack-version require --update-catalog")
	}
	if *stackSourceTag != "" || *stackSourceCommit != "" || *inventoryConfig != "" || *allowUnavailableSourceCharts {
		return fmt.Errorf("--stack-source-tag, --stack-source-commit, --inventory-config, and --allow-unavailable-source-charts require --generate-stack-inventory")
	}
	if *catalogPath == "" {
		*catalogPath = filepath.Join(repoRoot, "docs", "version-catalog", *target+".yaml")
	}
	if !filepath.IsAbs(*catalogPath) {
		*catalogPath = filepath.Join(repoRoot, *catalogPath)
	}
	if *freezeStack != "" || *freezeTrain != "" {
		if *freezeStack == "" || *freezeTrain == "" {
			return fmt.Errorf("--freeze-stack and --freeze-train must be used together")
		}
		if *updateCatalog || *check || *stackVersion != "" || *computeStackVersion != "" || *observabilityStackVersion != "" {
			return fmt.Errorf("--freeze-stack cannot be combined with catalog update, check, or stack version flags")
		}
		snapshotPath, err := freezeStackDocumentation(repoRoot, *catalogPath, *freezeStack, *freezeTrain)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s for %s train %s\n", relOrAbs(repoRoot, snapshotPath), *freezeStack, *freezeTrain)
		return nil
	}

	var catalog *Catalog
	if *updateCatalog {
		base, err := loadCatalogIfPresent(*catalogPath)
		if err != nil {
			return err
		}
		updated, err := updateCatalogFromGitHubInventories(repoRoot, map[string]string{
			selfManagedStackKey:   *stackVersion,
			computePlaneStackKey:  *computeStackVersion,
			observabilityStackKey: *observabilityStackVersion,
		}, base)
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
				return fmt.Errorf("%w: %s does not match the resolved GitHub inventory for stack release %s", ErrCheckFailed, relOrAbs(repoRoot, *catalogPath), updated.Stack.Version)
			}
		} else {
			if err := writeCatalogAfterStackSourceValidation(repoRoot, *catalogPath, updated); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "updated %s for stack release %s\n", relOrAbs(repoRoot, *catalogPath), updated.Stack.Version)
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

func resolveRepoPath(repoRoot, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(repoRoot, path)
}

func writeCatalogAfterStackSourceValidation(repoRoot, catalogPath string, catalog *Catalog) error {
	if catalog.Stack.SourceCommit == "" {
		return fmt.Errorf("cannot write updated catalog for stack release %s without an immutable source snapshot", catalog.Stack.Version)
	}
	if err := validateStackSourceSnapshot(repoRoot, catalog); err != nil {
		return fmt.Errorf("validate stack source snapshot: %w", err)
	}
	return WriteCatalog(catalogPath, catalog)
}

func setArtifactVersionByNameAndType(catalog *Catalog, name string, artifactType ArtifactType, version string) bool {
	changed := false
	for i := range catalog.Artifacts {
		if catalog.Artifacts[i].Name == name && catalog.Artifacts[i].Type == artifactType {
			catalog.Artifacts[i].Version = version
			changed = true
		}
	}
	for i := range catalog.SupplementalArtifacts {
		if catalog.SupplementalArtifacts[i].Name == name && catalog.SupplementalArtifacts[i].Type == artifactType {
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
