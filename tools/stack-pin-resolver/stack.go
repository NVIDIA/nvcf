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

// HelmfileDir holds the self-managed stack's release definitions.
const HelmfileDir = "deploy/stacks/self-managed/helmfile.d"

var (
	nameRE    = regexp.MustCompile(`^\s+- name:\s*(\S+)`)
	chartRE   = regexp.MustCompile(`^\s+chart:\s*(.+?)\s*$`)
	versionRE = regexp.MustCompile(`^(\s+version:\s*)([0-9]\S*)(\s*)$`)
	// The shared template most releases inherit. The inner braces are escaped
	// in the helmfile source because helmfile passes the expression through to
	// helm.
	templateChartRE = regexp.MustCompile(`helm-nvcf-\{\{.*?\.Release\.Name.*?\}\}`)
	// An override-with-default line names the real chart inside the default:
	//   chart: {{ $someVar | default "nvcf/helm-nvcf-llm-request-router" | quote }}
	// The default is the chart used unless an operator overrides it, so it is
	// the one an automated bump should follow.
	defaultChartRE = regexp.MustCompile(`default\s+"([^"]+)"`)
)

// A Release is one pinned entry in the stack.
type Release struct {
	Name string
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
			value, found = m[1], true
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
	paths, err := filepath.Glob(filepath.Join(root, HelmfileDir, "*.yaml.gotmpl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var out []Release
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		lines := strings.Split(string(b), "\n")
		for _, blk := range splitReleases(lines) {
			body := lines[blk.start : blk.end+1]

			versionLine, version := -1, ""
			for i, line := range body {
				if m := versionRE.FindStringSubmatch(line); m != nil {
					versionLine, version = blk.start+i, m[2]
					break
				}
			}
			if versionLine < 0 {
				// Not a pin. The shared templates block and the repositories
				// block both match the release shape but carry no version.
				continue
			}

			r := Release{Name: blk.name, File: filepath.Base(path), Version: version, VersionLine: versionLine}
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
		blocks = append(blocks, block{name: m[1], start: i, end: len(lines) - 1})
	}
	return blocks
}

// WritePin replaces the version on a single release's pin line.
func WritePin(root string, r Release, version string) error {
	path := filepath.Join(root, HelmfileDir, r.File)
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(b), "\n")
	if r.VersionLine < 0 || r.VersionLine >= len(lines) {
		return fmt.Errorf("%s: pin line %d is out of range for %s", r.Release(), r.VersionLine, path)
	}
	m := versionRE.FindStringSubmatch(lines[r.VersionLine])
	if m == nil {
		// The file changed under us. Rewriting a line that is no longer a pin
		// would corrupt the stack, so stop instead.
		return fmt.Errorf("%s: line %d in %s is no longer a version pin", r.Release(), r.VersionLine+1, path)
	}
	lines[r.VersionLine] = m[1] + version + m[3]

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
