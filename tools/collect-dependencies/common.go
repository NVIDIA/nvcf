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
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

const (
	httpUserAgent      = "nvcf-collect-dependencies/1 (NVCF umbrella dependency rollup)"
	cratesIOUserAgent  = httpUserAgent
	goToolTimeout      = 15 * time.Minute
	helmToolTimeout    = 3 * time.Minute
	shortToolTimeout   = 90 * time.Second
	httpTimeoutShort   = 12 * time.Second
	httpTimeoutDefault = 15 * time.Second
	httpTimeoutLong    = 25 * time.Second
)

var (
	repoRoot string

	cargoMissingLogged      bool
	githubLicenseWarned     bool
	githubAuthLogged        bool
	goListHintLogged        bool
	goModDownloadHintLogged bool
	helmMissingLogged       bool

	licenseFileNames = []string{
		"LICENSE",
		"LICENSE.md",
		"LICENSE.txt",
		"LICENCE",
		"LICENCE.md",
		"COPYING",
	}

	helmLicenseAnnotationKeys = []string{
		"licenses",
		"license",
		"artifacthub.io/license",
		"artifacthub.io/licenses",
	}

	skipDirs = map[string]struct{}{
		".git":         {},
		"vendor":       {},
		"node_modules": {},
		"target":       {},
		".venv":        {},
		"venv":         {},
	}
)

type manifestFile struct {
	Imports []importEntry `yaml:"imports"`
}

type importEntry struct {
	Path string `yaml:"path"`
	Repo string `yaml:"repo"`
}

type args struct {
	Language string
}

type dependencyScan struct {
	Go     map[string]struct{}
	Rust   map[string]struct{}
	Python map[string]struct{}
	Java   map[string]struct{}
	Helm   map[string]struct{}
}

type dependencyRow struct {
	Language string
	SortKey  string
	Spec     string
	License  string
}

type xmlNode struct {
	XMLName  xml.Name  `xml:""`
	Content  string    `xml:",chardata"`
	Children []xmlNode `xml:",any"`
}

type helmDependency struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
}

type helmManifest struct {
	Dependencies []helmDependency `yaml:"dependencies"`
}

type helmChartMetadata struct {
	Annotations map[string]string `yaml:"annotations"`
	License     string            `yaml:"license"`
	Home        string            `yaml:"home"`
	Sources     []string          `yaml:"sources"`
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func mergeSet(dst, src map[string]struct{}) {
	for key := range src {
		dst[key] = struct{}{}
	}
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func parseTOMLFile(path string) map[string]any {
	var out map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if err := toml.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func parseModulesTxtModuleHeaders(text string) []string {
	var mods []string
	re := regexp.MustCompile(`^(\S+)\s+(\S+)\s*$`)
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "##") {
			continue
		}
		body := strings.TrimSpace(line[2:])
		m := re.FindStringSubmatch(body)
		if len(m) != 3 {
			continue
		}
		mod, ver := m[1], m[2]
		if mod == "go.mod" || ver == "explicit" {
			continue
		}
		if matched, _ := regexp.MatchString(`^v?\d`, ver); !matched {
			continue
		}
		mods = append(mods, mod)
	}
	return mods
}

func sniffLicenseText(text string) string {
	if text == "" {
		return ""
	}
	chunk := text
	if len(chunk) > 16000 {
		chunk = chunk[:16000]
	}
	if m := regexp.MustCompile(`(?i)SPDX-License-Identifier:\s*([^\s*/\n]+)`).FindStringSubmatch(chunk); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	cl := strings.ToLower(chunk)
	if strings.Contains(cl, "apache license") && strings.Contains(cl, "version 2") {
		return "Apache-2.0"
	}
	if strings.Contains(cl, "permission is hereby granted, free of charge") &&
		(strings.Contains(cl, "substantial portions of the software") || strings.Contains(cl, "all copies or substantial portions")) {
		return "MIT"
	}
	if strings.Contains(cl, "mit license") || regexp.MustCompile(`(?s)\bmit\b.*permission`).MatchString(cl) {
		return "MIT"
	}
	if strings.Contains(cl, "bsd 3-clause") || strings.Contains(cl, "bsd-3-clause") || strings.Contains(cl, "3-clause bsd") {
		return "BSD-3-Clause"
	}
	if strings.Contains(cl, "bsd 2-clause") || strings.Contains(cl, "bsd-2-clause") || strings.Contains(cl, "2-clause bsd") {
		return "BSD-2-Clause"
	}
	if strings.Contains(cl, "bsd 3-clause") || strings.Contains(cl, "redistribution and use in source and binary forms") {
		if strings.Contains(cl, "neither the name") || strings.Contains(cl, "3-clause") {
			return "BSD-3-Clause"
		}
		if strings.Contains(cl, "2-clause") || strings.Contains(cl, "two clause") {
			return "BSD-2-Clause"
		}
		return "BSD-3-Clause"
	}
	if strings.Contains(cl, "mozilla public license") || strings.Contains(cl, "mpl 2.0") {
		return "MPL-2.0"
	}
	if strings.Contains(cl, "isc license") {
		return "ISC"
	}
	if strings.Contains(cl, "gnu lesser general public license") || strings.Contains(cl[:min(2000, len(cl))], "lgpl") {
		return "LGPL (version in file)"
	}
	if strings.Contains(cl, "gnu general public license") && !strings.Contains(cl[:min(500, len(cl))], "lesser") {
		return "GPL (version in file)"
	}
	if strings.Contains(cl[:min(800, len(cl))], "unlicense") {
		return "Unlicense"
	}
	return ""
}

func stripLeadingCopyrightBlock(text string) string {
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) {
		s := strings.TrimSpace(lines[i])
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") ||
			regexp.MustCompile(`(?i)^Copyright\s`).MatchString(s) ||
			strings.HasPrefix(s, "©") || strings.HasPrefix(strings.ToUpper(s), "(C)") {
			i++
			continue
		}
		break
	}
	return strings.Join(lines[i:], "\n")
}

func looksLikeLicenseSummaryLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(line))
	if strings.HasPrefix(lower, "permalink:") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return false
	}
	if strings.Contains(lower, "license") || strings.Contains(lower, "licence") {
		return true
	}
	if strings.Contains(lower, "licensed under") || strings.Contains(lower, "proprietary") || strings.Contains(lower, "public domain") {
		return true
	}
	return false
}

func fallbackLicenseLabel(text string) string {
	stripped := stripLeadingCopyrightBlock(text)
	for _, line := range strings.Split(stripped, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || regexp.MustCompile(`(?i)^Copyright\s`).MatchString(s) {
			continue
		}
		if !looksLikeLicenseSummaryLine(s) {
			continue
		}
		if lic := sniffLicenseText(s); lic != "" {
			return lic
		}
		return truncateString(s, 120)
	}
	head := truncateString(strings.TrimSpace(strings.SplitN(text, "\n", 2)[0]), 120)
	if looksLikeLicenseSummaryLine(head) {
		if lic := sniffLicenseText(head); lic != "" {
			return lic
		}
		return head
	}
	return ""
}

func licenseFromPlainDir(base string) string {
	st, err := os.Stat(base)
	if err != nil || !st.IsDir() {
		return ""
	}
	for _, name := range licenseFileNames {
		path := filepath.Join(base, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(raw)
		if lic := sniffLicenseText(text); lic != "" {
			return lic
		}
		stripped := stripLeadingCopyrightBlock(text)
		if stripped != text {
			if lic := sniffLicenseText(stripped); lic != "" {
				return lic
			}
		}
		if fallback := fallbackLicenseLabel(text); fallback != "" {
			return fallback
		}
	}
	return ""
}

func readLicenseUnder(vendorRoot, modulePath string) string {
	return licenseFromPlainDir(filepath.Join(vendorRoot, filepath.FromSlash(modulePath)))
}

func addLicense(dst map[string]map[string]struct{}, key, value string) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return
	}
	if _, ok := dst[key]; !ok {
		dst[key] = map[string]struct{}{}
	}
	dst[key][value] = struct{}{}
}

func mergeLicenseMap(dst, src map[string]map[string]struct{}) {
	for key, values := range src {
		for value := range values {
			addLicense(dst, key, value)
		}
	}
}

func missingGoModules(allModules map[string]struct{}, goLicenses map[string]map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for mod := range allModules {
		if goLicenseForModule(goLicenses, mod) == nil {
			out[mod] = struct{}{}
		}
	}
	return out
}

func groupStringSetMap(in map[string]map[string]struct{}) map[string][]string {
	return sortedGroupMap(in)
}

func sortedGroupMap(in map[string]map[string]struct{}) map[string][]string {
	out := map[string][]string{}
	for key, values := range in {
		out[key] = sortedKeys(values)
	}
	return out
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedCrateStems(allRust map[string]struct{}) []string {
	set := map[string]struct{}{}
	for spec := range allRust {
		fields := strings.Fields(spec)
		if len(fields) > 0 {
			set[fields[0]] = struct{}{}
		}
	}
	return sortedKeys(set)
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func childText(node xmlNode, local string) string {
	for _, child := range node.Children {
		if child.XMLName.Local == local {
			return strings.TrimSpace(child.Content)
		}
	}
	return ""
}

func runCommand(dir string, env map[string]string, timeout time.Duration, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		merged := os.Environ()
		for k, v := range env {
			merged = append(merged, k+"="+v)
		}
		cmd.Env = merged
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), context.DeadlineExceeded
	}
	return stdout.String(), stderr.String(), err
}

func githubAuthToken() string {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

func githubOwnerRepoFromModulePath(modulePath string) (string, string) {
	parts := strings.Split(modulePath, "/")
	if len(parts) < 3 || strings.ToLower(parts[0]) != "github.com" {
		return "", ""
	}
	return parts[1], parts[2]
}

func githubOwnerRepoFromURL(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if strings.HasPrefix(raw, "git+") {
		raw = raw[4:]
	}
	var path string
	if strings.HasPrefix(raw, "git@github.com:") {
		path = strings.SplitN(raw, ":", 2)[1]
	} else {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", ""
		}
		host := strings.ToLower(parsed.Host)
		if host != "github.com" && host != "www.github.com" {
			return "", ""
		}
		path = parsed.Path
	}
	parts := filterEmpty(strings.Split(path, "/"))
	if len(parts) < 2 {
		return "", ""
	}
	repo := parts[1]
	repo = strings.TrimSuffix(repo, ".git")
	if repo == "" {
		return "", ""
	}
	return parts[0], repo
}

func githubRepoLicenseSPDX(owner, repo string, cache map[string]*string) string {
	key := owner + "/" + repo
	if cached, ok := cache[key]; ok {
		if cached == nil {
			return ""
		}
		return *cached
	}
	token := githubAuthToken()
	if !githubAuthLogged {
		githubAuthLogged = true
		if token != "" {
			fmt.Fprintln(os.Stderr, "collect-dependencies: GitHub license API using GITHUB_TOKEN/GH_TOKEN (authenticated).")
		} else {
			fmt.Fprintln(os.Stderr, "collect-dependencies: GitHub license API unauthenticated (no GITHUB_TOKEN or GH_TOKEN in this process env - root/sudo shells often miss user profile exports).")
		}
	}
	headers := map[string]string{
		"User-Agent":           "nvcf-collect-dependencies/1",
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/license", url.PathEscape(owner), url.PathEscape(repo))
	body, status, err := httpGetStringWithStatus(apiURL, httpTimeoutLong, headers)
	if err == nil {
		var data map[string]any
		if json.Unmarshal([]byte(body), &data) == nil {
			if license, ok := asMap(data["license"]); ok {
				if spdx, _ := license["spdx_id"].(string); strings.TrimSpace(spdx) != "" && strings.ToUpper(strings.TrimSpace(spdx)) != "NOASSERTION" {
					val := strings.TrimSpace(spdx)
					cache[key] = &val
					return val
				}
				if name, _ := license["name"].(string); strings.TrimSpace(name) != "" {
					val := strings.TrimSpace(name)
					cache[key] = &val
					return val
				}
			}
		}
	}
	if status == 403 && !githubLicenseWarned {
		githubLicenseWarned = true
		hint := "collect-dependencies: GitHub license API 403. "
		if token == "" {
			hint += "No token in this process: if you use sudo, run `sudo -E go run ./tools/collect-dependencies` or export GITHUB_TOKEN as root."
		} else {
			hint += "Token is set but rejected: renew the PAT, authorize SSO for the org, or fix fine-grained token repository access."
		}
		fmt.Fprintln(os.Stderr, hint)
	}
	if status == 404 || status == 451 {
		cache[key] = nil
		return ""
	}
	htmlLic := githubRepoLicenseFromHTML(owner, repo)
	if htmlLic == "" {
		cache[key] = nil
		return ""
	}
	cache[key] = &htmlLic
	return htmlLic
}

func githubRepoLicenseFromHTML(owner, repo string) string {
	pageURL := fmt.Sprintf("https://github.com/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	html, err := httpGetString(pageURL, 20*time.Second, map[string]string{"User-Agent": httpUserAgent})
	if err != nil {
		return ""
	}
	patterns := []string{
		`data-content="([^"]+?) license"`,
		`>([^<]+?) license<`,
		`aria-label="([^"]+?) license"`,
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)` + pattern)
		for _, match := range re.FindAllStringSubmatch(html, -1) {
			if len(match) != 2 {
				continue
			}
			value := strings.TrimSpace(match[1])
			if value == "" || len(value) > 120 {
				continue
			}
			lower := strings.ToLower(value)
			if lower == "view license" || lower == "custom license" {
				continue
			}
			return value
		}
	}
	return githubRepoLicenseFromEmbeddedLicenseFile(owner, repo, html)
}

func githubRepoLicenseFromEmbeddedLicenseFile(owner, repo, html string) string {
	re := regexp.MustCompile(`(?is)"displayName":"(LICENSE[^"]*)".*?"refName":"([^"]+)".*?"path":"([^"]+)"`)
	match := re.FindStringSubmatch(html)
	if len(match) != 4 {
		return ""
	}
	displayName := jsonStringLiteralToText(match[1])
	refName := jsonStringLiteralToText(match[2])
	path := jsonStringLiteralToText(match[3])
	if !strings.HasPrefix(strings.ToLower(displayName), "license") || refName == "" || path == "" {
		return ""
	}
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(refName), strings.ReplaceAll(path, " ", "%20"))
	raw, err := httpGetString(rawURL, 20*time.Second, map[string]string{"User-Agent": httpUserAgent})
	if err != nil {
		return ""
	}
	return sniffLicenseText(raw)
}

func jsonStringLiteralToText(value string) string {
	var out string
	if err := json.Unmarshal([]byte(`"`+value+`"`), &out); err != nil {
		return value
	}
	return out
}

func httpGetString(rawURL string, timeout time.Duration, headers map[string]string) (string, error) {
	body, _, err := httpGetStringWithStatus(rawURL, timeout, headers)
	return body, err
}

func httpGetBytesWithStatus(rawURL string, timeout time.Duration, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.StatusCode, fmt.Errorf("http %d", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

func httpGetStringWithStatus(rawURL string, timeout time.Duration, headers map[string]string) (string, int, error) {
	body, status, err := httpGetBytesWithStatus(rawURL, timeout, headers)
	return string(body), status, err
}

func fallback(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func filterEmpty(values []string) []string {
	var out []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
