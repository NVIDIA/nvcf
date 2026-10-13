// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// stageEditionPreview derives a Development-first configuration in a disposable
// clone. The caller must not pass the original working tree.
func stageEditionPreview(root string) error {
	path := filepath.Join(root, "fern", "docs.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("read preview configuration: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("preview configuration must be a mapping")
	}
	var versions *yaml.Node
	fields := document.Content[0].Content
	for i := 0; i < len(fields); i += 2 {
		if fields[i].Value == "versions" {
			if versions != nil {
				return fmt.Errorf("preview configuration has duplicate versions fields")
			}
			versions = fields[i+1]
		}
	}
	if versions == nil || versions.Kind != yaml.SequenceNode || len(versions.Content) == 0 {
		return fmt.Errorf("preview configuration requires a nonempty versions list")
	}
	index, navigation, err := previewNavigation(versions)
	if err != nil {
		return err
	}
	if err := stageEditionLinks(root, navigation); err != nil {
		return err
	}
	selected := versions.Content[index]
	copy(versions.Content[1:index+1], versions.Content[:index])
	versions.Content[0] = selected
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, output.Bytes(), 0o644)
}

func previewNavigation(versions *yaml.Node) (int, string, error) {
	// A prepared release branch has a local default and no Development entry.
	// It can use the same helper without adding a synthetic version.
	index := 0
	found := false
	for i, node := range versions.Content {
		var version struct {
			Slug        string `yaml:"slug"`
			DisplayName string `yaml:"display-name"`
		}
		if err := node.Decode(&version); err != nil {
			return 0, "", fmt.Errorf("read preview version: %w", err)
		}
		if version.Slug == "dev" || version.DisplayName == "Development" {
			if found {
				return 0, "", fmt.Errorf("preview configuration has multiple Development versions")
			}
			index, found = i, true
		}
	}
	// Only a visible version sourced from this clone can show candidate edits.
	var selected struct {
		Path   string `yaml:"path"`
		Ref    string `yaml:"ref"`
		Hidden bool   `yaml:"hidden"`
	}
	if err := versions.Content[index].Decode(&selected); err != nil {
		return 0, "", fmt.Errorf("read selected preview version: %w", err)
	}
	if selected.Ref != "" || selected.Hidden || !safeFernPath(selected.Path) {
		return 0, "", fmt.Errorf("preview requires a visible local Development version or a local release-branch default")
	}
	return index, selected.Path, nil
}
