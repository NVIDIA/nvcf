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
// frozen documentation train for one stack. The main catalog is left in
// development state; only the snapshot marks the stack qualified.
func freezeStackDocumentation(repoRoot, catalogPath, stack, train string) (string, error) {
	slug, err := documentationProductSlug(stack)
	if err != nil {
		return "", err
	}
	if !documentationTrainRe.MatchString(train) {
		return "", fmt.Errorf("--freeze-train must use %s", documentationTrainFormat)
	}
	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		return "", err
	}
	metadata, err := catalog.ReleaseSet.Stacks.byName(stack)
	if err != nil {
		return "", err
	}
	releasedTrain, ok := releaseTrain(metadata.Version)
	if !ok {
		return "", fmt.Errorf("release_set %s version %q is not a semantic version", stack, metadata.Version)
	}
	if releasedTrain != train {
		return "", fmt.Errorf("release_set %s version %s belongs to train %s, not %s; sync the catalog to a %s.x release first", stack, metadata.Version, releasedTrain, train, train)
	}
	if len(catalog.PublicationPending) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: freezing %s documentation while artifacts are publication pending: %s\n", stack, strings.Join(catalog.PublicationPending, ", "))
	}
	if err := validateStackSourceSnapshot(repoRoot, catalog); err != nil {
		return "", fmt.Errorf("validate stack source snapshot: %w", err)
	}
	metadata.DocumentationVersion = train
	metadata.Status = ReleaseSetQualified

	snapshotPath := filepath.Join(filepath.Dir(catalogPath), slug+"-"+train+".yaml")
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
