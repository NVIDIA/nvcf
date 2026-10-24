// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type comparableArtifact struct {
	Type        string
	Name        string
	Reference   string
	Digest      string
	Requirement ManifestRequirement
	Stacks      []string
}

type comparableInventorySet struct {
	Sources   []stackSourceRelease
	Artifacts map[string]comparableArtifact
}

func compareInventorySetDirectories(fromDirectory, toDirectory string) (string, error) {
	from, err := loadComparableInventorySet(fromDirectory)
	if err != nil {
		return "", fmt.Errorf("load previous release set: %w", err)
	}
	to, err := loadComparableInventorySet(toDirectory)
	if err != nil {
		return "", fmt.Errorf("load current release set: %w", err)
	}
	return renderInventorySetComparison(from, to), nil
}

func loadComparableInventorySet(directory string) (comparableInventorySet, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return comparableInventorySet{}, err
	}
	if len(paths) == 0 {
		return comparableInventorySet{}, fmt.Errorf("no JSON inventories found in %s", directory)
	}
	sort.Strings(paths)
	set := comparableInventorySet{Artifacts: map[string]comparableArtifact{}}
	seenSources := map[string]struct{}{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return comparableInventorySet{}, fmt.Errorf("read %s: %w", path, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var inventory resolvedStackInventory
		if err := decoder.Decode(&inventory); err != nil {
			return comparableInventorySet{}, fmt.Errorf("decode %s: %w", path, err)
		}
		if err := requireJSONEOF(decoder); err != nil {
			return comparableInventorySet{}, fmt.Errorf("decode %s: %w", path, err)
		}
		if err := validateResolvedStackInventory(inventory); err != nil {
			return comparableInventorySet{}, fmt.Errorf("validate %s: %w", path, err)
		}
		if _, exists := seenSources[inventory.Source.Tag]; !exists {
			seenSources[inventory.Source.Tag] = struct{}{}
			set.Sources = append(set.Sources, inventory.Source)
		}
		requirements := make(map[string]bool, len(inventory.Releases))
		for _, release := range inventory.Releases {
			requirements[resolvedReleaseKey(release.Plane, release.Name)] = release.Required
		}
		for _, artifact := range inventory.Artifacts {
			candidate, err := comparableArtifactFromResolved(artifact, requirements)
			if err != nil {
				return comparableInventorySet{}, fmt.Errorf("%s: %w", path, err)
			}
			key := artifact.Type + "\x00" + artifact.Name
			if existing, exists := set.Artifacts[key]; exists {
				if existing.Reference != candidate.Reference {
					return comparableInventorySet{}, fmt.Errorf("artifact conflict for %s %s: %s and %s", artifact.Type, artifact.Name, existing.Reference, candidate.Reference)
				}
				candidate.Stacks = mergeSortedStrings(existing.Stacks, candidate.Stacks)
				if existing.Requirement == ManifestRequired {
					candidate.Requirement = ManifestRequired
				}
			}
			set.Artifacts[key] = candidate
		}
	}
	sort.Slice(set.Sources, func(i, j int) bool { return set.Sources[i].Tag < set.Sources[j].Tag })
	return set, nil
}

func comparableArtifactFromResolved(artifact resolvedInventoryArtifact, requirements map[string]bool) (comparableArtifact, error) {
	stacks := map[string]struct{}{}
	requirement := ManifestOptional
	for _, source := range artifact.Sources {
		stack, err := stackKeyForPlane(source.Plane)
		if err != nil {
			return comparableArtifact{}, err
		}
		stacks[stack] = struct{}{}
		if resolvedArtifactSourceIsRequired(source, requirements) {
			requirement = ManifestRequired
		}
	}
	owners := make([]string, 0, len(stacks))
	for stack := range stacks {
		owners = append(owners, stack)
	}
	sort.Strings(owners)
	return comparableArtifact{
		Type: artifact.Type, Name: artifact.Name, Reference: artifact.Reference,
		Digest: artifact.Digest, Requirement: requirement, Stacks: owners,
	}, nil
}

func mergeSortedStrings(left, right []string) []string {
	values := make(map[string]struct{}, len(left)+len(right))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		values[value] = struct{}{}
	}
	merged := make([]string, 0, len(values))
	for value := range values {
		merged = append(merged, value)
	}
	sort.Strings(merged)
	return merged
}

func renderInventorySetComparison(from, to comparableInventorySet) string {
	var added, removed, changed []string
	keys := map[string]struct{}{}
	for key := range from.Artifacts {
		keys[key] = struct{}{}
	}
	for key := range to.Artifacts {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		before, hadBefore := from.Artifacts[key]
		after, hasAfter := to.Artifacts[key]
		switch {
		case !hadBefore:
			added = append(added, formatComparableArtifact(after))
		case !hasAfter:
			removed = append(removed, formatComparableArtifact(before))
		default:
			var differences []string
			if before.Reference != after.Reference {
				differences = append(differences, fmt.Sprintf("reference `%s` -> `%s`", before.Reference, after.Reference))
			}
			if before.Digest != after.Digest && before.Reference == after.Reference {
				differences = append(differences, fmt.Sprintf("digest `%s` -> `%s`", valueOrNone(before.Digest), valueOrNone(after.Digest)))
			}
			if before.Requirement != after.Requirement {
				differences = append(differences, fmt.Sprintf("requirement `%s` -> `%s`", before.Requirement, after.Requirement))
			}
			if strings.Join(before.Stacks, ",") != strings.Join(after.Stacks, ",") {
				differences = append(differences, fmt.Sprintf("stacks `%s` -> `%s`", strings.Join(before.Stacks, ", "), strings.Join(after.Stacks, ", ")))
			}
			if len(differences) > 0 {
				changed = append(changed, fmt.Sprintf("- `%s` (%s): %s", after.Name, after.Type, strings.Join(differences, "; ")))
			}
		}
	}

	var report strings.Builder
	report.WriteString("# Release set artifact comparison\n\n")
	report.WriteString("Previous sources: " + formatComparisonSources(from.Sources) + "\n\n")
	report.WriteString("Current sources: " + formatComparisonSources(to.Sources) + "\n\n")
	writeComparisonSection(&report, "Added artifacts", added)
	writeComparisonSection(&report, "Removed artifacts", removed)
	writeComparisonSection(&report, "Changed artifacts", changed)
	unchanged := len(from.Artifacts) - len(removed) - len(changed)
	if unchanged < 0 {
		unchanged = 0
	}
	report.WriteString(fmt.Sprintf("Unchanged artifacts: %d\n", unchanged))
	return report.String()
}

func formatComparableArtifact(artifact comparableArtifact) string {
	return fmt.Sprintf("- `%s` (%s) at `%s`, %s, stacks: `%s`", artifact.Name, artifact.Type, artifact.Reference, artifact.Requirement, strings.Join(artifact.Stacks, ", "))
}

func formatComparisonSources(sources []stackSourceRelease) string {
	formatted := make([]string, len(sources))
	for index, source := range sources {
		formatted[index] = fmt.Sprintf("`%s`", source.Tag)
	}
	return strings.Join(formatted, ", ")
}

func writeComparisonSection(report *strings.Builder, heading string, entries []string) {
	report.WriteString("## " + heading + "\n\n")
	if len(entries) == 0 {
		report.WriteString("None.\n\n")
		return
	}
	for _, entry := range entries {
		report.WriteString(entry + "\n")
	}
	report.WriteString("\n")
}

func valueOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
