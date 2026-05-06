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

package agentskill

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
)

// Install copies the embedded skill bundle into each target directory. The
// embedded FS is verified against its manifest before any writes; if Verify
// fails, no targets are touched.
//
// Each target gets the data/ tree contents (manifest.json + the markdown
// files) plus a .version file recording the build SHA. Subdirectories are
// created with 0o755; files written with 0o644. Existing files are
// overwritten — Install is idempotent on re-run with unchanged content.
//
// On partial failure across multiple targets, earlier targets are left
// written and the function returns the offending target's error. Install is
// eventually-consistent rather than transactional; callers should rely on
// idempotent re-run, not rollback. (For the current cobra default — two
// targets, both under $HOME — a partial-failure path is implausible enough
// that the simpler semantic is preferred.)
func Install(ctx context.Context, targetDirs []string) error {
	if len(targetDirs) == 0 {
		return errors.New("agentskill: no target directories specified")
	}
	if err := Verify(embeddedFS); err != nil {
		return fmt.Errorf("manifest verification failed; refusing to install: %w", err)
	}
	m, err := LoadManifest(embeddedFS)
	if err != nil {
		return err
	}
	for _, target := range targetDirs {
		if err := installInto(ctx, target, m); err != nil {
			return fmt.Errorf("install %s: %w", target, err)
		}
	}
	return nil
}

func installInto(_ context.Context, target string, m *Manifest) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	// Write manifest.json + every file referenced by it.
	paths := []string{"manifest.json"}
	for _, mf := range m.Files {
		paths = append(paths, mf.Path)
	}
	for _, rel := range paths {
		body, err := fs.ReadFile(embeddedFS, "data/"+rel)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
	}
	// .version file with the binary's build SHA.
	return os.WriteFile(filepath.Join(target, ".version"), []byte(buildSHA()+"\n"), 0o644)
}

// BuildSHA returns the VCS revision the binary was built from, with a
// "+dirty" suffix if the working tree was modified at build time.
// Returns "unknown" when build info is unavailable (e.g., go test, go run,
// or builds without VCS info).
func BuildSHA() string { return buildSHA() }

func buildSHA() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var sha, dirty string
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			sha = s.Value
		}
		if s.Key == "vcs.modified" && s.Value == "true" {
			dirty = "+dirty"
		}
	}
	if sha == "" {
		return "unknown"
	}
	return sha + dirty
}

// Uninstall removes each target directory (RemoveAll). Idempotent — missing
// targets return nil.
func Uninstall(_ context.Context, targetDirs []string) error {
	for _, target := range targetDirs {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("uninstall %s: %w", target, err)
		}
	}
	return nil
}
