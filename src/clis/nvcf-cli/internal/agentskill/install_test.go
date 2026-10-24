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
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestInstall_WritesPublicUserSkills(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	if err := Install(ctx, []string{tmpDir}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	m, err := LoadManifest(FS())
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	// Count files: managed manifest marker + TotalFiles manifest entries + version marker.
	wantCount := 1 + m.TotalFiles + 1
	var got int
	err = filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			got++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got != wantCount {
		t.Errorf("file count = %d, want %d", got, wantCount)
	}

	// Spot-check public user skills exist and content matches embedded.
	skillPath := filepath.Join(tmpDir, "nvcf-self-managed-cli", "SKILL.md")
	diskContent, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	embeddedContent, err := fs.ReadFile(FS(), "data/nvcf-self-managed-cli/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	if !bytes.Equal(diskContent, embeddedContent) {
		t.Error("SKILL.md content mismatch between installed and embedded")
	}

	installSkillPath := filepath.Join(tmpDir, "nvcf-self-managed-installation", "SKILL.md")
	if _, err := os.Stat(installSkillPath); err != nil {
		t.Errorf("expected nvcf-self-managed-installation/SKILL.md to exist: %v", err)
	}

	// Spot-check a prompts file exists.
	promptsFile := filepath.Join(tmpDir, "nvcf-self-managed-cli", "prompts", "install-from-scratch.md")
	if _, err := os.Stat(promptsFile); err != nil {
		t.Errorf("expected prompts/install-from-scratch.md to exist: %v", err)
	}

	// Check version marker exists.
	versionPath := filepath.Join(tmpDir, ".nvcf-cli-public-skills.version")
	if _, err := os.Stat(versionPath); err != nil {
		t.Errorf("expected .version file: %v", err)
	}
}

func TestInstall_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	if err := Install(ctx, []string{tmpDir}); err != nil {
		t.Fatalf("Install (first): %v", err)
	}
	if err := Install(ctx, []string{tmpDir}); err != nil {
		t.Fatalf("Install (second): %v", err)
	}

	// Count files after second run — should be the same as after first.
	m, err := LoadManifest(FS())
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	wantCount := 1 + m.TotalFiles + 1
	var got int
	_ = filepath.Walk(tmpDir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			got++
		}
		return nil
	})
	if got != wantCount {
		t.Errorf("after second install, file count = %d, want %d", got, wantCount)
	}
}

// TestInstall_RefusesOnCorruptManifest covers Verify independently in
// manifest_test.go; Install's call to Verify is a thin pass-through. Adding a
// package-level FS override would add unnecessary complexity because
// manifest_test.go already exercises every corruption path.

func TestUninstall_RemovesAll(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()
	unrelated := filepath.Join(tmpDir, "unrelated-skill")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatalf("mkdir unrelated: %v", err)
	}

	if err := Install(ctx, []string{tmpDir}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Uninstall(ctx, []string{tmpDir}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "nvcf-self-managed-cli")); !os.IsNotExist(err) {
		t.Errorf("expected nvcf-self-managed-cli to be removed after Uninstall, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "nvcf-self-managed-installation")); !os.IsNotExist(err) {
		t.Errorf("expected nvcf-self-managed-installation to be removed after Uninstall, got: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("expected unrelated skill to remain after Uninstall, got: %v", err)
	}
}

func TestUninstall_Idempotent(t *testing.T) {
	ctx := context.Background()
	// Uninstall a path that was never created.
	never := filepath.Join(t.TempDir(), "never-existed")
	if err := Uninstall(ctx, []string{never}); err != nil {
		t.Fatalf("Uninstall on missing path: %v", err)
	}
}

func TestInstall_MultipleTargets(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	ctx := context.Background()

	if err := Install(ctx, []string{dir1, dir2}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Both dirs should have the same public CLI skill content.
	content1, err := os.ReadFile(filepath.Join(dir1, "nvcf-self-managed-cli", "SKILL.md"))
	if err != nil {
		t.Fatalf("read dir1/SKILL.md: %v", err)
	}
	content2, err := os.ReadFile(filepath.Join(dir2, "nvcf-self-managed-cli", "SKILL.md"))
	if err != nil {
		t.Fatalf("read dir2/SKILL.md: %v", err)
	}
	if !bytes.Equal(content1, content2) {
		t.Error("SKILL.md content differs between the two target directories")
	}

	// Both dirs should have the version marker.
	for _, dir := range []string{dir1, dir2} {
		vp := filepath.Join(dir, ".nvcf-cli-public-skills.version")
		if _, err := os.Stat(vp); err != nil {
			t.Errorf("expected .version in %s: %v", dir, err)
		}
	}
}
