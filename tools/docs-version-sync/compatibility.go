// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// renderCompatibilityMatrix renders the current release of each stack and, for
// each stack train, the minimum train of the other stacks it works with.
func renderCompatibilityMatrix(catalog *Catalog) (string, error) {
	if catalog.ReleaseSet == (ReleaseSetMetadata{}) {
		return "", fmt.Errorf("compatibility matrix requires release_set stack releases")
	}
	if len(catalog.Compatibility) == 0 {
		return "", fmt.Errorf("compatibility matrix requires at least one compatibility entry")
	}
	var b strings.Builder
	b.WriteString("## Current stack releases\n\n")
	b.WriteString("| Stack | Latest release | Source tag |\n| --- | --- | --- |\n")
	for _, stack := range releaseSetStackNames {
		metadata, err := catalog.ReleaseSet.Stacks.byName(stack)
		if err != nil {
			return "", err
		}
		slug, err := documentationProductSlug(stack)
		if err != nil {
			return "", err
		}
		b.WriteString(fmt.Sprintf("| [%s](/nvcf/%s/) | `%s` | `%s` |\n",
			documentationStackDisplayName(stack), slug, metadata.Version, metadata.SourceTag))
	}
	b.WriteString("\n## Compatible stack versions\n\n")
	b.WriteString("| Stack | Release | Works with |\n| --- | --- | --- |\n")
	entries := append([]CompatibilityEntry(nil), catalog.Compatibility...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Stack != entries[j].Stack {
			return stackOrder(entries[i].Stack) < stackOrder(entries[j].Stack)
		}
		return compareTrains(entries[i].Train, entries[j].Train) > 0
	})
	for _, entry := range entries {
		worksWith := make([]string, 0, len(releaseSetStackNames)-1)
		for _, other := range releaseSetStackNames {
			if other == entry.Stack {
				continue
			}
			worksWith = append(worksWith, formatRequirement(other, entry.CompatibleWith[other]))
		}
		b.WriteString(fmt.Sprintf("| %s | `%s` | %s |\n", documentationStackDisplayName(entry.Stack), entry.Train, strings.Join(worksWith, ", ")))
	}
	return b.String(), nil
}

func stackOrder(stack string) int {
	for index, name := range releaseSetStackNames {
		if name == stack {
			return index
		}
	}
	return len(releaseSetStackNames)
}

// formatRequirement turns "1.0+" into "Compute plane 1.0 or later" and "1.0"
// into "Compute plane 1.0 only".
func formatRequirement(stack, requirement string) string {
	name := documentationStackDisplayName(stack)
	if train, open := strings.CutSuffix(requirement, "+"); open {
		return fmt.Sprintf("%s `%s` or later", name, train)
	}
	return fmt.Sprintf("%s `%s` only", name, requirement)
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
