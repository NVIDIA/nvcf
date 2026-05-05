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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func parseCargoTOML(path string) map[string]struct{} {
	out := map[string]struct{}{}
	data := parseTOMLFile(path)
	if data == nil {
		return out
	}
	for _, sec := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
		block, ok := asMap(data[sec])
		if !ok {
			continue
		}
		for name, spec := range block {
			if name == "package" {
				continue
			}
			switch v := spec.(type) {
			case string:
				out[fmt.Sprintf("%s %s", name, strings.TrimSpace(v))] = struct{}{}
			case map[string]any:
				version, _ := v["version"].(string)
				line := strings.TrimSpace(fmt.Sprintf("%s %s", name, strings.TrimSpace(version)))
				out[line] = struct{}{}
			default:
				out[name] = struct{}{}
			}
		}
	}
	return out
}

func rustNameKeys(crateName string) []string {
	n := strings.ToLower(strings.TrimSpace(crateName))
	return []string{n, strings.ReplaceAll(n, "_", "-"), strings.ReplaceAll(n, "-", "_")}
}

func cargoWorkspaceRootDir(cargoTOMLDir string) string {
	stdout, _, err := runCommand(cargoTOMLDir, nil, shortToolTimeout, "cargo", "locate-project", "--workspace", "--message-format", "json")
	if err != nil {
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(stdout), &data); err != nil {
		return ""
	}
	root, _ := data["root"].(string)
	if root == "" {
		return ""
	}
	if st, err := os.Stat(root); err != nil || st.IsDir() {
		return ""
	}
	return filepath.Dir(root)
}

func cargoMetadataLicenses(projectDir string) map[string]string {
	out := map[string]string{}
	for _, args := range [][]string{
		{"cargo", "metadata", "--format-version", "1", "--locked"},
		{"cargo", "metadata", "--format-version", "1"},
	} {
		stdout, _, err := runCommand(projectDir, nil, 3*time.Minute, args[0], args[1:]...)
		if err != nil {
			var execErr *exec.Error
			if errors.As(err, &execErr) {
				if !cargoMissingLogged {
					fmt.Fprintln(os.Stderr, "collect-dependencies: `cargo` not on PATH; Rust licenses will use crates.io only (or stay blank).")
					cargoMissingLogged = true
				}
				return out
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return out
			}
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(stdout), &data); err != nil {
			continue
		}
		if pkgs, ok := asSlice(data["packages"]); ok {
			for _, pkg := range pkgs {
				m, ok := asMap(pkg)
				if !ok {
					continue
				}
				name, _ := m["name"].(string)
				lic, _ := m["license"].(string)
				if strings.TrimSpace(name) != "" && strings.TrimSpace(lic) != "" {
					out[name] = strings.TrimSpace(lic)
				}
			}
		}
		return out
	}
	return out
}

func collectRustLicenses(importRoots []string) map[string]map[string]struct{} {
	cargoDirs := map[string]struct{}{}
	for _, root := range importRoots {
		_ = walkImportRoot(root, func(path string, d fs.DirEntry) error {
			if !d.IsDir() && d.Name() == "Cargo.toml" {
				cargoDirs[filepath.Dir(path)] = struct{}{}
			}
			return nil
		})
	}
	workspaceRoots := map[string]struct{}{}
	for dir := range cargoDirs {
		if root := cargoWorkspaceRootDir(dir); root != "" {
			workspaceRoots[root] = struct{}{}
		} else {
			workspaceRoots[dir] = struct{}{}
		}
	}
	acc := map[string]map[string]struct{}{}
	for _, cwd := range sortedKeys(workspaceRoots) {
		meta := cargoMetadataLicenses(cwd)
		for crate, lic := range meta {
			for _, key := range rustNameKeys(crate) {
				addLicense(acc, key, lic)
			}
		}
	}
	return acc
}

func cratesIOLicense(crateName string, cache map[string]*string) string {
	variants := uniqueStrings([]string{
		strings.ToLower(strings.TrimSpace(crateName)),
		strings.ReplaceAll(strings.ToLower(strings.TrimSpace(crateName)), "_", "-"),
		strings.ReplaceAll(strings.ToLower(strings.TrimSpace(crateName)), "-", "_"),
	})
	for _, v := range variants {
		if cached, ok := cache[v]; ok && cached != nil {
			return *cached
		}
	}
	for _, v := range variants {
		if _, ok := cache[v]; ok {
			continue
		}
		u := "https://crates.io/api/v1/crates/" + url.PathEscape(v)
		body, err := httpGetString(u, httpTimeoutDefault, map[string]string{"User-Agent": cratesIOUserAgent})
		if err != nil {
			cache[v] = nil
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(body), &data); err != nil {
			cache[v] = nil
			continue
		}
		if crate, ok := asMap(data["crate"]); ok {
			if lic, _ := crate["license"].(string); strings.TrimSpace(lic) != "" {
				for _, vv := range variants {
					licCopy := strings.TrimSpace(lic)
					cache[vv] = &licCopy
				}
				return strings.TrimSpace(lic)
			}
		}
		if versions, ok := asSlice(data["versions"]); ok {
			for _, version := range versions {
				vm, ok := asMap(version)
				if !ok {
					continue
				}
				if yanked, _ := vm["yanked"].(bool); yanked {
					continue
				}
				if lic, _ := vm["license"].(string); strings.TrimSpace(lic) != "" {
					for _, vv := range variants {
						licCopy := strings.TrimSpace(lic)
						cache[vv] = &licCopy
					}
					return strings.TrimSpace(lic)
				}
			}
		}
		cache[v] = nil
	}
	return ""
}

func rustLicenseForCrate(rustLicenses map[string]map[string]struct{}, crateStem string) map[string]struct{} {
	merged := map[string]struct{}{}
	for _, key := range rustNameKeys(crateStem) {
		for lic := range rustLicenses[key] {
			merged[lic] = struct{}{}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func groupRustRequirements(lines []string) (map[string][]string, []string) {
	groups := map[string]map[string]struct{}{}
	var unparsed []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			unparsed = append(unparsed, raw)
			continue
		}
		stem := strings.ReplaceAll(strings.ToLower(fields[0]), "_", "-")
		if _, ok := groups[stem]; !ok {
			groups[stem] = map[string]struct{}{}
		}
		groups[stem][line] = struct{}{}
	}
	return sortedGroupMap(groups), unparsed
}

func formatRustLicense(rustLicenses map[string]map[string]struct{}, spec string) string {
	stem := ""
	if fields := strings.Fields(spec); len(fields) > 0 {
		stem = fields[0]
	}
	if stem == "" {
		return "_(license not resolved)_"
	}
	s := rustLicenseForCrate(rustLicenses, stem)
	if len(s) == 0 {
		return "_(no license resolved - install `cargo` for `cargo metadata`, or allow crates.io fallback; see tools/collect-dependencies/README.md)_"
	}
	return strings.Join(sortedKeys(s), " / ")
}

func buildRustRows(allRust map[string]struct{}, rustLicenses map[string]map[string]struct{}) ([]dependencyRow, int) {
	groups, unparsed := groupRustRequirements(sortedKeys(allRust))
	rows := []dependencyRow{}
	for _, norm := range sortedMapKeys(groups) {
		originals := groups[norm]
		spec := ""
		if len(originals) == 1 {
			spec = "`" + originals[0] + "`"
		} else {
			inner := make([]string, 0, len(originals))
			for _, original := range originals {
				inner = append(inner, "`"+original+"`")
			}
			spec = fmt.Sprintf("`%s` (%s)", norm, strings.Join(inner, ", "))
		}
		rows = append(rows, dependencyRow{
			Language: "Rust",
			SortKey:  norm,
			Spec:     spec,
			License:  formatRustLicense(rustLicenses, norm),
		})
	}
	sort.Strings(unparsed)
	for _, line := range unparsed {
		rows = append(rows, dependencyRow{
			Language: "Rust",
			SortKey:  line,
			Spec:     "`" + line + "`",
			License:  formatRustLicense(rustLicenses, line),
		})
	}
	return rows, len(groups) + len(unparsed)
}
