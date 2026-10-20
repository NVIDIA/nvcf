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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// stackNamespace is a namespace stack releases deploy into. gates are the
// values keys of the condition: on those releases; none when one of them
// always installs. on is the default the stack's environments/base.yaml gives
// them, used only when the stack's values set none of the gates.
type stackNamespace struct {
	name  string
	gates []string
	on    bool
}

// deployed reports whether the stack installs a release into the namespace,
// given its layered values.
func (n stackNamespace) deployed(values map[string]any) bool {
	if len(n.gates) == 0 {
		return true
	}
	set := false
	for _, gate := range n.gates {
		v, ok := digAny(values, strings.Split(gate, ".")...).(bool)
		if v {
			return true
		}
		set = set || ok
	}
	return !set && n.on
}

// runtimeOwnedNamespaces are created at runtime by NVCA rather than by a
// stack release, and hold live work. No stack list may probe them, whatever a
// helmfile declares.
var runtimeOwnedNamespaces = map[string]bool{"nvcf-backend": true, "nvca-system": true}

var (
	// releaseItemRE matches the line that opens a list entry at the release
	// level, capturing the rest of the line as the entry's first key.
	releaseItemRE = regexp.MustCompile(`^ {0,2}- (.*)$`)

	// releaseKeyRE matches a release-level key. Indentation is capped at 4
	// spaces: keys nested inside a release's values: block sit at 6 or more
	// and are chart configuration, not the release's own namespace.
	releaseKeyRE = regexp.MustCompile(`^ {0,4}(namespace|condition|installed):\s*(.*?)\s*$`)

	// literalNamespaceRE accepts only a bare DNS label. A value containing a
	// Go template action is operator-configurable (the gateway controller
	// namespace is one), so its rendered value is not knowable from the
	// source.
	literalNamespaceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

	// conditionRE is a values path such as addons.kaiScheduler.enabled,
	// optionally followed by a comment.
	conditionRE = regexp.MustCompile(`^([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+)\s*(?:#.*)?$`)

	templateOpenRE  = regexp.MustCompile(`\{\{-?\s*(?:if|with|range)\b`)
	templateCloseRE = regexp.MustCompile(`\{\{-?\s*end\b`)
)

// helmfileRelease is what the stack check reads from one release entry.
// opaque is set when its gate cannot be read from the source: the entry sits
// inside a template block, or its condition: or installed: is templated.
type helmfileRelease struct {
	namespace string
	condition string
	disabled  bool
	opaque    bool
}

// helmfileReleases reads the release entries of one helmfile fragment.
func helmfileReleases(body string) []helmfileRelease {
	var out []helmfileRelease
	depth := 0
	for _, line := range strings.Split(body, "\n") {
		if m := releaseItemRE.FindStringSubmatch(line); m != nil {
			out = append(out, helmfileRelease{opaque: depth > 0})
			line = "    " + m[1]
		}
		depth += len(templateOpenRE.FindAllString(line, -1)) - len(templateCloseRE.FindAllString(line, -1))
		if len(out) == 0 {
			continue
		}
		cur := &out[len(out)-1]
		m := releaseKeyRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch value := strings.Trim(m[2], `"'`); m[1] {
		case "namespace":
			if literalNamespaceRE.MatchString(value) {
				cur.namespace = value
			}
		case "condition":
			if c := conditionRE.FindStringSubmatch(value); c != nil {
				cur.condition = c[1]
			} else {
				cur.opaque = true
			}
		case "installed":
			switch value {
			case "true":
			case "false":
				cur.disabled = true
			default:
				cur.opaque = true
			}
		}
	}
	return out
}

// stackReleaseNamespaces returns the namespaces that helmfile releases deploy
// into for the stack rooted at stackDir, each with the conditions that gate
// it, or nil when the stack is not readable.
//
// This is the authoritative answer to "which namespaces does the stack own":
// a hardcoded list that drifts either misses a real leftover or names a
// namespace the stack does not own.
//
// A release whose gate cannot be read from the source (a template block
// around it, a templated condition: or installed:) is left out, as is one
// installed: false. Only a namespace the stack is known to deploy into may
// fail the check.
//
// Callers fall back to the static list when this returns nil, which is the
// normal case for a released binary with no stack checkout alongside it.
func stackReleaseNamespaces(stackDir string) []stackNamespace {
	if stackDir == "" {
		return nil
	}
	// helmfile accepts plain .yaml fragments as well as .yaml.gotmpl.
	var entries []string
	for _, pattern := range []string{"*.yaml.gotmpl", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(stackDir, "helmfile.d", pattern))
		if err != nil {
			return nil
		}
		entries = append(entries, matches...)
	}
	if len(entries) == 0 {
		return nil
	}

	gates := make(map[string][]string)
	always := make(map[string]bool)
	for _, path := range entries {
		body, err := os.ReadFile(path)
		if err != nil {
			// Bail out rather than deriving a partial list. A single
			// unreadable fragment (a root-owned 0600 file left by "sudo
			// helmfile", say) would otherwise silently drop a third of the
			// namespaces, and the caller prefers any non-empty derived list
			// over the complete static fallback. That turns a truncated scan
			// into an affirmative all-clear.
			return nil
		}
		for _, r := range helmfileReleases(string(body)) {
			if r.namespace == "" || r.opaque || r.disabled {
				continue
			}
			if r.condition == "" {
				always[r.namespace] = true
			} else if !slices.Contains(gates[r.namespace], r.condition) {
				gates[r.namespace] = append(gates[r.namespace], r.condition)
			}
		}
	}

	var out []stackNamespace
	for ns := range always {
		out = append(out, stackNamespace{name: ns})
	}
	for ns, g := range gates {
		if !always[ns] {
			sort.Strings(g)
			out = append(out, stackNamespace{name: ns, gates: g})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// stackValues layers the stack's environments/base.yaml and the environment's
// own file the way helmfile does. It reports false when neither was read.
func stackValues(stackDir, env string) (map[string]any, bool) {
	if stackDir == "" {
		return nil, false
	}
	files := []string{filepath.Join(stackDir, "environments", "base.yaml")}
	if env != "" {
		files = append(files, filepath.Join(stackDir, "environments", env+".yaml"))
	}
	return loadValuesFiles(files)
}

// resolveStackNamespaces returns the namespaces to probe for the given plane:
// those the static list and the stack's own helmfile.d deploy into, given the
// gates the stack's values for env set.
func resolveStackNamespaces(stackDir, env string, static []stackNamespace) []string {
	values, _ := stackValues(stackDir, env)
	var out []string
	// Union, not replace. The derived list follows the stack, but it cannot see
	// namespaces behind a helmfiles: include, so on its own it can still be
	// short. Merging keeps the static list as a floor.
	for _, list := range [][]stackNamespace{static, stackReleaseNamespaces(stackDir)} {
		for _, ns := range list {
			if ns.deployed(values) {
				out = append(out, ns.name)
			}
		}
	}
	return mergeNamespaces(out)
}
