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
	"sort"
	"strings"
)

// helmfileNamespaceRE matches a literal `namespace: <name>` declaration on a
// helmfile release entry.
//
// Two deliberate restrictions, because a false positive here becomes a
// "delete this namespace" remediation:
//
//   - Indentation is capped at 4 spaces, the release-entry level. Keys nested
//     inside a release's `values:` block sit at 6 or more and must not be read
//     as release namespaces.
//   - The value must be a bare DNS label. A value containing a Go template
//     action is operator-configurable (the gateway controller namespace is
//     one), so its rendered value is not knowable from the source.
var helmfileNamespaceRE = regexp.MustCompile(`(?m)^ {0,4}namespace:\s*([a-z0-9][a-z0-9.-]*)\s*$`)

// stackReleaseNamespaces returns the namespaces that helmfile releases deploy
// into for the stack rooted at stackDir, or nil when the stack is not readable.
//
// This is the authoritative answer to "which namespaces does the stack own",
// which matters because the stale-namespace check's remediation is to delete
// them: a hardcoded list that drifts either misses a real leftover or, worse,
// names a namespace the stack does not own.
//
// Callers fall back to the static list when this returns nil, which is the
// normal case for a released binary with no stack checkout alongside it.
func stackReleaseNamespaces(stackDir string) []string {
	if stackDir == "" {
		return nil
	}
	entries, err := filepath.Glob(filepath.Join(stackDir, "helmfile.d", "*.yaml.gotmpl"))
	if err != nil || len(entries) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	for _, path := range entries {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, m := range helmfileNamespaceRE.FindAllStringSubmatch(string(body), -1) {
			if ns := strings.TrimSpace(m[1]); ns != "" {
				seen[ns] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}

	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// resolveStackNamespaces returns the namespaces to probe for the given plane.
// It prefers what the stack actually declares and falls back to the static
// list when no stack is available.
func resolveStackNamespaces(stackDir string, fallback []string) []string {
	if derived := stackReleaseNamespaces(stackDir); len(derived) > 0 {
		return derived
	}
	return fallback
}
