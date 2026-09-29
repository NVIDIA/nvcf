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
	// point of the audit, so it must survive decoding: a []Deploy does exactly
	// that, where a map lookup or a len() test would flatten the two together.
	Deploys []Deploy `json:"deploys"`
}

// Deploy is one service a chart ships. A plain JSON string names the service
// and lets chart-version-bumper's single appVersion/image-tag agreement
// decide which values.yaml line belongs to it. A chart with more than one
// first-party image cannot use that evidence, so a deploy entry may instead
// be an object naming the exact values.yaml paths (dotted, for example
// "otelCollector.imageTag") that carry that service's tag. The object may set
// values_files for repeated pins in other values files and app_version when the
// same service owns the chart's appVersion. This tool only audits which service
// ids are declared, so UnmarshalJSON exists primarily to make both shapes
// available to that audit.
type Deploy struct {
	Service     string
	ValuesPaths []string
	ValuesFiles []ValuesFile
	AppVersion  bool
}

// ValuesFile names an additional values file and the service-owned paths in it.
type ValuesFile struct {
	File  string   `json:"file"`
	Paths []string `json:"paths"`
}

func (d *Deploy) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s == "" {
			return fmt.Errorf("deploys entry: empty service id")
		}
		*d = Deploy{Service: s}
		return nil
	}
	var obj struct {
		Service     string       `json:"service"`
		ValuesPaths []string     `json:"values_paths"`
		ValuesFiles []ValuesFile `json:"values_files"`
		AppVersion  bool         `json:"app_version"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("deploys entry: %w", err)
	}
	if obj.Service == "" {
		return fmt.Errorf("deploys entry missing \"service\"")
	}
	if len(obj.ValuesPaths) == 0 && len(obj.ValuesFiles) == 0 {
		return fmt.Errorf("object deploys entry for %s requires values_paths or values_files", obj.Service)
	}
	for _, valuesFile := range obj.ValuesFiles {
		clean := filepath.Clean(valuesFile.File)
		if filepath.IsAbs(valuesFile.File) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("deploys entry for %s: values_files file %q must be relative to the chart path", obj.Service, valuesFile.File)
		}
		if len(valuesFile.Paths) == 0 {
			return fmt.Errorf("deploys entry for %s: values_files file %q requires paths", obj.Service, valuesFile.File)
		}
	}
	*d = Deploy{Service: obj.Service, ValuesPaths: obj.ValuesPaths, ValuesFiles: obj.ValuesFiles, AppVersion: obj.AppVersion}
	return nil
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
