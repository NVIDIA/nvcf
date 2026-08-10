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
	"fmt"
	"regexp"
)

func SyncInlineVersions(path, content string, catalog *Catalog) (string, bool, error) {
	switch path {
	case "docs/user/image-mirroring.md":
		return syncImageMirroring(content, catalog)
	case "docs/user/cluster-management/self-managed.md":
		return syncClusterManagementSelfManaged(content, catalog)
	case "docs/user/cluster-management/reference.md":
		return syncClusterManagementReference(content, catalog)
	default:
		return content, false, nil
	}
}

func syncImageMirroring(content string, catalog *Catalog) (string, bool, error) {
	chart, ok := catalog.findArtifact("helm-nvca-operator")
	if !ok {
		return "", false, fmt.Errorf("artifact helm-nvca-operator is required")
	}
	pullReference, err := catalog.chartPullReference(chart)
	if err != nil {
		return "", false, err
	}
	updated := content
	var total int
	var count int
	updated, count = replaceNVCAOperatorChartPull(updated, pullReference, chart.Version)
	total += count
	updated, count = replaceNVCAOperatorChartArchiveComments(updated, chart.Version)
	total += count
	updated, count = replaceNVCAOperatorChartPush(updated, chart.Version)
	total += count
	if total == 0 {
		return "", false, fmt.Errorf("nvca operator chart mirroring examples not found")
	}
	return updated, updated != content, nil
}

func syncClusterManagementSelfManaged(content string, catalog *Catalog) (string, bool, error) {
	chart, ok := catalog.findArtifact("helm-nvca-operator")
	if !ok {
		return "", false, fmt.Errorf("artifact helm-nvca-operator is required")
	}
	updated, count := replaceVersionTable(content, chart.Name, chart.Version)
	if count == 0 {
		return "", false, fmt.Errorf("version table for %s not found", chart.Name)
	}
	updated, _ = replaceHelmVersionArgument(updated, chart.Name, chart.Version)

	nvca, ok := catalog.findArtifact("nvca")
	if !ok {
		return "", false, fmt.Errorf("artifact nvca is required")
	}
	updated, count = replaceYAMLStringValue(updated, "nvcaVersion", nvca.Version)
	return updated, updated != content, nil
}

func syncClusterManagementReference(content string, catalog *Catalog) (string, bool, error) {
	helper, ok := catalog.findArtifact("nvcf-image-credential-helper")
	if !ok {
		return "", false, fmt.Errorf("artifact nvcf-image-credential-helper is required")
	}
	re := regexp.MustCompile(`(?m)(imageCredHelper:\n\s+imageRepository: ""\n\s+imageTag: )[^\n]+`)
	matches := re.FindAllStringIndex(content, -1)
	if len(matches) == 0 {
		return "", false, fmt.Errorf("imageCredHelper imageTag block not found")
	}
	updated := re.ReplaceAllString(content, "${1}"+helper.Version)
	return updated, updated != content, nil
}

func replaceVersionTable(content, chartName, version string) (string, int) {
	label := `(?:\*\*)?%s(?:\*\*)?`
	pattern := fmt.Sprintf(`(?s)(\| `+label+` \| %s \|\n\| --- \| --- \|\n\| `+label+` \| )`+"`[^`]+`"+`( \|)`, "Chart", regexp.QuoteMeta("`"+chartName+"`"), "Version")
	re := regexp.MustCompile(pattern)
	count := len(re.FindAllStringIndex(content, -1))
	return re.ReplaceAllString(content, "${1}`"+version+"`${2}"), count
}

func replaceHelmVersionArgument(content, chartName, version string) (string, int) {
	pattern := fmt.Sprintf(`(?ms)(oci://[^\s]+/%s\s+\\\n(?:[^\n]*\\\n)*?\s+--version )[^\s\\]+`, regexp.QuoteMeta(chartName))
	re := regexp.MustCompile(pattern)
	count := len(re.FindAllStringIndex(content, -1))
	return re.ReplaceAllString(content, "${1}"+version), count
}

func replaceYAMLStringValue(content, key, value string) (string, int) {
	pattern := fmt.Sprintf(`(?m)^(\s+%s:\s*)"[^"]+"`, regexp.QuoteMeta(key))
	re := regexp.MustCompile(pattern)
	count := len(re.FindAllStringIndex(content, -1))
	return re.ReplaceAllString(content, "${1}\""+value+"\""), count
}

func replaceNVCAOperatorChartPull(content, pullReference, version string) (string, int) {
	re := regexp.MustCompile(`(?:oci://[^\s]+/|[a-z0-9-]+/)(?:helm-nvca-operator|nvca-operator) --version [^\s]+`)
	count := len(re.FindAllStringIndex(content, -1))
	replacement := pullReference + " --version " + version
	return re.ReplaceAllString(content, replacement), count
}

func replaceNVCAOperatorChartArchiveComments(content, version string) (string, int) {
	re := regexp.MustCompile(`# This creates: (?:helm-nvca-operator|nvca-operator)-[^\s]+\.tgz`)
	count := len(re.FindAllStringIndex(content, -1))
	return re.ReplaceAllString(content, "# This creates: helm-nvca-operator-"+version+".tgz"), count
}

func replaceNVCAOperatorChartPush(content, version string) (string, int) {
	re := regexp.MustCompile(`helm push (?:helm-nvca-operator|nvca-operator)-[^\s]+\.tgz`)
	count := len(re.FindAllStringIndex(content, -1))
	return re.ReplaceAllString(content, "helm push helm-nvca-operator-"+version+".tgz"), count
}
