// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// renderCompatibilityMatrix renders the current release of each stack and, for
// each stack release, the minimum release of the other stacks it works with.
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
		return compareVersions(entries[i].Version, entries[j].Version) > 0
	})
	for _, entry := range entries {
		worksWith := make([]string, 0, len(releaseSetStackNames)-1)
		for _, other := range releaseSetStackNames {
			if other == entry.Stack {
				continue
			}
			worksWith = append(worksWith, formatRequirement(other, entry.CompatibleWith[other]))
		}
		b.WriteString(fmt.Sprintf("| %s | `%s` | %s |\n", documentationStackDisplayName(entry.Stack), entry.Version, strings.Join(worksWith, ", ")))
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

// formatRequirement turns "1.0.0+" into "Compute plane 1.0.0 or later" and
// "1.0.0" into "Compute plane 1.0.0 only".
func formatRequirement(stack, requirement string) string {
	name := documentationStackDisplayName(stack)
	if version, open := strings.CutSuffix(requirement, "+"); open {
		return fmt.Sprintf("%s `%s` or later", name, version)
	}
	return fmt.Sprintf("%s `%s` only", name, requirement)
}

// compareVersions orders stable semantic versions; inputs are validated by the catalog.
func compareVersions(a, b string) int {
	aParts := stableStackVersionRE.FindStringSubmatch(a)
	bParts := stableStackVersionRE.FindStringSubmatch(b)
	for index := 1; index <= 3; index++ {
		if comparison := compareNumericIdentifier(aParts[index], bParts[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}
