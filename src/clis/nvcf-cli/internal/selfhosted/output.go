/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package selfhosted

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// RenderText writes linkerd-style green/red/warn checkmarks grouped by category
// to w. Format mirrors `linkerd check` output (§5.3 of the SRD/SDD).
func RenderText(w io.Writer, results []CheckResult) {
	groups := groupByCategory(results)
	categoryOrder := orderedCategories(groups)
	overall := overallStatus(results)

	for _, cat := range categoryOrder {
		fmt.Fprintln(w, cat)
		fmt.Fprintln(w, strings.Repeat("-", len(cat)))
		for _, r := range groups[cat] {
			fmt.Fprintln(w, mark(r), r.Message)
			if !r.Passed && r.HintURL != "" {
				fmt.Fprintln(w, "    see", r.HintURL, "for hints")
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Status check results are %s\n", statusGlyph(overall))
}

// RenderJSON emits the schema documented in §6.2.
func RenderJSON(w io.Writer, results []CheckResult) {
	type checkOut struct {
		ID       string `json:"id"`
		Category string `json:"category"`
		Severity string `json:"severity"`
		Passed   bool   `json:"passed"`
		Message  string `json:"message"`
		HintURL  string `json:"hint_url,omitempty"`
	}
	out := struct {
		Status string     `json:"status"`
		Checks []checkOut `json:"checks"`
	}{
		Status: overallStatus(results),
	}
	for _, r := range results {
		out.Checks = append(out.Checks, checkOut{
			ID: r.ID, Category: r.Category, Severity: r.Severity,
			Passed: r.Passed, Message: r.Message, HintURL: r.HintURL,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func mark(r CheckResult) string {
	switch {
	case r.Passed:
		return "✓"
	case r.Severity == "warning":
		return "‼"
	default:
		return "×"
	}
}

func statusGlyph(status string) string {
	switch status {
	case "ok":
		return "✓"
	case "warning":
		return "‼"
	default:
		return "×"
	}
}

func overallStatus(results []CheckResult) string {
	hasWarn := false
	for _, r := range results {
		if !r.Passed && r.Severity == "error" {
			return "error"
		}
		if !r.Passed && r.Severity == "warning" {
			hasWarn = true
		}
	}
	if hasWarn {
		return "warning"
	}
	return "ok"
}

func groupByCategory(results []CheckResult) map[string][]CheckResult {
	g := make(map[string][]CheckResult)
	for _, r := range results {
		g[r.Category] = append(g[r.Category], r)
	}
	return g
}

func orderedCategories(g map[string][]CheckResult) []string {
	cats := make([]string, 0, len(g))
	for c := range g {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	return cats
}
