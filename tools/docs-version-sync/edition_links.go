// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var editionVersionPathRE = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){1,2}$`)

var editionLinkRE = regexp.MustCompile(`(\]\(|\bhref=["'])(/nvcf/[^"'()\s]+)`)

// stageEditionLinks converts only current-page links in authored edition content.
// Frozen archives and explicitly versioned destinations retain their identity.
func stageEditionLinks(root, navigationPath string) error {
	if !safeFernPath(navigationPath) {
		return fmt.Errorf("edition navigation must stay under fern/")
	}
	data, err := os.ReadFile(filepath.Join(root, "fern", navigationPath))
	if err != nil {
		return err
	}
	var navigation struct {
		Tabs map[string]struct {
			Slug string `yaml:"slug"`
		} `yaml:"tabs"`
		Navigation []map[string]any `yaml:"navigation"`
	}
	if err := yaml.Unmarshal(data, &navigation); err != nil {
		return err
	}
	routes := map[string]string{}
	pages := map[string]bool{}
	var walk func([]any, string) error
	walk = func(items []any, prefix string) error {
		for _, item := range items {
			node, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid edition navigation item")
			}
			if source, ok := node["path"].(string); ok {
				slug, ok := node["slug"].(string)
				if !ok || slug == "" {
					return fmt.Errorf("edition page %s requires an explicit slug", source)
				}
				navigatedPath := filepath.Clean(filepath.Join(root, "fern", filepath.Dir(navigationPath), source))
				path, err := filepath.EvalSymlinks(navigatedPath)
				if err != nil {
					return err
				}
				if path != navigatedPath {
					return fmt.Errorf("edition navigation must use the real source path instead of a symlink: %s", source)
				}
				relative, err := filepath.Rel(root, path)
				if err != nil || !strings.HasPrefix(relative, "docs"+string(filepath.Separator)) {
					return fmt.Errorf("edition page must be under docs/: %s", source)
				}
				for _, frozen := range []string{"docs/v", "docs/self-managed-", "docs/compute-plane-", "docs/observability-"} {
					if strings.HasPrefix(filepath.ToSlash(relative), frozen) {
						return fmt.Errorf("frozen source cannot be rewritten: %s", source)
					}
				}
				route := prefix + "/" + slug
				if previous, exists := routes[route]; exists && previous != path {
					return fmt.Errorf("duplicate edition route %s", route)
				}
				routes[route] = path
				if routes[prefix] == "" {
					routes[prefix] = path
				}
				pages[path] = true
			}
			if children, ok := node["contents"].([]any); ok {
				next := prefix
				if node["skip-slug"] != true {
					slug, ok := node["slug"].(string)
					if !ok || slug == "" {
						return fmt.Errorf("edition section needs an explicit slug or skip-slug")
					}
					next += "/" + slug
				}
				if err := walk(children, next); err != nil {
					return err
				}
			}
		}
		return nil
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	for _, tab := range navigation.Navigation {
		name, _ := tab["tab"].(string)
		layout, _ := tab["layout"].([]any)
		if err := walk(layout, "/nvcf/"+navigation.Tabs[name].Slug); err != nil {
			return err
		}
	}
	data, err = os.ReadFile(filepath.Join(root, "fern", "docs.yml"))
	if err != nil {
		return err
	}
	var config struct {
		Redirects []struct{ Source, Destination string } `yaml:"redirects"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}
	// Resolve exact aliases through the existing redirects; leave wildcard and
	// versioned routes for the archive adapter. Bound passes detect chains safely.
	for pass := 0; pass < len(config.Redirects); pass++ {
		changed := false
		for _, redirect := range config.Redirects {
			if !currentEditionRoute(redirect.Source) || strings.Contains(redirect.Source, ":") || routes[redirect.Source] != "" {
				continue
			}
			if destination := routes[strings.TrimRight(redirect.Destination, "/")]; destination != "" {
				routes[redirect.Source] = destination
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "fern", "changelog"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".md") || strings.HasSuffix(entry.Name(), ".mdx")) {
			pages[filepath.Join(root, "fern", "changelog", entry.Name())] = true
		}
	}
	for path := range pages {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var linkErr error
		updated := editionLinkRE.ReplaceAllStringFunc(string(data), func(match string) string {
			parts := editionLinkRE.FindStringSubmatch(match)
			u, err := url.Parse(parts[2])
			if err != nil {
				linkErr = err
				return match
			}
			if !currentEditionRoute(u.Path) {
				return match
			}
			target := routes[strings.TrimRight(u.Path, "/")]
			if target == "" {
				linkErr = fmt.Errorf("%s: no edition page for %s", path, u.Path)
				return match
			}
			relative, err := filepath.Rel(filepath.Dir(path), target)
			if err != nil {
				linkErr = err
				return match
			}
			u.Path = filepath.ToSlash(relative)
			return parts[1] + u.String()
		})
		if linkErr != nil {
			return linkErr
		}
		if updated != string(data) {
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func currentEditionRoute(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "nvcf" {
		return false
	}
	switch parts[1] {
	case "overview", "self-managed", "compute-plane", "observability":
	default:
		return false
	}
	if len(parts) > 2 && (parts[2] == "dev" || editionVersionPathRE.MatchString(parts[2])) {
		return false
	}
	return true
}
