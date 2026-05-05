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
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

func parsePyproject(path string) map[string]struct{} {
	out := map[string]struct{}{}
	data := parseTOMLFile(path)
	if data == nil {
		return out
	}
	project, ok := asMap(data["project"])
	if !ok {
		return out
	}
	for _, key := range []string{"dependencies", "optional-dependencies"} {
		val, ok := project[key]
		if !ok {
			continue
		}
		if list, ok := asSlice(val); ok {
			for _, item := range list {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					out[strings.TrimSpace(s)] = struct{}{}
				}
			}
			continue
		}
		if groups, ok := asMap(val); ok {
			for _, group := range groups {
				if list, ok := asSlice(group); ok {
					for _, item := range list {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							out[strings.TrimSpace(s)] = struct{}{}
						}
					}
				}
			}
		}
	}
	return out
}

func parseRequirementsTxt(path string) map[string]struct{} {
	out := map[string]struct{}{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimSpace(strings.SplitN(s, "#", 2)[0])
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

func parsePipfile(path string) map[string]struct{} {
	out := map[string]struct{}{}
	data := parseTOMLFile(path)
	if data == nil {
		return out
	}
	packages, ok := asMap(data["packages"])
	if !ok {
		return out
	}
	for name, ver := range packages {
		switch v := ver.(type) {
		case string:
			if strings.HasPrefix(v, "=") || strings.HasPrefix(v, ">") || strings.HasPrefix(v, "<") || strings.HasPrefix(v, "!") {
				out[name+v] = struct{}{}
			} else {
				out[name+" "+v] = struct{}{}
			}
		default:
			out[name] = struct{}{}
		}
	}
	return out
}

func pep508PackageName(line string) string {
	line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
	if line == "" || strings.HasPrefix(line, "-r ") || strings.HasPrefix(line, "-c ") {
		return ""
	}
	if idx := strings.Index(line, ";"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if idx := strings.Index(line, "["); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if strings.Contains(line, "@") {
		return ""
	}
	re := regexp.MustCompile(`^([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)
	m := re.FindStringSubmatch(line)
	if len(m) != 2 {
		return ""
	}
	return strings.ReplaceAll(strings.ToLower(m[1]), "_", "-")
}

func groupPythonRequirements(lines []string) (map[string][]string, []string) {
	groups := map[string]map[string]struct{}{}
	var unparsed []string
	for _, line := range lines {
		norm := pep508PackageName(line)
		if norm == "" {
			unparsed = append(unparsed, line)
			continue
		}
		if _, ok := groups[norm]; !ok {
			groups[norm] = map[string]struct{}{}
		}
		groups[norm][line] = struct{}{}
	}
	return sortedGroupMap(groups), unparsed
}

func pypiLicense(project string, cache map[string]*string) string {
	if cached, ok := cache[project]; ok {
		if cached == nil {
			return ""
		}
		return *cached
	}
	u := "https://pypi.org/pypi/" + url.PathEscape(project) + "/json"
	body, err := httpGetString(u, httpTimeoutShort, map[string]string{"User-Agent": httpUserAgent})
	if err != nil {
		cache[project] = nil
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		cache[project] = nil
		return ""
	}
	info, ok := asMap(data["info"])
	if !ok {
		cache[project] = nil
		return ""
	}
	if expr, _ := info["license_expression"].(string); strings.TrimSpace(expr) != "" {
		val := strings.TrimSpace(expr)
		cache[project] = &val
		return val
	}
	if lic, _ := info["license"].(string); strings.TrimSpace(lic) != "" {
		val := strings.TrimSpace(lic)
		cache[project] = &val
		return val
	}
	if classifiers, ok := asSlice(info["classifiers"]); ok {
		for _, c := range classifiers {
			if s, ok := c.(string); ok && strings.HasPrefix(s, "License :: ") {
				val := strings.TrimSpace(strings.Split(s, " :: ")[len(strings.Split(s, " :: "))-1])
				cache[project] = &val
				return val
			}
		}
	}
	cache[project] = nil
	return ""
}

func buildPythonRows(allPy map[string]struct{}, usePyPI bool, cache map[string]*string) ([]dependencyRow, int) {
	groups, unparsed := groupPythonRequirements(sortedKeys(allPy))
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
		lic := "_(PyPI lookup disabled)_"
		if usePyPI {
			if resolved := pypiLicense(norm, cache); resolved != "" {
				lic = resolved
			} else {
				lic = "_(PyPI: unknown or unreachable - set COLLECT_DEPS_NO_PYPI=1 to skip network)_"
			}
		}
		rows = append(rows, dependencyRow{
			Language: "Python",
			SortKey:  norm,
			Spec:     spec,
			License:  lic,
		})
	}
	sort.Strings(unparsed)
	for _, line := range unparsed {
		rows = append(rows, dependencyRow{
			Language: "Python",
			SortKey:  line,
			Spec:     "`" + line + "`",
			License:  "_(could not parse package name)_",
		})
	}
	return rows, len(groups) + len(unparsed)
}
