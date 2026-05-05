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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func parseGoModRequires(path string) map[string]struct{} {
	out := map[string]struct{}{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	text := string(raw)
	reBlock := regexp.MustCompile(`replace\s+\([^)]*\)`)
	text = reBlock.ReplaceAllString(text, "")
	reLine := regexp.MustCompile(`replace\s+[^\n]+\s+=>\s+[^\n]+`)
	text = reLine.ReplaceAllString(text, "")

	inReq := false
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "require (") {
			inReq = true
			continue
		}
		if inReq {
			if strings.HasPrefix(s, ")") {
				inReq = false
				continue
			}
			if s == "" || strings.HasPrefix(s, "//") {
				continue
			}
			fields := strings.Fields(s)
			if len(fields) == 0 {
				continue
			}
			mod := fields[0]
			if strings.HasPrefix(mod, "file://") {
				continue
			}
			out[mod] = struct{}{}
			continue
		}
		if m := regexp.MustCompile(`^require\s+(\S+)\s+v`).FindStringSubmatch(s); len(m) == 2 {
			out[m[1]] = struct{}{}
		}
	}
	return out
}

func findGoModuleDirs(importRoots []string) []string {
	seen := map[string]struct{}{}
	for _, root := range importRoots {
		_ = walkImportRoot(root, func(path string, d fs.DirEntry) error {
			if !d.IsDir() && d.Name() == "go.mod" {
				seen[filepath.Dir(path)] = struct{}{}
			}
			return nil
		})
	}
	return sortedKeys(seen)
}

func runGoModVendor(moduleDir string) bool {
	stdout, stderr, err := runCommand(moduleDir, nil, goToolTimeout, "go", "mod", "vendor")
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			fmt.Fprintln(os.Stderr, "collect-dependencies: `go` not on PATH; skipping `go mod vendor`.")
			return false
		}
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "collect-dependencies: go mod vendor timed out: %s\n", moduleDir)
			return false
		}
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		fmt.Fprintf(os.Stderr, "collect-dependencies: go mod vendor failed (%s): %s\n", moduleDir, fallback(msg, "non-zero exit"))
		return false
	}
	return true
}

func maybeVendorGoModules(importRoots []string) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("COLLECT_DEPS_GO_VENDOR")))
	if raw == "" || raw == "0" || raw == "false" || raw == "no" || raw == "off" {
		return
	}
	onlyMissing := false
	switch raw {
	case "1", "true", "yes", "all":
	case "missing":
		onlyMissing = true
	default:
		fmt.Fprintf(os.Stderr, "collect-dependencies: ignoring unknown COLLECT_DEPS_GO_VENDOR=%q (use 1, all, or missing)\n", raw)
		return
	}
	for _, dir := range findGoModuleDirs(importRoots) {
		if onlyMissing {
			if st, err := os.Stat(filepath.Join(dir, "vendor", "modules.txt")); err == nil && !st.IsDir() {
				continue
			}
		}
		fmt.Fprintf(os.Stderr, "collect-dependencies: go mod vendor → %s\n", dir)
		runGoModVendor(dir)
	}
}

func collectGoLicensesFromVendor(importRoot string) map[string]map[string]struct{} {
	acc := map[string]map[string]struct{}{}
	_ = filepath.WalkDir(importRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "target", ".venv", "venv":
				if path != importRoot {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "modules.txt" || filepath.Base(filepath.Dir(path)) != "vendor" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		vendorDir := filepath.Dir(path)
		for _, mod := range parseModulesTxtModuleHeaders(string(raw)) {
			if lic := readLicenseUnder(vendorDir, mod); lic != "" {
				addLicense(acc, mod, lic)
			}
		}
		return nil
	})
	return acc
}

func parseGoListMJSONStream(stdout string) []map[string]any {
	var out []map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			break
		}
		out = append(out, obj)
	}
	return out
}

func parseGoSumModuleVersions(goSum string) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	raw, err := os.ReadFile(goSum)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		parts := strings.Fields(s)
		if len(parts) < 3 {
			continue
		}
		mod := parts[0]
		ver := parts[1]
		if strings.HasSuffix(ver, "/go.mod") {
			ver = strings.TrimSuffix(ver, "/go.mod")
		}
		addLicense(out, mod, ver)
	}
	return out
}

func mergeGoSumVersions(goModuleRoots []string) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, root := range goModuleRoots {
		mergeLicenseMap(out, parseGoSumModuleVersions(filepath.Join(root, "go.sum")))
	}
	return out
}

func enrichGoLicensesFromGoModuleList(allModules map[string]struct{}, goLicenses map[string]map[string]struct{}, goModuleRoots []string) {
	if envBool("COLLECT_DEPS_NO_GO_LIST") {
		return
	}
	missing := missingGoModules(allModules, goLicenses)
	if len(missing) == 0 || len(goModuleRoots) == 0 {
		return
	}
	if !goListHintLogged {
		goListHintLogged = true
		fmt.Fprintln(os.Stderr, "collect-dependencies: resolving Go licenses via `go list -mod=mod -m -json all` in each umbrella module (module-cache LICENSE; use COLLECT_DEPS_NO_GO_LIST=1 to skip).")
	}
	for _, root := range goModuleRoots {
		stdout, _, err := runCommand(root, nil, 10*time.Minute, "go", "list", "-mod=mod", "-m", "-json", "all")
		if err != nil {
			var execErr *exec.Error
			if errors.As(err, &execErr) {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Fprintf(os.Stderr, "collect-dependencies: go list timed out in %s\n", root)
			}
			continue
		}
		for _, obj := range parseGoListMJSONStream(stdout) {
			path, _ := obj["Path"].(string)
			if _, ok := missing[path]; !ok {
				continue
			}
			dir, _ := obj["Dir"].(string)
			if lic := licenseFromPlainDir(dir); lic != "" {
				addLicense(goLicenses, path, lic)
				delete(missing, path)
			}
		}
		if len(missing) == 0 {
			return
		}
	}
}

func enrichGoLicensesFromGoModDownload(allModules map[string]struct{}, goLicenses map[string]map[string]struct{}, goModuleRoots []string) {
	if envBool("COLLECT_DEPS_NO_GO_MOD_DOWNLOAD") {
		return
	}
	missing := missingGoModules(allModules, goLicenses)
	if len(missing) == 0 || len(goModuleRoots) == 0 {
		return
	}
	versionsByMod := mergeGoSumVersions(goModuleRoots)
	if len(versionsByMod) == 0 {
		return
	}
	if !goModDownloadHintLogged {
		goModDownloadHintLogged = true
		fmt.Fprintln(os.Stderr, "collect-dependencies: resolving remaining Go licenses via `go mod download -json` (versions from go.sum; use COLLECT_DEPS_NO_GO_MOD_DOWNLOAD=1 to skip).")
	}
	for _, mod := range sortedKeys(missing) {
		versions := sortedKeys(versionsByMod[mod])
		for _, ver := range versions {
			if goLicenseForModule(goLicenses, mod) != nil {
				break
			}
			spec := mod + "@" + ver
			stdout, _, err := runCommand("", nil, 2*time.Minute, "go", "mod", "download", "-json", spec)
			if err != nil {
				var execErr *exec.Error
				if errors.As(err, &execErr) {
					return
				}
				if errors.Is(err, context.DeadlineExceeded) {
					continue
				}
				continue
			}
			var obj map[string]any
			if json.Unmarshal([]byte(stdout), &obj) != nil {
				continue
			}
			dir, _ := obj["Dir"].(string)
			if lic := licenseFromPlainDir(dir); lic != "" {
				addLicense(goLicenses, mod, lic)
				break
			}
		}
	}
}

func enrichGoLicensesFromGitHub(allModules map[string]struct{}, goLicenses map[string]map[string]struct{}) {
	if envBool("COLLECT_DEPS_NO_GITHUB") {
		return
	}
	repoCache := map[string]*string{}
	for _, mod := range sortedKeys(allModules) {
		if goLicenseForModule(goLicenses, mod) != nil {
			continue
		}
		owner, repo := githubOwnerRepoFromModulePath(mod)
		if owner == "" || repo == "" {
			continue
		}
		if spdx := githubRepoLicenseSPDX(owner, repo, repoCache); spdx != "" {
			addLicense(goLicenses, mod, spdx)
		}
	}
}

func goLicenseForModule(goLicenses map[string]map[string]struct{}, mod string) map[string]struct{} {
	if s, ok := goLicenses[mod]; ok {
		return s
	}
	bestLen := -1
	var best map[string]struct{}
	for key, value := range goLicenses {
		if mod == key || strings.HasPrefix(mod, key+"/") {
			if len(key) > bestLen {
				bestLen = len(key)
				best = value
			}
		}
	}
	return best
}

func formatGoLicense(goLicenses map[string]map[string]struct{}, mod string) string {
	s := goLicenseForModule(goLicenses, mod)
	if len(s) == 0 {
		return "_(no license from vendor, `go list` / `go mod download` module cache, GitHub API (`github.com/...` only), or `COLLECT_DEPS_GO_VENDOR` - see tools/collect-dependencies/README.md)_"
	}
	parts := sortedKeys(s)
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts, " / ") + " _(multiple vendor copies differ)_"
}

func buildGoRows(allGo map[string]struct{}, goLicenses map[string]map[string]struct{}) []dependencyRow {
	rows := make([]dependencyRow, 0, len(allGo))
	for _, mod := range sortedKeys(allGo) {
		rows = append(rows, dependencyRow{
			Language: "Go",
			SortKey:  mod,
			Spec:     "`" + mod + "`",
			License:  formatGoLicense(goLicenses, mod),
		})
	}
	return rows
}
