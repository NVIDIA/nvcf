// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// MetadataPath is the release metadata that github-release already owns. The
// chart to service edges live there rather than in a file of their own so a
// service rename has one place to update, not two.
const MetadataPath = "tools/ci/github-release-subprojects.json"

// ChartPrefix marks an entry as a chart rather than a service.
const ChartPrefix = "deploy/helm/"

// Entry is one subproject in the release metadata.
type Entry struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Deploys is nil when the key is absent and non-nil empty when the chart
	// declares it ships no first-party image. That distinction is the whole
	// point of the audit, so it must survive decoding: a []string does exactly
	// that, where a map lookup or a len() test would flatten the two together.
	Deploys []string `json:"deploys"`
}

// Metadata is the decoded release metadata file.
type Metadata struct {
	Services []Entry `json:"services"`
}

// LoadMetadata reads and decodes the release metadata.
func LoadMetadata(path string) (*Metadata, error) {
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

// Charts returns the entries that are charts.
func (m *Metadata) Charts() []Entry {
	var out []Entry
	for _, e := range m.Services {
		if strings.HasPrefix(e.Path, ChartPrefix) {
			out = append(out, e)
		}
	}
	return out
}

// ServiceIDs returns the ids of the entries that are not charts.
func (m *Metadata) ServiceIDs() map[string]bool {
	out := map[string]bool{}
	for _, e := range m.Services {
		if !strings.HasPrefix(e.Path, ChartPrefix) {
			out[e.ID] = true
		}
	}
	return out
}
