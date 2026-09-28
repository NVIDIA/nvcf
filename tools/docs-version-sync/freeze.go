// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// freezeStackDocumentation writes the catalog snapshot that accompanies a
// frozen documentation version for one stack. The main catalog is left in
// development state; only the snapshot marks the stack qualified.
func freezeStackDocumentation(repoRoot, catalogPath, stack, version string) (string, error) {
	slug, err := documentationProductSlug(stack)
	if err != nil {
		return "", err
	}
	if !validStableStackVersion(version) {
		return "", fmt.Errorf("--freeze-version must use %s", documentationVersionFormat)
	}
	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		return "", err
	}
	metadata, err := catalog.ReleaseSet.Stacks.byName(stack)
	if err != nil {
		return "", err
	}
	if metadata.Version != version {
		return "", fmt.Errorf("release_set %s version is %s, not %s; sync the catalog to release %s first", stack, metadata.Version, version, version)
	}
	if len(catalog.PublicationPending) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: freezing %s %s documentation while artifacts are publication pending: %s\n", stack, version, strings.Join(catalog.PublicationPending, ", "))
	}
	if err := validateStackSourceSnapshot(repoRoot, catalog); err != nil {
		return "", fmt.Errorf("validate stack source snapshot: %w", err)
	}
	metadata.DocumentationVersion = version
	metadata.Status = ReleaseSetQualified

	snapshotPath := filepath.Join(filepath.Dir(catalogPath), slug+"-"+version+".yaml")
	if _, err := os.Stat(snapshotPath); err == nil {
		return "", fmt.Errorf("refusing to overwrite existing catalog snapshot %s", relOrAbs(repoRoot, snapshotPath))
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := WriteCatalog(snapshotPath, catalog); err != nil {
		return "", err
	}
	return snapshotPath, nil
}
