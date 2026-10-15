// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"sort"
	"strings"
)

type resolvedManifestEntry struct {
	ID           string
	Name         string
	Version      string
	Distribution string
	Plane        ManifestPlane
	Kind         ManifestKind
	Requirement  ManifestRequirement
	Description  string
	GitHubURL    string
	UpstreamURL  string
}

type manifestSection struct {
	Heading     string
	Description string
	Plane       ManifestPlane
	Kind        ManifestKind
}

var manifestSections = []manifestSection{
	{Heading: "Control plane Helm charts", Plane: ManifestPlaneControl, Kind: ManifestKindChart},
	{Heading: "Control plane services and images", Plane: ManifestPlaneControl, Kind: ManifestKindServiceImage},
	{Heading: "Compute plane Helm charts", Plane: ManifestPlaneCompute, Kind: ManifestKindChart},
	{Heading: "Compute plane services and images", Plane: ManifestPlaneCompute, Kind: ManifestKindServiceImage},
	{
		Heading:     "EA-only CVE-impacted artifacts",
		Description: "These Early Access artifacts have known CVE impact. Use only the QA-qualified versions listed for this EA stack.",
		Kind:        ManifestKindEACVE,
	},
	{Heading: "Tools and deployment resources", Kind: ManifestKindResource},
}

func resolveManifestEntries(catalog *Catalog) ([]resolvedManifestEntry, error) {
	denylist := catalog.DenylistMap()
	artifacts := map[string]Artifact{}
	stack := catalog.stackArtifact()
	artifacts[stack.catalogKey()] = stack
	for _, artifact := range append(append([]Artifact{}, catalog.Artifacts...), catalog.SupplementalArtifacts...) {
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		artifacts[artifact.catalogKey()] = artifact
	}

	classified := map[string]struct{}{}
	entries := make([]resolvedManifestEntry, 0, len(catalog.Manifest.Entries))
	for _, metadata := range catalog.Manifest.Entries {
		entry := resolvedManifestEntry{
			Plane:       metadata.Plane,
			Kind:        metadata.Kind,
			Requirement: metadata.Requirement,
			Description: metadata.Description,
			GitHubURL:   metadata.GitHubURL,
			UpstreamURL: metadata.UpstreamURL,
		}
		if metadata.ArtifactID == "" {
			entry.ID = "static:" + metadata.Name
			entry.Name = metadata.Name
			entry.Version = metadata.Version
			entry.Distribution = metadata.Distribution
		} else {
			artifact, ok := artifacts[metadata.ArtifactID]
			if !ok {
				return nil, fmt.Errorf("manifest metadata references unknown artifact %s", metadata.ArtifactID)
			}
			path, err := catalog.artifactPath(artifact)
			if err != nil {
				return nil, err
			}
			entry.ID = metadata.ArtifactID
			entry.Name = artifact.Name
			entry.Version = artifact.Version
			entry.Distribution = path
			classified[metadata.ArtifactID] = struct{}{}
		}
		if entry.Name == "load_tester_supreme" {
			return nil, fmt.Errorf("load_tester_supreme must not appear in the manifest")
		}
		entries = append(entries, entry)
	}

	var missing []string
	for key := range artifacts {
		if _, ok := classified[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("unclassified artifacts: %s", strings.Join(missing, ", "))
	}
	return entries, nil
}

func renderManifestArtifactRegistryPaths(catalog *Catalog) (string, error) {
	entries, err := resolveManifestEntries(catalog)
	if err != nil {
		return "", err
	}
	return renderManifestTables(entries), nil
}

func renderManifestTables(entries []resolvedManifestEntry) string {
	var b strings.Builder
	for _, section := range manifestSections {
		b.WriteString("### " + section.Heading + "\n\n")
		if section.Description != "" {
			b.WriteString(section.Description + "\n\n")
		}
		sectionEntries := manifestEntriesForSection(entries, section)
		if section.Kind == ManifestKindResource {
			b.WriteString("| Artifact | Version | Description | Distribution | Source code |\n")
			b.WriteString("| --- | --- | --- | --- | --- |\n")
			for _, entry := range sectionEntries {
				b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | `%s` | %s |\n",
					entry.Name, entry.Version, entry.Description, entry.Distribution, formatManifestSources(entry)))
			}
		} else {
			b.WriteString("| Artifact | Version | Required | Description | Distribution | Source code |\n")
			b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
			for _, entry := range sectionEntries {
				b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | %s | `%s` | %s |\n",
					entry.Name, entry.Version, formatManifestRequirement(entry.Requirement), entry.Description,
					entry.Distribution, formatManifestSources(entry)))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func manifestEntriesForSection(entries []resolvedManifestEntry, section manifestSection) []resolvedManifestEntry {
	var matched []resolvedManifestEntry
	for _, entry := range entries {
		if entry.Kind != section.Kind {
			continue
		}
		if section.Kind != ManifestKindEACVE && section.Kind != ManifestKindResource && entry.Plane != section.Plane {
			continue
		}
		matched = append(matched, entry)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Name == matched[j].Name {
			return matched[i].ID < matched[j].ID
		}
		return matched[i].Name < matched[j].Name
	})
	return matched
}

func formatManifestRequirement(requirement ManifestRequirement) string {
	if requirement == ManifestOptional {
		return "Optional"
	}
	return "Required"
}

func formatManifestSources(entry resolvedManifestEntry) string {
	var sources []string
	if entry.GitHubURL != "" {
		sources = append(sources, "[GitHub]("+entry.GitHubURL+")")
	}
	if entry.UpstreamURL != "" {
		sources = append(sources, "[Upstream]("+entry.UpstreamURL+")")
	}
	return strings.Join(sources, " / ")
}
