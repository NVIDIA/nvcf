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
	Deploys []Deploy `json:"deploys"`
}

// Deploy is one service a chart ships. A plain JSON string names the service
// and leaves ValuesPaths empty, so PlanFor/Apply use the default evidence: the
// chart's single image tag agrees with its appVersion, or it does not.
//
// A chart that ships more than one first-party image cannot use that
// evidence: several tags could equal the old appVersion, or none could,
// without saying which one is this service's. For that case a deploy entry
// is an object naming ValuesPaths, the exact values.yaml paths (dotted, for
// example "otelCollector.imageTag") that carry this service's tag. Those
// paths move to the released version and nothing else does: appVersion is
// left alone, because in a multi-image chart it belongs to whichever other
// service (if any) uses the default single-image evidence.
type Deploy struct {
	Service     string
	ValuesPaths []string
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
		Service     string   `json:"service"`
		ValuesPaths []string `json:"values_paths"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("deploys entry: %w", err)
	}
	if obj.Service == "" {
		return fmt.Errorf("deploys entry missing \"service\"")
	}
	*d = Deploy{Service: obj.Service, ValuesPaths: obj.ValuesPaths}
	return nil
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

// ChartDeploy pairs a chart entry with how the released service's tag is
// found in it. ValuesPaths is empty for the default appVersion/image-tag
// agreement evidence, and holds the declared dotted paths when the deploy
// edge named them explicitly.
type ChartDeploy struct {
	Entry
	ValuesPaths []string
}

// ChartsDeploying returns the chart entries that declare they deploy
// serviceID, one ChartDeploy per chart.
func (m *Metadata) ChartsDeploying(serviceID string) []ChartDeploy {
	var out []ChartDeploy
	for _, e := range m.Services {
		if !strings.HasPrefix(e.Path, ChartPrefix) {
			continue
		}
		for _, d := range e.Deploys {
			if d.Service == serviceID {
				out = append(out, ChartDeploy{Entry: e, ValuesPaths: d.ValuesPaths})
				break
			}
		}
	}
	return out
}
