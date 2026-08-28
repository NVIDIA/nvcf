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
	tagLineRE    = regexp.MustCompile(`^(\s+)tag:\s*"?([^"\s#]*)"?`)
	// An image: key carrying a value on the same line, rather than opening a
	// block. Covers a flow mapping, image: { tag: "1.0.0" }, and a plain scalar
	// reference, image: registry/name:tag.
	inlineImageRE = regexp.MustCompile(`(?m)^\s*image:[ \t]*[^ \t\n#].*$`)
	keyLineRE     = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.-]+):`)
)

// An imageTag is a tag: entry that sits directly under an image: key, together
// with the line it was found on.
type imageTag struct {
	line  int
	value string
}

// imageTags returns the tag: entries that belong to an image block.
//
// Matching every indented tag: key instead would reach unrelated fields. A
// values.yaml may carry a tag: that is not an image tag at all, and one of those
// holding the same string as appVersion would be selected and rewritten, while
// one holding something else could refuse a chart whose image tag was fine.
// Ownership is decided by where the key sits, not by what it is called.
func imageTags(lines []string) []imageTag {
	var out []imageTag
	for i, l := range lines {
		m := tagLineRE.FindStringSubmatch(l)
		if m == nil || m[2] == "" {
			continue
		}
		indent := len(m[1])
		// The nearest preceding key at a smaller indent is this key's parent.
		for j := i - 1; j >= 0; j-- {
			pm := keyLineRE.FindStringSubmatch(lines[j])
			if pm == nil || len(pm[1]) >= indent {
				continue
			}
			if pm[2] == "image" {
				out = append(out, imageTag{line: i, value: m[2]})
			}
			break
		}
	}
	return out
}

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
		// An image declared inline rather than as a block is refused, not parsed.
		// imageTags finds nothing in it, which would look identical to a chart
		// that sets no tag at all: appVersion would move on its own and the
		// deployed image would stay where it was. A silent half-bump is worse
		// than a stop, and a YAML parser is a large answer to a shape no chart
		// here uses.
		if m := inlineImageRE.FindString(string(vb)); m != "" {
			return Plan{
				ActionRefuse,
				fmt.Sprintf("image is declared inline (%s), so its tag cannot be located", strings.TrimSpace(m)),
				current,
				nil,
			}, nil
		}
		for _, it := range imageTags(strings.Split(string(vb), "\n")) {
			tags = append(tags, it.value)
		}
	} else if !os.IsNotExist(err) {
		return Plan{}, fmt.Errorf("read %s: %w", valuesYAML, err)
	}

	if len(tags) == 0 {
		return Plan{ActionAppVersionOnly, "no image tag set", current, tags}, nil
	}

	// More than one image, and nothing says which belongs to the released
	// service. A tag equal to appVersion is not evidence of ownership: a chart
	// whose appVersion has fallen behind its own image can still match an
	// unrelated sidecar that happens to sit on that version, and the bump would
	// then move the sidecar and leave the service image alone. That is a wrong
	// edit dressed as a routine version bump, which is the failure this tool
	// exists to avoid, so it refuses instead.
	//
	// Every chart with a declared service edge currently ships zero or one image
	// tag, so nothing is blocked by this today. Charts that grow a second image
	// need a way to name the service's own tag before they can be bumped.
	if len(tags) > 1 {
		return Plan{
			ActionRefuse,
			fmt.Sprintf("chart declares %d image tags (%s) and none is marked as this service's, so the one to move cannot be identified",
				len(tags), strings.Join(tags, ", ")),
			current,
			tags,
		}, nil
	}

	// Exactly one image, so agreement is unambiguous evidence.
	if floating[tags[0]] {
		// latest is not a pin, and replacing it with a version is a behaviour
		// change rather than a bump.
		return Plan{ActionRefuse, "image tag is floating (" + tags[0] + ")", current, tags}, nil
	}
	if tags[0] == current {
		return Plan{ActionBoth, "appVersion and image tag agree", current, tags}, nil
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
	// Rewrite by line, and only lines imageTags identified. A regex over the
	// whole file would reach a tag: outside an image block that happens to hold
	// the same value.
	lines := strings.Split(string(vb), "\n")
	for _, it := range imageTags(lines) {
		if it.value != current {
			continue
		}
		m := tagLineRE.FindStringSubmatch(lines[it.line])
		suffix := lines[it.line][len(m[0]):]
		lines[it.line] = fmt.Sprintf("%stag: %q%s", m[1], version, suffix)
	}
	return writeFilePreservingMode(valuesYAML, strings.Join(lines, "\n"))
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
