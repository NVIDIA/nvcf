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
	"encoding/xml"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func parsePOMDirectDependencies(path string) map[string]struct{} {
	out := map[string]struct{}{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var root xmlNode
	if err := xml.Unmarshal(raw, &root); err != nil {
		return out
	}
	if root.XMLName.Local != "project" {
		return out
	}

	props := map[string]string{}
	var depsNode *xmlNode
	for i := range root.Children {
		child := &root.Children[i]
		switch child.XMLName.Local {
		case "properties":
			for _, p := range child.Children {
				if t := strings.TrimSpace(p.Content); t != "" && p.XMLName.Local != "" {
					props[p.XMLName.Local] = t
				}
			}
		case "dependencies":
			depsNode = child
		}
	}
	if depsNode == nil {
		return out
	}

	for _, dep := range depsNode.Children {
		if dep.XMLName.Local != "dependency" {
			continue
		}
		groupID := childText(dep, "groupId")
		artifactID := childText(dep, "artifactId")
		version := childText(dep, "version")
		scope := childText(dep, "scope")
		optional := strings.ToLower(childText(dep, "optional")) == "true" || childText(dep, "optional") == "1"
		if groupID == "" || artifactID == "" || strings.ToLower(scope) == "test" || optional {
			continue
		}
		version = expandMavenProperties(version, props, 0)
		if version != "" {
			out[groupID+":"+artifactID+":"+version] = struct{}{}
		} else {
			out[groupID+":"+artifactID] = struct{}{}
		}
	}
	return out
}

func expandMavenProperties(value string, props map[string]string, depth int) string {
	if value == "" || depth > 10 {
		return value
	}
	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	m := re.FindStringSubmatch(value)
	if len(m) != 2 {
		return value
	}
	key := strings.TrimSpace(m[1])
	if replacement, ok := props[key]; ok {
		return expandMavenProperties(strings.Replace(value, m[0], replacement, 1), props, depth+1)
	}
	return value
}

func mavenCoordKey(line string) string {
	g, a, _ := splitMavenCoordinate(line)
	if g == "" || a == "" {
		return strings.ToLower(line)
	}
	return strings.ToLower(g + ":" + a)
}

func splitMavenCoordinate(line string) (string, string, string) {
	parts := strings.SplitN(line, ":", 3)
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2]
	case 2:
		return parts[0], parts[1], ""
	default:
		return "", "", ""
	}
}

func isNVIDIAMavenGroupID(groupID string) bool {
	groupID = strings.TrimSpace(groupID)
	return groupID == "com.nvidia" || strings.HasPrefix(groupID, "com.nvidia.")
}

func mavenCentralPOMURL(groupID, artifactID, version string) string {
	return fmt.Sprintf("https://repo1.maven.org/maven2/%s/%s/%s/%s-%s.pom", strings.ReplaceAll(groupID, ".", "/"), artifactID, version, artifactID, version)
}

func licensesFromPOMRoot(root xmlNode) string {
	names := map[string]struct{}{}
	var ordered []string
	var walk func(xmlNode)
	walk = func(node xmlNode) {
		if node.XMLName.Local == "licenses" {
			for _, lic := range node.Children {
				if lic.XMLName.Local != "license" {
					continue
				}
				name := childText(lic, "name")
				if name != "" {
					if _, ok := names[name]; !ok {
						names[name] = struct{}{}
						ordered = append(ordered, name)
					}
				}
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return strings.Join(ordered, " / ")
}

func parentCoordsFromPOMRoot(root xmlNode) (string, string, string) {
	for _, child := range root.Children {
		if child.XMLName.Local != "parent" {
			continue
		}
		return childText(child, "groupId"), childText(child, "artifactId"), childText(child, "version")
	}
	return "", "", ""
}

func fetchMavenPOMBytes(pomURL string) []byte {
	for attempt := 0; attempt < 2; attempt++ {
		mavenHTTPThrottle()
		body, status, err := httpGetBytesWithStatus(pomURL, httpTimeoutDefault, map[string]string{"User-Agent": cratesIOUserAgent})
		if err == nil {
			return body
		}
		if status == 404 {
			return nil
		}
		if attempt == 0 {
			time.Sleep(750 * time.Millisecond)
		}
	}
	return nil
}

func mavenCentralLicenseFromPOMURL(pomURL string, pomCache map[string]*string, visiting map[string]struct{}, maxParentDepth int) string {
	if cached, ok := pomCache[pomURL]; ok {
		if cached == nil {
			return ""
		}
		return *cached
	}
	if maxParentDepth < 0 {
		return ""
	}
	if _, ok := visiting[pomURL]; ok {
		return ""
	}
	visiting[pomURL] = struct{}{}
	defer delete(visiting, pomURL)

	raw := fetchMavenPOMBytes(pomURL)
	if len(raw) == 0 {
		pomCache[pomURL] = nil
		return ""
	}
	var root xmlNode
	if err := xml.Unmarshal(raw, &root); err != nil {
		pomCache[pomURL] = nil
		return ""
	}
	lic := licensesFromPOMRoot(root)
	if lic == "" {
		pg, pa, pv := parentCoordsFromPOMRoot(root)
		if pg != "" && pa != "" && pv != "" {
			lic = mavenCentralLicenseFromPOMURL(mavenCentralPOMURL(pg, pa, pv), pomCache, visiting, maxParentDepth-1)
		}
	}
	if lic == "" {
		pomCache[pomURL] = nil
		return ""
	}
	licCopy := lic
	pomCache[pomURL] = &licCopy
	return lic
}

func mavenSearchLatestVersion(groupID, artifactID string, cache map[string]*string) string {
	key := groupID + ":" + artifactID
	if cached, ok := cache[key]; ok {
		if cached == nil {
			return ""
		}
		return *cached
	}
	metadataURL := fmt.Sprintf("https://repo1.maven.org/maven2/%s/%s/maven-metadata.xml", strings.ReplaceAll(groupID, ".", "/"), artifactID)
	for attempt := 0; attempt < 2; attempt++ {
		mavenHTTPThrottle()
		body, _, err := httpGetBytesWithStatus(metadataURL, httpTimeoutDefault, map[string]string{"User-Agent": cratesIOUserAgent})
		if err == nil {
			var root xmlNode
			if xml.Unmarshal(body, &root) == nil {
				for _, child := range root.Children {
					if child.XMLName.Local != "versioning" {
						continue
					}
					release := childText(child, "release")
					latest := childText(child, "latest")
					out := firstNonEmpty(release, latest)
					if out != "" {
						outCopy := out
						cache[key] = &outCopy
						return out
					}
				}
			}
		}
		if attempt == 0 {
			time.Sleep(750 * time.Millisecond)
		}
	}
	query := fmt.Sprintf(`g:"%s" AND a:"%s"`, groupID, artifactID)
	searchURL := "https://search.maven.org/solrsearch/select?" + url.Values{
		"q":    []string{query},
		"rows": []string{"1"},
		"wt":   []string{"json"},
	}.Encode()
	for attempt := 0; attempt < 2; attempt++ {
		mavenHTTPThrottle()
		body, _, err := httpGetStringWithStatus(searchURL, httpTimeoutDefault, map[string]string{"User-Agent": cratesIOUserAgent})
		if err == nil {
			var data map[string]any
			if json.Unmarshal([]byte(body), &data) == nil {
				if response, ok := asMap(data["response"]); ok {
					if docs, ok := asSlice(response["docs"]); ok && len(docs) > 0 {
						if first, ok := asMap(docs[0]); ok {
							if ver, _ := first["latestVersion"].(string); strings.TrimSpace(ver) != "" {
								out := strings.TrimSpace(ver)
								outCopy := out
								cache[key] = &outCopy
								return out
							}
						}
					}
				}
			}
		}
		if attempt == 0 {
			time.Sleep(750 * time.Millisecond)
		}
	}
	cache[key] = nil
	return ""
}

func mavenCentralLicenseForCoordinate(line string, searchCache, pomCache map[string]*string) string {
	g, a, v := splitMavenCoordinate(line)
	if g == "" || a == "" {
		return ""
	}
	if v == "" {
		v = mavenSearchLatestVersion(g, a, searchCache)
		if v == "" {
			return ""
		}
	}
	return mavenCentralLicenseFromPOMURL(mavenCentralPOMURL(g, a, v), pomCache, map[string]struct{}{}, 12)
}

func groupJavaCoordinates(lines []string) (map[string][]string, []string) {
	groups := map[string]map[string]struct{}{}
	var unparsed []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !strings.Contains(line, ":") {
			unparsed = append(unparsed, raw)
			continue
		}
		g, a, _ := splitMavenCoordinate(line)
		if g == "" || a == "" {
			unparsed = append(unparsed, raw)
			continue
		}
		key := mavenCoordKey(line)
		if _, ok := groups[key]; !ok {
			groups[key] = map[string]struct{}{}
		}
		groups[key][line] = struct{}{}
	}
	return sortedGroupMap(groups), unparsed
}

func mavenHTTPThrottle() {
	raw := strings.TrimSpace(os.Getenv("COLLECT_DEPS_MAVEN_THROTTLE_SEC"))
	if raw == "" {
		raw = "0.05"
	}
	sec, err := strconv.ParseFloat(raw, 64)
	if err != nil || sec <= 0 {
		return
	}
	time.Sleep(time.Duration(sec * float64(time.Second)))
}

func buildJavaRows(allJava map[string]struct{}, useMavenCentral bool, searchCache, pomCache map[string]*string) ([]dependencyRow, int) {
	groups, unparsed := groupJavaCoordinates(sortedKeys(allJava))
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
		licLine := originals[0]
		for _, original := range originals {
			if _, _, version := splitMavenCoordinate(original); version != "" {
				licLine = original
				break
			}
		}
		groupID, _, _ := splitMavenCoordinate(licLine)
		lic := "_(Maven Central lookup disabled)_"
		if useMavenCentral {
			if isNVIDIAMavenGroupID(groupID) {
				lic = "_(NVIDIA `com.nvidia.*` artifact; license not resolved via Maven Central)_"
			} else if resolved := mavenCentralLicenseForCoordinate(licLine, searchCache, pomCache); resolved != "" {
				lic = resolved
			} else {
				lic = "_(third-party: expected `<licenses>` from Maven Central POM - missing, 404, or network; fix or see tools/collect-dependencies/README.md)_"
			}
		}
		rows = append(rows, dependencyRow{
			Language: "Java",
			SortKey:  norm,
			Spec:     spec,
			License:  lic,
		})
	}
	sort.Strings(unparsed)
	for _, line := range unparsed {
		rows = append(rows, dependencyRow{
			Language: "Java",
			SortKey:  line,
			Spec:     "`" + line + "`",
			License:  "_(could not parse Maven coordinates)_",
		})
	}
	return rows, len(groups) + len(unparsed)
}
