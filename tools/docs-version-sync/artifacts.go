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
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

func ParseArtifactList(r io.Reader, denylist map[string]DenylistEntry) ([]Artifact, error) {
	scanner := bufio.NewScanner(r)
	counts := map[string]int{}
	var artifacts []Artifact
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		artifact, err := ParseArtifactRef(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		counts[artifact.Name]++
		if counts[artifact.Name] == 2 {
			for i := range artifacts {
				if artifacts[i].Name == artifact.Name && artifacts[i].ID == "" {
					artifacts[i].ID = artifactID(artifact.Name, 1)
					break
				}
			}
			artifact.ID = artifactID(artifact.Name, counts[artifact.Name])
		} else if counts[artifact.Name] > 2 {
			artifact.ID = artifactID(artifact.Name, counts[artifact.Name])
		}
		artifacts = append(artifacts, artifact)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func ParseArtifactRef(raw string) (Artifact, error) {
	source := raw
	ref := strings.TrimPrefix(raw, "oci://")
	if !strings.Contains(ref, "/") {
		versionSep := strings.LastIndexByte(ref, ':')
		if versionSep < 0 {
			return Artifact{}, fmt.Errorf("artifact reference %q has no tag", raw)
		}
		name := ref[:versionSep]
		version := ref[versionSep+1:]
		if name == "" || version == "" {
			return Artifact{}, fmt.Errorf("artifact reference %q has empty name or tag", raw)
		}
		return Artifact{
			Name:    name,
			Type:    inferArtifactType(raw, name),
			Version: version,
			Source:  source,
		}, nil
	}

	firstSlash := strings.IndexByte(ref, '/')
	if firstSlash < 0 {
		return Artifact{}, fmt.Errorf("artifact reference %q has no registry namespace", raw)
	}
	host := ref[:firstSlash]
	path := ref[firstSlash+1:]
	lastSlash := strings.LastIndexByte(path, '/')
	if lastSlash < 0 {
		return Artifact{}, fmt.Errorf("artifact reference %q has no artifact name", raw)
	}
	namespace := path[:lastSlash]
	nameAndVersion := path[lastSlash+1:]
	versionSep := strings.LastIndexByte(nameAndVersion, ':')
	if versionSep < 0 {
		return Artifact{}, fmt.Errorf("artifact reference %q has no tag", raw)
	}
	name := nameAndVersion[:versionSep]
	version := nameAndVersion[versionSep+1:]
	if name == "" || version == "" {
		return Artifact{}, fmt.Errorf("artifact reference %q has empty name or tag", raw)
	}
	return Artifact{
		Name:              name,
		Type:              inferArtifactType(raw, name),
		Version:           version,
		Source:            source,
		registryHost:      host,
		registryNamespace: namespace,
	}, nil
}

var artifactIDRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func artifactID(name string, ordinal int) string {
	clean := artifactIDRe.ReplaceAllString(name, "-")
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = "artifact"
	}
	return fmt.Sprintf("%s-%d", clean, ordinal)
}

func inferArtifactType(raw, name string) ArtifactType {
	if name == "nvcf-self-managed-stack" || name == "nvcf-base" || name == "nvcf-cli" {
		return ArtifactTypeResource
	}
	if strings.HasPrefix(raw, "oci://") || strings.HasPrefix(name, "helm-") {
		return ArtifactTypeChart
	}
	switch name {
	case "gdn-streaming", "gxcache", "nvcf-gateway-routes", "nvcf-observability-reference-stack", "nvcf-example-dashboards":
		return ArtifactTypeChart
	default:
		return ArtifactTypeImage
	}
}
