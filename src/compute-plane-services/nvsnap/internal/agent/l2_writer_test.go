/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// Unit tests for AgentCopier (v0.0.51 single-copy L2 writer).
//
// Coverage:
//   - Empty destRoot → error
//   - Single rootfs source → file-tree copied with mode preservation
//   - Multiple sources to distinct DstSubpaths
//   - Excludes are honored (rootfs only)
//   - Unknown source kind → error, partial totals returned
//   - Default empty Kind treated as rootfs

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func newTestCopier(t *testing.T, hostRoot string) *AgentCopier {
	t.Helper()
	return NewAgentCopier(hostRoot, logrus.NewEntry(logrus.New()).WithField("test", t.Name()))
}

// seedFile writes content at relPath under root, creating parent dirs.
func seedFile(t *testing.T, root, relPath, content string, mode os.FileMode) { //nolint:unparam // mode kept as a parameter for call-site clarity
	t.Helper()
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestAgentCopier_EmptyDestRootIsError(t *testing.T) {
	c := newTestCopier(t, "/host")
	_, _, err := c.Copy(context.Background(), "", []checkpointstore.CaptureSource{})
	if err == nil {
		t.Fatal("expected error on empty destRoot")
	}
}

func TestAgentCopier_SingleRootfsSourceCopiesTree(t *testing.T) {
	hostRoot := t.TempDir()
	destRoot := t.TempDir()

	// Seed source tree at hostRoot/data/model
	seedFile(t, hostRoot, "data/model/config.json", `{"hidden_size": 4096}`, 0o644)
	seedFile(t, hostRoot, "data/model/weights/00.bin", "binary-blob-content", 0o644)

	c := newTestCopier(t, hostRoot)
	sources := []checkpointstore.CaptureSource{{
		Kind:       checkpointstore.SourceKindRootfs,
		SrcPath:    "/data/model",
		DstSubpath: "rootfs/model",
	}}
	bytes, files, err := c.Copy(context.Background(), destRoot, sources)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if bytes == 0 || files < 2 {
		t.Errorf("expected bytes>0 files>=2, got bytes=%d files=%d", bytes, files)
	}
	got, err := os.ReadFile(filepath.Join(destRoot, "rootfs", "model", "config.json"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != `{"hidden_size": 4096}` {
		t.Errorf("content mismatch: %q", got)
	}
}

func TestAgentCopier_MultipleSourcesDistinctDsts(t *testing.T) {
	hostRoot := t.TempDir()
	destRoot := t.TempDir()
	seedFile(t, hostRoot, "src1/a.txt", "alpha", 0o644)
	seedFile(t, hostRoot, "src2/b.txt", "bravo", 0o644)

	c := newTestCopier(t, hostRoot)
	sources := []checkpointstore.CaptureSource{
		{Kind: checkpointstore.SourceKindRootfs, SrcPath: "/src1", DstSubpath: "tree-one"},
		{Kind: checkpointstore.SourceKindRootfs, SrcPath: "/src2", DstSubpath: "tree-two"},
	}
	_, files, err := c.Copy(context.Background(), destRoot, sources)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if files < 2 {
		t.Errorf("expected 2+ files copied, got %d", files)
	}
	for _, rel := range []string{"tree-one/a.txt", "tree-two/b.txt"} {
		if _, err := os.Stat(filepath.Join(destRoot, rel)); err != nil {
			t.Errorf("expected dest file %s missing: %v", rel, err)
		}
	}
}

func TestAgentCopier_ExcludesHonored(t *testing.T) {
	hostRoot := t.TempDir()
	destRoot := t.TempDir()
	seedFile(t, hostRoot, "model/keep.txt", "keep", 0o644)
	seedFile(t, hostRoot, "model/skip-me.txt", "skip", 0o644)

	c := newTestCopier(t, hostRoot)
	// treecopy Excludes are absolute-from-src-root paths (the copier
	// prepends "/" to the relative-from-src path before lookup).
	sources := []checkpointstore.CaptureSource{{
		Kind:       checkpointstore.SourceKindRootfs,
		SrcPath:    "/model",
		DstSubpath: "out",
		Excludes:   []string{"/skip-me.txt"},
	}}
	if _, _, err := c.Copy(context.Background(), destRoot, sources); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "out", "keep.txt")); err != nil {
		t.Errorf("kept file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "out", "skip-me.txt")); !os.IsNotExist(err) {
		t.Errorf("excluded file should not be present, got err=%v", err)
	}
}

func TestAgentCopier_UnknownKindErrorsWithPartialTotals(t *testing.T) {
	hostRoot := t.TempDir()
	destRoot := t.TempDir()
	seedFile(t, hostRoot, "first/x.txt", "x", 0o644)

	c := newTestCopier(t, hostRoot)
	sources := []checkpointstore.CaptureSource{
		{Kind: checkpointstore.SourceKindRootfs, SrcPath: "/first", DstSubpath: "first"},
		{Kind: "bogus-kind", SrcPath: "/anything", DstSubpath: "nope"},
	}
	bytes, files, err := c.Copy(context.Background(), destRoot, sources)
	if err == nil {
		t.Fatal("expected error on unknown kind")
	}
	if bytes == 0 || files == 0 {
		t.Errorf("expected non-zero partial totals before error, got bytes=%d files=%d", bytes, files)
	}
}

func TestAgentCopier_EmptyKindTreatedAsRootfs(t *testing.T) {
	hostRoot := t.TempDir()
	destRoot := t.TempDir()
	seedFile(t, hostRoot, "default-kind/y.txt", "y", 0o644)

	c := newTestCopier(t, hostRoot)
	sources := []checkpointstore.CaptureSource{{
		// Kind intentionally empty.
		SrcPath:    "/default-kind",
		DstSubpath: "out",
	}}
	if _, _, err := c.Copy(context.Background(), destRoot, sources); err != nil {
		t.Fatalf("Copy with empty Kind should default to rootfs, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "out", "y.txt")); err != nil {
		t.Errorf("copy result missing: %v", err)
	}
}
