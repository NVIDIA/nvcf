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
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func parseHelmDependencyManifest(path string) map[string]struct{} {
	out := map[string]struct{}{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var mf helmManifest
	if err := yaml.Unmarshal(raw, &mf); err != nil {
		return out
	}
	for _, dep := range mf.Dependencies {
		name := strings.TrimSpace(dep.Name)
		version := strings.TrimSpace(dep.Version)
		repository := normalizeHelmRepository(dep.Repository)
		if name == "" || version == "" || repository == "" || strings.HasPrefix(repository, "file://") {
			continue
		}
		out[helmDependencyRecord(name, version, repository)] = struct{}{}
	}
	return out
}

func normalizeHelmRepository(repository string) string {
	repo := strings.TrimSpace(repository)
	for strings.HasSuffix(repo, "/") {
		repo = strings.TrimSuffix(repo, "/")
	}
	return repo
}

func helmDependencyRecord(name, version, repository string) string {
	return strings.TrimSpace(name) + "\t" + strings.TrimSpace(version) + "\t" + normalizeHelmRepository(repository)
}

func splitHelmDependencyRecord(record string) (string, string, string) {
	parts := strings.SplitN(record, "\t", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

func helmDependencyKey(name, repository string) string {
	return strings.ToLower(strings.TrimSpace(name)) + "\t" + strings.ToLower(normalizeHelmRepository(repository))
}

func isInternalHelmRepository(repository string) bool {
	repo := normalizeHelmRepository(repository)
	if repo == "" {
		return false
	}
	parsed, err := url.Parse(repo)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	return strings.HasSuffix(host, "nvidia.com") || strings.HasSuffix(host, "nvidia.cn")
}

func helmOriginalSpec(record string) string {
	name, version, repository := splitHelmDependencyRecord(record)
	if name == "" || version == "" || repository == "" {
		return record
	}
	return fmt.Sprintf("%s %s @ %s", name, version, repository)
}

func groupHelmDependencies(lines []string) (map[string][]string, []string) {
	groups := map[string]map[string]struct{}{}
	var unparsed []string
	for _, raw := range lines {
		name, version, repository := splitHelmDependencyRecord(raw)
		if name == "" || version == "" || repository == "" {
			unparsed = append(unparsed, raw)
			continue
		}
		key := helmDependencyKey(name, repository)
		if _, ok := groups[key]; !ok {
			groups[key] = map[string]struct{}{}
		}
		groups[key][helmOriginalSpec(raw)] = struct{}{}
	}
	return sortedGroupMap(groups), unparsed
}

func runHelmCommand(args []string, env map[string]string, timeout time.Duration) (string, int, error) {
	stdout, stderr, err := runCommand("", env, timeout, args[0], args[1:]...)
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			if !helmMissingLogged {
				fmt.Fprintln(os.Stderr, "collect-dependencies: `helm` not on PATH; Helm licenses will stay blank.")
				helmMissingLogged = true
			}
			return "", 127, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", 124, err
		}
		if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
			return stdout, exitErr.ExitCode(), nil
		}
		return stdout, 1, err
	}
	_ = stderr
	return stdout, 0, nil
}

func parseHelmShowChartMetadata(text string) helmChartMetadata {
	var meta helmChartMetadata
	if err := yaml.Unmarshal([]byte(text), &meta); err != nil {
		return helmChartMetadata{Annotations: map[string]string{}}
	}
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	return meta
}

func helmChartLicenseFromMetadata(meta helmChartMetadata, githubCache map[string]*string) string {
	for _, key := range helmLicenseAnnotationKeys {
		if value := strings.TrimSpace(meta.Annotations[key]); value != "" && value != "|" {
			return value
		}
	}
	if strings.TrimSpace(meta.License) != "" {
		return strings.TrimSpace(meta.License)
	}
	candidates := []string{}
	if strings.TrimSpace(meta.Home) != "" {
		candidates = append(candidates, strings.TrimSpace(meta.Home))
	}
	for _, source := range meta.Sources {
		if strings.TrimSpace(source) != "" {
			candidates = append(candidates, strings.TrimSpace(source))
		}
	}
	for _, rawURL := range candidates {
		owner, repo := githubOwnerRepoFromURL(rawURL)
		if owner == "" || repo == "" {
			continue
		}
		if lic := githubRepoLicenseSPDX(owner, repo, githubCache); lic != "" {
			return lic
		}
	}
	return ""
}

func helmHTTPRepoAlias(repository string, env map[string]string, aliasCache map[string]*string) string {
	repo := normalizeHelmRepository(repository)
	if cached, ok := aliasCache[repo]; ok {
		if cached == nil {
			return ""
		}
		return *cached
	}
	alias := fmt.Sprintf("repo%d", len(aliasCache)+1)
	_, code, err := runHelmCommand([]string{"helm", "repo", "add", alias, repo}, env, helmToolTimeout)
	if err != nil || code != 0 {
		aliasCache[repo] = nil
		return ""
	}
	aliasCache[repo] = &alias
	return alias
}

func helmShowChartMetadata(name, version, repository string, env map[string]string, aliasCache map[string]*string, chartCache map[string]*helmChartMetadata) *helmChartMetadata {
	repo := normalizeHelmRepository(repository)
	key := name + "\t" + version + "\t" + repo
	if cached, ok := chartCache[key]; ok {
		return cached
	}
	ref := ""
	if strings.HasPrefix(repo, "oci://") {
		if strings.EqualFold(filepath.Base(repo), name) {
			ref = repo
		} else {
			ref = strings.TrimSuffix(repo, "/") + "/" + name
		}
	} else {
		alias := helmHTTPRepoAlias(repo, env, aliasCache)
		if alias == "" {
			chartCache[key] = nil
			return nil
		}
		ref = alias + "/" + name
	}
	stdout, code, err := runHelmCommand([]string{"helm", "show", "chart", ref, "--version", version}, env, helmToolTimeout)
	if err != nil || code != 0 {
		chartCache[key] = nil
		return nil
	}
	meta := parseHelmShowChartMetadata(stdout)
	chartCache[key] = &meta
	return &meta
}

func collectHelmLicenses(allHelm map[string]struct{}) map[string]map[string]struct{} {
	if envBool("COLLECT_DEPS_NO_HELM_SHOW") || len(allHelm) == 0 {
		return map[string]map[string]struct{}{}
	}
	githubCache := map[string]*string{}
	chartCache := map[string]*helmChartMetadata{}
	aliasCache := map[string]*string{}
	out := map[string]map[string]struct{}{}

	tmpDir, err := os.MkdirTemp("", "collect-deps-helm-")
	if err != nil {
		return out
	}
	defer os.RemoveAll(tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	_ = os.MkdirAll(cacheDir, 0o755)
	repoCfg := filepath.Join(tmpDir, "repositories.yaml")
	_ = os.WriteFile(repoCfg, []byte{}, 0o644)

	env := map[string]string{
		"HELM_REPOSITORY_CACHE":  cacheDir,
		"HELM_REPOSITORY_CONFIG": repoCfg,
	}

	for _, record := range sortedKeys(allHelm) {
		name, version, repository := splitHelmDependencyRecord(record)
		if name == "" || version == "" || repository == "" {
			continue
		}
		meta := helmShowChartMetadata(name, version, repository, env, aliasCache, chartCache)
		if meta == nil {
			continue
		}
		if lic := helmChartLicenseFromMetadata(*meta, githubCache); lic != "" {
			addLicense(out, helmDependencyKey(name, repository), lic)
		}
	}
	return out
}

func formatHelmLicense(helmLicenses map[string]map[string]struct{}, key string) string {
	if s := helmLicenses[key]; len(s) > 0 {
		parts := sortedKeys(s)
		return strings.Join(parts, " / ")
	}
	_, repository, _ := strings.Cut(key, "\t")
	if isInternalHelmRepository(repository) {
		return "_(internal chart repository; license not resolved here)_"
	}
	return "_(no license resolved - set `COLLECT_DEPS_NO_HELM_SHOW=1` to skip `helm show chart`, or provide chart metadata / source repo license hints)_"
}

func buildHelmRows(allHelm map[string]struct{}, helmLicenses map[string]map[string]struct{}) ([]dependencyRow, int) {
	groups, unparsed := groupHelmDependencies(sortedKeys(allHelm))
	rows := []dependencyRow{}
	for _, key := range sortedMapKeys(groups) {
		originals := groups[key]
		spec := ""
		if len(originals) == 1 {
			spec = "`" + originals[0] + "`"
		} else {
			inner := make([]string, 0, len(originals))
			for _, original := range originals {
				inner = append(inner, "`"+original+"`")
			}
			label := strings.Fields(strings.SplitN(originals[0], " @ ", 2)[0])[0]
			spec = fmt.Sprintf("`%s` (%s)", label, strings.Join(inner, ", "))
		}
		rows = append(rows, dependencyRow{
			Language: "Helm",
			SortKey:  key,
			Spec:     spec,
			License:  formatHelmLicense(helmLicenses, key),
		})
	}
	sort.Strings(unparsed)
	for _, line := range unparsed {
		rows = append(rows, dependencyRow{
			Language: "Helm",
			SortKey:  line,
			Spec:     "`" + line + "`",
			License:  "_(could not parse Helm dependency)_",
		})
	}
	return rows, len(groups) + len(unparsed)
}
