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

// Action is what a chart can do with a released version.
type Action int

const (
	// ActionBoth moves appVersion and the image tag that agrees with it.
	ActionBoth Action = iota
	// ActionAppVersionOnly moves appVersion; the chart sets no image tag.
	ActionAppVersionOnly
	// ActionRefuse moves nothing and reports why.
	ActionRefuse
	// ActionSkip moves nothing because there is no chart to move.
	ActionSkip
)

// Floating tags are not pins. Replacing one with a version is a behaviour
// change rather than a bump, so a chart carrying one is refused.
var floating = map[string]bool{
	"latest": true,
	"main":   true,
	"stable": true,
	"edge":   true,
}

// Chart and values files are rewritten line by line rather than round-tripped
// through a YAML library, which would discard comments, key order, and quoting
// style across the whole file for the sake of one value.
var (
	appVersionRE = regexp.MustCompile(`(?m)^(appVersion:\s*)"?([^"\s#]+)"?(.*)$`)
	tagRE        = regexp.MustCompile(`(?m)^\s+tag:\s*"?([^"\s#]*)"?`)
)

// A Plan is what to do for one chart.
type Plan struct {
	Action  Action
	Detail  string
	Current string
	Tags    []string
}

// ChartFiles locates a chart's Chart.yaml and values.yaml under chartPath.
// Some chart directories hold the chart directly; others nest it one level
// down.
func ChartFiles(root, chartPath string) (chartYAML, valuesYAML string) {
	base := filepath.Join(root, chartPath)
	candidates := []string{filepath.Join(base, "Chart.yaml")}
	if nested, err := filepath.Glob(filepath.Join(base, "*", "Chart.yaml")); err == nil {
		sort.Strings(nested)
		candidates = append(candidates, nested...)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, filepath.Join(filepath.Dir(c), "values.yaml")
		}
	}
	return "", ""
}

// Plan decides what one chart can do with the released version.
func PlanFor(root string, chart Entry, version string) (Plan, error) {
	chartYAML, valuesYAML := ChartFiles(root, chart.Path)
	if chartYAML == "" {
		return Plan{Action: ActionSkip, Detail: fmt.Sprintf("no Chart.yaml under %s", chart.Path)}, nil
	}

	b, err := os.ReadFile(chartYAML)
	if err != nil {
		return Plan{}, fmt.Errorf("read %s: %w", chartYAML, err)
	}
	m := appVersionRE.FindStringSubmatch(string(b))
	if m == nil {
		return Plan{Action: ActionSkip, Detail: "chart declares no appVersion"}, nil
	}
	current := m[2]

	var tags []string
	if vb, err := os.ReadFile(valuesYAML); err == nil {
		for _, hit := range tagRE.FindAllStringSubmatch(string(vb), -1) {
			if hit[1] != "" {
				tags = append(tags, hit[1])
			}
		}
	} else if !os.IsNotExist(err) {
		return Plan{}, fmt.Errorf("read %s: %w", valuesYAML, err)
	}

	// Agreement first. A chart may ship several images, and only the tag equal
	// to appVersion is rewritten, so a floating tag on a different image is none
	// of this service's business. Checking floating first refused the whole
	// chart because a sidecar was pinned to latest.
	for _, t := range tags {
		if t == current {
			return Plan{ActionBoth, "appVersion and image tag agree", current, tags}, nil
		}
	}
	if len(tags) == 0 {
		return Plan{ActionAppVersionOnly, "no image tag set", current, tags}, nil
	}
	// No tag agrees, so one of these is this service's image. If any of them
	// floats there is nothing safe to rewrite: latest is not a pin, and
	// replacing it with a version is a behaviour change rather than a bump.
	var floats []string
	for _, t := range tags {
		if floating[t] {
			floats = append(floats, t)
		}
	}
	if len(floats) > 0 {
		return Plan{ActionRefuse, "image tag is floating (" + strings.Join(floats, ", ") + ")", current, tags}, nil
	}
	return Plan{
		ActionRefuse,
		fmt.Sprintf("appVersion %s does not match image tag(s) %s", current, strings.Join(tags, ", ")),
		current,
		tags,
	}, nil
}

// Apply writes the planned change for one chart.
func Apply(root string, chart Entry, version string, p Plan) error {
	chartYAML, valuesYAML := ChartFiles(root, chart.Path)
	b, err := os.ReadFile(chartYAML)
	if err != nil {
		return fmt.Errorf("read %s: %w", chartYAML, err)
	}
	text := string(b)
	current := appVersionRE.FindStringSubmatch(text)[2]

	// Read before the first write. Writing Chart.yaml and then failing to read
	// values.yaml leaves appVersion moved with the image tag behind, which is
	// exactly the drift state the next run refuses.
	var vb []byte
	if p.Action == ActionBoth {
		vb, err = os.ReadFile(valuesYAML)
		if err != nil {
			return fmt.Errorf("read %s: %w", valuesYAML, err)
		}
	}

	replaced := false
	updated := appVersionRE.ReplaceAllStringFunc(text, func(line string) string {
		if replaced {
			return line
		}
		replaced = true
		g := appVersionRE.FindStringSubmatch(line)
		return fmt.Sprintf("%s%q%s", g[1], version, g[3])
	})
	if err := writeFilePreservingMode(chartYAML, updated); err != nil {
		return err
	}

	if p.Action != ActionBoth {
		return nil
	}
	// Replace only tag lines holding the value appVersion also held. Any other
	// tag in this file belongs to a different image, and moving it would point
	// a sidecar at a version that was never built for it.
	matching := regexp.MustCompile(`(?m)^(\s+tag:\s*)"?` + regexp.QuoteMeta(current) + `"?(\s*(?:#.*)?)$`)
	out := matching.ReplaceAllString(string(vb), fmt.Sprintf(`${1}"%s"${2}`, version))
	return writeFilePreservingMode(valuesYAML, out)
}

func writeFilePreservingMode(path, content string) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
