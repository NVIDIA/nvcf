// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MetadataPath is the release metadata every subproject is already registered
// in. The migration paths live here rather than in a new per-stack file so
// that a stack has one place describing how it releases, not two.
const MetadataPath = "tools/ci/github-release-subprojects.json"

const stackPrefix = "deploy/stacks/"

// Stack is one released stack bundle and the paths whose migrations ship with
// it. A stack that declares no migration paths is a stack that ships no
// schema, and the check is a no-op for it.
type Stack struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	MigrationPaths []string `json:"migration_paths"`
}

type metadata struct {
	Services []Stack `json:"services"`
}

// LoadStack finds a stack by its release-metadata id.
func LoadStack(root, id string) (Stack, error) {
	raw, err := os.ReadFile(filepath.Join(root, MetadataPath))
	if err != nil {
		return Stack{}, err
	}
	var meta metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Stack{}, fmt.Errorf("%s: %w", MetadataPath, err)
	}
	for _, s := range meta.Services {
		if s.ID != id {
			continue
		}
		if !strings.HasPrefix(s.Path, stackPrefix) {
			return Stack{}, fmt.Errorf("%q is not a stack: its path %q is not under %s", id, s.Path, stackPrefix)
		}
		return s, nil
	}
	return Stack{}, fmt.Errorf("no subproject %q in %s", id, MetadataPath)
}
