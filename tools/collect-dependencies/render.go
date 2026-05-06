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
	"os"
	"regexp"
	"sort"
	"strings"
)

func renderGroupedDependencyRows(rows []dependencyRow) []string {
	if len(rows) == 0 {
		return nil
	}
	grouped := map[string][]dependencyRow{}
	for _, row := range rows {
		group := normalizeLicenseGroup(row.License)
		grouped[group] = append(grouped[group], row)
	}
	groupNames := sortedMapKeys(grouped)
	sort.Slice(groupNames, func(i, j int) bool {
		if groupNames[i] == "Unresolved" {
			return false
		}
		if groupNames[j] == "Unresolved" {
			return true
		}
		return strings.ToLower(groupNames[i]) < strings.ToLower(groupNames[j])
	})

	lines := []string{}
	for _, group := range groupNames {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "## "+group, "")
		rows := grouped[group]
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Language != rows[j].Language {
				return rows[i].Language < rows[j].Language
			}
			return rows[i].SortKey < rows[j].SortKey
		})
		for _, row := range rows {
			line := fmt.Sprintf("- `%s`: %s", row.Language, row.Spec)
			if group == "Unresolved" {
				note := strings.TrimSpace(row.License)
				if note == "" {
					note = "_(license not resolved)_"
				}
				line += " - " + note
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func normalizeLicenseGroup(raw string) string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" || (strings.HasPrefix(normalized, "_(") && strings.HasSuffix(normalized, ")_")) {
		return "Unresolved"
	}
	replacements := []struct {
		old *regexp.Regexp
		new string
	}{
		{regexp.MustCompile(`(?i)\b(?:the )?apache(?: software)? license(?:,?\s*version)?\s*2(?:\.0)?\b`), "Apache-2.0"},
		{regexp.MustCompile(`(?i)\bapache[- ]?2(?:\.0)?\b`), "Apache-2.0"},
		{regexp.MustCompile(`(?i)\bthe mit license\b`), "MIT"},
		{regexp.MustCompile(`(?i)\bmit license\b`), "MIT"},
		{regexp.MustCompile(`(?i)\bmozilla public license\b[^0-9]*(1\.0|1\.1|2\.0)\b`), "MPL-$1"},
		{regexp.MustCompile(`(?i)\bmpl\b[^0-9]*(1\.0|1\.1|2\.0)\b`), "MPL-$1"},
		{regexp.MustCompile(`(?i)\beclipse public license\b[^0-9]*(1\.0|2\.0)\b`), "EPL-$1"},
		{regexp.MustCompile(`(?i)\bepl[- ]?(1\.0|2\.0)\b`), "EPL-$1"},
		{regexp.MustCompile(`(?i)\bbsd[- ]?2[- ]clause\b`), "BSD-2-Clause"},
		{regexp.MustCompile(`(?i)\bbsd[- ]?3[- ]clause\b`), "BSD-3-Clause"},
		{regexp.MustCompile(`(?i)\bwtfpl\b`), "WTFPL"},
	}
	for _, replacement := range replacements {
		normalized = replacement.old.ReplaceAllString(normalized, replacement.new)
	}
	normalized = regexp.MustCompile(`(?i)\bgplv?2\b`).ReplaceAllString(normalized, "GPL-2.0")
	normalized = regexp.MustCompile(`(?i)\bw/\s*`).ReplaceAllString(normalized, "with ")
	normalized = regexp.MustCompile(`(?i)\bcpe\b`).ReplaceAllString(normalized, "Classpath Exception")
	normalized = regexp.MustCompile(`\s+\+\s+`).ReplaceAllString(normalized, " / ")
	normalized = regexp.MustCompile(`\s*/\s*`).ReplaceAllString(normalized, " / ")
	normalized = regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(normalized), " ")
	return normalized
}

func writeMarkdown(path, header string, lines []string) error {
	body := ""
	if len(lines) == 0 {
		body = "\n_(none)_\n"
	} else {
		body = strings.Join(lines, "\n") + "\n"
	}
	return os.WriteFile(path, []byte(header+body), 0o644)
}
