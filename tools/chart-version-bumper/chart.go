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
	// ActionValuesPaths moves exactly the declared values.yaml paths.
	// appVersion is left alone: in a multi-image chart it belongs to whichever
	// other service, if any, uses the default single-image evidence.
	ActionValuesPaths
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
	// block, wherever it appears on that line.
	//
	// Anchoring to the start of the line only caught the simplest form. These
	// are all valid YAML and all hide the tag from a line scan:
	//
	//	image: { tag: "1.0.0" }
	//	app: { image: { tag: "1.0.0" } }
	//	  - image: { tag: "1.0.0" }
	//	image: registry/name:tag
	//
	// So the rule is inverted: rather than enumerate the shapes that hide a tag,
	// anything that is not a plain block image: is refused. The optional prefix
	// must end at a space, { or , so that a colon inside a value, such as
	// repository: myimage:1.0.0, is not mistaken for an image key, and excluding
	// # keeps commented lines out.
	inlineImageRE = regexp.MustCompile(`(?m)^(?:[^#\n]*[\s{,])?image:[ \t]*[^ \t\n#].*$`)
	keyLineRE     = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.-]+):`)
	// A scalar key: value line, indent and key captured with the trailing
	// colon and whitespace so a rewrite can splice the new value back in
	// without disturbing indentation or a trailing comment. Mirrors
	// appVersionRE's shape for the same reason: quotes around the value are
	// optional on read and always added on write.
	scalarValueRE = regexp.MustCompile(`^(\s*[A-Za-z0-9_.-]+:\s*)"?([^"\s#]*)"?(.*)$`)
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

// resolveValuesPath finds the line holding the scalar value at a dotted path,
// for example "otelCollector.imageTag", in values.yaml.
//
// Matching walks the full ancestor chain rather than taking the nearest
// parent alone, because a chart with more than one image can hold the same
// leaf key more than once under different parents (otelCollector.imageTag
// and helmManaged.otelCollector.imageTag both end in imageTag): a
// nearest-parent match cannot tell those apart, only the full path can.
func resolveValuesPath(lines []string, path string) (line int, value string, err error) {
	segments := strings.Split(path, ".")
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	for i, l := range lines {
		m := keyLineRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		indent := len(m[1])
		key := m[2]
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == len(segments)-1 && key == segments[len(stack)] {
			full := true
			for depth, f := range stack {
				if f.key != segments[depth] {
					full = false
					break
				}
			}
			if full {
				vm := scalarValueRE.FindStringSubmatch(l)
				if vm == nil || vm[2] == "" {
					return 0, "", fmt.Errorf("values path %q: line %d has no scalar value", path, i+1)
				}
				return i, vm[2], nil
			}
		}
		stack = append(stack, frame{indent: indent, key: key})
	}
	return 0, "", fmt.Errorf("values path %q not found", path)
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
		return g[1] + scalarLike(line[len(g[1]):], version) + g[3]
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
		valueStart := strings.Index(m[0], "tag:") + len("tag:")
		valueStart += len(m[0][valueStart:]) - len(strings.TrimLeft(m[0][valueStart:], " \t"))
		lines[it.line] = m[0][:valueStart] + scalarLike(m[0][valueStart:], version) + suffix
	}
	return writeFilePreservingMode(valuesYAML, strings.Join(lines, "\n"))
}

// PlanForValuesPaths decides what to do for a chart whose deploy edge names
// the exact values.yaml paths holding this service's tag, rather than relying
// on single-image discovery. When ownsAppVersion is true, agreement between
// those paths and appVersion is required and all of them move together.
func PlanForValuesPaths(root string, chart Entry, version string, paths []string, files []ValuesFile, ownsAppVersion bool) (Plan, error) {
	chartYAML, valuesYAML := ChartFiles(root, chart.Path)
	if chartYAML == "" {
		return Plan{Action: ActionSkip, Detail: fmt.Sprintf("no Chart.yaml under %s", chart.Path)}, nil
	}
	specs, err := declaredValuesSpecs(root, chart, valuesYAML, paths, files)
	if err != nil {
		return Plan{}, err
	}
	var names, values []string
	for _, spec := range specs {
		b, err := os.ReadFile(spec.path)
		if err != nil {
			return Plan{}, fmt.Errorf("read %s: %w", spec.path, err)
		}
		lines := strings.Split(string(b), "\n")
		for _, path := range spec.paths {
			_, value, err := resolveValuesPath(lines, path)
			if err != nil {
				return Plan{ActionRefuse, valuesPathName(spec.label, err.Error()), "", nil}, nil
			}
			names = append(names, valuesPathName(spec.label, path))
			values = append(values, value)
		}
	}

	// The declared paths are the ownership evidence here, in place of the
	// appVersion agreement the default path uses. If they disagree, that
	// evidence is broken: something already drifted, and moving every path to
	// the same new version would paper over it rather than report it.
	current := values[0]
	for _, v := range values[1:] {
		if v != current {
			return Plan{
				ActionRefuse,
				fmt.Sprintf("declared values paths disagree: %s", describePaths(names, values)),
				"",
				values,
			}, nil
		}
	}
	detail := "declared values path(s): " + strings.Join(names, ", ")
	if ownsAppVersion {
		b, err := os.ReadFile(chartYAML)
		if err != nil {
			return Plan{}, fmt.Errorf("read %s: %w", chartYAML, err)
		}
		match := appVersionRE.FindStringSubmatch(string(b))
		if match == nil {
			return Plan{ActionRefuse, "chart declares no appVersion", current, values}, nil
		}
		if match[2] != current {
			return Plan{
				ActionRefuse,
				fmt.Sprintf("appVersion %s does not match declared values path(s) %s", match[2], describePaths(names, values)),
				current,
				values,
			}, nil
		}
		detail += " and appVersion"
	}
	if floating[current] {
		return Plan{ActionRefuse, "image tag is floating (" + current + ")", current, values}, nil
	}
	return Plan{ActionValuesPaths, detail, current, values}, nil
}

// describePaths pairs each declared path with the value found there, for a
// refusal message that shows exactly where the disagreement is.
func describePaths(paths, values []string) string {
	parts := make([]string, len(paths))
	for i, p := range paths {
		parts[i] = fmt.Sprintf("%s=%s", p, values[i])
	}
	return strings.Join(parts, ", ")
}

// ApplyValuesPaths writes the released artifact version to every path this
// deploy edge named and, when ownsAppVersion is true, to appVersion. Other
// images in the chart are left alone.
func ApplyValuesPaths(root string, chart Entry, version string, paths []string, files []ValuesFile, ownsAppVersion bool) error {
	chartYAML, valuesYAML := ChartFiles(root, chart.Path)
	specs, err := declaredValuesSpecs(root, chart, valuesYAML, paths, files)
	if err != nil {
		return err
	}
	groups := groupDeclaredValuesSpecs(specs)
	updates := make([]fileUpdate, 0, len(groups)+1)
	for _, group := range groups {
		b, err := os.ReadFile(group.path)
		if err != nil {
			return fmt.Errorf("read %s: %w", group.path, err)
		}
		lines := strings.Split(string(b), "\n")
		for _, spec := range group.specs {
			for _, path := range spec.paths {
				line, _, err := resolveValuesPath(lines, path)
				if err != nil {
					return fmt.Errorf("%s: %w", group.path, err)
				}
				m := scalarValueRE.FindStringSubmatch(lines[line])
				lines[line] = m[1] + scalarLike(lines[line][len(m[1]):], version) + m[3]
			}
		}
		update, err := prepareFileUpdate(group.path, b, strings.Join(lines, "\n"))
		if err != nil {
			return err
		}
		updates = append(updates, update)
	}
	if !ownsAppVersion {
		return commitFileUpdates(updates)
	}

	// Read and prepare both files before the first write. A missing or malformed
	// Chart.yaml must not leave values.yaml moved on its own.
	chartBytes, err := os.ReadFile(chartYAML)
	if err != nil {
		return fmt.Errorf("read %s: %w", chartYAML, err)
	}
	chartText := string(chartBytes)
	match := appVersionRE.FindStringSubmatch(chartText)
	if match == nil {
		return fmt.Errorf("appVersion not found in %s", chartYAML)
	}
	replaced := false
	chartText = appVersionRE.ReplaceAllStringFunc(chartText, func(line string) string {
		if replaced {
			return line
		}
		replaced = true
		groups := appVersionRE.FindStringSubmatch(line)
		return groups[1] + scalarLike(line[len(groups[1]):], version) + groups[3]
	})
	chartUpdate, err := prepareFileUpdate(chartYAML, chartBytes, chartText)
	if err != nil {
		return err
	}
	updates = append([]fileUpdate{chartUpdate}, updates...)
	return commitFileUpdates(updates)
}

type declaredValuesGroup struct {
	path  string
	specs []declaredValuesSpec
}

// groupDeclaredValuesSpecs ensures multiple declarations resolving to the
// same file are applied to one buffer and committed as one file update.
func groupDeclaredValuesSpecs(specs []declaredValuesSpec) []declaredValuesGroup {
	var groups []declaredValuesGroup
	groupIndex := make(map[string]int)
	for _, spec := range specs {
		index, ok := groupIndex[spec.path]
		if !ok {
			index = len(groups)
			groupIndex[spec.path] = index
			groups = append(groups, declaredValuesGroup{path: spec.path})
		}
		groups[index].specs = append(groups[index].specs, spec)
	}
	return groups
}

type fileUpdate struct {
	path     string
	original []byte
	updated  []byte
	mode     os.FileMode
}

func prepareFileUpdate(path string, original []byte, updated string) (fileUpdate, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileUpdate{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return fileUpdate{
		path:     path,
		original: append([]byte(nil), original...),
		updated:  []byte(updated),
		mode:     info.Mode().Perm(),
	}, nil
}

type fileWriter func(path string, content []byte, mode os.FileMode) error

// commitFileUpdates restores every attempted file if one write fails. This is
// best-effort because the filesystem can also reject a rollback, in which case
// the returned error reports both failures instead of hiding the partial state.
func commitFileUpdates(updates []fileUpdate) error {
	return commitFileUpdatesWith(updates, os.WriteFile)
}

func commitFileUpdatesWith(updates []fileUpdate, write fileWriter) error {
	for i, update := range updates {
		if err := write(update.path, update.updated, update.mode); err != nil {
			var rollbackErrors []string
			for j := i; j >= 0; j-- {
				attempted := updates[j]
				if rollbackErr := write(attempted.path, attempted.original, attempted.mode); rollbackErr != nil {
					rollbackErrors = append(rollbackErrors, fmt.Sprintf("restore %s: %v", attempted.path, rollbackErr))
				}
			}
			if len(rollbackErrors) > 0 {
				return fmt.Errorf("write %s: %w; rollback failed: %s", update.path, err, strings.Join(rollbackErrors, "; "))
			}
			return fmt.Errorf("write %s: %w", update.path, err)
		}
	}
	return nil
}

type declaredValuesSpec struct {
	path  string
	label string
	paths []string
}

func declaredValuesSpecs(root string, chart Entry, valuesYAML string, paths []string, files []ValuesFile) ([]declaredValuesSpec, error) {
	var specs []declaredValuesSpec
	if len(paths) > 0 {
		specs = append(specs, declaredValuesSpec{path: valuesYAML, paths: paths})
	}
	for _, file := range files {
		clean := filepath.Clean(file.File)
		if filepath.IsAbs(file.File) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("values file %q must be relative to the chart path", file.File)
		}
		if len(file.Paths) == 0 {
			return nil, fmt.Errorf("values file %q declares no paths", file.File)
		}
		specs = append(specs, declaredValuesSpec{
			path:  filepath.Join(root, chart.Path, clean),
			label: filepath.ToSlash(clean),
			paths: file.Paths,
		})
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no declared values paths for chart %s", chart.ID)
	}
	return specs, nil
}

func valuesPathName(file, path string) string {
	if file == "" {
		return path
	}
	return file + ":" + path
}

// scalarLike renders version the way the value it replaces was written:
// quoted if that value was quoted, bare otherwise. Charts are often vendored
// or regenerated by tools that keep each line's existing style (yq does), and
// a check that diffs the regenerated file fails on a quoting-only change.
func scalarLike(original, version string) string {
	if strings.HasPrefix(original, `"`) {
		return fmt.Sprintf("%q", version)
	}
	return version
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
