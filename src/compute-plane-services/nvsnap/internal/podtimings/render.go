/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package podtimings

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// RenderOptions controls table presentation. Zero value (no namespace
// shown, no node shown) matches single-namespace single-node use.
type RenderOptions struct {
	// ShowNamespace adds a NAMESPACE column. Set when listing across
	// namespaces — operators want to know where each pod lives.
	ShowNamespace bool

	// ShowNode adds a NODE column. Useful for debugging cross-node
	// cascade fetch behavior; off by default to keep the line short.
	ShowNode bool

	// MaxNameLen truncates pod names. 0 means no truncation. Helps
	// when stdout is a narrow terminal; we don't auto-detect width.
	MaxNameLen int
}

// RenderTable writes a human-readable table of timings followed by a
// per-function summary footer. Intended for stdout when --json is
// not set.
//
// Output shape (single namespace, no node column):
//
//	NAME                              MODE        READY-IN  HASH              FVID
//	0-sr-1a9b3b3.fastapi-cold        cold-start  3m08s     -                 cd1116dc...
//	0-sr-68599d4.fastapi-restore     restored    43s       85ec4d75ee57c1be  cd1116dc...
//	...
//
//	SUMMARY (1 fvID):
//	  cd1116dc-...:  1 cold (avg 3m08s)  39 restored (avg 43s)  →  4.4× faster, 1h14m saved
func RenderTable(w io.Writer, timings []PodTiming, opts RenderOptions) error {
	if len(timings) == 0 {
		_, err := fmt.Fprintln(w, "No pods found.")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// Header
	header := []any{"NAME"}
	if opts.ShowNamespace {
		header = append([]any{"NAMESPACE"}, header...)
	}
	header = append(header, "MODE", "READY-IN", "HASH", "FVID")
	if opts.ShowNode {
		header = append(header, "NODE")
	}
	_, _ = fmt.Fprintln(tw, joinTabs(header))

	for i := range timings {
		t := &timings[i]
		row := []any{}
		if opts.ShowNamespace {
			row = append(row, t.Namespace)
		}
		row = append(row,
			truncate(t.Name, opts.MaxNameLen),
			string(t.Mode),
			formatReady(*t),
			valueOrDash(t.Hash),
			valueOrDash(shortFVID(t.FunctionVersionID)),
		)
		if opts.ShowNode {
			row = append(row, valueOrDash(t.NodeName))
		}
		_, _ = fmt.Fprintln(tw, joinTabs(row))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	// Per-function summary footer
	groups := SummaryByFunction(timings)
	_, _ = fmt.Fprintln(w)
	if len(groups) == 1 {
		_, _ = fmt.Fprintln(w, "SUMMARY:")
	} else {
		_, _ = fmt.Fprintf(w, "SUMMARY (%d function-versions):\n", len(groups))
	}
	for _, g := range groups {
		_, _ = fmt.Fprintln(w, "  "+formatGroup(g))
	}
	return nil
}

// RenderJSON writes the same data as JSON, with a top-level
// {timings, summary, groups} envelope. The summary is the
// overall (all-fvIDs) stats; groups is the per-fvID breakdown.
func RenderJSON(w io.Writer, timings []PodTiming) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Timings []PodTiming     `json:"timings"`
		Summary Stats           `json:"summary"`
		Groups  []FunctionGroup `json:"groups"`
	}{
		Timings: timings,
		Summary: Summary(timings),
		Groups:  SummaryByFunction(timings),
	})
}

func formatGroup(g FunctionGroup) string {
	fvID := shortFVID(g.FunctionVersionID)
	if fvID == "" {
		fvID = "(unattributed)"
	}
	s := g.Stats
	line := fmt.Sprintf("%s:  ", fvID)
	parts := []string{}
	if s.Cold > 0 {
		parts = append(parts, fmt.Sprintf("%d cold (avg %s)", s.Cold, formatDuration(s.AvgCold)))
	}
	if s.Restored > 0 {
		parts = append(parts, fmt.Sprintf("%d restored (avg %s)", s.Restored, formatDuration(s.AvgRestored)))
	}
	if s.NotReady > 0 {
		parts = append(parts, fmt.Sprintf("%d not-ready", s.NotReady))
	}
	line += joinComma(parts)
	if s.SpeedupX > 0 {
		line += fmt.Sprintf("  →  %.1f× faster, %s saved", s.SpeedupX, formatDuration(s.TotalSavings))
	}
	return line
}

func formatReady(t PodTiming) string {
	if t.NotReady {
		return "not-ready"
	}
	return formatDuration(t.Duration)
}

// formatDuration returns "3m08s" / "43s" / "1h14m". Avoids
// fractional seconds for stable golden-file tests and easier reading.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)

	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0 && s > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// shortFVID returns the first 8 chars of a UUID-shaped fvID, or the
// whole value if shorter / non-UUID. Operators recognize their
// functions by the first segment.
func shortFVID(fvID string) string {
	const n = 8
	if len(fvID) <= n {
		return fvID
	}
	if fvID[8] == '-' { // looks like a UUID — show prefix + ellipsis
		return fvID[:n] + "..."
	}
	return fvID[:n]
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinTabs(parts []any) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\t"
		}
		out += fmt.Sprintf("%v", p)
	}
	return out
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
