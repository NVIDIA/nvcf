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

// Command bazel-consolidation-inventory regenerates the measurements quoted in
// docs/dev/bazel-consolidation.md.
//
// The plan quotes live counts, and those move: an in-flight change to pin the
// worker Bazel versions invalidated part of the first draft within hours of it
// being written. Run this instead of trusting the prose, and update the doc plus
// its measurement stamp when the numbers move.
//
// Design constraint. Earlier versions of this tool answered confidently when
// they could not compute the answer: a failed git call, a broken selector, an
// unreadable file, a no-match, or a declaration the parser did not understand
// each became a plausible zero. A tool whose purpose is to make quoted numbers
// reproducible must fail rather than guess. Every measurement therefore returns
// an error on anything it cannot account for, the report is assembled only after
// all measurements succeed, and nothing is printed on failure.
//
// Two distinctions are load-bearing, because collapsing either produced a real
// defect:
//   - Absent is not unparseable. Zero sources reports "none"; a source that
//     exists but cannot be interpreted is an error.
//   - A clean tree is not an unknown tree. Measurements read working-tree
//     contents of tracked files, so the tree must be clean for the output to
//     correspond to the stamped commit.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	vendorRe          = regexp.MustCompile(`(^|/)vendor/`)
	serviceModuleRe   = regexp.MustCompile(`^src/.*/MODULE\.bazel$`)
	moduleRe          = regexp.MustCompile(`(^|/)MODULE\.bazel$`)
	lockRe            = regexp.MustCompile(`(^|/)MODULE\.bazel\.lock$`)
	bazelrcRe         = regexp.MustCompile(`^src/.*/\.bazelrc$`)
	bazelversionRe    = regexp.MustCompile(`^src/.*/\.bazelversion$`)
	workspaceStatusRe = regexp.MustCompile(`(^|/)workspace_status\.sh$`)
	rulesOciRe        = regexp.MustCompile(`(^|/)rules/oci/`)
	buildRe           = regexp.MustCompile(`(^|/)BUILD\.bazel$`)
	nvcaVendorBuildRe = regexp.MustCompile(`^src/compute-plane-services/nvca/vendor/.*/BUILD\.bazel$`)

	// A Bazel version is a release identifier, not merely a token starting with
	// a digit. Accepting "9-not-a-version" previously let a malformed pin be
	// reported as though it were a real version.
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	semverRe  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	digestRe  = regexp.MustCompile(`^sha256:[0-9a-f]+$`)
	digitsRe  = regexp.MustCompile(`[0-9]+`)
)

func main() {
	allowDirty := flag.Bool("allow-dirty", false,
		"measure a dirty tree; the stamp then says so explicitly")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"usage: %s [--allow-dirty] [repo-root]\n\n"+
				"Regenerates the measurements quoted in docs/dev/bazel-consolidation.md.\n",
			filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()

	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	tree, err := NewTree(root, *allowDirty)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	report, err := BuildReport(tree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(report)
}

// BuildReport computes every measurement, then formats. A failure in any
// measurement aborts before anything is printed, so no partial or
// default-valued report can reach stdout.
func BuildReport(t *Tree) (string, error) {
	serviceModules := t.Select(serviceModuleRe, false)
	allModules := t.Select(moduleRe, true)
	locks := t.Select(lockRe, true)
	bazelrcs := t.Select(bazelrcRe, false)
	workspaceStatus := t.Select(workspaceStatusRe, true)
	ociFiles := t.Select(rulesOciRe, false)
	buildFiles := t.Select(buildRe, false)
	nvcaVendored := t.Select(nvcaVendorBuildRe, true)

	lockBytes, err := t.TotalBytes(locks)
	if err != nil {
		return "", err
	}
	ociLines, err := t.TotalLines(ociFiles)
	if err != nil {
		return "", err
	}
	bazelrcDistinct, err := t.Distinct(bazelrcs)
	if err != nil {
		return "", err
	}
	wsDistinct, err := t.Distinct(workspaceStatus)
	if err != nil {
		return "", err
	}
	versions, err := BazelVersions(t)
	if err != nil {
		return "", err
	}
	subpackages, err := t.Occurrences(buildFiles, "//:__subpackages__")
	if err != nil {
		return "", err
	}
	publicVis, err := t.Occurrences(buildFiles, "//visibility:public")
	if err != nil {
		return "", err
	}
	packageGroups, err := t.Occurrences(buildFiles, "package_group(")
	if err != nil {
		return "", err
	}
	goFirst, err := GoSDKVersions(t, false, "first-party")
	if err != nil {
		return "", err
	}
	goAll, err := GoSDKVersions(t, true, "vendored-inclusive")
	if err != nil {
		return "", err
	}
	pulls, err := OCIPulls(t)
	if err != nil {
		return "", err
	}

	stamp := t.Head
	if t.Dirty {
		stamp += fmt.Sprintf("  (WORKING TREE, not %s: --allow-dirty was passed)", t.Head)
	}
	tagOnly := "-"
	if len(pulls.TagOnly) > 0 {
		tagOnly = strings.Join(pulls.TagOnly, ",")
	}

	lines := []string{
		fmt.Sprintf("measured at: %s", stamp),
		fmt.Sprintf("date:        %s", t.Date),
		"",
		fmt.Sprintf("service modules (src, excluding vendor) : %d", len(serviceModules)),
		fmt.Sprintf("MODULE.bazel total (incl. root/helpers) : %d", len(allModules)),
		fmt.Sprintf("MODULE.bazel.lock files                 : %d", len(locks)),
		fmt.Sprintf("MODULE.bazel.lock total size            : %.1f MB",
			float64(lockBytes)/1048576),
		"",
		fmt.Sprintf("subtree .bazelrc                        : %d (%d distinct)",
			len(bazelrcs), bazelrcDistinct),
		fmt.Sprintf("subtree .bazelversion values            : %s", versions),
		fmt.Sprintf("workspace_status.sh copies              : %d (%d variants)",
			len(workspaceStatus), wsDistinct),
		fmt.Sprintf("rules/oci copied files                  : %d files, %d lines",
			len(ociFiles), ociLines),
		"",
		fmt.Sprintf("root-relative //:__subpackages__         : %d", subpackages),
		fmt.Sprintf("//visibility:public (outside vendor)    : %d", publicVis),
		fmt.Sprintf("package_group definitions               : %d", packageGroups),
		fmt.Sprintf("nvca vendored BUILD.bazel               : %d", len(nvcaVendored)),
		"",
		fmt.Sprintf("go_sdk.download (first-party)           : %s", goFirst),
		fmt.Sprintf("go_sdk.download (incl. vendored)        : %s", goAll),
		fmt.Sprintf("oci.pull declarations                   : %d (%d from nvcr.io)",
			pulls.Declarations, pulls.FromNVCR),
		fmt.Sprintf("oci.pull distinct images / digests      : %d images, %d digests",
			pulls.Images, pulls.Digests),
		fmt.Sprintf("oci.pull without a digest (tag-only)    : %d (%s)",
			len(pulls.TagOnly), tagOnly),
	}
	return strings.Join(lines, "\n"), nil
}

// BazelVersions returns the distribution of subtree Bazel versions.
//
// Zero subtree files is the intended post-consolidation state and reports
// "none". A file that exists but does not contain exactly one release
// identifier is malformed, and is an error rather than an absence: reporting it
// as "none" would announce the plan's success condition on the strength of a
// parse failure.
func BazelVersions(t *Tree) (string, error) {
	paths := t.Select(bazelversionRe, false)
	if len(paths) == 0 {
		return "none (all subtrees use the root version)", nil
	}
	counts := map[string]int{}
	for _, path := range paths {
		text, err := t.Read(path)
		if err != nil {
			return "", err
		}
		var found []string
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			found = append(found, line)
		}
		if len(found) != 1 {
			return "", fmt.Errorf(
				"%s has %d content line(s), expected exactly 1; the file is empty or malformed",
				path, len(found))
		}
		if !versionRe.MatchString(found[0]) {
			return "", fmt.Errorf(
				"%s contains %q, which is not a Bazel release version", path, found[0])
		}
		counts[found[0]]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return versionLess(keys[i], keys[j]) })
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%d on %s  ", counts[k], k)
	}
	return b.String(), nil
}

// GoSDKVersions returns Go SDK pins, distinguishing absence from a declaration
// the parser cannot interpret.
//
// The version is read from the `version = "..."` attribute specifically. An
// earlier implementation scanned the call body for any version-like token,
// which picked up numbers from trailing comments and reported a version for a
// declaration whose version was a variable.
func GoSDKVersions(t *Tree, includeVendor bool, label string) (string, error) {
	paths := t.Select(moduleRe, includeVendor)
	var versions []string
	total := 0
	for _, path := range paths {
		text, err := t.Read(path)
		if err != nil {
			return "", err
		}
		calls, err := FindCalls(text, "go_sdk.download")
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		total += len(calls)
		for _, call := range calls {
			value, literal, found := StringAttr(call.Body, "version")
			if !found {
				return "", fmt.Errorf(
					"%s:%d: %s go_sdk.download has no version attribute",
					path, call.Line, label)
			}
			if !literal {
				return "", fmt.Errorf(
					"%s:%d: %s go_sdk.download version is not a string literal; "+
						"the declaration form is not understood by this parser",
					path, call.Line, label)
			}
			if !semverRe.MatchString(value) {
				return "", fmt.Errorf(
					"%s:%d: %s go_sdk.download version %q is not a Go release version",
					path, call.Line, label, value)
			}
			versions = append(versions, value)
		}
	}
	if total == 0 {
		return "none", nil
	}
	return joinUniqueVersions(versions), nil
}

// Pulls is the oci.pull summary.
type Pulls struct {
	Declarations int
	FromNVCR     int
	Images       int
	Digests      int
	TagOnly      []string
}

// OCIPulls summarizes oci.pull declarations.
//
// Every call must be parsed and classified. An earlier implementation skipped
// calls whose image attribute it could not read, which silently lowered the
// declaration count: an unparseable declaration became an absent one.
func OCIPulls(t *Tree) (Pulls, error) {
	var p Pulls
	images := map[string]bool{}
	digests := map[string]bool{}
	for _, path := range t.Select(moduleRe, false) {
		text, err := t.Read(path)
		if err != nil {
			return Pulls{}, err
		}
		calls, err := FindCalls(text, "oci.pull")
		if err != nil {
			return Pulls{}, fmt.Errorf("%s: %w", path, err)
		}
		for _, call := range calls {
			image, literal, found := StringAttr(call.Body, "image")
			if !found {
				return Pulls{}, fmt.Errorf(
					"%s:%d: oci.pull has no image attribute", path, call.Line)
			}
			if !literal {
				return Pulls{}, fmt.Errorf(
					"%s:%d: oci.pull image is not a string literal; the "+
						"declaration form is not understood by this parser",
					path, call.Line)
			}
			p.Declarations++
			images[image] = true
			if strings.HasPrefix(image, "nvcr.io/") {
				p.FromNVCR++
			}
			digest, digestLiteral, digestFound := StringAttr(call.Body, "digest")
			switch {
			case !digestFound:
				p.TagOnly = append(p.TagOnly, image)
			case !digestLiteral:
				return Pulls{}, fmt.Errorf(
					"%s:%d: oci.pull digest is not a string literal", path, call.Line)
			case !digestRe.MatchString(digest):
				return Pulls{}, fmt.Errorf(
					"%s:%d: oci.pull digest %q is not a sha256 digest",
					path, call.Line, digest)
			default:
				digests[digest] = true
			}
		}
	}
	p.Images = len(images)
	p.Digests = len(digests)
	sort.Strings(p.TagOnly)
	return p, nil
}

func joinUniqueVersions(versions []string) string {
	seen := map[string]bool{}
	var unique []string
	for _, v := range versions {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	sort.Slice(unique, func(i, j int) bool { return versionLess(unique[i], unique[j]) })
	var b strings.Builder
	for _, v := range unique {
		b.WriteString(v)
		b.WriteString(" ")
	}
	return b.String()
}

// versionLess orders version strings numerically, so 1.25.6 precedes 1.25.10.
func versionLess(a, b string) bool {
	av, bv := versionKey(a), versionKey(b)
	for i := 0; i < len(av) && i < len(bv); i++ {
		if av[i] != bv[i] {
			return av[i] < bv[i]
		}
	}
	if len(av) != len(bv) {
		return len(av) < len(bv)
	}
	return a < b
}

func versionKey(v string) []int {
	parts := digitsRe.FindAllString(v, -1)
	key := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			// Unreachable: the regexp matches digits only. Treat as lowest.
			n = 0
		}
		key = append(key, n)
	}
	return key
}
