// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// StacksGlob matches every stack's release definitions. A chart is not
// necessarily deployed by the self-managed stack: nvca-operator, for
// example, is pinned only in deploy/stacks/nvcf-compute-plane. Scanning every
// stack instead of one hardcoded directory is what lets Audit's "no chart
// resolves to this release" check mean what it says.
const StacksGlob = "deploy/stacks/*/helmfile.d"

var (
	nameRE  = regexp.MustCompile(`^(\s+)- name:\s*(\S+)`)
	chartRE = regexp.MustCompile(`^(\s+)chart:\s*(.+?)\s*$`)
	// Split so the value can be validated separately from the line shape. A
	// version line whose value is not a version is reported, not skipped.
	versionLineRE = regexp.MustCompile(`^(\s+version:\s*)(.*?)(\s*)$`)
	// Bare, or quoted on both ends. Nothing in the stack is quoted today, but a
	// value that gains quotes must not silently stop being recognised as a pin.
	//
	// Optional quotes on each end independently would accept `"1.0.0` and
	// `1.0.0"`, which are malformed YAML. Treating those as a valid pin means
	// the rewrite replaces the line and quietly launders the error away instead
	// of stopping and reporting it.
	versionValueRE = regexp.MustCompile(`^(?:v?[0-9][^"\s]*|"v?[0-9][^"\s]*")$`)
	// The shared template most releases inherit. The inner braces are escaped
	// in the helmfile source because helmfile passes the expression through to
	// helm.
	templateChartRE = regexp.MustCompile(`helm-nvcf-\{\{.*?\.Release\.Name.*?\}\}`)
	// An override-with-default line names the real chart inside the default:
	//   chart: {{ $someVar | default "nvcf/helm-nvcf-llm-request-router" | quote }}
	// The default is the chart used unless an operator overrides it, so it is
	// the one an automated bump should follow.
	defaultChartRE = regexp.MustCompile(`default\s+"([^"]+)"`)
	// A version line templated the same way an environment can override any
	// other release field:
	//   version: {{ dig "prometheusOperatorCrds" "version" "31.0.1" .Values | quote }}
	// dig's last argument before .Values is its default, used unless an
	// environment file sets prometheusOperatorCrds.version. That default is
	// the version this chart actually ships at absent an override, so it is
	// the value an automated bump reads and rewrites. The non-capturing group
	// consumes every earlier quoted dig key so the capture always lands on the
	// last one.
	digVersionDefaultRE = regexp.MustCompile(`dig\s+(?:"[^"]*"\s+)*"([^"]*)"\s+\.Values`)
)

// A Release is one pinned entry in the stack.
type Release struct {
	Name string
	// File is the helmfile path relative to root, for example
	// deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl. Kept
	// relative to root, not just the basename, so WritePin can reconstruct
	// the full path directly: a release can live in any stack, not only the
	// one a caller happens to be thinking about.
	File string
	// Chart is the published chart name this release pins, empty when
	// Unresolved is set.
	Chart string
	// Unresolved says why the chart could not be determined. A release that
	// cannot be read might be the one that pins the chart being bumped, which
	// is why an unresolved entry blocks the whole run rather than being skipped.
	Unresolved string
	Version    string
	// VersionLine is the index into the file's lines holding the pin, so the
	// rewrite can replace exactly that line and nothing else.
	VersionLine int
	// DigDefault is set when the version pin is a
	// `dig ... "<default>" .Values` template expression rather than a plain
	// scalar. WritePin then replaces only the quoted default inside that
	// expression, leaving the surrounding template untouched.
	DigDefault bool
}

// ChartNameForRelease returns the chart a stack release pins.
//
// The helmfile names a chart in one of three ways, and the third cannot be
// resolved by reading the file:
//
//	explicit    chart: nvcf/helm-reval
//	convention  no chart: line, inherits a template of the form
//	            nvcf/helm-nvcf-{{ .Release.Name }}
//	templated   chart: {{ ... }} with anything else inside
//
// A templated chart line is reported as unresolved rather than guessed at.
func ChartNameForRelease(releaseName string, body []string) (string, error) {
	value := ""
	found := false
	for _, line := range body {
		if m := chartRE.FindStringSubmatch(line); m != nil {
			value, found = m[2], true
			break
		}
	}
	if !found {
		// No chart line: inherits the shared template, which appends the
		// release name to a fixed prefix.
		return "helm-nvcf-" + releaseName, nil
	}
	if !strings.Contains(value, "{{") {
		return lastPathSegment(value), nil
	}
	if templateChartRE.MatchString(value) {
		return "helm-nvcf-" + releaseName, nil
	}
	if m := defaultChartRE.FindStringSubmatch(value); m != nil {
		return lastPathSegment(m[1]), nil
	}
	return "", fmt.Errorf("chart line is templated and not a known form: %s", value)
}

func lastPathSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// LoadStack returns every pinned release in the stack.
//
// Split line by line rather than with one regex over the whole file: matching a
// release block needs "up to the next release or end of file", which is a
// lookahead, and Go's regexp engine has none. Tracking line numbers also lets
// the rewrite replace one exact line instead of reconstructing a block.
func LoadStack(root string) ([]Release, error) {
	paths, err := filepath.Glob(filepath.Join(root, StacksGlob, "*.yaml.gotmpl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var out []Release
	for _, path := range paths {
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("relativize %s: %w", path, err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		lines := strings.Split(string(b), "\n")
		for _, blk := range splitReleases(lines) {
			body := lines[blk.start : blk.end+1]

			// Only at the release's own field indent. A version: nested deeper
			// belongs to a values block or a sub-object, and treating it as the
			// pin would rewrite an unrelated key.
			fieldIndent := blk.indent + 2
			versionLine, version, malformed := -1, "", ""
			digDefault := false
			for i, line := range body {
				m := versionLineRE.FindStringSubmatch(line)
				if m == nil || len(m[1])-len("version:")-countTrailingSpace(m[1]) != fieldIndent {
					continue
				}
				if !versionValueRE.MatchString(m[2]) {
					if dm := digVersionDefaultRE.FindStringSubmatch(m[2]); dm != nil {
						versionLine, version, digDefault = blk.start+i, dm[1], true
						break
					}
					// A release-level version that cannot be read is reported.
					// Skipping it would drop a real pin silently, which is the
					// failure this tool exists to prevent.
					malformed = m[2]
					break
				}
				versionLine, version = blk.start+i, strings.Trim(m[2], `"`)
				break
			}
			if versionLine < 0 && malformed == "" {
				// Not a pin. The shared templates block and the repositories
				// block both match the release shape but carry no version.
				continue
			}

			r := Release{Name: blk.name, File: relPath, Version: version, VersionLine: versionLine, DigDefault: digDefault}
			if malformed != "" {
				r.Unresolved = fmt.Sprintf("version is not a recognisable pin: %s", malformed)
				out = append(out, r)
				continue
			}
			chart, err := ChartNameForRelease(blk.name, body)
			if err != nil {
				r.Unresolved = err.Error()
			} else {
				r.Chart = chart
			}
			out = append(out, r)
		}
	}
	return out, nil
}

type block struct {
	name       string
	indent     int
	start, end int
}

func splitReleases(lines []string) []block {
	var blocks []block
	for i, line := range lines {
		m := nameRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if n := len(blocks); n > 0 {
			blocks[n-1].end = i - 1
		}
		blocks = append(blocks, block{name: m[2], indent: len(m[1]), start: i, end: len(lines) - 1})
	}
	return blocks
}

// countTrailingSpace counts the run of spaces at the end of s, which is how the
// indent of a "version:" line is recovered from its captured prefix.
func countTrailingSpace(s string) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == ' '; i-- {
		n++
	}
	return n
}

// WritePin replaces the version on a single release's pin line.
func WritePin(root string, r Release, version string) error {
	path := filepath.Join(root, r.File)
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(b), "\n")
	if r.VersionLine < 0 || r.VersionLine >= len(lines) {
		return fmt.Errorf("%s: pin line %d is out of range for %s", r.Release(), r.VersionLine, path)
	}
	m := versionLineRE.FindStringSubmatch(lines[r.VersionLine])
	if m == nil {
		// The file changed under us. Rewriting a line that is no longer a pin
		// would corrupt the stack, so stop instead.
		return fmt.Errorf("%s: line %d in %s is no longer a version pin", r.Release(), r.VersionLine+1, path)
	}
	if r.DigDefault {
		// Replace only the quoted default inside the dig(...) expression, so
		// the template around it, and any environment override it still
		// respects, survive the bump untouched.
		idx := digVersionDefaultRE.FindStringSubmatchIndex(m[2])
		if idx == nil {
			return fmt.Errorf("%s: line %d in %s is no longer a dig-default version pin", r.Release(), r.VersionLine+1, path)
		}
		lines[r.VersionLine] = m[1] + m[2][:idx[2]] + version + m[2][idx[3]:] + m[3]
	} else {
		lines[r.VersionLine] = m[1] + version + m[3]
	}

	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Release names the release for error messages.
func (r Release) Release() string { return r.Name }
