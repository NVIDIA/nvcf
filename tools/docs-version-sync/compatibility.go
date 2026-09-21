// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// renderCompatibilityMatrix renders the current release of each stack and the
// declared compatible trains between stacks.
func renderCompatibilityMatrix(catalog *Catalog) (string, error) {
	if catalog.ReleaseSet == (ReleaseSetMetadata{}) {
		return "", fmt.Errorf("compatibility matrix requires release_set stack releases")
	}
	if len(catalog.Compatibility) == 0 {
		return "", fmt.Errorf("compatibility matrix requires at least one compatibility entry")
	}
	var b strings.Builder
	b.WriteString("### Current stack releases\n\n")
	b.WriteString("| Stack | Latest release | Source tag | Documentation |\n| --- | --- | --- | --- |\n")
	for _, stack := range releaseSetStackNames {
		metadata, err := catalog.ReleaseSet.Stacks.byName(stack)
		if err != nil {
			return "", err
		}
		slug, err := documentationProductSlug(stack)
		if err != nil {
			return "", err
		}
		b.WriteString(fmt.Sprintf("| %s | `%s` | `%s` | [%s](/nvcf/%s/) |\n",
			documentationStackDisplayName(stack), metadata.Version, metadata.SourceTag, documentationVersionLabel(*metadata), slug))
	}
	b.WriteString("\n### Qualified trains\n\n")
	b.WriteString("| Stack | Train | Self-managed trains | Compute plane trains | Observability trains |\n| --- | --- | --- | --- | --- |\n")
	entries := append([]CompatibilityEntry(nil), catalog.Compatibility...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Stack != entries[j].Stack {
			return stackOrder(entries[i].Stack) < stackOrder(entries[j].Stack)
		}
		return compareTrains(entries[i].Train, entries[j].Train) > 0
	})
	for _, entry := range entries {
		cells := make([]string, 0, len(releaseSetStackNames))
		for _, other := range releaseSetStackNames {
			if other == entry.Stack {
				cells = append(cells, "this stack")
				continue
			}
			cells = append(cells, formatTrains(entry.CompatibleWith[other]))
		}
		b.WriteString(fmt.Sprintf("| %s | `%s` | %s |\n", documentationStackDisplayName(entry.Stack), entry.Train, strings.Join(cells, " | ")))
	}
	return b.String(), nil
}

func documentationVersionLabel(metadata StackReleaseMetadata) string {
	if metadata.Status == ReleaseSetQualified {
		return metadata.DocumentationVersion
	}
	return "dev"
}

func stackOrder(stack string) int {
	for index, name := range releaseSetStackNames {
		if name == stack {
			return index
		}
	}
	return len(releaseSetStackNames)
}

func formatTrains(trains []string) string {
	sorted := append([]string(nil), trains...)
	sort.SliceStable(sorted, func(i, j int) bool { return compareTrains(sorted[i], sorted[j]) > 0 })
	for index, train := range sorted {
		sorted[index] = "`" + train + "`"
	}
	return strings.Join(sorted, ", ")
}

// compareTrains orders X.Y trains numerically; inputs are validated by the catalog.
func compareTrains(a, b string) int {
	var aMajor, aMinor, bMajor, bMinor int
	fmt.Sscanf(a, "%d.%d", &aMajor, &aMinor)
	fmt.Sscanf(b, "%d.%d", &bMajor, &bMinor)
	switch {
	case aMajor != bMajor:
		return aMajor - bMajor
	default:
		return aMinor - bMinor
	}
}
