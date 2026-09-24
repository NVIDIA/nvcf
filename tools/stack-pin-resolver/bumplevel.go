// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"regexp"
	"strconv"
	"strings"
)

// semverRE is the full SemVer 2.0.0 grammar with an optional leading v:
// MAJOR.MINOR.PATCH, an optional pre-release of dot-separated non-empty
// identifiers, and an optional build suffix. A trailing "-" or an empty
// identifier is not a version and yields no level.
var semverRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// BumpLevel names the semver step from one version to a higher one: "major",
// "minor" or "patch". It is "" when either side is not MAJOR.MINOR.PATCH or
// when the move is not upward, so a caller can tell "no level" from "patch".
//
// The workflow turns the level into the Conventional Commit type of the bump
// commit, so the chart's own release (and, one step later, the stack's)
// carries the same semver step as the service release that caused it.
func BumpLevel(from, to string) string {
	a, ok := parseCore(from)
	if !ok {
		return ""
	}
	b, ok := parseCore(to)
	if !ok {
		return ""
	}
	switch {
	case b[0] > a[0]:
		return "major"
	case b[0] < a[0]:
		return ""
	case b[1] > a[1]:
		return "minor"
	case b[1] < a[1]:
		return ""
	case b[2] > a[2]:
		return "patch"
	}
	return ""
}

// HigherLevel returns the larger of two levels; "" is the lowest.
func HigherLevel(a, b string) string {
	rank := map[string]int{"": 0, "patch": 1, "minor": 2, "major": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func parseCore(v string) ([3]int, bool) {
	var out [3]int
	m := semverRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return out, false
	}
	for i, p := range m[1:4] {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
