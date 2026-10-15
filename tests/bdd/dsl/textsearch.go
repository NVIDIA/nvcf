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

package dsl

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FilesDoNotContain recursively inspects regular files under root and
// fails if any interpolated fixed string appears.
func FilesDoNotContain(root string, needles []string) error {
	root = strings.TrimSpace(Interpolate(root))
	if root == "" {
		return fmt.Errorf("rendered manifests directory is empty")
	}
	if len(needles) == 0 {
		return fmt.Errorf("excluded text list is empty")
	}

	resolvedNeedles := make([]string, 0, len(needles))
	for _, rawNeedle := range needles {
		needle := Interpolate(rawNeedle)
		if needle == "" {
			return fmt.Errorf("excluded text is empty")
		}
		resolvedNeedles = append(resolvedNeedles, needle)
	}

	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect rendered manifests directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("rendered manifests path %q is not a directory", root)
	}

	filesInspected := 0
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect rendered manifest %q: %w", path, walkErr)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		filesInspected++
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read rendered manifest %q: %w", path, err)
		}
		for _, needle := range resolvedNeedles {
			if bytes.Contains(body, []byte(needle)) {
				return fmt.Errorf("rendered manifest %q contains excluded text %q", path, needle)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if filesInspected == 0 {
		return fmt.Errorf("rendered manifests directory %q contains no regular files", root)
	}
	return nil
}
