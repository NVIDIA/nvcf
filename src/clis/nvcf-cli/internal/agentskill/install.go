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
	"sort"
	"strings"
)

const (
	manifestMarker = ".nvcf-cli-public-skills.manifest.json"
	versionMarker  = ".nvcf-cli-public-skills.version"
)

// Install copies the embedded public user skills into each target directory. The
// embedded FS is verified against its manifest before any writes; if Verify
// fails, no targets are touched.
//
// Each target is a base skills directory. It receives every skill directory in
// the data/ tree plus hidden manifest/version markers recording the installed
// bundle. Subdirectories are created with 0o755; files written with 0o644.
// Existing files are overwritten. Install is idempotent on re-run with
// unchanged content.
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
	fsys := FS()
	if err := Verify(fsys); err != nil {
		return fmt.Errorf("manifest verification failed; refusing to install: %w", err)
	}
	m, err := LoadManifest(fsys)
	if err != nil {
		return err
	}
	for _, target := range targetDirs {
		if err := installInto(ctx, fsys, target, m); err != nil {
			return fmt.Errorf("install %s: %w", target, err)
		}
	}
	return nil
}

func installInto(_ context.Context, fsys fs.FS, target string, m *Manifest) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for _, mf := range m.Files {
		rel := mf.Path
		body, err := fs.ReadFile(fsys, "data/"+rel)
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
	manifestBody, err := fs.ReadFile(fsys, "data/manifest.json")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, manifestMarker), manifestBody, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, versionMarker), []byte(buildSHA()+"\n"), 0o644)
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

// SkillNames returns the public user skill directory names present in a
// manifest. Paths without a top-level directory are ignored.
func SkillNames(m *Manifest) []string {
	seen := map[string]struct{}{}
	for _, mf := range m.Files {
		name, _, ok := strings.Cut(mf.Path, "/")
		if !ok || name == "" {
			continue
		}
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Uninstall removes only the managed public user skill directories and hidden
// markers from each target directory. Idempotent: missing targets or missing
// managed skill directories return nil.
func Uninstall(_ context.Context, targetDirs []string) error {
	m, err := LoadManifest(FS())
	if err != nil {
		return err
	}
	skillNames := SkillNames(m)
	for _, target := range targetDirs {
		for _, skillName := range skillNames {
			if err := os.RemoveAll(filepath.Join(target, skillName)); err != nil {
				return fmt.Errorf("uninstall %s/%s: %w", target, skillName, err)
			}
		}
		for _, marker := range []string{manifestMarker, versionMarker} {
			if err := os.Remove(filepath.Join(target, marker)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s/%s: %w", target, marker, err)
			}
		}
	}
	return nil
}
