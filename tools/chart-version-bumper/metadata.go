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

// MetadataPath is the release metadata that github-release already owns.
const MetadataPath = "tools/ci/github-release-subprojects.json"

// ChartPrefix marks an entry as a chart rather than a service.
const ChartPrefix = "deploy/helm/"

// Entry is one subproject in the release metadata.
type Entry struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Deploys []string `json:"deploys"`
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

// ServiceForTag maps a release tag to the service that owns it and the version
// the tag carries.
//
// The released tag carries the version, so there is no "newest version" lookup
// and none of the ordering questions that come with one.
func (m *Metadata) ServiceForTag(tag string) (serviceID, version string, err error) {
	bestPath, bestID := "", ""
	for _, e := range m.Services {
		// Charts are excluded: a chart release is stack-pin-resolver's business,
		// and a chart path could otherwise shadow the service it deploys.
		if e.Path == "" || strings.HasPrefix(e.Path, ChartPrefix) {
			continue
		}
		if !strings.HasPrefix(tag, e.Path+"/v") {
			continue
		}
		// Longest match wins: subtree paths nest, so a shorter path can be a
		// prefix of the one that actually owns the tag.
		if bestPath == "" || len(e.Path) > len(bestPath) {
			bestPath, bestID = e.Path, e.ID
		}
	}
	if bestPath == "" {
		return "", "", fmt.Errorf("no service in release metadata owns the tag %s", tag)
	}
	return bestID, tag[len(bestPath)+2:], nil
}

// ChartsDeploying returns the chart entries that declare they deploy serviceID.
func (m *Metadata) ChartsDeploying(serviceID string) []Entry {
	var out []Entry
	for _, e := range m.Services {
		if !strings.HasPrefix(e.Path, ChartPrefix) {
			continue
		}
		for _, s := range e.Deploys {
			if s == serviceID {
				out = append(out, e)
				break
			}
		}
	}
	return out
}
