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
	"strings"

	"gopkg.in/yaml.v3"
)

type pnpmLockfile struct {
	Packages map[string]any `yaml:"packages"`
}

type nodeDependency struct {
	Name    string
	Version string
}

func parsePNPMLock(path string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pnpm lockfile %s: %w", path, err)
	}
	var lock pnpmLockfile
	if err := yaml.Unmarshal(raw, &lock); err != nil {
		return nil, fmt.Errorf("parse pnpm lockfile %s: %w", path, err)
	}
	for key := range lock.Packages {
		dep, ok := parsePNPMPackageKey(key)
		if !ok {
			continue
		}
		out[dep.Name+"@"+dep.Version] = struct{}{}
	}
	return out, nil
}

func parsePNPMPackageKey(key string) (nodeDependency, bool) {
	key = strings.TrimPrefix(strings.TrimSpace(key), "/")
	if index := strings.Index(key, "("); index >= 0 {
		key = key[:index]
	}
	separator := strings.LastIndex(key, "@")
	if separator <= 0 || separator == len(key)-1 {
		return nodeDependency{}, false
	}
	name := key[:separator]
	version := key[separator+1:]
	if strings.HasPrefix(name, "file:") || strings.HasPrefix(name, "link:") ||
		strings.HasPrefix(version, "file:") || strings.HasPrefix(version, "link:") {
		return nodeDependency{}, false
	}
	return nodeDependency{Name: name, Version: version}, true
}

func splitNodeDependency(key string) (nodeDependency, bool) {
	separator := strings.LastIndex(key, "@")
	if separator <= 0 || separator == len(key)-1 {
		return nodeDependency{}, false
	}
	return nodeDependency{Name: key[:separator], Version: key[separator+1:]}, true
}

type npmLookupResult struct {
	license string
	err     error
}

func npmLicense(dep nodeDependency, cache map[string]npmLookupResult) npmLookupResult {
	key := dep.Name + "@" + dep.Version
	if cached, ok := cache[key]; ok {
		return cached
	}
	result := npmLookupResult{}
	defer func() { cache[key] = result }()
	u := "https://registry.npmjs.org/" + url.PathEscape(dep.Name) + "/" + url.PathEscape(dep.Version)
	body, err := httpGetString(u, httpTimeoutShort, map[string]string{"User-Agent": httpUserAgent})
	if err != nil {
		result.err = err
		fmt.Fprintf(os.Stderr, "collect-dependencies: npm metadata %s: %v\n", key, err)
		return result
	}
	var metadata struct {
		License any `json:"license"`
	}
	if err := json.Unmarshal([]byte(body), &metadata); err != nil {
		result.err = fmt.Errorf("invalid npm metadata JSON")
		return result
	}
	result.license = npmLicenseValue(metadata.License)
	return result
}

func npmLicenseValue(value any) string {
	switch license := value.(type) {
	case string:
		return strings.TrimSpace(license)
	case map[string]any:
		if name, ok := license["type"].(string); ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

func buildNodeRows(allNode map[string]struct{}, useNPM bool, cache map[string]npmLookupResult) ([]dependencyRow, int) {
	rows := []dependencyRow{}
	keys := sortedKeys(allNode)
	for _, key := range keys {
		dep, ok := splitNodeDependency(key)
		if !ok {
			continue
		}
		license := "_(npm registry lookup disabled)_"
		if useNPM {
			result := npmLicense(dep, cache)
			switch {
			case result.license != "":
				license = result.license
			case result.err != nil:
				license = "_(npm license lookup unavailable: " + result.err.Error() + ")_"
			default:
				license = "_(npm metadata contains no recognized license)_"
			}
		}
		rows = append(rows, dependencyRow{
			Language: "Node.js",
			SortKey:  key,
			Spec:     fmt.Sprintf("`%s@%s`", dep.Name, dep.Version),
			License:  license,
		})
	}
	return rows, len(rows)
}
