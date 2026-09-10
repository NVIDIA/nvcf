// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MetadataPath is the release metadata that github-release already owns. The
// chart's published name lives there rather than being derived from the
// directory, because the two differ: deploy/helm/sis publishes helm-nvcf-sis.
const MetadataPath = "tools/ci/github-release-subprojects.json"

// Entry is one subproject in the release metadata.
type Entry struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	ServiceName string `json:"service_name"`
}

// Metadata is the decoded release metadata file.
type Metadata struct {
	Services []Entry `json:"services"`
}

// LoadMetadata reads and decodes the release metadata under root.
func LoadMetadata(root string) (*Metadata, error) {
	path := filepath.Join(root, MetadataPath)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read release metadata: %w", err)
	}
	var m Metadata
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &m, nil
}
