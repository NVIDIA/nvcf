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
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImportHasMaterializedSubmodules(t *testing.T) {
	t.Parallel()

	importDir := t.TempDir()
	writeTestFile(t, filepath.Join(importDir, ".gitmodules"), `[submodule "deps/child"]
	path = deps/child
	url = https://github.com/NVIDIA/example/child.git
`)
	if err := os.MkdirAll(filepath.Join(importDir, "deps", "child"), 0o755); err != nil {
		t.Fatalf("mkdir submodule dir: %v", err)
	}

	ready, err := importHasMaterializedSubmodules(importDir)
	if err != nil {
		t.Fatalf("importHasMaterializedSubmodules(empty): %v", err)
	}
	if ready {
		t.Fatal("expected empty submodule directory to be reported incomplete")
	}

	writeTestFile(t, filepath.Join(importDir, "deps", "child", "README.md"), "materialized\n")
	ready, err = importHasMaterializedSubmodules(importDir)
	if err != nil {
		t.Fatalf("importHasMaterializedSubmodules(materialized): %v", err)
	}
	if !ready {
		t.Fatal("expected populated submodule directory to be reported complete")
	}
}

func TestMaterializeSubmodules(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file:git:ssh:https")

	tmpDir := t.TempDir()
	submoduleRepo := filepath.Join(tmpDir, "submodule")
	createGitRepo(t, submoduleRepo)
	writeTestFile(t, filepath.Join(submoduleRepo, "submodule.txt"), "hello from submodule\n")
	gitCommitAll(t, submoduleRepo, "add submodule file")

	parentRepo := filepath.Join(tmpDir, "parent")
	createGitRepo(t, parentRepo)
	writeTestFile(t, filepath.Join(parentRepo, "root.txt"), "root file\n")
	gitCommitAll(t, parentRepo, "add root file")
	runGitForTest(t, parentRepo, "-c", "protocol.file.allow=always", "submodule", "add", submoduleRepo, "deps/child")
	gitCommitAll(t, parentRepo, "add child submodule")

	cloneDir := filepath.Join(tmpDir, "clone")
	runGitForTest(t, tmpDir, "clone", parentRepo, cloneDir)

	ready, err := importHasMaterializedSubmodules(cloneDir)
	if err != nil {
		t.Fatalf("importHasMaterializedSubmodules(before): %v", err)
	}
	if ready {
		t.Fatal("expected plain clone to have incomplete submodule contents")
	}

	if err := materializeSubmodules(syncOptions{}, "test/import", cloneDir, false); err != nil {
		t.Fatalf("materializeSubmodules: %v", err)
	}

	destDir := filepath.Join(tmpDir, "mirrored")
	if err := mirror(cloneDir, destDir); err != nil {
		t.Fatalf("mirror: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "deps", "child", "submodule.txt"))
	if err != nil {
		t.Fatalf("read mirrored submodule file: %v", err)
	}
	if string(got) != "hello from submodule\n" {
		t.Fatalf("unexpected mirrored contents: %q", string(got))
	}

	if _, err := os.Stat(filepath.Join(destDir, "deps", "child", ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected mirrored submodule metadata to be excluded, got err=%v", err)
	}
}

func createGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo %s: %v", dir, err)
	}
	runGitForTest(t, dir, "init")
}

func gitCommitAll(t *testing.T, dir, message string) {
	t.Helper()
	runGitForTest(t, dir, "add", ".")
	runGitForTest(t, dir,
		"-c", "user.name=Cursor Test",
		"-c", "user.email=cursor-test@example.com",
		"commit", "-m", message,
	)
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s failed: %v\nstdout:\n%s\nstderr:\n%s", args, dir, err, stdout.String(), stderr.String())
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
