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
var semverRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

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

// CompareVersions orders two versions: negative when a sorts below b, zero
// when they are the same version, positive when a sorts above b. ok is false
// when either side is not a version, so a caller can tell "not comparable"
// from "equal".
//
// This is SemVer 2.0.0 precedence with one addition. A pre-release of the
// form nv-X.Y.Z is a wrapper release built from the upstream version in front
// of it, as in the collector's 0.160.0-nv-0.4.7, not a pre-release of that
// upstream version. Plain SemVer would compare nv-9 and nv-10 as text and
// would put a wrapper below the bare upstream version it wraps. So the
// upstream core is compared first, then the wrapper release as a version of
// its own, and a wrapper sorts above the bare upstream version.
func CompareVersions(a, b string) (int, bool) {
	am := semverRE.FindStringSubmatch(strings.TrimSpace(a))
	bm := semverRE.FindStringSubmatch(strings.TrimSpace(b))
	if am == nil || bm == nil {
		return 0, false
	}
	for i := 1; i <= 3; i++ {
		if c := compareNumeric(am[i], bm[i]); c != 0 {
			return c, true
		}
	}
	aWrapper, aWrapped := wrapperRelease(am[4])
	bWrapper, bWrapped := wrapperRelease(bm[4])
	switch {
	case aWrapped && bWrapped:
		return CompareVersions(aWrapper, bWrapper)
	case aWrapped:
		return 1, true
	case bWrapped:
		return -1, true
	}
	return comparePrerelease(am[4], bm[4]), true
}

// wrapperRelease returns X.Y.Z from a pre-release of the form nv-X.Y.Z.
func wrapperRelease(prerelease string) (string, bool) {
	rest, ok := strings.CutPrefix(prerelease, "nv-")
	if !ok || !semverRE.MatchString(rest) {
		return "", false
	}
	return rest, true
}

// comparePrerelease applies SemVer 2.0.0 rule 11 to two pre-release strings.
// No pre-release sorts above any pre-release. Identifiers compare one by one:
// numeric ones by value, others as ASCII text, and numeric below text. When
// every shared identifier is equal, the longer list sorts higher.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		aNum, bNum := isNumeric(as[i]), isNumeric(bs[i])
		var c int
		switch {
		case aNum && bNum:
			c = compareNumeric(as[i], bs[i])
		case aNum:
			c = -1
		case bNum:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return len(as) - len(bs)
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// compareNumeric compares two digit strings by value without parsing them,
// so no length of number overflows.
func compareNumeric(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
